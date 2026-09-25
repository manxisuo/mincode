// Package repomap builds a compact structural map of a workspace to improve
// code navigation and context efficiency.
//
// Go files are parsed with the standard library go/ast so the map lists real
// top-level symbols. Other recognized code files contribute their path (and
// language) only, which keeps the first version dependency-free and fast.
package repomap

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/manxisuo/mincode/internal/ctxmgr"
)

// Defaults bound the map so one build cannot flood the context window.
const (
	DefaultMaxTokens = 1500
	DefaultMaxFiles  = 400
	DefaultScanCap   = 2000
	maxFileBytes     = 512 * 1024
	maxSymbolsFile   = 12
	scoreSymbolCap   = 20
	maxSigLen        = 110
)

// skipDirs are never descended into when scanning for map candidates.
var skipDirs = map[string]bool{
	".git":         true,
	".mincode":     true,
	"node_modules": true,
	"vendor":       true,
	"__pycache__":  true,
	".idea":        true,
	".vscode":      true,
	"dist":         true,
	"build":        true,
	"target":       true,
	".venv":        true,
	"venv":         true,
	".next":        true,
}

// Options controls one map build.
type Options struct {
	// MaxTokens caps the rendered map size (estimated tokens). 0 = default.
	MaxTokens int
	// MaxFiles caps how many files are rendered. 0 = default.
	MaxFiles int
	// Subpath limits the scan to a workspace-relative directory ("" = root).
	Subpath string
	// Focus boosts files/symbols whose path or symbol name contains this term.
	Focus string
}

// Symbol is one top-level declaration.
type Symbol struct {
	Kind string `json:"kind"` // func | method | type | struct | interface | const | var
	Name string `json:"name"`
	Sig  string `json:"sig,omitempty"`
	Line int    `json:"line,omitempty"`
}

// FileEntry is one file in the map.
type FileEntry struct {
	Path      string   `json:"path"`
	Lang      string   `json:"lang"`
	Package   string   `json:"package,omitempty"`
	Lines     int      `json:"lines,omitempty"`
	Symbols   []Symbol `json:"symbols,omitempty"`
	Score     int      `json:"score"`
	Truncated bool     `json:"truncated,omitempty"`
}

// Map is the result of one build.
type Map struct {
	Root      string      `json:"root"`
	Subpath   string      `json:"subpath,omitempty"`
	Focus     string      `json:"focus,omitempty"`
	Files     []FileEntry `json:"files"`
	Text      string      `json:"text"`
	Tokens    int         `json:"tokens"`
	Scanned   int         `json:"scanned"`
	Skipped   int         `json:"skipped"`
	Truncated bool        `json:"truncated"`
	BuildMS   int64       `json:"build_ms"`
}

type rawFile struct {
	abs  string
	rel  string
	lang string
}

// Build scans workspace (optionally limited to opts.Subpath) and returns a
// ranked, budgeted repository map. Build never escapes the workspace root.
func Build(ctx context.Context, workspace string, opts Options) (*Map, error) {
	start := time.Now()
	if opts.MaxTokens <= 0 {
		opts.MaxTokens = DefaultMaxTokens
	}
	if opts.MaxFiles <= 0 {
		opts.MaxFiles = DefaultMaxFiles
	}

	base, err := filepath.Abs(workspace)
	if err != nil {
		return nil, fmt.Errorf("resolve workspace: %w", err)
	}
	base = filepath.Clean(base)

	scanRoot := base
	if opts.Subpath != "" && opts.Subpath != "." {
		abs, err := inside(base, opts.Subpath)
		if err != nil {
			return nil, err
		}
		info, err := os.Stat(abs)
		if err != nil {
			return nil, fmt.Errorf("repo_map path %q: %w", opts.Subpath, err)
		}
		if !info.IsDir() {
			abs = filepath.Dir(abs)
		}
		scanRoot = abs
	}

	m := &Map{Root: base, Subpath: opts.Subpath, Focus: opts.Focus}

	candidates, scanned, skipped, err := scanFiles(ctx, base, scanRoot)
	if err != nil {
		return nil, err
	}
	m.Scanned = scanned
	m.Skipped = skipped
	if len(candidates) == 0 {
		m.Text = "Repository map: no code files found" + subpathNote(opts.Subpath) + ".\n"
		m.Tokens = ctxmgr.EstimateTokens(m.Text)
		m.BuildMS = time.Since(start).Milliseconds()
		return m, nil
	}

	entries := make([]FileEntry, 0, len(candidates))
	asts := make(map[string]*ast.File)
	for _, rf := range candidates {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		e, tree := analyze(rf)
		entries = append(entries, e)
		if tree != nil {
			asts[e.Path] = tree
		}
	}

	refs := refCounts(entries, asts)
	for i := range entries {
		entries[i].Score = score(&entries[i], refs[entries[i].Path], opts.Focus)
	}
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].Score != entries[j].Score {
			return entries[i].Score > entries[j].Score
		}
		return entries[i].Path < entries[j].Path
	})
	if len(entries) > opts.MaxFiles {
		entries = entries[:opts.MaxFiles]
		m.Truncated = true
	}

	m.Text = render(m, entries, opts.MaxTokens)
	m.Tokens = ctxmgr.EstimateTokens(m.Text)
	m.BuildMS = time.Since(start).Milliseconds()
	return m, nil
}

