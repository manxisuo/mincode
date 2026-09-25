package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/manxisuo/mincode/internal/config"
	"github.com/manxisuo/mincode/internal/llm"
	"github.com/manxisuo/mincode/internal/observability"
)

func newTestAppWithWorkspace(t *testing.T) (*App, string) {
	t.Helper()
	wsDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(wsDir, "hello.txt"), []byte("hello line\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	traceDir := t.TempDir()
	cfgPath := filepath.Join(t.TempDir(), "mincode.yaml")
	yaml := "provider:\n  type: fake\n  model: fake-model\ntrace:\n  dir: " + filepath.ToSlash(traceDir) + "\n"
	if err := os.WriteFile(cfgPath, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}

	app, err := NewApp(Options{ConfigPath: cfgPath, Workspace: wsDir})
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	app.out = &buf
	t.Cleanup(func() { _ = app.Close() })
	return app, wsDir
}

func TestSingleShotStillWorks(t *testing.T) {
	app, _ := newTestAppWithWorkspace(t)
	if err := app.singleShot(context.Background(), "hello"); err != nil {
		t.Fatal(err)
	}
	events, err := observability.ReadEvents(app.TracePath())
	if err != nil {
		t.Fatal(err)
	}
	joined := ""
	for _, e := range events {
		joined += string(e.Type) + ","
	}
	if !strings.Contains(joined, string(observability.EventLLMRequestFinished)) {
		t.Fatalf("events = %s", joined)
	}
}

func TestAgentToolExecutionInCLI(t *testing.T) {
	app, _ := newTestAppWithWorkspace(t)

	fake := &llm.FakeProvider{
		Responses: []llm.ChatResponse{
			{ToolCalls: []llm.ToolCall{{
				ID:        "1",
				Name:      "read_file",
				Arguments: `{"path":"hello.txt"}`,
			}}},
			{Content: "file contains hello line"},
		},
	}
	app.agent.Provider = fake

	var buf bytes.Buffer
	app.out = &buf
	if err := app.runTurn(context.Background(), "read hello.txt"); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "read_file") {
		t.Fatalf("expected tool echo, got %q", out)
	}
	if !strings.Contains(out, "file contains hello line") {
		t.Fatalf("expected final answer, got %q", out)
	}

	events, err := observability.ReadEvents(app.TracePath())
	if err != nil {
		t.Fatal(err)
	}
	var hasTool, hasState bool
	for _, e := range events {
		if e.Type == observability.EventToolFinished {
			hasTool = true
		}
		if e.Type == observability.EventAgentStateChanged {
			hasState = true
		}
	}
	if !hasTool || !hasState {
		t.Fatalf("missing tool/state events")
	}
}

func TestTimelineCommand(t *testing.T) {
	app, _ := newTestAppWithWorkspace(t)
	fake := &llm.FakeProvider{
		Responses: []llm.ChatResponse{
			{ToolCalls: []llm.ToolCall{{
				ID: "1", Name: "list_dir", Arguments: `{"path":"."}`,
			}}},
			{Content: "done"},
		},
	}
	app.agent.Provider = fake
	if err := app.runTurn(context.Background(), "list"); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	app.out = &buf
	quit := app.handleCommand(context.Background(), "/timeline")
	if quit {
		t.Fatal("timeline should not quit")
	}
	out := buf.String()
	if !strings.Contains(out, "Tool Start") || !strings.Contains(out, "LLM Response") {
		t.Fatalf("timeline = %q", out)
	}
	if !strings.Contains(out, "Context Built") {
		t.Fatalf("timeline missing context: %q", out)
	}
}

func TestContextSnapshotCommand(t *testing.T) {
	app, _ := newTestAppWithWorkspace(t)
	var buf bytes.Buffer
	app.out = &buf
	app.handleCommand(context.Background(), "/context")
	if !strings.Contains(buf.String(), "no context snapshot") {
		t.Fatalf("empty state = %q", buf.String())
	}

	if err := app.runTurn(context.Background(), "hello"); err != nil {
		t.Fatal(err)
	}
	buf.Reset()
	app.handleCommand(context.Background(), "/context")
	out := buf.String()
	if !strings.Contains(out, "Context Snapshot") || !strings.Contains(out, "system") {
		t.Fatalf("snapshot = %q", out)
	}
}

