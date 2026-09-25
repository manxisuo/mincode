package websearch

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestFakeProviderScriptedOrder(t *testing.T) {
	f := NewFakeProvider(
		SearchResponse{Results: []Result{{Title: "first", URL: "https://a.example"}}},
		SearchResponse{Results: []Result{{Title: "second", URL: "https://b.example"}}},
	)
	r1, err := f.Search(context.Background(), SearchRequest{Query: "q1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(r1.Results) != 1 || r1.Results[0].Title != "first" {
		t.Fatalf("r1 = %+v", r1)
	}
	r2, err := f.Search(context.Background(), SearchRequest{Query: "q2"})
	if err != nil {
		t.Fatal(err)
	}
	if r2.Results[0].Title != "second" {
		t.Fatalf("r2 = %+v", r2)
	}
	// Last response repeats.
	r3, err := f.Search(context.Background(), SearchRequest{Query: "q3"})
	if err != nil {
		t.Fatal(err)
	}
	if r3.Results[0].Title != "second" {
		t.Fatalf("r3 = %+v", r3)
	}
	if f.Calls() != 3 {
		t.Fatalf("calls = %d", f.Calls())
	}
}

func TestFakeProviderEchoesQuery(t *testing.T) {
	f := NewFakeProvider(SearchResponse{Results: []Result{{Title: "x"}}})
	resp, err := f.Search(context.Background(), SearchRequest{Query: "golang context"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Query != "golang context" {
		t.Fatalf("query = %q", resp.Query)
	}
}

func TestFakeProviderMaxResults(t *testing.T) {
	f := NewFakeProvider(SearchResponse{Results: []Result{
		{Title: "a"}, {Title: "b"}, {Title: "c"},
	}})
	resp, err := f.Search(context.Background(), SearchRequest{Query: "q", MaxResults: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Results) != 2 {
		t.Fatalf("results = %+v", resp.Results)
	}
	// Unlimited when MaxResults <= 0.
	resp2, _ := f.Search(context.Background(), SearchRequest{Query: "q"})
	if len(resp2.Results) != 3 {
		t.Fatalf("results = %+v", resp2.Results)
	}
}

func TestFakeProviderEmptyQuery(t *testing.T) {
	f := NewFakeProvider(SearchResponse{})
	if _, err := f.Search(context.Background(), SearchRequest{Query: "  "}); !errors.Is(err, ErrEmptyQuery) {
		t.Fatalf("err = %v, want ErrEmptyQuery", err)
	}
}

func TestFakeProviderError(t *testing.T) {
	f := NewFakeProvider(SearchResponse{})
	f.Err = errors.New("forced")
	if _, err := f.Search(context.Background(), SearchRequest{Query: "q"}); err == nil {
		t.Fatal("expected error")
	}
}

func TestFakeProviderCancelled(t *testing.T) {
	f := NewFakeProvider(SearchResponse{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := f.Search(ctx, SearchRequest{Query: "q"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}

func TestFakeProviderRecordsRequests(t *testing.T) {
	f := NewFakeProvider(SearchResponse{})
	_, _ = f.Search(context.Background(), SearchRequest{Query: "one"})
	_, _ = f.Search(context.Background(), SearchRequest{Query: "two", MaxResults: 5})

	reqs := f.Requests()
	if len(reqs) != 2 {
		t.Fatalf("reqs = %+v", reqs)
	}
	if reqs[0].Query != "one" || reqs[1].Query != "two" || reqs[1].MaxResults != 5 {
		t.Fatalf("reqs = %+v", reqs)
	}
}

func TestFakeProviderHook(t *testing.T) {
	f := NewFakeProvider(SearchResponse{})
	var seen string
	f.OnSearch = func(req SearchRequest) { seen = req.Query }
	_, _ = f.Search(context.Background(), SearchRequest{Query: "hooked"})
	if seen != "hooked" {
		t.Fatalf("hook saw %q", seen)
	}
}

func TestProviderErrorMessage(t *testing.T) {
	pe := &ProviderError{Provider: "tavily", Status: 429, Body: "rate limited"}
	if !strings.Contains(pe.Error(), "tavily") || !strings.Contains(pe.Error(), "429") {
		t.Fatalf("error = %q", pe.Error())
	}
	wrapped := &ProviderError{Provider: "tavily", Err: context.DeadlineExceeded}
	if !errors.Is(wrapped, context.DeadlineExceeded) {
		t.Fatal("Unwrap should expose the underlying error")
	}
}
