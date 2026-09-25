package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/manxisuo/mincode/internal/agent"
	"github.com/manxisuo/mincode/internal/llm"
	"github.com/manxisuo/mincode/internal/observability"
	"github.com/manxisuo/mincode/internal/tools"
)

func repoMapServer(t *testing.T, enabled bool) *httptest.Server {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nfunc main() {}\nfunc helper() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "pkg", "util.go"), []byte("package pkg\n\ntype Thing struct{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ws, err := tools.NewWorkspace(dir)
	if err != nil {
		t.Fatal(err)
	}
	bus := observability.NewBus()
	metrics := observability.NewMetricsCollector()
	ag := agent.New(&llm.FakeProvider{}, tools.NewRegistry(), bus, "repo-test", 5, "sys", 8000)
	srv := New(Options{
		Workspace:      dir,
		SessionID:      "repo-test",
		RepoMapEnabled: enabled,
		RepoMapTokens:  1200,
	}, ag, bus, metrics, nil, nil, ws, nil)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts
}

func getJSON(t *testing.T, url string) map[string]any {
	t.Helper()
	res, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var out map[string]any
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestRepoMapAPIEnabled(t *testing.T) {
	ts := repoMapServer(t, true)

	first := getJSON(t, ts.URL+"/api/repomap")
	if first["enabled"] != true {
		t.Fatalf("enabled = %v", first["enabled"])
	}
	files, _ := first["files"].([]any)
	if len(files) == 0 {
		t.Fatalf("expected files, got %v", first)
	}
	f0, _ := files[0].(map[string]any)
	rank, _ := f0["rank"].(map[string]any)
	if rank == nil || rank["total"] == nil {
		t.Fatalf("missing ranking rationale: %+v", f0)
	}
	if text, _ := first["text"].(string); text == "" {
		t.Fatal("missing rendered text")
	}
	if misses, _ := first["cache_misses"].(float64); misses < 1 {
		t.Fatalf("first build should report cache misses, got %v", first["cache_misses"])
	}

	// Second call reuses the shared incremental cache.
	second := getJSON(t, ts.URL+"/api/repomap")
	if hits, _ := second["cache_hits"].(float64); hits <= 0 {
		t.Fatalf("expected cache hits on second build, got %v", second["cache_hits"])
	}
}

func TestRepoMapAPIFocusAndPath(t *testing.T) {
	ts := repoMapServer(t, true)
	data := getJSON(t, ts.URL+"/api/repomap?focus=Thing&path=pkg")
	if data["focus"] != "Thing" || data["subpath"] != "pkg" {
		t.Fatalf("focus/subpath = %v / %v", data["focus"], data["subpath"])
	}
	files, _ := data["files"].([]any)
	if len(files) != 1 {
		t.Fatalf("subpath should limit to 1 file, got %d", len(files))
	}
}

func TestRepoMapAPIDisabled(t *testing.T) {
	ts := repoMapServer(t, false)
	data := getJSON(t, ts.URL+"/api/repomap")
	if data["enabled"] != false {
		t.Fatalf("enabled = %v, want false", data["enabled"])
	}
}

func TestMetricsAPIIncludesRepoMapCache(t *testing.T) {
	ag, bus, metrics := testAgent(t)
	srv := New(Options{
		Addr:      "127.0.0.1:0",
		Workspace: t.TempDir(),
		SessionID: "web-test",
	}, ag, bus, metrics, nil, nil, nil, nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	bus.Publish(observability.NewEvent("web-test", 0, observability.EventRepoMapBuilt,
		observability.RepoMapData{CacheHits: 7, CacheMisses: 3}))

	data := getJSON(t, ts.URL+"/api/metrics")
	if data["repo_map_builds"] != float64(1) {
		t.Fatalf("repo_map_builds = %v", data["repo_map_builds"])
	}
	if data["repo_map_cache_hits"] != float64(7) || data["repo_map_cache_misses"] != float64(3) {
		t.Fatalf("cache = %v/%v", data["repo_map_cache_hits"], data["repo_map_cache_misses"])
	}
}