func TestInstructionsLoadedAtStartup(t *testing.T) {
	wsDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(wsDir, "AGENTS.md"), []byte("# Project\nAlways cite paths.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(wsDir, "svc"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wsDir, "svc", "AGENTS.md"), []byte("svc rules\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wsDir, "svc", "a.txt"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	traceDir := t.TempDir()
	cfgPath := filepath.Join(t.TempDir(), "mincode.yaml")
	yaml := "provider:\n  type: fake\n  model: fake-model\ntrace:\n  dir: " + filepath.ToSlash(traceDir) + "\n"
	if err := os.WriteFile(cfgPath, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}

	app, err := NewApp(Options{ConfigPath: cfgPath, Workspace: wsDir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.Close() })

	if !strings.Contains(app.agent.Ctx.Instructions(), "Always cite paths") {
		t.Fatalf("root instructions not in context: %q", app.agent.Ctx.Instructions())
	}

	var buf bytes.Buffer
	app.out = &buf
	app.handleCommand(context.Background(), "/instructions")
	out := buf.String()
	if !strings.Contains(out, "AGENTS.md") || !strings.Contains(out, "Always cite paths") {
		t.Fatalf("instructions cmd = %q", out)
	}

	// Touch a nested file so nested AGENTS.md joins context.
	fake := &llm.FakeProvider{
		Responses: []llm.ChatResponse{
			{ToolCalls: []llm.ToolCall{{
				ID: "1", Name: "read_file", Arguments: `{"path":"svc/a.txt"}`,
			}}},
			{Content: "ok"},
		},
	}
	app.agent.Provider = fake
	if err := app.runTurn(context.Background(), "read svc"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(app.agent.Ctx.Instructions(), "svc rules") {
		t.Fatalf("nested instructions missing: %q", app.agent.Ctx.Instructions())
	}

	events, err := observability.ReadEvents(app.TracePath())
	if err != nil {
		t.Fatal(err)
	}
	loads := 0
	for _, e := range events {
		if e.Type == observability.EventInstructionLoaded {
			loads++
		}
	}
	if loads < 2 {
		t.Fatalf("instruction.loaded events = %d, want >= 2", loads)
	}

	buf.Reset()
	app.handleCommand(context.Background(), "/timeline")
	if !strings.Contains(buf.String(), "Instructions") {
		t.Fatalf("timeline missing instructions: %q", buf.String())
	}
}

func TestWebFetchToolRegistered(t *testing.T) {
	app, _ := newTestAppWithWorkspace(t)
	if _, ok := app.agent.Tools.Get("web_fetch"); !ok {
		t.Fatal("web_fetch should always be registered (no backend needed)")
	}
}

func TestBuildWebSearch(t *testing.T) {
	if p, err := buildWebSearch(config.Config{}); err != nil || p != nil {
		t.Fatalf("disabled: provider=%v err=%v", p, err)
	}
	p, err := buildWebSearch(config.Config{WebSearch: config.WebSearchConfig{Type: "fake"}})
	if err != nil || p == nil {
		t.Fatalf("fake: provider=%v err=%v", p, err)
	}
	if p.Name() != "fake" {
		t.Fatalf("name = %q", p.Name())
	}

	// tavily requires an api key.
	if _, err := buildWebSearch(config.Config{WebSearch: config.WebSearchConfig{Type: "tavily"}}); err == nil {
		t.Fatal("tavily without api_key should error")
	}
	tp, err := buildWebSearch(config.Config{WebSearch: config.WebSearchConfig{Type: "tavily", APIKey: "tvly-x"}})
	if err != nil || tp == nil {
		t.Fatalf("tavily: provider=%v err=%v", tp, err)
	}
	if tp.Name() != "tavily" {
		t.Fatalf("name = %q", tp.Name())
	}

	// searxng requires a base_url but no api key.
	if _, err := buildWebSearch(config.Config{WebSearch: config.WebSearchConfig{Type: "searxng"}}); err == nil {
		t.Fatal("searxng without base_url should error")
	}
	sp, err := buildWebSearch(config.Config{WebSearch: config.WebSearchConfig{Type: "searxng", BaseURL: "http://localhost:8080"}})
	if err != nil || sp == nil {
		t.Fatalf("searxng: provider=%v err=%v", sp, err)
	}
	if sp.Name() != "searxng" {
		t.Fatalf("name = %q", sp.Name())
	}

	if _, err := buildWebSearch(config.Config{WebSearch: config.WebSearchConfig{Type: "bogus"}}); err == nil {
		t.Fatal("unknown type should error")
	}
}

func TestWebSearchToolRegisteredWhenConfigured(t *testing.T) {
	wsDir := t.TempDir()
	cfgPath := filepath.Join(t.TempDir(), "mincode.yaml")
	yaml := "provider:\n  type: fake\nmodel: fake-model\nwebsearch:\n  type: fake\nmax_results: 3\n"
	if err := os.WriteFile(cfgPath, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	app, err := NewApp(Options{ConfigPath: cfgPath, Workspace: wsDir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.Close() })

	if _, ok := app.agent.Tools.Get("web_search"); !ok {
		t.Fatal("web_search should be registered when configured")
	}

	// Without websearch config the tool must not be offered to the model.
	cfgPath2 := filepath.Join(t.TempDir(), "mincode.yaml")
	if err := os.WriteFile(cfgPath2, []byte("provider:\n  type: fake\nmodel: fake-model\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	app2, err := NewApp(Options{ConfigPath: cfgPath2, Workspace: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app2.Close() })
	if _, ok := app2.agent.Tools.Get("web_search"); ok {
		t.Fatal("web_search must not be registered when disabled")
	}
}
