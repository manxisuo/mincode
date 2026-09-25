package cli

import (
	"strings"
	"testing"

	"github.com/manxisuo/mincode/internal/observability"
)

func TestFormatMetricsRepoMapCache(t *testing.T) {
	out := formatMetrics(observability.Metrics{
		RepoMapBuilds:      3,
		RepoMapCacheHits:   6,
		RepoMapCacheMisses: 2,
	})
	if !strings.Contains(out, "Repo Map") || !strings.Contains(out, "75%") || !strings.Contains(out, "6/8") {
		t.Fatalf("metrics missing repo map cache line:\n%s", out)
	}
}

func TestFormatMetricsNoRepoMap(t *testing.T) {
	out := formatMetrics(observability.Metrics{LLMCalls: 1})
	if strings.Contains(out, "Repo Map") {
		t.Fatalf("unexpected repo map line:\n%s", out)
	}
}