// scanFiles collects code candidates under scanRoot. Rel paths are relative to
// base (the workspace root) so the model always sees workspace paths.
func scanFiles(ctx context.Context, base, scanRoot string) ([]rawFile, int, int, error) {
	var out []rawFile
	scanned, skipped := 0, 0
	err := filepath.WalkDir(scanRoot, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil // skip unreadable entries
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		name := d.Name()
		if d.IsDir() {
			if path != scanRoot && (skipDirs[name] || name == "testdata") {
				return filepath.SkipDir
			}
			return nil
		}
		lang := langForName(name)
		if lang == "" || isGenerated(name) {
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil {
			return nil
		}
		if info.Size() > maxFileBytes {
			skipped++
			return nil
		}
		rel, rerr := filepath.Rel(base, path)
		if rerr != nil {
			rel = name
		}
		scanned++
		if scanned > DefaultScanCap {
			return fs.SkipAll
		}
		out = append(out, rawFile{abs: path, rel: filepath.ToSlash(rel), lang: lang})
		return nil
	})
	if err != nil && err != fs.SkipAll {
		return out, scanned, skipped, err
	}
	return out, scanned, skipped, nil
}

// analyze extracts symbols from one candidate. tree is non-nil only for
// successfully parsed Go files.
func analyze(rf rawFile) (FileEntry, *ast.File) {
	if rf.lang != "go" {
		return FileEntry{Path: rf.rel, Lang: rf.lang}, nil
	}
	src, err := os.ReadFile(rf.abs)
	if err != nil {
		return FileEntry{Path: rf.rel, Lang: "go"}, nil
	}
	fset := token.NewFileSet()
	f, perr := parser.ParseFile(fset, rf.abs, src, parser.SkipObjectResolution)
	if perr != nil || f == nil {
		return FileEntry{Path: rf.rel, Lang: "go", Lines: countLines(src)}, nil
	}
	e := FileEntry{
		Path:    rf.rel,
		Lang:    "go",
		Package: f.Name.Name,
		Lines:   fset.Position(f.End()).Line,
		Symbols: extractSymbols(fset, f),
	}
	return e, f
}

func extractSymbols(fset *token.FileSet, f *ast.File) []Symbol {
	var out []Symbol
	for _, decl := range f.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			name := d.Name.Name
			kind := "func"
			if d.Recv != nil && len(d.Recv.List) > 0 {
				name = recvName(d.Recv.List[0].Type) + "." + name
				kind = "method"
			}
			out = append(out, Symbol{
				Kind: kind,
				Name: name,
				Sig:  compact("func " + name + sigOf(fset, d.Type)),
				Line: fset.Position(d.Pos()).Line,
			})
		case *ast.GenDecl:
			switch d.Tok {
			case token.TYPE:
				for _, spec := range d.Specs {
					ts, ok := spec.(*ast.TypeSpec)
					if !ok || ts.Name.Name == "_" {
						continue
					}
					out = append(out, Symbol{
						Kind: kindOfType(ts.Type),
						Name: ts.Name.Name,
						Line: fset.Position(ts.Pos()).Line,
					})
				}
			case token.CONST, token.VAR:
				kind := strings.ToLower(d.Tok.String())
				for _, spec := range d.Specs {
					vs, ok := spec.(*ast.ValueSpec)
					if !ok {
						continue
					}
					for _, n := range vs.Names {
						if n.Name == "_" {
							continue
						}
						out = append(out, Symbol{Kind: kind, Name: n.Name, Line: fset.Position(n.Pos()).Line})
					}
				}
			}
		}
		if len(out) >= maxSymbolsFile {
			break
		}
	}
	if len(out) > maxSymbolsFile {
		out = out[:maxSymbolsFile]
	}
	return out
}

