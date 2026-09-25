package websearch

import (
	"context"
	"strings"
	"sync"
)

// FakeProvider is a scripted search backend for tests and offline demos.
type FakeProvider struct {
	mu sync.Mutex
	// Responses are returned in order; the last one repeats.
	Responses []SearchResponse
	// Err, if set, is returned on every Search call.
	Err error
	// OnSearch is an optional hook invoked with each request.
	OnSearch func(req SearchRequest)

	calls int
	reqs  []SearchRequest
}

// NewFakeProvider creates a fake provider that returns the given responses in order.
func NewFakeProvider(responses ...SearchResponse) *FakeProvider {
	return &FakeProvider{Responses: responses}
}

// Name implements Provider.
func (f *FakeProvider) Name() string { return "fake" }

// Calls returns how many times Search was invoked.
func (f *FakeProvider) Calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// Requests returns a copy of all captured requests.
func (f *FakeProvider) Requests() []SearchRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]SearchRequest, len(f.reqs))
	copy(out, f.reqs)
	return out
}

// Search implements Provider.
func (f *FakeProvider) Search(ctx context.Context, req SearchRequest) (*SearchResponse, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.Query) == "" {
		return nil, ErrEmptyQuery
	}

	f.mu.Lock()
	f.calls++
	f.reqs = append(f.reqs, req)
	err := f.Err
	var resp SearchResponse
	if len(f.Responses) > 0 {
		idx := f.calls - 1
		if idx >= len(f.Responses) {
			idx = len(f.Responses) - 1
		}
		resp = f.Responses[idx]
	}
	hook := f.OnSearch
	f.mu.Unlock()

	if hook != nil {
		hook(req)
	}
	if err != nil {
		return nil, err
	}

	if resp.Query == "" {
		resp.Query = req.Query
	}
	if req.MaxResults > 0 && len(resp.Results) > req.MaxResults {
		resp.Results = resp.Results[:req.MaxResults]
	}
	return &resp, nil
}
