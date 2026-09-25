package websearch

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// SearxNGProvider talks to a self-hosted SearxNG instance's JSON API.
// No API key is required; base_url must point at the instance root.
// The instance must allow the "json" output format (settings.yml:
// search.formats includes json).
type SearxNGProvider struct {
	BaseURL    string
	HTTPClient *http.Client
}

// NewSearxNGProvider builds a SearxNG search provider.
func NewSearxNGProvider(baseURL string, timeout time.Duration) *SearxNGProvider {
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	return &SearxNGProvider{
		BaseURL:    strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		HTTPClient: &http.Client{Timeout: timeout},
	}
}

func (p *SearxNGProvider) Name() string { return "searxng" }

type searxngResponse struct {
	Results []struct {
		URL     string  `json:"url"`
		Title   string  `json:"title"`
		Content string  `json:"content"`
		Engine  string  `json:"engine"`
		Score   float64 `json:"score"`
	} `json:"results"`
}

// Search implements Provider.
func (p *SearxNGProvider) Search(ctx context.Context, req SearchRequest) (*SearchResponse, error) {
	if strings.TrimSpace(req.Query) == "" {
		return nil, ErrEmptyQuery
	}
	if p.BaseURL == "" {
		return nil, &ProviderError{Provider: p.Name(), Err: fmt.Errorf("base_url is required for searxng")}
	}

	q := url.Values{}
	q.Set("q", req.Query)
	q.Set("format", "json")
	endpoint := p.BaseURL + "/search?" + q.Encode()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	httpReq.Header.Set("Accept", "application/json")

	start := time.Now()
	resp, err := p.HTTPClient.Do(httpReq)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, &ProviderError{Provider: p.Name(), Err: err}
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, &ProviderError{Provider: p.Name(), Status: resp.StatusCode, Err: err}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &ProviderError{Provider: p.Name(), Status: resp.StatusCode, Body: string(raw)}
	}

	var sr searxngResponse
	if err := json.Unmarshal(raw, &sr); err != nil {
		return nil, &ProviderError{Provider: p.Name(), Status: resp.StatusCode, Err: fmt.Errorf("decode response: %w", err)}
	}

	out := &SearchResponse{
		Query:   req.Query,
		Results: make([]Result, 0, len(sr.Results)),
		Usage:   Usage{Requests: 1, DurationMS: time.Since(start).Milliseconds()},
	}
	for _, r := range sr.Results {
		out.Results = append(out.Results, Result{
			Title:   r.Title,
			URL:     r.URL,
			Snippet: r.Content,
		})
	}
	if req.MaxResults > 0 && len(out.Results) > req.MaxResults {
		out.Results = out.Results[:req.MaxResults]
	}
	return out, nil
}
