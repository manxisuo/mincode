package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefault(t *testing.T) {
	cfg := Default()
	if cfg.Provider.Type != "openai-compatible" {
		t.Fatalf("type = %q", cfg.Provider.Type)
	}
	if cfg.Agent.MaxSteps != 30 {
		t.Fatalf("max steps = %d", cfg.Agent.MaxSteps)
	}
	if cfg.Data.Location != "global" {
		t.Fatalf("data.location = %q", cfg.Data.Location)
	}
}

func TestLoadMissingExplicitPath(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "nope.yaml"))
	if err == nil {
		t.Fatal("expected error for missing explicit config")
	}
}

func TestLoadYAMLAndEnvOverride(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mincode.yaml")
	content := `
provider:
  type: fake
  model: scripted-model
  base_url: http://localhost:1234/v1
data:
  location: workspace
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("MINCODE_MODEL", "env-model")
	t.Setenv("MINCODE_API_KEY", "sk-test")
	t.Setenv("OPENAI_API_KEY", "should-not-win")

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Provider.Type != "fake" {
		t.Fatalf("type = %q", cfg.Provider.Type)
	}
	if cfg.Provider.Model != "env-model" {
		t.Fatalf("model = %q, env should override yaml", cfg.Provider.Model)
	}
	if cfg.Provider.APIKey != "sk-test" {
		t.Fatalf("api key = %q", cfg.Provider.APIKey)
	}
	if cfg.Provider.BaseURL != "http://localhost:1234/v1" {
		t.Fatalf("base_url = %q", cfg.Provider.BaseURL)
	}
	if cfg.Data.Location != "workspace" {
		t.Fatalf("data.location = %q", cfg.Data.Location)
	}
}

func TestLoadMissingFileInCwdIsOK(t *testing.T) {
	dir := t.TempDir()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(wd) })

	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Provider.Model == "" {
		t.Fatal("expected default model")
	}
}

func TestLoadFromWorkspaceConfig(t *testing.T) {
	ws := t.TempDir()
	yaml := "provider:\n  type: fake\n  model: ws-model\n  base_url: https://api.deepseek.com/v1\n"
	if err := os.WriteFile(filepath.Join(ws, "mincode.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}

	other := t.TempDir()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(other); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(wd) })

	cfg, err := LoadFrom("", ws)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Provider.Model != "ws-model" {
		t.Fatalf("model = %q", cfg.Provider.Model)
	}
	if cfg.Provider.BaseURL != "https://api.deepseek.com/v1" {
		t.Fatalf("base_url = %q", cfg.Provider.BaseURL)
	}
}

func TestTracePath(t *testing.T) {
	p := TracePath("traces", "abc")
	if filepath.Base(p) != "abc.jsonl" {
		t.Fatalf("path = %q", p)
	}
}

func TestWebSearchConfigYAML(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mincode.yaml")
	content := "provider:\n  type: fake\nwebsearch:\n  type: fake\n  max_results: 3\n  timeout_sec: 15\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.WebSearch.Type != "fake" {
		t.Fatalf("type = %q", cfg.WebSearch.Type)
	}
	if cfg.WebSearch.MaxResults != 3 || cfg.WebSearch.TimeoutSec != 15 {
		t.Fatalf("websearch = %+v", cfg.WebSearch)
	}
	if !cfg.WebSearch.Enabled() {
		t.Fatal("expected Enabled")
	}
}

func TestWebSearchDisabledByDefault(t *testing.T) {
	cfg := Default()
	if cfg.WebSearch.Enabled() {
		t.Fatalf("websearch should be disabled by default: %+v", cfg.WebSearch)
	}
}

func TestWebSearchEnvOverride(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mincode.yaml")
	if err := os.WriteFile(path, []byte("provider:\n  type: fake\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MINCODE_WEBSEARCH_TYPE", "fake")
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.WebSearch.Type != "fake" {
		t.Fatalf("env should override type, got %q", cfg.WebSearch.Type)
	}
}
