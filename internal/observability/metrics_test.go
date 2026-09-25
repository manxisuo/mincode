package observability

import (
	"strings"
	"testing"
)

func TestMetricsRepoMapCacheStruct(t *testing.T) {
	c := NewMetricsCollector()
	// Startup build: all misses.
	c.Handle(NewEvent("s", 0, EventRepoMapBuilt, RepoMapData{Files: 10, CacheHits: 0, CacheMisses: 188}))
	// Tool build: all hits.
	c.Handle(NewEvent("s", 0, EventRepoMapBuilt, RepoMapData{Files: 3, CacheHits: 188, CacheMisses: 0}))

	m := c.Snapshot()
	if m.RepoMapBuilds != 2 {
		t.Fatalf("builds = %d, want 2", m.RepoMapBuilds)
	}
	if m.RepoMapCacheHits != 188 || m.RepoMapCacheMisses != 188 {
		t.Fatalf("cache = %d/%d, want 188/188", m.RepoMapCacheHits, m.RepoMapCacheMisses)
	}
	if got := m.RepoMapCacheTotal(); got != 376 {
		t.Fatalf("total = %d, want 376", got)
	}
	if rate := m.RepoMapHitRate(); rate < 0.49 || rate > 0.51 {
		t.Fatalf("hit rate = %f, want ~0.5", rate)
	}
	if s := m.Format(); !strings.Contains(s, "Repo Map Cache") || !strings.Contains(s, "50%") {
		t.Fatalf("format missing repo map cache:\n%s", s)
	}
}

func TestMetricsRepoMapCacheJSONMap(t *testing.T) {
	c := NewMetricsCollector()
	c.Handle(NewEvent("s", 0, EventRepoMapBuilt, map[string]any{
		"cache_hits":   float64(3),
		"cache_misses": float64(1),
	}))
	m := c.Snapshot()
	if m.RepoMapBuilds != 1 || m.RepoMapCacheHits != 3 || m.RepoMapCacheMisses != 1 {
		t.Fatalf("json map not folded: %+v", m)
	}
	if rate := m.RepoMapHitRate(); rate != 0.75 {
		t.Fatalf("hit rate = %f, want 0.75", rate)
	}
}

func TestMetricsRepoMapNoLookups(t *testing.T) {
	m := Metrics{}
	if m.RepoMapHitRate() != 0 {
		t.Fatal("empty metrics should have 0 hit rate")
	}
	if strings.Contains(m.Format(), "Repo Map") {
		t.Fatal("no repo map line should be printed when no builds")
	}
}

func TestMetricsReset(t *testing.T) {
	c := NewMetricsCollector()
	c.Handle(NewEvent("s", 0, EventRepoMapBuilt, RepoMapData{CacheMisses: 2}))
	c.Reset()
	if m := c.Snapshot(); m.RepoMapBuilds != 0 || m.RepoMapCacheMisses != 0 {
		t.Fatalf("reset did not clear: %+v", m)
	}
}
