package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/manxisuo/mincode/internal/repomap"
)

const (
	repoMapDefaultTokens = repomap.DefaultMaxTokens
	repoMapHardTokens    = 4000
)

// RepoMap returns a compact structural map of the workspace (or a subdirectory)
// so the model can locate files and symbols without reading everything.
type RepoMap struct {
	WS *Workspace
	// MaxTokens is the default map budget when the model does not specify one.
	MaxTokens int
	// MaxFiles caps rendered files.
	MaxFiles int
	// Cache, when set, reuses parsing across repeated calls.
	Cache *repomap.Cache
}

func (t *RepoMap) Name() string { return "repo_map" }

func (t *RepoMap) Description() string {
	return `Return a compact structural map of the workspace: workspace-relative paths plus top-level symbols for Go files, ranked by structural relevance.
Use it FIRST to orient in an unfamiliar repository, then read_file the specific paths it highlights.
Pass "path" to focus on a subdirectory and "focus" with a term (e.g. a package or symbol) to boost matching files.`
}

func (t *RepoMap) Schema() JSONSchema {
	return JSONSchema{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "Optional workspace-relative directory to map (default: workspace root)",
			},
			"focus": map[string]any{
				"type":        "string",
				"description": "Optional term; files/symbols containing it are ranked first",
			},
			"max_tokens": map[string]any{
				"type":        "integer",
				"description": "Response size budget in estimated tokens (default 1500, max 4000)",
			},
		},
	}
}

type repoMapArgs struct {
	Path      string `json:"path"`
	Focus     string `json:"focus"`
	MaxTokens int    `json:"max_tokens"`
}

func (t *RepoMap) Execute(ctx context.Context, raw json.RawMessage) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if t.WS == nil {
		return Result{Content: "workspace not configured", IsError: true}, nil
	}
	var args repoMapArgs
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &args); err != nil {
			return Result{Content: "invalid arguments: " + err.Error(), IsError: true}, nil
		}
	}

	sub := strings.TrimSpace(args.Path)
	var rel string
	if sub != "" && sub != "." {
		abs, err := t.WS.Resolve(sub)
		if err != nil {
			return Result{Content: err.Error(), IsError: true}, nil
		}
		info, err := os.Stat(abs)
		if err != nil {
			return Result{Content: "stat " + sub + ": " + err.Error(), IsError: true}, nil
		}
		if !info.IsDir() {
			abs = filepath.Dir(abs)
		}
		if r, err := filepath.Rel(t.WS.Root(), abs); err == nil {
			rel = filepath.ToSlash(r)
		}
	}

	budget := args.MaxTokens
	if budget <= 0 {
		budget = t.MaxTokens
	}
	if budget <= 0 {
		budget = repoMapDefaultTokens
	}
	if budget > repoMapHardTokens {
		budget = repoMapHardTokens
	}

	m, err := repomap.Build(ctx, t.WS.Root(), repomap.Options{
		MaxTokens: budget,
		MaxFiles:  t.MaxFiles,
		Subpath:   rel,
		Focus:     strings.TrimSpace(args.Focus),
		Cache:     t.Cache,
	})
	if err != nil {
		return Result{Content: "repo_map: " + err.Error(), IsError: true}, nil
	}

	return Result{
		Content: m.Text,
		Meta: map[string]any{
			"path":         rel,
			"focus":        strings.TrimSpace(args.Focus),
			"files":        len(m.Files),
			"symbols":      countSymbols(m),
			"tokens":       m.Tokens,
			"scanned":      m.Scanned,
			"skipped":      m.Skipped,
			"truncated":    m.Truncated,
			"build_ms":     m.BuildMS,
			"cache_hits":   m.CacheHits,
			"cache_misses": m.CacheMisses,
		},
	}, nil
}

func countSymbols(m *repomap.Map) int {
	n := 0
	for _, f := range m.Files {
		n += len(f.Symbols)
	}
	return n
}
