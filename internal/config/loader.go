package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"gopkg.in/yaml.v3"
)

// Load reads configuration from an optional YAML file, then applies
// environment variable overrides. Missing file is not an error when
// path is empty; a non-empty missing path is an error.
func Load(path string) (Config, error) {
	return LoadFrom(path, "")
}

// LoadFrom is like Load but also searches the workspace directory for
// mincode.yaml / mincode.yml when path is empty.
func LoadFrom(path, workspace string) (Config, error) {
	cfg := Default()

	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return cfg, fmt.Errorf("read config %s: %w", path, err)
		}
		if err := yaml.Unmarshal(data, &cfg); err != nil {
			return cfg, fmt.Errorf("parse config %s: %w", path, err)
		}
	} else {
		var candidates []string
		if workspace != "" {
			candidates = append(candidates,
				filepath.Join(workspace, "mincode.yaml"),
				filepath.Join(workspace, "mincode.yml"),
			)
		}
		candidates = append(candidates, "mincode.yaml", "mincode.yml")

		for _, candidate := range candidates {
			data, err := os.ReadFile(candidate)
			if err != nil {
				continue
			}
			if err := yaml.Unmarshal(data, &cfg); err != nil {
				return cfg, fmt.Errorf("parse config %s: %w", candidate, err)
			}
			break
		}
	}

	applyEnv(&cfg)
	normalize(&cfg)
	return cfg, nil
}

func applyEnv(cfg *Config) {
	if v := os.Getenv("MINCODE_PROVIDER_TYPE"); v != "" {
		cfg.Provider.Type = v
	}
	if v := os.Getenv("MINCODE_BASE_URL"); v != "" {
		cfg.Provider.BaseURL = v
	}
	if v := firstEnv("MINCODE_API_KEY", "OPENAI_API_KEY"); v != "" {
		cfg.Provider.APIKey = v
	}
	if v := firstEnv("MINCODE_MODEL", "OPENAI_MODEL"); v != "" {
		cfg.Provider.Model = v
	}
	if v := os.Getenv("MINCODE_MAX_TOKENS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.Provider.MaxTokens = n
		}
	}
	if v := os.Getenv("MINCODE_DATA_LOCATION"); v != "" {
		cfg.Data.Location = v
	}
	if v := os.Getenv("MINCODE_DATA_ROOT"); v != "" {
		cfg.Data.Root = v
	}
	if v := os.Getenv("MINCODE_WEBSEARCH_TYPE"); v != "" {
		cfg.WebSearch.Type = v
	}
	if v := os.Getenv("MINCODE_WEBSEARCH_API_KEY"); v != "" {
		cfg.WebSearch.APIKey = v
	}
	if v := os.Getenv("MINCODE_WEBSEARCH_BASE_URL"); v != "" {
		cfg.WebSearch.BaseURL = v
	}
	if v := os.Getenv("MINCODE_EMBEDDING_TYPE"); v != "" {
		cfg.Embedding.Type = v
	}
	if v := os.Getenv("MINCODE_EMBEDDING_API_KEY"); v != "" {
		cfg.Embedding.APIKey = v
	}
	if v := os.Getenv("MINCODE_EMBEDDING_BASE_URL"); v != "" {
		cfg.Embedding.BaseURL = v
	}
	if v := os.Getenv("MINCODE_EMBEDDING_MODEL"); v != "" {
		cfg.Embedding.Model = v
	}
}

func firstEnv(keys ...string) string {
	for _, k := range keys {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	return ""
}

func normalize(cfg *Config) {
	if cfg.Provider.Type == "" {
		cfg.Provider.Type = "openai-compatible"
	}
	if cfg.Provider.Model == "" {
		cfg.Provider.Model = "gpt-4o-mini"
	}
	if cfg.Provider.TimeoutSec <= 0 {
		cfg.Provider.TimeoutSec = 120
	}
	if cfg.Provider.Stream == nil {
		on := true
		cfg.Provider.Stream = &on
	}
	if cfg.Agent.MaxSteps <= 0 {
		cfg.Agent.MaxSteps = 30
	}
	if cfg.Agent.TokenBudget <= 0 {
		cfg.Agent.TokenBudget = 32000
	}
	// compress_at: 0 → default; negative → disabled.
	if cfg.Agent.CompressAt == 0 {
		cfg.Agent.CompressAt = 18000
	}
	if cfg.Agent.CompressAt < 0 {
		cfg.Agent.CompressAt = 0
	}
	if cfg.Agent.SystemPrompt == "" {
		cfg.Agent.SystemPrompt = DefaultSystemPrompt
	}
	if cfg.Agent.ParallelTools == nil {
		on := true
		cfg.Agent.ParallelTools = &on
	}
	if cfg.Agent.MaxParallel <= 0 {
		cfg.Agent.MaxParallel = 4
	}
	if cfg.Agent.Reflection == nil {
		off := false
		cfg.Agent.Reflection = &off
	}
	if cfg.Agent.MaxReflections <= 0 {
		cfg.Agent.MaxReflections = 2
	}
	if cfg.Agent.RepoMap == nil {
		on := true
		cfg.Agent.RepoMap = &on
	}
	if cfg.Agent.RepoMapTokens <= 0 {
		cfg.Agent.RepoMapTokens = 1500
	}
	if cfg.CodeSearch.Enabled == nil {
		on := true
		cfg.CodeSearch.Enabled = &on
	}
	if cfg.CodeSearch.Backend == "" {
		cfg.CodeSearch.Backend = "lexical"
	}
	if cfg.CodeSearch.TopK <= 0 {
		cfg.CodeSearch.TopK = 6
	}
	if cfg.CodeSearch.MaxTokens <= 0 {
		cfg.CodeSearch.MaxTokens = 800
	}
	if cfg.Embedding.TimeoutSec <= 0 {
		cfg.Embedding.TimeoutSec = 60
	}
	if cfg.Embedding.BatchSize <= 0 {
		cfg.Embedding.BatchSize = 64
	}
	if cfg.Data.Location == "" {
		cfg.Data.Location = "global"
	}
	if cfg.Data.Location != "global" && cfg.Data.Location != "workspace" {
		cfg.Data.Location = "global"
	}
}

// EnsureTraceDir creates the trace directory if needed.
func EnsureTraceDir(dir string) error {
	return os.MkdirAll(dir, 0o755)
}

// TracePath returns the JSONL path for a session id.
func TracePath(dir, sessionID string) string {
	return filepath.Join(dir, sessionID+".jsonl")
}
