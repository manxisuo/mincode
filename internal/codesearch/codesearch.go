// Package codesearch provides lexical, query-relevant code retrieval over a
// workspace. It builds on repomap's parsed structure (files + Go top-level
// symbols) and is intentionally embedding-free for the first version, so it
// stays deterministic, dependency-free and testable. The Searcher interface
// leaves room for a future embedding backend.
package codesearch

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"unicode"

	"github.com/manxisuo/mincode/internal/ctxmgr"
	"github.com/manxisuo/mincode/internal/repomap"
)

// BackendLexical is the only implemented retrieval backend.
const BackendLexical = "lexical"

const (
	defaultMaxTokens = 800
	defaultMaxChunks = 20000
	bm25K1           = 1.2
	bm25B            = 0.75
	nameTokenWeight  = 2
)

// Chunk is one searchable code unit: a top-level symbol for Go files, or the
// file itself for non-Go files.
type Chunk struct {
	Path    string `json:"path"`
	Package string `json:"package,omitempty"`
	Kind    string `json:"kind"`
	Name    string `json:"name"`
	Sig     string `json:"sig,omitempty"`
	Line    int    `json:"line,omitempty"`
	Text    string `json:"text"`

	tokens []string
}

// Hit is one ranked search result.
type Hit struct {
	Chunk
	Score   float64  `json:"score"`
	Matched []string `json:"matched,omitempty"`
}

// Searcher retrieves the top-k chunks for a query.
type Searcher interface {
	Search(ctx context.Context, query string, k int) ([]Hit, error)
}

// Options configures an Index.
type Options struct {
	// MaxTokens caps a rendered context block (0 = default 800).
	MaxTokens int
	// MaxChunks caps indexed chunks (0 = default 20000).
	MaxChunks int
	// Cache reuses Go parsing with the repo map (optional).
	Cache *repomap.Cache
}

// Index builds and searches a lexical index for one workspace.
type Index struct {
	workspace string
	maxTokens int
	maxChunks int
	cache     *repomap.Cache
}

// New creates an index over workspace.
func New(workspace string, opts Options) *Index {
	if opts.MaxTokens <= 0 {
		opts.MaxTokens = defaultMaxTokens
	}
	if opts.MaxChunks <= 0 {
		opts.MaxChunks = defaultMaxChunks
	}
	return &Index{
		workspace: workspace,
		maxTokens: opts.MaxTokens,
		maxChunks: opts.MaxChunks,
		cache:     opts.Cache,
	}
}

// Snapshot is an immutable built index.
type Snapshot struct {
	Chunks      []Chunk
	DF          map[string]int
	AvgDL       float64
	Scanned     int
	Symbols     int
	BuildMS     int64
	CacheHits   int
	CacheMisses int
}

// build scans the workspace and constructs the lexical index.
func (ix *Index) build(ctx context.Context) (*Snapshot, error) {
	m, entries, err := repomap.Scan(ctx, ix.workspace, repomap.Options{Cache: ix.cache})
	if err != nil {
		return nil, err
	}
	chunks := ix.chunksFrom(entries)

	df := make(map[string]int, len(chunks)*4)
	total := 0
	for i := range chunks {
		total += len(chunks[i].tokens)
		seen := make(map[string]bool, len(chunks[i].tokens))
		for _, t := range chunks[i].tokens {
			if !seen[t] {
				seen[t] = true
				df[t]++
			}
		}
	}
	avg := 0.0
	if len(chunks) > 0 {
		avg = float64(total) / float64(len(chunks))
	}
	return &Snapshot{
		Chunks:      chunks,
		DF:          df,
		AvgDL:       avg,
		Scanned:     m.Scanned,
		Symbols:     countSymbols(chunks),
		BuildMS:     m.BuildMS,
		CacheHits:   m.CacheHits,
		CacheMisses: m.CacheMisses,
	}, nil
}

func (ix *Index) chunksFrom(entries []repomap.FileEntry) []Chunk {
	chunks := make([]Chunk, 0, len(entries)*4)
	for _, e := range entries {
		pathToks := tokenize(e.Path)
		pkgToks := tokenize(e.Package)
		if len(e.Symbols) == 0 {
			c := Chunk{
				Path:    e.Path,
				Package: e.Package,
				Kind:    "file",
				Name:    baseName(e.Path),
				Text:    e.Path + " [" + e.Lang + "]",
			}
			c.tokens = joinTokens(pathToks, pkgToks, tokenize(e.Lang))
			chunks = append(chunks, c)
			continue
		}
		for _, s := range e.Symbols {
			label := s.Sig
			if label == "" {
				label = s.Kind + " " + s.Name
			}
			c := Chunk{
				Path:    e.Path,
				Package: e.Package,
				Kind:    s.Kind,
				Name:    s.Name,
				Sig:     s.Sig,
				Line:    s.Line,
				Text:    label,
			}
			nameToks := tokenize(lastName(s.Name))
			toks := make([]string, 0, len(nameToks)*(nameTokenWeight+1)+8)
			for i := 0; i < nameTokenWeight; i++ {
				toks = append(toks, nameToks...)
			}
			toks = append(toks, tokenize(s.Name)...)
			toks = append(toks, tokenize(s.Sig)...)
			toks = append(toks, tokenize(s.Kind)...)
			toks = append(toks, pkgToks...)
			toks = append(toks, pathToks...)
			c.tokens = toks
			chunks = append(chunks, c)
			if len(chunks) >= ix.maxChunks {
				return chunks
			}
		}
		if len(chunks) >= ix.maxChunks {
			break
		}
	}
	return chunks
}

