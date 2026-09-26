package codesearch

import (
	"context"
	"sync"
	"testing"

	"github.com/manxisuo/mincode/internal/embedding"
)

type countingEmbedder struct {
	inner embedding.Provider
	mu    sync.Mutex
	calls int
	texts int
}

func (c *countingEmbedder) Name() string  { return c.inner.Name() }
func (c *countingEmbedder) Model() string { return c.inner.Model() }
func (c *countingEmbedder) Dim() int      { return c.inner.Dim() }

func (c *countingEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	c.mu.Lock()
	c.calls++
	c.texts += len(texts)
	c.mu.Unlock()
	return c.inner.Embed(ctx, texts)
}

func (c *countingEmbedder) textCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.texts
}

func embeddingWorkspace(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	writeFile(t, root, "ctx/budget.go", `package context

// enforceBudget caps the context token budget for every request.
func enforceBudget(estimated, budget int) bool { return estimated > budget }
`)
	writeFile(t, root, "other/thing.go", `package other

func UnrelatedThing(x int) int { return x + 1 }
`)
	return root
}

func TestEmbeddingSearcherRanksRelevant(t *testing.T) {
	s := NewEmbedding(embeddingWorkspace(t), embedding.NewFakeProvider("fake", 256), EmbeddingOptions{})
	if s.Backend() != "embedding" {
		t.Fatalf("backend = %q", s.Backend())
	}
	hits, err := s.Search(context.Background(), "enforce context token budget", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 || hits[0].Path != "ctx/budget.go" {
		t.Fatalf("top hit = %+v", hits)
	}

	// A doc-comment term only present in the source excerpt should still match.
	hits, err = s.Search(context.Background(), "every request", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 || hits[0].Path != "ctx/budget.go" {
		t.Fatalf("excerpt match failed: %+v", hits)
	}
}

func TestEmbeddingSearcherCachesVectors(t *testing.T) {
	cnt := &countingEmbedder{inner: embedding.NewFakeProvider("fake", 128)}
	s := NewEmbedding(embeddingWorkspace(t), cnt, EmbeddingOptions{})
	if _, err := s.Search(context.Background(), "budget", 3); err != nil {
		t.Fatal(err)
	}
	first := cnt.textCount()
	if first < 2 {
		t.Fatalf("expected chunk embeddings on first build, got %d", first)
	}
	if _, err := s.Search(context.Background(), "permission", 3); err != nil {
		t.Fatal(err)
	}
	second := cnt.textCount()
	if second-first != 1 {
		t.Fatalf("rebuild should embed only the query (+1), got +%d", second-first)
	}
}

func TestEmbeddingSearcherEmptyQuery(t *testing.T) {
	s := NewEmbedding(embeddingWorkspace(t), embedding.NewFakeProvider("fake", 64), EmbeddingOptions{})
	if hits, err := s.Search(context.Background(), "   ", 3); err != nil || len(hits) != 0 {
		t.Fatalf("empty query: hits=%v err=%v", hits, err)
	}
	if text, _, err := s.SearchContext(context.Background(), "", 3); err != nil || text != "" {
		t.Fatalf("empty context: %q err=%v", text, err)
	}
}
