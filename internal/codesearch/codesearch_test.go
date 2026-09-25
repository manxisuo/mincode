package codesearch

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, root, rel, content string) {
	t.Helper()
	abs := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func testWorkspace(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	writeFile(t, root, "ctx/budget.go", `package context

const DefaultBudgetTokens = 32000

func BuildRequest() int { return DefaultBudgetTokens }
`)
	writeFile(t, root, "other/thing.go", `package other

func UnrelatedThing() {}
`)
	writeFile(t, root, "web/app.ts", "export function boot() {}\n")
	return root
}

func TestSearchRanksRelevantSymbol(t *testing.T) {
	ix := New(testWorkspace(t), Options{})
	hits, err := ix.Search(context.Background(), "context budget", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 {
		t.Fatal("expected hits")
	}
	if hits[0].Path != "ctx/budget.go" {
		t.Fatalf("top hit = %s, want ctx/budget.go; hits=%+v", hits[0].Path, hits)
	}
	if len(hits[0].Matched) == 0 {
		t.Fatalf("expected matched terms on top hit: %+v", hits[0])
	}
	for _, h := range hits {
		if strings.Contains(h.Path, "other") {
			t.Fatalf("unrelated file should not rank for 'context budget': %+v", h)
		}
	}
}

func TestSearchMatchesPathAndCamelCase(t *testing.T) {
	ix := New(testWorkspace(t), Options{})

	// Path-based (non-Go) match.
	hits, err := ix.Search(context.Background(), "app", 5)
	if err != nil {
		t.Fatal(err)
	}
	foundPath := false
	for _, h := range hits {
		if h.Path == "web/app.ts" {
			foundPath = true
		}
	}
	if !foundPath {
		t.Fatalf("expected path match for 'app', got %+v", hits)
	}

	// CamelCase split: BuildRequest → build + request.
	hits, err = ix.Search(context.Background(), "request", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 || hits[0].Path != "ctx/budget.go" {
		t.Fatalf("camelCase split failed, hits=%+v", hits)
	}
}

func TestSearchNoMatch(t *testing.T) {
	ix := New(testWorkspace(t), Options{})
	hits, err := ix.Search(context.Background(), "zzzznotpresent", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 0 {
		t.Fatalf("expected no hits, got %+v", hits)
	}
	if _, _, err := ix.SearchContext(context.Background(), "zzzznotpresent", 5); err != nil {
		t.Fatal(err)
	}
}

func TestSearchContextRenders(t *testing.T) {
	ix := New(testWorkspace(t), Options{MaxTokens: 500})
	text, summary, err := ix.SearchContext(context.Background(), "budget", 5)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "Relevant code") || !strings.Contains(text, "ctx/budget.go") {
		t.Fatalf("rendered text = %q", text)
	}
	if len(summary) == 0 || !strings.Contains(summary[0], "ctx/budget.go") {
		t.Fatalf("summary = %v", summary)
	}
}

func TestSearchCancelled(t *testing.T) {
	ix := New(testWorkspace(t), Options{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ix.Search(ctx, "budget", 3); err == nil {
		t.Fatal("expected context error")
	}
}

func TestTokenize(t *testing.T) {
	got := tokenize("DefaultBudgetTokens internal/ctx_mgr buildRequest")
	want := map[string]bool{"default": true, "budget": true, "tokens": true, "internal": true, "ctx": true, "mgr": true, "build": true, "request": true}
	seen := map[string]bool{}
	for _, w := range got {
		seen[w] = true
	}
	for w := range want {
		if !seen[w] {
			t.Fatalf("tokenize missing %q in %v", w, got)
		}
	}
	// Stopwords / short tokens dropped.
	for _, w := range tokenize("a of the func type") {
		if w == "a" || w == "of" || w == "the" || w == "func" || w == "type" {
			t.Fatalf("stopword leaked: %q", w)
		}
	}
}