// Search returns the top-k chunks for query (lexical BM25), best first.
func (ix *Index) Search(ctx context.Context, query string, k int) ([]Hit, error) {
	if k <= 0 {
		k = 6
	}
	snap, err := ix.build(ctx)
	if err != nil {
		return nil, err
	}
	terms := dedupe(tokenize(query))
	if len(terms) == 0 || len(snap.Chunks) == 0 {
		return nil, nil
	}

	n := float64(len(snap.Chunks))
	hits := make([]Hit, 0, len(snap.Chunks))
	for _, c := range snap.Chunks {
		tf := termFreq(c.tokens)
		dl := float64(len(c.tokens))
		score := 0.0
		var matched []string
		for _, t := range terms {
			f := float64(tf[t])
			if f == 0 {
				continue
			}
			df := float64(snap.DF[t])
			idf := math.Log(1 + (n-df+0.5)/(df+0.5))
			denom := f + bm25K1*(1-bm25B+bm25B*dl/snap.AvgDL)
			score += idf * (f * (bm25K1 + 1)) / denom
			matched = append(matched, t)
		}
		if score > 0 {
			hits = append(hits, Hit{Chunk: c, Score: score, Matched: matched})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		return hits[i].Path < hits[j].Path
	})
	if len(hits) > k {
		hits = hits[:k]
	}
	return hits, nil
}

// SearchContext searches and renders a compact block for context injection.
// It returns the rendered text (empty when nothing matched) and a short
// per-hit summary for observability.
func (ix *Index) SearchContext(ctx context.Context, query string, k int) (string, []string, error) {
	hits, err := ix.Search(ctx, query, k)
	if err != nil {
		return "", nil, err
	}
	if len(hits) == 0 {
		return "", nil, nil
	}
	text := Render(query, hits, ix.maxTokens)
	summary := make([]string, 0, len(hits))
	for _, h := range hits {
		loc := h.Path
		if h.Line > 0 {
			loc = fmt.Sprintf("%s:%d", h.Path, h.Line)
		}
		summary = append(summary, fmt.Sprintf("%s %.2f", loc, h.Score))
	}
	return text, summary, nil
}

// Render formats hits as a compact, budgeted context block.
func Render(query string, hits []Hit, maxTokens int) string {
	if maxTokens <= 0 {
		maxTokens = defaultMaxTokens
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Relevant code (lexical search: %q):\n", query)
	used := ctxmgr.EstimateTokens(b.String())
	included := 0
	for _, h := range hits {
		var line strings.Builder
		line.WriteString("  ")
		line.WriteString(h.Path)
		if h.Line > 0 {
			fmt.Fprintf(&line, ":%d", h.Line)
		}
		line.WriteString("  ")
		line.WriteString(h.Text)
		if len(h.Matched) > 0 {
			line.WriteString("  [")
			line.WriteString(strings.Join(h.Matched, " "))
			line.WriteString("]")
		}
		line.WriteString("\n")
		lt := ctxmgr.EstimateTokens(line.String())
		if included > 0 && used+lt > maxTokens {
			break
		}
		b.WriteString(line.String())
		used += lt
		included++
	}
	if included < len(hits) {
		fmt.Fprintf(&b, "... (%d more match(es); refine the query)\n", len(hits)-included)
	}
	return b.String()
}

// --- tokenization ---

// tokenize lowercases and splits an identifier/path into search terms on
// separators and camelCase boundaries. Terms shorter than 2 runes or common
// stopwords are dropped.
func tokenize(s string) []string {
	var out []string
	var cur []rune
	flush := func() {
		if len(cur) == 0 {
			return
		}
		word := strings.ToLower(string(cur))
		cur = cur[:0]
		if len([]rune(word)) < 2 || stopwords[word] {
			return
		}
		out = append(out, word)
	}
	runes := []rune(s)
	for i, r := range runes {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			if i > 0 && unicode.IsUpper(r) {
				prev := runes[i-1]
				if unicode.IsLower(prev) || unicode.IsDigit(prev) {
					flush()
				}
			}
			cur = append(cur, unicode.ToLower(r))
		default:
			flush()
		}
	}
	flush()
	return out
}

func dedupe(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := in[:0]
	for _, s := range in {
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

func termFreq(tokens []string) map[string]int {
	tf := make(map[string]int, len(tokens))
	for _, t := range tokens {
		tf[t]++
	}
	return tf
}

func joinTokens(parts ...[]string) []string {
	total := 0
	for _, p := range parts {
		total += len(p)
	}
	out := make([]string, 0, total)
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func countSymbols(chunks []Chunk) int {
	n := 0
	for _, c := range chunks {
		if c.Kind != "file" {
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

func baseName(path string) string {
	path = strings.ReplaceAll(path, "\\", "/")
	if i := strings.LastIndex(path, "/"); i >= 0 {
		return path[i+1:]
	}
	return path
}

// stopwords are terms that carry little retrieval signal. Declarations like
// func/type/const are intentionally dropped since they appear in most sigs.
var stopwords = map[string]bool{
	"the": true, "and": true, "for": true, "with": true, "this": true,
	"that": true, "from": true, "func": true, "type": true, "const": true,
	"var": true, "return": true, "package": true, "true": true, "false": true,
	"nil": true, "err": true, "error": true, "string": true, "int": true,
	"bool": true, "map": true, "struct": true, "interface": true, "any": true,
	"of": true, "to": true, "in": true, "is": true, "on": true, "or": true,
	"as": true, "an": true, "it": true, "by": true, "at": true, "be": true,
	"do": true, "if": true,
}
