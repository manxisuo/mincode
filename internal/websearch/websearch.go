// Package websearch defines the replaceable web-search backend abstraction.
//
// The core runtime depends only on the Provider interface, never on a specific
// search vendor. Real HTTP-backed implementations and a FakeProvider for tests
// both satisfy it, mirroring internal/llm.
package websearch

import (
	"context"
	"errors"
	"fmt"
	"unicode/utf8"
)

// Provider is the replaceable web-search backend interface.
type Provider interface {
	// Name returns a short provider identifier (e.g. "tavily", "fake").
	Name() string
	// Search runs one query and returns ranked results.
	Search(ctx context.Context, req SearchRequest) (*SearchResponse, error)
}

// SearchRequest is a provider-agnostic search query.
type SearchRequest struct {
	Query      string
	MaxResults int
}

// Result is one ranked search hit.
type Result struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet,omitempty"`
}

// SearchResponse is a provider-agnostic search response.
type SearchResponse struct {
	Query   string   `json:"query"`
	Results []Result `json:"results"`
	Usage   Usage    `json:"usage,omitempty"`
}

// Usage records provider-reported cost for one search.
type Usage struct {
	Requests   int   `json:"requests,omitempty"`
	DurationMS int64 `json:"duration_ms,omitempty"`
}

// Sentinel errors for search failures.
var (
	ErrEmptyQuery = errors.New("websearch: empty query")
	ErrNoResult   = errors.New("websearch: no results")
)

// ProviderError wraps a search-backend failure with context.
type ProviderError struct {
	Provider string
	Status   int
	Body     string
	Err      error
}

func (e *ProviderError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%s search error (status %d): %v", e.Provider, e.Status, e.Err)
	}
	return fmt.Sprintf("%s search error (status %d): %s", e.Provider, e.Status, truncate(e.Body, 200))
}

func (e *ProviderError) Unwrap() error { return e.Err }

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	// Avoid splitting a multi-byte UTF-8 rune.
	end := n
	for end > 0 && !utf8.RuneStart(s[end]) {
		end--
	}
	return s[:end] + "..."
}
