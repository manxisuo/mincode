package embedding

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestFakeProviderSimilarity(t *testing.T) {
	p := NewFakeProvider("m", 128)
	if p.Dim() != 128 || p.Name() != "fake" || p.Model() != "m" {
		t.Fatalf("provider meta = %s/%s/%d", p.Name(), p.Model(), p.Dim())
	}
	ctx := context.Background()
	vecs, err := p.Embed(ctx, []string{
		"context token budget enforcement",
		"context token budget enforcement",
		"shell permission dangerous command deny",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(vecs) != 3 || len(vecs[0]) != 128 {
		t.Fatalf("shape = %d x %d", len(vecs), len(vecs[0]))
	}
	if s := Cosine(vecs[0], vecs[1]); s < 0.999 {
		t.Fatalf("identical texts should be ~1.0, got %f", s)
	}
	if s := Cosine(vecs[0], vecs[2]); s >= 0.5 {
		t.Fatalf("unrelated texts should be far apart, got %f", s)
	}
}

func TestCosine(t *testing.T) {
	if Cosine(nil, nil) != 0 || Cosine([]float32{1}, []float32{1, 2}) != 0 {
		t.Fatal("empty/mismatched should be 0")
	}
	if s := Cosine([]float32{1, 0}, []float32{0, 1}); s != 0 {
		t.Fatalf("orthogonal = %f", s)
	}
	if s := Cosine([]float32{0, 3}, []float32{0, 5}); s < 0.999 {
		t.Fatalf("parallel = %f", s)
	}
}

func TestEmbeddingTokenize(t *testing.T) {
	got := Tokenize("DefaultBudgetTokens internal/ctx_mgr")
	want := map[string]bool{"default": true, "budget": true, "tokens": true, "internal": true, "ctx": true, "mgr": true}
	seen := map[string]bool{}
	for _, w := range got {
		seen[w] = true
	}
	for w := range want {
		if !seen[w] {
			t.Fatalf("Tokenize missing %q in %v", w, got)
		}
	}
}

func TestCompatibleProviderBatches(t *testing.T) {
	var requests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/embeddings" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("auth = %q", r.Header.Get("Authorization"))
		}
		requests++
		var req struct {
			Input []string `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode: %v", err)
		}
		out := map[string]any{"data": []any{}}
		data := out["data"].([]any)
		for i, in := range req.Input {
			data = append(data, map[string]any{
				"index":     i,
				"embedding": []float32{float32(len(in)), 1, 0, 0},
			})
		}
		out["data"] = data
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	}))
	defer srv.Close()

	p := NewCompatibleProvider(srv.URL+"/v1", "test-key", "text-embed", 5*time.Second, 2)
	vecs, err := p.Embed(context.Background(), []string{"a", "bb", "ccc"})
	if err != nil {
		t.Fatal(err)
	}
	if len(vecs) != 3 {
		t.Fatalf("vectors = %d", len(vecs))
	}
	// Batch size 2 over 3 inputs → 2 requests.
	if requests != 2 {
		t.Fatalf("requests = %d, want 2", requests)
	}
	if vecs[0][0] != 1 || vecs[2][0] != 3 {
		t.Fatalf("order not preserved: %v %v", vecs[0], vecs[2])
	}
	if p.Dim() != 4 {
		t.Fatalf("dim = %d", p.Dim())
	}
}

func TestCompatibleProviderError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"bad key"}`))
	}))
	defer srv.Close()

	p := NewCompatibleProvider(srv.URL, "bad", "m", time.Second, 8)
	if _, err := p.Embed(context.Background(), []string{"x"}); err == nil {
		t.Fatal("expected error")
	}
}

func TestCompatibleProviderMissingKey(t *testing.T) {
	p := NewCompatibleProvider("http://127.0.0.1:1", "", "m", time.Second, 8)
	if _, err := p.Embed(context.Background(), []string{"x"}); err == nil {
		t.Fatal("expected missing-key error")
	}
	if _, err := p.Embed(context.Background(), nil); err != nil {
		t.Fatalf("empty input should be a no-op: %v", err)
	}
}
