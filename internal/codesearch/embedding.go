package codesearch

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/manxisuo/mincode/internal/embedding"
	"github.com/manxisuo/mincode/internal/repomap"
)

const (
	defaultEmbedTokens  = 800
	defaultEmbedChunks  = 2000
	defaultExcerptLines = 24
	excerptLookback     = 2
	maxEmbedTextBytes   = 4000
)

// EmbeddingOptions configures an embedding-backed searcher.
type EmbeddingOptions struct {
	MaxTokens    int
	MaxChunks    int
	ExcerptLines int
	Cache        *repomap.Cache
}

// EmbeddingSearcher implements Searcher using vector similarity. Chunk text
// includes a bounded source excerpt (with a small lookback so doc comments are
// captured), which makes retrieval meaningfully semantic.
type EmbeddingSearcher struct {
	workspace    string
	embedder     embedding.Provider
	maxTokens    int
	maxChunks    int
	excerptLines int
	cache        *repomap.Cache

	mu       sync.Mutex // serializes builds (which may hit the network)
	vectors  map[string][]float32
	srcCache map[string]sourceFile
}

type sourceFile struct {
	modTime int64
	size    int64
	lines   []string
}

type embedChunk struct {
	Chunk
	embedText string
	vec       []float32
}

// NewEmbedding creates an embedding searcher. embedder must be non-nil.
func NewEmbedding(workspace string, embedder embedding.Provider, opts EmbeddingOptions) *EmbeddingSearcher {
	if opts.MaxTokens <= 0 {
		opts.MaxTokens = defaultEmbedTokens
	}
	if opts.MaxChunks <= 0 {
		opts.MaxChunks = defaultEmbedChunks
	}
	if opts.ExcerptLines <= 0 {
		opts.ExcerptLines = defaultExcerptLines
	}
	return &EmbeddingSearcher{
		workspace:    workspace,
		embedder:     embedder,
		maxTokens:    opts.MaxTokens,
		maxChunks:    opts.MaxChunks,
		excerptLines: opts.ExcerptLines,
		cache:        opts.Cache,
		vectors:      make(map[string][]float32),
		srcCache:     make(map[string]sourceFile),
	}
}

// Backend identifies the retrieval backend for observability.
func (s *EmbeddingSearcher) Backend() string { return "embedding" }

// Search embeds the query and returns the top-k chunks by cosine similarity.
func (s *EmbeddingSearcher) Search(ctx context.Context, query string, k int) ([]Hit, error) {
	if k <= 0 {
		k = 6
	}
	if strings.TrimSpace(query) == "" {
		return nil, nil
	}
	chunks, err := s.prepare(ctx)
	if err != nil {
		return nil, err
	}
	if len(chunks) == 0 {
		return nil, nil
	}
	qv, err := s.embedder.Embed(ctx, []string{query})
	if err != nil {
		return nil, err
	}
	if len(qv) == 0 {
		return nil, nil
	}
	q := qv[0]
	qTerms := dedupe(tokenize(query))

	hits := make([]Hit, 0, len(chunks))
	for _, c := range chunks {
		score := embedding.Cosine(q, c.vec)
		if score <= 0 {
			continue
		}
		matched := matchedTerms(qTerms, c.embedText)
		hits = append(hits, Hit{Chunk: c.Chunk, Score: score, Matched: matched})
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		return hits[i].Path < hits[j].Path
	})
	if len(hits) > k {
		hits = hits[:k]
	}
	return hits, nil
}

// SearchContext searches and renders a compact block for context injection.
func (s *EmbeddingSearcher) SearchContext(ctx context.Context, query string, k int) (string, []string, error) {
	hits, err := s.Search(ctx, query, k)
	if err != nil {
		return "", nil, err
	}
	if len(hits) == 0 {
		return "", nil, nil
	}
	text := Render(query, hits, s.maxTokens)
	summary := make([]string, 0, len(hits))
	for _, h := range hits {
		loc := h.Path
		if h.Line > 0 {
			loc = fmt.Sprintf("%s:%d", h.Path, h.Line)
		}
		summary = append(summary, fmt.Sprintf("%s %.3f", loc, h.Score))
	}
	return text, summary, nil
}

