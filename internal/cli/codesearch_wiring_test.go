package cli

import (
	"testing"

	"github.com/manxisuo/mincode/internal/config"
	"github.com/manxisuo/mincode/internal/repomap"
)

func TestBuildEmbedderRequiresModel(t *testing.T) {
	cfg := config.Default()
	cfg.Embedding.Type = "openai-compatible"
	cfg.Provider.APIKey = "sk-x"
	cfg.Provider.BaseURL = "https://api.example.com/v1"
	cfg.Embedding.Model = ""
	if _, err := buildEmbedder(cfg); err == nil {
		t.Fatal("expected model-required error for openai-compatible embedding")
	}

	cfg.Embedding.Model = "text-embedding-3-small"
	if _, err := buildEmbedder(cfg); err != nil {
		t.Fatalf("valid config should build: %v", err)
	}
}

func TestBuildCodeSearcher(t *testing.T) {
	cache := repomap.NewCache()
	ws := t.TempDir()

	cfg := config.Default() // backend lexical
	s, err := buildCodeSearcher(cfg, ws, cache)
	if err != nil || s.Backend() != "lexical" {
		t.Fatalf("lexical: backend=%v err=%v", s, err)
	}

	cfg.CodeSearch.Backend = "embedding"
	cfg.Embedding.Type = "fake"
	s, err = buildCodeSearcher(cfg, ws, cache)
	if err != nil || s.Backend() != "embedding" {
		t.Fatalf("embedding: backend=%v err=%v", s, err)
	}

	// embedding backend without an embedding provider must fail loudly.
	cfg.Embedding.Type = ""
	if _, err := buildCodeSearcher(cfg, ws, cache); err == nil {
		t.Fatal("expected error when embedding.type is unset")
	}

	// unknown backend.
	cfg.Embedding.Type = "fake"
	cfg.CodeSearch.Backend = "bogus"
	if _, err := buildCodeSearcher(cfg, ws, cache); err == nil {
		t.Fatal("expected unknown-backend error")
	}
}

func TestBuildEmbedderFallbackCreds(t *testing.T) {
	cfg := config.Default()
	cfg.Embedding.Type = "openai-compatible"
	cfg.Embedding.Model = "text-embed"
	cfg.Embedding.APIKey = ""
	cfg.Provider.APIKey = "sk-provider"
	cfg.Provider.BaseURL = "https://api.example.com/v1"

	if _, err := buildEmbedder(cfg); err != nil {
		t.Fatalf("should fall back to provider api_key/base_url: %v", err)
	}

	cfg.Provider.APIKey = ""
	if _, err := buildEmbedder(cfg); err == nil {
		t.Fatal("expected missing api key error")
	}
}
