package webfetch

import (
	"context"
	"strings"
	"sync"
)

// FakeFetcher is a scripted fetcher for tests and offline demos.
type FakeFetcher struct {
	mu sync.Mutex
	// Responses are returned in order; the last one repeats.
	Responses []Response
	// Err, if set, is returned on every Fetch call.
	Err error
	// OnFetch is an optional hook invoked with each request.
	OnFetch func(req Request)

	calls int
	reqs  []Request
}

// NewFakeFetcher creates a fake fetcher that returns the given responses in order.
func NewFakeFetcher(responses ...Response) *FakeFetcher {
	return &FakeFetcher{Responses: responses}
}

// Name implements Fetcher.
func (f *FakeFetcher) Name() string { return "fake" }

// Calls returns how many times Fetch was invoked.
func (f *FakeFetcher) Calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// Requests returns a copy of all captured requests.
func (f *FakeFetcher) Requests() []Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]Request, len(f.reqs))
	copy(out, f.reqs)
	return out
}

// Fetch implements Fetcher.
func (f *FakeFetcher) Fetch(ctx context.Context, req Request) (*Response, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.URL) == "" {
		return nil, ErrEmptyURL
	}

	f.mu.Lock()
	f.calls++
	f.reqs = append(f.reqs, req)
	err := f.Err
	var resp Response
	if len(f.Responses) > 0 {
		idx := f.calls - 1
		if idx >= len(f.Responses) {
			idx = len(f.Responses) - 1
		}
		resp = f.Responses[idx]
	}
	hook := f.OnFetch
	f.mu.Unlock()

	if hook != nil {
		hook(req)
	}
	if err != nil {
		return nil, err
	}
	if resp.URL == "" {
		resp.URL = req.URL
	}
	return &resp, nil
}
