// Package embedding provides a replaceable text-embedding provider abstraction
// plus a deterministic Fake (offline tests) and an OpenAI-compatible HTTP
// provider. It is used by lexical/semantic code search and future RAG.
package embedding

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io"
	"math"
	"net/http"
	"strings"
	"time"
	"unicode"
)

// Provider turns text into vectors. Implementations must be safe for the
// dimensions reported by Dim (0 means "unknown until first call").
type Provider interface {
	Name() string
	Model() string
	Dim() int
	Embed(ctx context.Context, texts []string) ([][]float32, error)
}

// Error is a provider failure with optional HTTP status.
type Error struct {
	Provider string
	Status   int
	Err      error
}

func (e *Error) Error() string {
	if e.Status != 0 {
		return fmt.Sprintf("embedding %s: status %d: %v", e.Provider, e.Status, e.Err)
	}
	return fmt.Sprintf("embedding %s: %v", e.Provider, e.Err)
}

func (e *Error) Unwrap() error { return e.Err }

// --- Deterministic fake provider ---

// FakeProvider hashes tokens into a fixed-dimension bag-of-words vector. It is
// deterministic and offline: texts sharing tokens get higher cosine similarity.
type FakeProvider struct {
	model string
	dim   int
}

// NewFakeProvider creates a fake embedder. dim <= 0 uses 256.
func NewFakeProvider(model string, dim int) *FakeProvider {
	if dim <= 0 {
		dim = 256
	}
	if model == "" {
		model = "fake-embedding"
	}
	return &FakeProvider{model: model, dim: dim}
}

func (p *FakeProvider) Name() string  { return "fake" }
func (p *FakeProvider) Model() string { return p.model }
func (p *FakeProvider) Dim() int      { return p.dim }

func (p *FakeProvider) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	out := make([][]float32, len(texts))
	for i, t := range texts {
		out[i] = hashEmbed(t, p.dim)
	}
	return out, nil
}

func hashEmbed(text string, dim int) []float32 {
	v := make([]float32, dim)
	for _, tok := range Tokenize(text) {
		h := fnv.New32a()
		_, _ = h.Write([]byte(tok))
		v[h.Sum32()%uint32(dim)]++
	}
	var norm float64
	for _, x := range v {
		norm += float64(x) * float64(x)
	}
	if norm > 0 {
		n := float32(math.Sqrt(norm))
		for i := range v {
			v[i] /= n
		}
	}
	return v
}

// Cosine returns the cosine similarity of a and b (0 when either is empty or
// the dimensions differ).
func Cosine(a, b []float32) float64 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

// --- OpenAI-compatible provider ---

const (
	defaultTimeout   = 60 * time.Second
	defaultBatchSize = 64
	maxResponseBytes = 32 << 20
)

// CompatibleProvider calls an OpenAI-compatible /embeddings endpoint.
type CompatibleProvider struct {
	BaseURL    string
	APIKey     string
	ModelName  string
	BatchSize  int
	HTTPClient *http.Client

	dim int
}

// NewCompatibleProvider builds a provider. timeout <= 0 uses 60s; batch <= 0
// uses 64.
func NewCompatibleProvider(baseURL, apiKey, model string, timeout time.Duration, batch int) *CompatibleProvider {
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	if batch <= 0 {
		batch = defaultBatchSize
	}
	return &CompatibleProvider{
		BaseURL:    strings.TrimRight(baseURL, "/"),
		APIKey:     apiKey,
		ModelName:  model,
		BatchSize:  batch,
		HTTPClient: &http.Client{Timeout: timeout},
	}
}

func (p *CompatibleProvider) Name() string  { return "openai-compatible" }
func (p *CompatibleProvider) Model() string { return p.ModelName }
func (p *CompatibleProvider) Dim() int      { return p.dim }

type embedRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

type embedResponse struct {
	Data []struct {
		Index     int       `json:"index"`
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
}

// Embed sends texts in batches and returns one vector per input, preserving
// order.
func (p *CompatibleProvider) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	if p.APIKey == "" {
		return nil, &Error{Provider: p.Name(), Err: fmt.Errorf("missing api key")}
	}
	out := make([][]float32, 0, len(texts))
	for start := 0; start < len(texts); start += p.BatchSize {
		end := start + p.BatchSize
		if end > len(texts) {
			end = len(texts)
		}
		vecs, err := p.embedBatch(ctx, texts[start:end])
		if err != nil {
			return nil, err
		}
		out = append(out, vecs...)
	}
	return out, nil
}

func (p *CompatibleProvider) embedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	body, err := json.Marshal(embedRequest{Model: p.ModelName, Input: texts})
	if err != nil {
		return nil, &Error{Provider: p.Name(), Err: err}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.BaseURL+"/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, &Error{Provider: p.Name(), Err: err}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.APIKey)

	resp, err := p.HTTPClient.Do(req)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, &Error{Provider: p.Name(), Err: err}
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, &Error{Provider: p.Name(), Status: resp.StatusCode, Err: err}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &Error{Provider: p.Name(), Status: resp.StatusCode, Err: fmt.Errorf("%s", truncateBody(raw))}
	}
	var wr embedResponse
	if err := json.Unmarshal(raw, &wr); err != nil {
		return nil, &Error{Provider: p.Name(), Status: resp.StatusCode, Err: fmt.Errorf("decode response: %w", err)}
	}
	if len(wr.Data) != len(texts) {
		return nil, &Error{Provider: p.Name(), Status: resp.StatusCode,
			Err: fmt.Errorf("expected %d embeddings, got %d", len(texts), len(wr.Data))}
	}
	out := make([][]float32, len(wr.Data))
	for _, d := range wr.Data {
		if d.Index < 0 || d.Index >= len(out) {
			return nil, &Error{Provider: p.Name(), Err: fmt.Errorf("bad embedding index %d", d.Index)}
		}
		out[d.Index] = d.Embedding
		if p.dim == 0 {
			p.dim = len(d.Embedding)
		}
	}
	for i, v := range out {
		if v == nil {
			return nil, &Error{Provider: p.Name(), Err: fmt.Errorf("missing embedding at index %d", i)}
		}
	}
	return out, nil
}

func truncateBody(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 300 {
		return s[:300] + "…"
	}
	return s
}

// Tokenize lowercases and splits text on separators and camelCase boundaries,
// dropping single characters. Exported so search backends can reuse it.
func Tokenize(s string) []string {
	var out []string
	var cur []rune
	flush := func() {
		if len(cur) < 2 {
			cur = cur[:0]
			return
		}
		out = append(out, strings.ToLower(string(cur)))
		cur = cur[:0]
	}
	runes := []rune(s)
	for i, r := range runes {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			if i > 0 && unicode.IsUpper(r) {
				prev := runes[i-1]
				if unicode.IsLower(prev) || unicode.IsDigit(prev) {
					flush()
				}
			}
			cur = append(cur, unicode.ToLower(r))
		default:
			flush()
		}
	}
	flush()
	return out
}