func recvName(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.StarExpr:
		return recvName(t.X)
	case *ast.Ident:
		return t.Name
	case *ast.IndexExpr:
		return recvName(t.X)
	case *ast.IndexListExpr:
		return recvName(t.X)
	}
	return "?"
}

func sigOf(fset *token.FileSet, ft *ast.FuncType) string {
	if ft == nil {
		return "(...)"
	}
	var b strings.Builder
	if err := printer.Fprint(&b, fset, ft); err != nil {
		return "(...)"
	}
	// printer renders the func keyword; callers add "func <name>" themselves.
	return strings.TrimPrefix(strings.TrimSpace(b.String()), "func")
}

func kindOfType(e ast.Expr) string {
	switch e.(type) {
	case *ast.StructType:
		return "struct"
	case *ast.InterfaceType:
		return "interface"
	default:
		return "type"
	}
}

// refCounts is a cheap centrality proxy: for each Go file, how many identifiers
// it uses that are exported top-level names defined in a different file.
func refCounts(entries []FileEntry, asts map[string]*ast.File) map[string]int {
	defined := map[string]string{}
	for _, e := range entries {
		for _, s := range e.Symbols {
			name := lastName(s.Name)
			if !isExported(name) {
				continue
			}
			if _, ok := defined[name]; !ok {
				defined[name] = e.Path
			}
		}
	}
	refs := make(map[string]int, len(asts))
	for path, f := range asts {
		local := map[string]bool{}
		for _, s := range entries {
			if s.Path == path {
				for _, sym := range s.Symbols {
					local[lastName(sym.Name)] = true
				}
			}
		}
		ast.Inspect(f, func(n ast.Node) bool {
			id, ok := n.(*ast.Ident)
			if !ok {
				return true
			}
			if def, ok := defined[id.Name]; ok && def != path && !local[id.Name] {
				refs[path]++
			}
			return true
		})
	}
	return refs
}

func score(e *FileEntry, refs int, focus string) int {
	s := 0
	exported := 0
	for _, sym := range e.Symbols {
		if isExported(lastName(sym.Name)) {
			exported++
		}
	}
	// Cap the raw definition count so a couple of huge files cannot crowd out
	// the rest of the repository; cross-file references carry more signal.
	defs := len(e.Symbols)
	if defs > scoreSymbolCap {
		defs = scoreSymbolCap
	}
	s += defs + exported*2 + refs*2
	if strings.HasSuffix(e.Path, "_test.go") {
		s -= 5
	}
	if focus != "" {
		lp := strings.ToLower(focus)
		if strings.Contains(strings.ToLower(e.Path), lp) {
			s += 50
		}
		for _, sym := range e.Symbols {
			if strings.Contains(strings.ToLower(sym.Name), lp) {
				s += 20
			}
		}
	}
	return s
}

// render writes ranked files until the token budget is exhausted. Top files get
// path + symbols; the remainder are listed path-only for breadth, so navigation
// still works even when the budget cannot cover every symbol.
func render(m *Map, entries []FileEntry, maxTokens int) string {
	var b strings.Builder
	b.WriteString("Repository map (ranked by structural relevance; workspace-relative paths, Go top-level symbols).\n")
	used := ctxmgr.EstimateTokens(b.String())

	included := 0
	for _, e := range entries {
		block := renderDetail(e)
		bt := ctxmgr.EstimateTokens(block)
		if included > 0 && used+bt > maxTokens {
			break
		}
		b.WriteString(block)
		used += bt
		included++
	}

	// Path-only tier for the remaining files so the model still sees the layout.
	paths := 0
	if included < len(entries) {
		var pb strings.Builder
		pb.WriteString("Other files:\n")
		for _, e := range entries[included:] {
			line := e.Path + " [" + e.Lang + "]\n"
			lt := ctxmgr.EstimateTokens(line)
			if used+lt > maxTokens {
				break
			}
			pb.WriteString(line)
			used += lt
			paths++
		}
		if paths > 0 {
			b.WriteString(pb.String())
		}
	}

	total := included + paths
	if total < len(entries) {
		m.Truncated = true
		fmt.Fprintf(&b, "... (truncated: %d more file(s); call repo_map(path=...) to focus a subtree)\n", len(entries)-total)
	}
	m.Files = entries[:total]
	return b.String()
}

