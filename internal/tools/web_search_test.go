package tools

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/manxisuo/mincode/internal/websearch"
)

func TestWebSearchFormatsResults(t *testing.T) {
	fake := websearch.NewFakeProvider(websearch.SearchResponse{
		Results: []websearch.Result{
			{Title: "Result A", URL: "https://a.example", Snippet: "first snippet"},
			{Title: "Result B", URL: "https://b.example", Snippet: "second snippet"},
		},
	})
	tool := &WebSearch{Provider: fake}

	res, err := tool.Execute(context.Background(), mustJSON(t, map[string]any{"query": "golang"}))
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("unexpected error result: %s", res.Content)
	}
	for _, want := range []string{"2 result(s)", "Result A", "https://a.example", "first snippet", "Result B", "https://b.example"} {
		if !strings.Contains(res.Content, want) {
			t.Fatalf("missing %q in:\n%s", want, res.Content)
		}
	}
	if res.Meta["count"] != 2 {
		t.Fatalf("meta count = %v", res.Meta["count"])
	}
}

func TestWebSearchRequiresQuery(t *testing.T) {
	tool := &WebSearch{Provider: websearch.NewFakeProvider(websearch.SearchResponse{})}
	res, err := tool.Execute(context.Background(), mustJSON(t, map[string]any{"query": "   "}))
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError || !strings.Contains(res.Content, "query is required") {
		t.Fatalf("res = %+v", res)
	}
}

func TestWebSearchInvalidArguments(t *testing.T) {
	tool := &WebSearch{Provider: websearch.NewFakeProvider(websearch.SearchResponse{})}
	res, err := tool.Execute(context.Background(), []byte("{not json"))
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError || !strings.Contains(res.Content, "invalid arguments") {
		t.Fatalf("res = %+v", res)
	}
}

func TestWebSearchNoProvider(t *testing.T) {
	tool := &WebSearch{}
	res, err := tool.Execute(context.Background(), mustJSON(t, map[string]any{"query": "x"}))
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError || !strings.Contains(res.Content, "not configured") {
		t.Fatalf("res = %+v", res)
	}
}

func TestWebSearchProviderError(t *testing.T) {
	fake := websearch.NewFakeProvider(websearch.SearchResponse{})
	fake.Err = errors.New("boom")
	tool := &WebSearch{Provider: fake}

	res, err := tool.Execute(context.Background(), mustJSON(t, map[string]any{"query": "x"}))
	if err != nil {
		t.Fatalf("provider error should be recoverable, got err = %v", err)
	}
	if !res.IsError || !strings.Contains(res.Content, "search failed") {
		t.Fatalf("res = %+v", res)
	}
}

func TestWebSearchEmptyResults(t *testing.T) {
	fake := websearch.NewFakeProvider(websearch.SearchResponse{Results: nil})
	tool := &WebSearch{Provider: fake}

	res, err := tool.Execute(context.Background(), mustJSON(t, map[string]any{"query": "nothing"}))
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("empty results should not be an error: %s", res.Content)
	}
	if !strings.Contains(res.Content, "no results") {
		t.Fatalf("content = %q", res.Content)
	}
	if res.Meta["count"] != 0 {
		t.Fatalf("meta count = %v", res.Meta["count"])
	}
}

func TestWebSearchMaxResultsDefaultAndClamp(t *testing.T) {
	fake := websearch.NewFakeProvider(websearch.SearchResponse{Results: []websearch.Result{{Title: "x"}}})
	tool := &WebSearch{Provider: fake}

	if _, err := tool.Execute(context.Background(), mustJSON(t, map[string]any{"query": "a"})); err != nil {
		t.Fatal(err)
	}
	if got := fake.Requests()[0].MaxResults; got != webSearchDefaultMaxResults {
		t.Fatalf("default max = %d, want %d", got, webSearchDefaultMaxResults)
	}

	if _, err := tool.Execute(context.Background(), mustJSON(t, map[string]any{"query": "b", "max_results": 50})); err != nil {
		t.Fatal(err)
	}
	if got := fake.Requests()[1].MaxResults; got != webSearchHardMaxResults {
		t.Fatalf("clamped max = %d, want %d", got, webSearchHardMaxResults)
	}

	if _, err := tool.Execute(context.Background(), mustJSON(t, map[string]any{"query": "c", "max_results": 3})); err != nil {
		t.Fatal(err)
	}
	if got := fake.Requests()[2].MaxResults; got != 3 {
		t.Fatalf("explicit max = %d, want 3", got)
	}
}

func TestWebSearchCancellationPropagates(t *testing.T) {
	tool := &WebSearch{Provider: websearch.NewFakeProvider(websearch.SearchResponse{})}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := tool.Execute(ctx, mustJSON(t, map[string]any{"query": "x"})); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

type blockingProvider struct{}

func (blockingProvider) Name() string { return "blocking" }
func (blockingProvider) Search(ctx context.Context, _ websearch.SearchRequest) (*websearch.SearchResponse, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestWebSearchTimeoutIsRecoverable(t *testing.T) {
	tool := &WebSearch{Provider: blockingProvider{}, Timeout: 10 * time.Millisecond}
	res, err := tool.Execute(context.Background(), mustJSON(t, map[string]any{"query": "slow"}))
	if err != nil {
		t.Fatalf("internal timeout must not be fatal, got err = %v", err)
	}
	if !res.IsError || !strings.Contains(res.Content, "search failed") {
		t.Fatalf("res = %+v", res)
	}
}

func TestWebSearchNameAndSchema(t *testing.T) {
	tool := &WebSearch{}
	if tool.Name() != "web_search" {
		t.Fatalf("name = %q", tool.Name())
	}
	raw := string(tool.Schema().Raw())
	for _, want := range []string{"query", "max_results"} {
		if !strings.Contains(raw, want) {
			t.Fatalf("schema missing %q: %s", want, raw)
		}
	}
}