// prepare scans the workspace, builds chunks, and embeds any new ones. Cached
// vectors are reused by content hash, so unchanged chunks are not re-embedded.
func (s *EmbeddingSearcher) prepare(ctx context.Context) ([]embedChunk, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	m, entries, err := repomap.Scan(ctx, s.workspace, repomap.Options{Cache: s.cache})
	if err != nil {
		return nil, err
	}
	chunks := s.chunks(entries, m.Root)
	if len(chunks) == 0 {
		return nil, nil
	}

	hashes := make([]string, len(chunks))
	var (
		missingIdx  []int
		missingText []string
	)
	for i := range chunks {
		h := hashText(chunks[i].embedText)
		hashes[i] = h
		if _, ok := s.vectors[h]; !ok {
			missingIdx = append(missingIdx, i)
			missingText = append(missingText, chunks[i].embedText)
		}
	}
	if len(missingText) > 0 {
		vecs, err := s.embedder.Embed(ctx, missingText)
		if err != nil {
			return nil, err
		}
		for j, idx := range missingIdx {
			s.vectors[hashes[idx]] = vecs[j]
		}
	}
	for i := range chunks {
		chunks[i].vec = s.vectors[hashes[i]]
	}
	return chunks, nil
}

// chunks builds embeddable chunks with bounded source excerpts.
func (s *EmbeddingSearcher) chunks(entries []repomap.FileEntry, base string) []embedChunk {
	out := make([]embedChunk, 0, len(entries)*4)
	for _, e := range entries {
		var lines []string
		if len(e.Symbols) > 0 {
			lines = s.sourceLines(filepath.Join(base, filepath.FromSlash(e.Path)))
		}
		if len(e.Symbols) == 0 {
			label := e.Path + " [" + e.Lang + "]"
			out = append(out, embedChunk{
				Chunk:     Chunk{Path: e.Path, Package: e.Package, Kind: "file", Name: baseName(e.Path), Text: label},
				embedText: strings.Join([]string{e.Path, e.Package, e.Lang}, " "),
			})
			continue
		}
		for _, sym := range e.Symbols {
			label := sym.Sig
			if label == "" {
				label = sym.Kind + " " + sym.Name
			}
			excerpt := excerpt(lines, sym.Line, sym.EndLine, s.excerptLines)
			head := strings.Join([]string{e.Path, e.Package, sym.Kind, sym.Name, sym.Sig}, " ")
			embedText := head
			if excerpt != "" {
				embedText += "\n" + excerpt
			}
			if len(embedText) > maxEmbedTextBytes {
				embedText = embedText[:maxEmbedTextBytes]
			}
			out = append(out, embedChunk{
				Chunk: Chunk{
					Path:    e.Path,
					Package: e.Package,
					Kind:    sym.Kind,
					Name:    sym.Name,
					Sig:     sym.Sig,
					Line:    sym.Line,
					Text:    label,
				},
				embedText: embedText,
			})
			if len(out) >= s.maxChunks {
				return out
			}
		}
	}
	return out
}

// sourceLines returns cached file lines, re-reading when mtime/size changed.
func (s *EmbeddingSearcher) sourceLines(abs string) []string {
	if abs == "" {
		return nil
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil
	}
	modTime, size := info.ModTime().UnixNano(), info.Size()
	if sf, ok := s.srcCache[abs]; ok && sf.modTime == modTime && sf.size == size {
		return sf.lines
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return nil
	}
	raw := strings.Split(string(data), "\n")
	lines := make([]string, len(raw))
	for i, ln := range raw {
		lines[i] = strings.TrimRight(ln, "\r")
	}
	s.srcCache[abs] = sourceFile{modTime: modTime, size: size, lines: lines}
	return lines
}

// excerpt joins the source lines for one symbol (with a small lookback for doc
// comments), bounded by maxLines.
func excerpt(lines []string, start, end, maxLines int) string {
	if len(lines) == 0 || start <= 0 {
		return ""
	}
	from := start - 1 - excerptLookback
	if from < 0 {
		from = 0
	}
	to := end
	if to <= start {
		to = start + maxLines
	}
	if to > start+maxLines {
		to = start + maxLines
	}
	if to > len(lines) {
		to = len(lines)
	}
	if from >= to {
		return ""
	}
	return strings.Join(lines[from:to], "\n")
}

func matchedTerms(queryTerms []string, text string) []string {
	if len(queryTerms) == 0 {
		return nil
	}
	have := make(map[string]bool)
	for _, t := range tokenize(text) {
		have[t] = true
	}
	var out []string
	for _, t := range queryTerms {
		if have[t] {
			out = append(out, t)
		}
	}
	return out
}

func hashText(s string) string {
	sum := sha1.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}
