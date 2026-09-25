package config

import (
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Layer is one source that contributed a config value (T-obs-7).
type Layer struct {
	From  string `json:"from"` // default | file | env:NAME | cli
	Value string `json:"value"`
	Path  string `json:"path,omitempty"`
}

// FieldResolution is the cascade for one config key.
type FieldResolution struct {
	Key    string  `json:"key"`
	Value  string  `json:"value"`
	Winner string  `json:"winner"`
	Trail  []Layer `json:"trail"`
}

// Resolution explains how effective config values were chosen.
type Resolution struct {
	ConfigFile string            `json:"config_file,omitempty"`
	Fields     []FieldResolution `json:"fields"`
}

// Resolve walks Default → YAML file → environment → CLI overrides for the
// keys that matter in the Inspector. api_key values are masked.
func Resolve(path, workspace string, envGet func(string) string, cli map[string]string) Resolution {
	if envGet == nil {
		envGet = os.Getenv
	}
	cfgFile, fileMap := loadFileMap(path, workspace)
	out := Resolution{ConfigFile: cfgFile}

	type spec struct {
		key  string
		env  []string
		get  func() string
		dflt func() string
		mask bool
	}
	base := Default()
	specs := []spec{
		{"provider.type", []string{"MINCODE_PROVIDER_TYPE"}, func() string { return base.Provider.Type }, func() string { return "openai-compatible" }, false},
		{"provider.model", []string{"MINCODE_MODEL", "OPENAI_MODEL"}, func() string { return base.Provider.Model }, func() string { return "gpt-4o-mini" }, false},
		{"provider.base_url", []string{"MINCODE_BASE_URL"}, func() string { return base.Provider.BaseURL }, func() string { return "https://api.openai.com/v1" }, false},
		{"provider.api_key", []string{"MINCODE_API_KEY", "OPENAI_API_KEY"}, func() string { return "" }, func() string { return "" }, true},
		{"provider.max_tokens", []string{"MINCODE_MAX_TOKENS"}, func() string { return itoa(base.Provider.MaxTokens) }, func() string { return "" }, false},
		{"agent.max_steps", nil, func() string { return itoa(base.Agent.MaxSteps) }, func() string { return "30" }, false},
		{"agent.token_budget", nil, func() string { return itoa(base.Agent.TokenBudget) }, func() string { return "32000" }, false},
		{"agent.compress_at", nil, func() string { return itoa(base.Agent.CompressAt) }, func() string { return "18000" }, false},
		{"agent.repo_map", nil, func() string { return boolStr(base.Agent.RepoMap) }, func() string { return "true" }, false},
		{"agent.repo_map_tokens", nil, func() string { return itoa(base.Agent.RepoMapTokens) }, func() string { return "1500" }, false},
		{"websearch.type", []string{"MINCODE_WEBSEARCH_TYPE"}, func() string { return base.WebSearch.Type }, func() string { return "" }, false},
		{"websearch.api_key", []string{"MINCODE_WEBSEARCH_API_KEY"}, func() string { return "" }, func() string { return "" }, true},
		{"websearch.base_url", []string{"MINCODE_WEBSEARCH_BASE_URL"}, func() string { return "" }, func() string { return "" }, false},
		{"data.location", []string{"MINCODE_DATA_LOCATION"}, func() string { return base.Data.Location }, func() string { return "global" }, false},
	}

	for _, sp := range specs {
		fr := FieldResolution{Key: sp.key}
		add := func(from, val string, path string) {
			if val == "" {
				return
			}
			show := val
			if sp.mask {
				show = maskSecret(val)
			}
			fr.Trail = append(fr.Trail, Layer{From: from, Value: show, Path: path})
			fr.Value = show
			fr.Winner = from
		}
		add("default", sp.dflt(), "")
		if fileMap != nil {
			if v, ok := yamlString(fileMap, strings.Split(sp.key, ".")...); ok {
				add("file", v, cfgFile)
			}
		}
		for _, ek := range sp.env {
			if v := envGet(ek); v != "" {
				add("env:"+ek, v, "")
				break
			}
		}
		if cli != nil {
			if v, ok := cli[sp.key]; ok && v != "" {
				add("cli", v, "")
			}
		}
		if fr.Value == "" {
			fr.Value = sp.get()
			if fr.Winner == "" {
				fr.Winner = "default"
			}
		}
		out.Fields = append(out.Fields, fr)
	}
	return out
}

func loadFileMap(path, workspace string) (string, map[string]any) {
	var candidates []string
	if path != "" {
		candidates = []string{path}
	} else {
		if workspace != "" {
			candidates = append(candidates,
				filepath.Join(workspace, "mincode.yaml"),
				filepath.Join(workspace, "mincode.yml"),
			)
		}
		candidates = append(candidates, "mincode.yaml", "mincode.yml")
	}
	for _, c := range candidates {
		data, err := os.ReadFile(c)
		if err != nil {
			continue
		}
		var m map[string]any
		if yaml.Unmarshal(data, &m) != nil {
			continue
		}
		abs, err := filepath.Abs(c)
		if err != nil {
			abs = c
		}
		return abs, m
	}
	return "", nil
}

func yamlString(m map[string]any, keys ...string) (string, bool) {
	var cur any = m
	for _, k := range keys {
		mm, ok := cur.(map[string]any)
		if !ok {
			return "", false
		}
		cur, ok = mm[k]
		if !ok {
			return "", false
		}
	}
	switch v := cur.(type) {
	case string:
		return v, true
	case int:
		return itoa(v), true
	case float64:
		return itoa(int(v)), true
	case bool:
		if v {
			return "true", true
		}
		return "false", true
	}
	return "", false
}

func maskSecret(s string) string {
	if len(s) <= 4 {
		return "****"
	}
	return s[:2] + "…" + s[len(s)-2:]
}

func boolStr(v *bool) string {
	if v != nil && !*v {
		return "false"
	}
	return "true"
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	if neg {
		b = append([]byte{'-'}, b...)
	}
	return string(b)
}