func renderDetail(e FileEntry) string {
	var fb strings.Builder
	fb.WriteString(e.Path)
	fmt.Fprintf(&fb, " [%s", e.Lang)
	if e.Package != "" {
		fmt.Fprintf(&fb, " pkg %s", e.Package)
	}
	if e.Lines > 0 {
		fmt.Fprintf(&fb, ", %d lines", e.Lines)
	}
	fb.WriteString("]\n")
	for _, sym := range e.Symbols {
		if sym.Sig != "" {
			fmt.Fprintf(&fb, "  %s\n", sym.Sig)
		} else {
			fmt.Fprintf(&fb, "  %s %s\n", sym.Kind, sym.Name)
		}
	}
	return fb.String()
}

func subpathNote(sub string) string {
	if sub == "" || sub == "." {
		return ""
	}
	return " under " + sub
}

func compact(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= maxSigLen {
		return s
	}
	end := maxSigLen
	for end > 0 && !utf8.RuneStart(s[end]) {
		end--
	}
	return s[:end] + "…"
}

func countLines(src []byte) int {
	if len(src) == 0 {
		return 0
	}
	n := 1
	for _, c := range src {
		if c == '\n' {
			n++
		}
	}
	return n
}

func lastName(name string) string {
	if i := strings.LastIndex(name, "."); i >= 0 {
		return name[i+1:]
	}
	return name
}

func isExported(name string) bool {
	if name == "" {
		return false
	}
	r, _ := utf8.DecodeRuneInString(name)
	return r >= 'A' && r <= 'Z'
}

// inside resolves a workspace-relative subpath and rejects escapes.
func inside(root, p string) (string, error) {
	candidate := filepath.Clean(filepath.Join(root, p))
	if !underWorkspace(root, candidate) {
		return "", fmt.Errorf("repo_map path %q escapes workspace", p)
	}
	return candidate, nil
}

func underWorkspace(root, abs string) bool {
	root = filepath.Clean(root)
	abs = filepath.Clean(abs)
	if strings.EqualFold(abs, root) {
		return true
	}
	prefix := root + string(filepath.Separator)
	return len(abs) > len(root) && strings.EqualFold(abs[:len(prefix)], prefix)
}

// langForName returns a language label for recognized code file names, or "".
func langForName(name string) string {
	switch strings.ToLower(name) {
	case "makefile":
		return "make"
	case "dockerfile":
		return "docker"
	case "go.mod":
		return "gomod"
	}
	return langByExt[strings.ToLower(filepath.Ext(name))]
}

var langByExt = map[string]string{
	".go": "go",
	".js": "js", ".jsx": "js", ".mjs": "js", ".cjs": "js",
	".ts": "ts", ".tsx": "ts",
	".py":   "py",
	".rb":   "rb",
	".rs":   "rs",
	".java": "java",
	".kt":   "kt", ".kts": "kt",
	".c": "c", ".h": "c",
	".cc": "cpp", ".cpp": "cpp", ".cxx": "cpp", ".hpp": "cpp",
	".cs":    "cs",
	".php":   "php",
	".swift": "swift",
	".scala": "scala",
	".sh":    "sh", ".bash": "sh", ".zsh": "sh",
	".ps1":    "ps1",
	".sql":    "sql",
	".proto":  "proto",
	".vue":    "vue",
	".svelte": "svelte",
	".lua":    "lua",
	".dart":   "dart",
	".ex":     "ex", ".exs": "ex",
	".clj":  "clj",
	".hs":   "hs",
	".zig":  "zig",
	".toml": "toml",
}

// isGenerated skips machine-generated files that would drown out hand-written code.
func isGenerated(name string) bool {
	lower := strings.ToLower(name)
	return strings.HasSuffix(lower, ".pb.go") || strings.HasPrefix(lower, "zz_generated")
}
