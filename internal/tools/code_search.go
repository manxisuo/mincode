package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/manxisuo/mincode/internal/codesearch"
)

const (
	codeSearchDefaultK = 6
	codeSearchHardK    = 15
)

// CodeSearch exposes lexical, query-relevant code retrieval as a read-only
// tool over the workspace index.
type CodeSearch struct {
	Searcher codesearch.Searcher
	// DefaultK caps results when the model does not specify one.
	DefaultK int
}

func (t *CodeSearch) Name() string { return "code_search" }

func (t *CodeSearch) Description() string {
	return `Find the most relevant code chunks for a natural-language or symbol query, ranked lexically over file paths, package names, symbol names and signatures.
Use it when you know roughly what you are looking for (e.g. "context token budget", "shell permission classify") to jump to the right symbols instead of guessing glob/grep. Treat returned paths as candidates and read_file the exact spots before editing.`
}

func (t *CodeSearch) Schema() JSONSchema {
	return JSONSchema{
		"type": "object",
		"properties": map[string]any{
			"query": map[string]any{
				"type":        "string",
				"description": "Search query: words, a symbol name, or a short phrase",
			},
			"max_results": map[string]any{
				"type":        "integer",
				"description": "Maximum results (default 6, max 15)",
			},
		},
		"required": []string{"query"},
	}
}

type codeSearchArgs struct {
	Query      string `json:"query"`
	MaxResults int    `json:"max_results"`
}

func (t *CodeSearch) Execute(ctx context.Context, raw json.RawMessage) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if t.Searcher == nil {
		return Result{Content: "code search not configured", IsError: true}, nil
	}
	var args codeSearchArgs
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &args); err != nil {
			return Result{Content: "invalid arguments: " + err.Error(), IsError: true}, nil
		}
	}
	query := strings.TrimSpace(args.Query)
	if query == "" {
		return Result{Content: "query is required", IsError: true}, nil
	}
	k := args.MaxResults
	if k <= 0 {
		k = t.DefaultK
	}
	if k <= 0 {
		k = codeSearchDefaultK
	}
	if k > codeSearchHardK {
		k = codeSearchHardK
	}

	start := time.Now()
	hits, err := t.Searcher.Search(ctx, query, k)
	elapsed := time.Since(start)
	if err != nil {
		return Result{Content: "code_search: " + err.Error(), IsError: true}, nil
	}
	if len(hits) == 0 {
		return Result{
			Content: "no matches",
			Meta:    map[string]any{"query": query, "results": 0, "duration_ms": elapsed.Milliseconds()},
		}, nil
	}

	top := make([]string, 0, len(hits))
	for _, h := range hits {
		if h.Line > 0 {
			top = append(top, fmt.Sprintf("%s:%d", h.Path, h.Line))
		} else {
			top = append(top, h.Path)
		}
	}
	return Result{
		Content: codesearch.Render(query, hits, 0),
		Meta: map[string]any{
			"query":       query,
			"results":     len(hits),
			"top":         top,
			"duration_ms": elapsed.Milliseconds(),
		},
	}, nil
}
