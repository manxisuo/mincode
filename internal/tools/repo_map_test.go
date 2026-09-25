package tools

import (
	"context"
	"strings"
	"testing"
)

func TestRepoMapToolBasic(t *testing.T) {
	ws := newWS(t)
	write(t, ws, "main.go", "package main\n\nfunc main() {}\n")
	write(t, ws, "pkg/a.go", "package pkg\n\ntype Thing struct{}\n\nfunc NewThing() Thing { return Thing{} }\n")

	tool := &RepoMap{WS: ws}
	res, err := tool.Execute(context.Background(), mustJSON(t, map[string]any{}))
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("unexpected error: %s", res.Content)
	}
	if !strings.Contains(res.Content, "main.go") || !strings.Contains(res.Content, "NewThing") {
		t.Fatalf("content = %s", res.Content)
	}
	if n, _ := res.Meta["files"].(int); n < 2 {
		t.Fatalf("meta files = %v", res.Meta["files"])
	}
}

func TestRepoMapToolSubpath(t *testing.T) {
	ws := newWS(t)
	write(t, ws, "sub/inside.go", "package sub\n\nfunc Inside() {}\n")
	write(t, ws, "other/outside.go", "package other\n\nfunc Outside() {}\n")

	tool := &RepoMap{WS: ws}
	res, err := tool.Execute(context.Background(), mustJSON(t, map[string]any{"path": "sub"}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Content, "sub/inside.go") {
		t.Fatalf("missing sub file: %s", res.Content)
	}
	if strings.Contains(res.Content, "Outside") {
		t.Fatalf("subpath leaked outside file: %s", res.Content)
	}
}

func TestRepoMapToolPathEscape(t *testing.T) {
	ws := newWS(t)
	tool := &RepoMap{WS: ws}
	res, err := tool.Execute(context.Background(), mustJSON(t, map[string]any{"path": "../outside"}))
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatalf("expected error result for escape, got %q", res.Content)
	}
}

func TestRepoMapToolInvalidArgs(t *testing.T) {
	ws := newWS(t)
	tool := &RepoMap{WS: ws}
	res, err := tool.Execute(context.Background(), []byte("{not json"))
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("expected invalid-args error result")
	}
}

func TestRepoMapToolMaxTokensCap(t *testing.T) {
	ws := newWS(t)
	write(t, ws, "a.go", "package a\n\nfunc A() {}\n")
	tool := &RepoMap{WS: ws}
	res, err := tool.Execute(context.Background(), mustJSON(t, map[string]any{"max_tokens": 999999}))
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("unexpected error: %s", res.Content)
	}
	if tok, _ := res.Meta["tokens"].(int); tok > repoMapHardTokens+50 {
		t.Fatalf("tokens = %d exceeds hard cap %d", tok, repoMapHardTokens)
	}
}

func TestRepoMapToolCancelled(t *testing.T) {
	ws := newWS(t)
	write(t, ws, "a.go", "package a\n")
	tool := &RepoMap{WS: ws}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := tool.Execute(ctx, mustJSON(t, map[string]any{})); err == nil {
		t.Fatal("expected context error")
	}
}

func TestRepoMapToolMissingPath(t *testing.T) {
	ws := newWS(t)
	tool := &RepoMap{WS: ws}
	res, err := tool.Execute(context.Background(), mustJSON(t, map[string]any{"path": "does-not-exist"}))
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("expected error result for missing path")
	}
}
