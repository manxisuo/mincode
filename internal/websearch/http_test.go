package websearch

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHTTPProviderSearch(t *testing.T) {
	var gotPath, gotAuth string
	var gotBody httpSearchRequest

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"results": []map[string]any{
				{"title": "Go", "url": "https://go.dev", "content": "The Go language", "score": 0.9},
				{"title": "Docs", "url": "https://pkg.go.dev", "content": "Package docs", "score": 0.8},
			},
			"response_time": 0.42,
		})
	}))
	defer srv.Close()

	p := NewHTTPProvider(srv.URL, "tvly-secret", 0)
	resp, err := p.Search(context.Background(), SearchRequest{Query: "golang", MaxResults: 5})
	if err != nil {
		t.Fatal(err)
	}
	if p.Name() != "tavily" {
		t.Fatalf("name = %q", p.Name())
	}
	if gotPath != "/search" {
		t.Fatalf("path = %q", gotPath)
	}
	if gotAuth != "Bearer tvly-secret" {
		t.Fatalf("auth = %q", gotAuth)
	}
	if gotBody.Query != "golang" || gotBody.APIKey != "tvly-secret" || gotBody.MaxResults != 5 {
		t.Fatalf("body = %+v", gotBody)
	}
	if resp.Query != "golang" || len(resp.Results) != 2 {
		t.Fatalf("resp = %+v", resp)
	}
	if resp.Results[0].Title != "Go" || resp.Results[0].URL != "https://go.dev" || resp.Results[0].Snippet != "The Go language" {
		t.Fatalf("result[0] = %+v", resp.Results[0])
	}
	if resp.Usage.Requests != 1 {
		t.Fatalf("usage = %+v", resp.Usage)
	}
}

func TestHTTPProviderTruncatesToMaxResults(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"results": []map[string]any{
				{"title": "a", "url": "https://a"}, {"title": "b", "url": "https://b"}, {"title": "c", "url": "https://c"},
			},
		})
	}))
	defer srv.Close()

	p := NewHTTPProvider(srv.URL, "k", 0)
	resp, err := p.Search(context.Background(), SearchRequest{Query: "q", MaxResults: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Results) != 2 {
		t.Fatalf("results = %d, want 2", len(resp.Results))
	}
}

func TestHTTPProviderHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"detail":"bad key"}`))
	}))
	defer srv.Close()

	p := NewHTTPProvider(srv.URL, "bad", 0)
	_, err := p.Search(context.Background(), SearchRequest{Query: "q"})
	var pe *ProviderError
	if !errors.As(err, &pe) {
		t.Fatalf("err = %v, want ProviderError", err)
	}
	if pe.Status != http.StatusUnauthorized {
		t.Fatalf("status = %d", pe.Status)
	}
	if !strings.Contains(pe.Error(), "401") {
		t.Fatalf("error = %q", pe.Error())
	}
}

func TestHTTPProviderNoAPIKey(t *testing.T) {
	p := NewHTTPProvider("http://localhost", "", 0)
	if _, err := p.Search(context.Background(), SearchRequest{Query: "q"}); !errors.Is(err, ErrNoAPIKey) {
		t.Fatalf("err = %v, want ErrNoAPIKey", err)
	}
}

func TestHTTPProviderEmptyQuery(t *testing.T) {
	p := NewHTTPProvider("http://localhost", "k", 0)
	if _, err := p.Search(context.Background(), SearchRequest{Query: "  "}); !errors.Is(err, ErrEmptyQuery) {
		t.Fatalf("err = %v, want ErrEmptyQuery", err)
	}
}

func TestHTTPProviderMalformedResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{not json`))
	}))
	defer srv.Close()

	p := NewHTTPProvider(srv.URL, "k", 0)
	_, err := p.Search(context.Background(), SearchRequest{Query: "q"})
	var pe *ProviderError
	if !errors.As(err, &pe) {
		t.Fatalf("err = %v, want ProviderError", err)
	}
}

func TestHTTPProviderCancelled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()

	p := NewHTTPProvider(srv.URL, "k", 0)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.Search(ctx, SearchRequest{Query: "q"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestHTTPProviderDefaultBaseURL(t *testing.T) {
	p := NewHTTPProvider("", "k", 0)
	if p.BaseURL != DefaultBaseURL {
		t.Fatalf("base url = %q, want %q", p.BaseURL, DefaultBaseURL)
	}
	// Trailing slash is trimmed so "/search" is not doubled.
	p2 := NewHTTPProvider("https://example.com/", "k", 0)
	if p2.BaseURL != "https://example.com" {
		t.Fatalf("base url = %q", p2.BaseURL)
	}
}
