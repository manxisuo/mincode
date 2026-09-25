// Package webfetch defines the replaceable URL-fetching backend abstraction.
//
// Unlike web search, fetching a known URL needs no third-party service: the
// core runtime depends only on the Fetcher interface, with an HTTP-backed
// implementation and a FakeFetcher for tests, mirroring internal/websearch.
package webfetch

import (
	"context"
	"errors"
	"fmt"
)

// Fetcher is the replaceable URL-fetch backend interface.
type Fetcher interface {
	// Name returns a short fetcher identifier (e.g. "http", "fake").
	Name() string
	// Fetch retrieves one URL and returns its extracted text.
	Fetch(ctx context.Context, req Request) (*Response, error)
}

// Request is a provider-agnostic fetch request.
type Request struct {
	URL string
	// MaxBytes caps how much of the response body is read (0 = implementation default).
	MaxBytes int
}

// Response is the extracted result of one fetch.
type Response struct {
	// URL is the final URL after redirects.
	URL         string `json:"url"`
	Status      int    `json:"status"`
	ContentType string `json:"content_type,omitempty"`
	Title       string `json:"title,omitempty"`
	Text        string `json:"text"`
	// Bytes is how many raw response bytes were read.
	Bytes int `json:"bytes"`
	// Truncated is true when the body exceeded the byte cap.
	Truncated bool `json:"truncated,omitempty"`
}

// Sentinel errors for fetch failures.
var (
	ErrEmptyURL    = errors.New("webfetch: empty url")
	ErrBadScheme   = errors.New("webfetch: only http and https are allowed")
	ErrBlockedHost = errors.New("webfetch: target address is blocked (private/loopback/link-local)")
	ErrNotText     = errors.New("webfetch: response is not textual")
)

// FetchError wraps an HTTP-level fetch failure with context.
type FetchError struct {
	URL    string
	Status int
	Err    error
}

func (e *FetchError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("webfetch %s: %v", e.URL, e.Err)
	}
	return fmt.Sprintf("webfetch %s: HTTP %d", e.URL, e.Status)
}

func (e *FetchError) Unwrap() error { return e.Err }
