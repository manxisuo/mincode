package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/manxisuo/mincode/internal/llm"
	"github.com/manxisuo/mincode/internal/observability"
)

func TestCodeSearchInjectedOnTurn(t *testing.T) {
	app, ws := newTestAppWithWorkspace(t)
	if err := os.WriteFile(filepath.Join(ws, "budget.go"),
		[]byte("package context\n\nconst DefaultBudgetTokens = 32000\n\nfunc BuildRequest() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	app.agent.Provider = llm.NewFakeProvider("fake", "ok")

	var buf bytes.Buffer
	app.out = &buf
	if err := app.runTurn(context.Background(), "where is the context budget"); err != nil {
		t.Fatal(err)
	}

	events, err := observability.ReadEvents(app.TracePath())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range events {
		if e.Type != observability.EventCodeSearchInjected {
			continue
		}
		found = true
		if data, ok := e.Data.(map[string]any); ok {
			if hits, _ := data["hits"].(float64); hits < 1 {
				t.Fatalf("expected >=1 hit: %+v", data)
			}
		}
	}
	if !found {
		t.Fatal("missing code_search.injected event")
	}
}

func TestCodeSearchToolRegistered(t *testing.T) {
	app, _ := newTestAppWithWorkspace(t)
	found := false
	for _, n := range app.toolNames() {
		if n == "code_search" {
			found = true
		}
	}
	if !found {
		t.Fatalf("code_search tool not listed: %v", app.toolNames())
	}
}
