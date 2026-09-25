package websearch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// DefaultBaseURL is the Tavily search API root.
const DefaultBaseURL = "https://api.tavily.com"

// HTTPProvider talks to a Tavily-compatible /search API.
// base_url may be overridden for proxies or self-hosted endpoints.
type HTTPProvider struct {
	BaseURL    string
	APIKey     string
	HTTPClient *http.Client
}

// NewHTTPProvider builds an HTTP search provider from config-like fields.
func NewHTTPProvider(baseURL, apiKey string, timeout time.Duration) *HTTPProvider {
	if strings.TrimSpace(baseURL) == "" {
		baseURL = DefaultBaseURL
	}
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	return &HTTPProvider{
		BaseURL:    strings.TrimRight(baseURL, "/"),
		APIKey:     apiKey,
		HTTPClient: &http.Client{Timeout: timeout},
	}
}

func (p *HTTPProvider) Name() string { return "tavily" }

type httpSearchRequest struct {
	APIKey     string `json:"api_key"`
	Query      string `json:"query"`
	MaxResults int    `json:"max_results,omitempty"`
}

type httpSearchResponse struct {
	Results []struct {
		Title   string  `json:"title"`
		URL     string  `json:"url"`
		Content string  `json:"content"`
		Score   float64 `json:"score"`
	} `json:"results"`
	ResponseTime float64 `json:"response_time"`
}

// Search implements Provider.
func (p *HTTPProvider) Search(ctx context.Context, req SearchRequest) (*SearchResponse, error) {
	if strings.TrimSpace(req.Query) == "" {
		return nil, ErrEmptyQuery
	}
	if p.APIKey == "" {
		return nil, ErrNoAPIKey
	}

	body, err := json.Marshal(httpSearchRequest{
		APIKey:     p.APIKey,
		Query:      req.Query,
		MaxResults: req.MaxResults,
	})
	if err != nil {
		return nil, fmt.Errorf("encode request: %w", err)
	}

	url := p.BaseURL + "/search"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+p.APIKey)

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

	var hr httpSearchResponse
	if err := json.Unmarshal(raw, &hr); err != nil {
		return nil, &ProviderError{Provider: p.Name(), Status: resp.StatusCode, Err: fmt.Errorf("decode response: %w", err)}
	}

	out := &SearchResponse{
		Query:   req.Query,
		Results: make([]Result, 0, len(hr.Results)),
		Usage:   Usage{Requests: 1, DurationMS: time.Since(start).Milliseconds()},
	}
	for _, r := range hr.Results {
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
