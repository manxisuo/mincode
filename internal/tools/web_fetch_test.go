package tools

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/manxisuo/mincode/internal/webfetch"
)

func TestWebFetchFormatsResult(t *testing.T) {
	fake := webfetch.NewFakeFetcher(webfetch.Response{
		URL:         "https://example.com/page",
		Status:      200,
		ContentType: "text/html",
		Title:       "Example Page",
		Text:        "Hello body text",
		Bytes:       1234,
	})
	tool := &WebFetch{Fetcher: fake}

	res, err := tool.Execute(context.Background(), mustJSON(t, map[string]any{"url": "https://example.com"}))
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("unexpected error result: %s", res.Content)
	}
	for _, want := range []string{"# Example Page", "URL: https://example.com/page", "Hello body text"} {
		if !strings.Contains(res.Content, want) {
			t.Fatalf("missing %q in:\n%s", want, res.Content)
		}
	}
	if res.Meta["status"] != 200 || res.Meta["title"] != "Example Page" {
		t.Fatalf("meta = %+v", res.Meta)
	}
	if reqs := fake.Requests(); len(reqs) != 1 || reqs[0].URL != "https://example.com" {
		t.Fatalf("reqs = %+v", reqs)
	}
}

func TestWebFetchRequiresURL(t *testing.T) {
	tool := &WebFetch{Fetcher: webfetch.NewFakeFetcher(webfetch.Response{})}
	res, err := tool.Execute(context.Background(), mustJSON(t, map[string]any{"url": "  "}))
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError || !strings.Contains(res.Content, "url is required") {
		t.Fatalf("res = %+v", res)
	}
}

func TestWebFetchInvalidArguments(t *testing.T) {
	tool := &WebFetch{Fetcher: webfetch.NewFakeFetcher(webfetch.Response{})}
	res, err := tool.Execute(context.Background(), []byte("{bad"))
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError || !strings.Contains(res.Content, "invalid arguments") {
		t.Fatalf("res = %+v", res)
	}
}

func TestWebFetchNoFetcher(t *testing.T) {
	tool := &WebFetch{}
	res, err := tool.Execute(context.Background(), mustJSON(t, map[string]any{"url": "https://x"}))
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError || !strings.Contains(res.Content, "not configured") {
		t.Fatalf("res = %+v", res)
	}
}

func TestWebFetchRecoverableError(t *testing.T) {
	fake := webfetch.NewFakeFetcher(webfetch.Response{})
	fake.Err = webfetch.ErrBlockedHost
	tool := &WebFetch{Fetcher: fake}

	res, err := tool.Execute(context.Background(), mustJSON(t, map[string]any{"url": "http://10.0.0.1"}))
	if err != nil {
		t.Fatalf("blocked host should be recoverable, got err = %v", err)
	}
	if !res.IsError || !strings.Contains(res.Content, "fetch failed") {
		t.Fatalf("res = %+v", res)
	}
}

func TestWebFetchCancellationPropagates(t *testing.T) {
	tool := &WebFetch{Fetcher: webfetch.NewFakeFetcher(webfetch.Response{})}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := tool.Execute(ctx, mustJSON(t, map[string]any{"url": "https://x"})); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

type blockingFetcher struct{}

func (blockingFetcher) Name() string { return "blocking" }
func (blockingFetcher) Fetch(ctx context.Context, _ webfetch.Request) (*webfetch.Response, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestWebFetchTimeoutIsRecoverable(t *testing.T) {
	tool := &WebFetch{Fetcher: blockingFetcher{}, Timeout: 10 * time.Millisecond}
	res, err := tool.Execute(context.Background(), mustJSON(t, map[string]any{"url": "https://slow.example"}))
	if err != nil {
		t.Fatalf("internal timeout must not be fatal, got err = %v", err)
	}
	if !res.IsError || !strings.Contains(res.Content, "fetch failed") {
		t.Fatalf("res = %+v", res)
	}
}

func TestWebFetchNameAndSchema(t *testing.T) {
	tool := &WebFetch{}
	if tool.Name() != "web_fetch" {
		t.Fatalf("name = %q", tool.Name())
	}
	raw := string(tool.Schema().Raw())
	if !strings.Contains(raw, "url") {
		t.Fatalf("schema = %s", raw)
	}
}
