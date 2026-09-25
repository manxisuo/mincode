package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/manxisuo/mincode/internal/llm"
	"github.com/manxisuo/mincode/internal/observability"
)

func TestRepoMapInjectedIntoContext(t *testing.T) {
	app, ws := newTestAppWithWorkspace(t)
	if err := os.WriteFile(filepath.Join(ws, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	app.loadRepoMap()

	got := app.agent.Ctx.RepoMap()
	if !strings.Contains(got, "main.go") || !strings.Contains(got, "func main") {
		t.Fatalf("repo map not injected: %q", got)
	}

	events, err := observability.ReadEvents(app.TracePath())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range events {
		if e.Type == observability.EventRepoMapBuilt {
			found = true
		}
	}
	if !found {
		t.Fatal("missing repo_map.built startup event")
	}
}

func TestRepoMapToolEmitsEvent(t *testing.T) {
	app, ws := newTestAppWithWorkspace(t)
	if err := os.WriteFile(filepath.Join(ws, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	app.agent.Provider = &llm.FakeProvider{Responses: []llm.ChatResponse{
		{ToolCalls: []llm.ToolCall{{ID: "1", Name: "repo_map", Arguments: `{}`}}},
		{Content: "done"},
	}}

	var buf bytes.Buffer
	app.out = &buf
	if err := app.runTurn(context.Background(), "map the repo"); err != nil {
		t.Fatal(err)
	}

	events, err := observability.ReadEvents(app.TracePath())
	if err != nil {
		t.Fatal(err)
	}
	toolReason := false
	for _, e := range events {
		if e.Type != observability.EventRepoMapBuilt {
			continue
		}
		if data, ok := e.Data.(map[string]any); ok {
			if r, _ := data["reason"].(string); r == "agent tool" {
				toolReason = true
			}
		}
	}
	if !toolReason {
		t.Fatal("missing repo_map.built with reason=agent tool")
	}
}

func TestRepoMapDisabled(t *testing.T) {
	wsDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(wsDir, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(t.TempDir(), "mincode.yaml")
	yaml := "provider:\n  type: fake\n  model: fake-model\nagent:\n  repo_map: false\n"
	if err := os.WriteFile(cfgPath, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	app, err := NewApp(Options{ConfigPath: cfgPath, Workspace: wsDir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.Close() })

	if app.repoMap != nil {
		t.Fatal("repo map should not be built when disabled")
	}
	if app.agent.Ctx.RepoMap() != "" {
		t.Fatal("repo map should not be injected when disabled")
	}
	if _, ok := app.agent.Tools.Get("repo_map"); ok {
		t.Fatal("repo_map tool should not be registered when disabled")
	}
}
