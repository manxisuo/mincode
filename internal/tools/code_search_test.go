package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/manxisuo/mincode/internal/codesearch"
)

func TestCodeSearchToolBasic(t *testing.T) {
	ws := newWS(t)
	write(t, ws, "ctx/budget.go", "package context\n\nconst DefaultBudgetTokens = 32000\n\nfunc BuildRequest() {}\n")
	write(t, ws, "other.go", "package other\n\nfunc Nope() {}\n")

	ix := codesearch.New(ws.Root(), codesearch.Options{})
	tool := &CodeSearch{Searcher: ix, DefaultK: 5}
	res, err := tool.Execute(context.Background(), mustJSON(t, map[string]any{"query": "context budget"}))
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("unexpected error: %s", res.Content)
	}
	if !strings.Contains(res.Content, "ctx/budget.go") {
		t.Fatalf("content = %s", res.Content)
	}
	if n, _ := res.Meta["results"].(int); n < 1 {
		t.Fatalf("meta results = %v", res.Meta["results"])
	}
}

func TestCodeSearchToolNoMatches(t *testing.T) {
	ws := newWS(t)
	write(t, ws, "a.go", "package a\n\nfunc A() {}\n")
	ix := codesearch.New(ws.Root(), codesearch.Options{})
	tool := &CodeSearch{Searcher: ix, DefaultK: 5}
	res, err := tool.Execute(context.Background(), mustJSON(t, map[string]any{"query": "zzzznope"}))
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("no-match should not be an error: %s", res.Content)
	}
	if !strings.Contains(res.Content, "no matches") {
		t.Fatalf("content = %s", res.Content)
	}
}

func TestCodeSearchToolValidation(t *testing.T) {
	ws := newWS(t)
	ix := codesearch.New(ws.Root(), codesearch.Options{})
	tool := &CodeSearch{Searcher: ix}

	if res, _ := tool.Execute(context.Background(), []byte("{bad")); !res.IsError {
		t.Fatal("expected invalid-args error")
	}
	if res, _ := tool.Execute(context.Background(), mustJSON(t, map[string]any{})); !res.IsError {
		t.Fatal("expected missing-query error")
	}
	if res, _ := (&CodeSearch{}).Execute(context.Background(), mustJSON(t, map[string]any{"query": "x"})); !res.IsError {
		t.Fatal("expected unconfigured error")
	}
}

func TestCodeSearchToolCancelled(t *testing.T) {
	ws := newWS(t)
	ix := codesearch.New(ws.Root(), codesearch.Options{})
	tool := &CodeSearch{Searcher: ix}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := tool.Execute(ctx, mustJSON(t, map[string]any{"query": "x"})); err == nil {
		t.Fatal("expected context error")
	}
}
