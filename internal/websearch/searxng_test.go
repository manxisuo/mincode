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

func TestSearxNGSearch(t *testing.T) {
	var gotPath, gotQuery, gotFormat, gotAccept string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.Query().Get("q")
		gotFormat = r.URL.Query().Get("format")
		gotAccept = r.Header.Get("Accept")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"results": []map[string]any{
				{"title": "Go", "url": "https://go.dev", "content": "The Go language", "engine": "google"},
				{"title": "Docs", "url": "https://pkg.go.dev", "content": "Package docs", "engine": "bing"},
			},
			"number_of_results": 2,
		})
	}))
	defer srv.Close()

	p := NewSearxNGProvider(srv.URL, 0)
	if p.Name() != "searxng" {
		t.Fatalf("name = %q", p.Name())
	}

	resp, err := p.Search(context.Background(), SearchRequest{Query: "golang", MaxResults: 5})
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/search" {
		t.Fatalf("path = %q", gotPath)
	}
	if gotQuery != "golang" || gotFormat != "json" {
		t.Fatalf("query=%q format=%q", gotQuery, gotFormat)
	}
	if gotAccept != "application/json" {
		t.Fatalf("accept = %q", gotAccept)
	}
	if len(resp.Results) != 2 || resp.Results[0].Title != "Go" || resp.Results[0].URL != "https://go.dev" {
		t.Fatalf("resp = %+v", resp)
	}
	if resp.Results[0].Snippet != "The Go language" {
		t.Fatalf("snippet = %q", resp.Results[0].Snippet)
	}
	if resp.Usage.Requests != 1 {
		t.Fatalf("usage = %+v", resp.Usage)
	}
}

func TestSearxNGTruncatesToMaxResults(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"results": []map[string]any{
				{"title": "a", "url": "https://a"}, {"title": "b", "url": "https://b"}, {"title": "c", "url": "https://c"},
			},
		})
	}))
	defer srv.Close()

	p := NewSearxNGProvider(srv.URL, 0)
	resp, err := p.Search(context.Background(), SearchRequest{Query: "q", MaxResults: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Results) != 2 {
		t.Fatalf("results = %d, want 2", len(resp.Results))
	}
}

func TestSearxNGHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("json format disabled"))
	}))
	defer srv.Close()

	p := NewSearxNGProvider(srv.URL, 0)
	_, err := p.Search(context.Background(), SearchRequest{Query: "q"})
	var pe *ProviderError
	if !errors.As(err, &pe) {
		t.Fatalf("err = %v, want ProviderError", err)
	}
	if pe.Status != http.StatusForbidden {
		t.Fatalf("status = %d", pe.Status)
	}
}

func TestSearxNGNullResults(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"results":null}`))
	}))
	defer srv.Close()

	p := NewSearxNGProvider(srv.URL, 0)
	resp, err := p.Search(context.Background(), SearchRequest{Query: "q"})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Results) != 0 {
		t.Fatalf("results = %+v", resp.Results)
	}
}

func TestSearxNGMalformedResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not json"))
	}))
	defer srv.Close()

	p := NewSearxNGProvider(srv.URL, 0)
	_, err := p.Search(context.Background(), SearchRequest{Query: "q"})
	var pe *ProviderError
	if !errors.As(err, &pe) {
		t.Fatalf("err = %v, want ProviderError", err)
	}
}

func TestSearxNGEmptyQuery(t *testing.T) {
	p := NewSearxNGProvider("http://localhost:8080", 0)
	if _, err := p.Search(context.Background(), SearchRequest{Query: "  "}); !errors.Is(err, ErrEmptyQuery) {
		t.Fatalf("err = %v, want ErrEmptyQuery", err)
	}
}

func TestSearxNGMissingBaseURL(t *testing.T) {
	p := NewSearxNGProvider("", 0)
	_, err := p.Search(context.Background(), SearchRequest{Query: "q"})
	var pe *ProviderError
	if !errors.As(err, &pe) {
		t.Fatalf("err = %v, want ProviderError", err)
	}
	if !strings.Contains(err.Error(), "base_url") {
		t.Fatalf("error = %q", err.Error())
	}
}

func TestSearxNGCancelled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()

	p := NewSearxNGProvider(srv.URL, 0)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.Search(ctx, SearchRequest{Query: "q"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}
