package cli

import (
	"fmt"
	"strings"
	"time"

	"github.com/manxisuo/mincode/internal/codesearch"
	"github.com/manxisuo/mincode/internal/config"
	"github.com/manxisuo/mincode/internal/embedding"
	"github.com/manxisuo/mincode/internal/repomap"
)

// buildCodeSearcher selects the retrieval backend from config.
func buildCodeSearcher(cfg config.Config, workspace string, cache *repomap.Cache) (codesearch.Retriever, error) {
	switch cfg.CodeSearch.Backend {
	case "", codesearch.BackendLexical:
		return codesearch.New(workspace, codesearch.Options{
			MaxTokens: cfg.CodeSearch.MaxTokens,
			Cache:     cache,
		}), nil
	case "embedding":
		emb, err := buildEmbedder(cfg)
		if err != nil {
			return nil, err
		}
		return codesearch.NewEmbedding(workspace, emb, codesearch.EmbeddingOptions{
			MaxTokens: cfg.CodeSearch.MaxTokens,
			Cache:     cache,
		}), nil
	default:
		return nil, fmt.Errorf("unknown codesearch backend %q", cfg.CodeSearch.Backend)
	}
}

// buildEmbedder constructs the embedding provider from config.
func buildEmbedder(cfg config.Config) (embedding.Provider, error) {
	switch cfg.Embedding.Type {
	case "fake":
		return embedding.NewFakeProvider(cfg.Embedding.Model, cfg.Embedding.Dim), nil
	case "openai-compatible", "openai":
		if strings.TrimSpace(cfg.Embedding.Model) == "" {
			return nil, fmt.Errorf("embedding type %q requires model (e.g. text-embedding-3-small); it is not shared with provider.model", cfg.Embedding.Type)
		}
		apiKey := cfg.Embedding.APIKey
		if apiKey == "" {
			apiKey = cfg.Provider.APIKey
		}
		baseURL := cfg.Embedding.BaseURL
		if baseURL == "" {
			baseURL = cfg.Provider.BaseURL
		}
		if strings.TrimSpace(apiKey) == "" {
			return nil, fmt.Errorf("embedding type %q requires api_key (or MINCODE_EMBEDDING_API_KEY)", cfg.Embedding.Type)
		}
		if strings.TrimSpace(baseURL) == "" {
			return nil, fmt.Errorf("embedding type %q requires base_url", cfg.Embedding.Type)
		}
		return embedding.NewCompatibleProvider(
			baseURL, apiKey, cfg.Embedding.Model,
			time.Duration(cfg.Embedding.TimeoutSec)*time.Second, cfg.Embedding.BatchSize,
		), nil
	default:
		return nil, fmt.Errorf(`codesearch backend "embedding" requires embedding.type (fake|openai-compatible)`)
	}
}
