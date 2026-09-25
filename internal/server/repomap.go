package server

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/manxisuo/mincode/internal/repomap"
)

const repoMapAPIMaxTokens = 4000

// handleRepoMap builds (or returns) the repository map with ranking rationale.
// Query params: path (subdir), focus (ranking term), max_tokens.
func (s *Server) handleRepoMap(w http.ResponseWriter, r *http.Request) {
	if !s.opts.RepoMapEnabled {
		writeJSON(w, http.StatusOK, map[string]any{"enabled": false})
		return
	}
	if s.ws == nil {
		writeErr(w, http.StatusServiceUnavailable, "workspace unavailable")
		return
	}

	budget := s.opts.RepoMapTokens
	if budget <= 0 {
		budget = repomap.DefaultMaxTokens
	}
	if v := strings.TrimSpace(r.URL.Query().Get("max_tokens")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			budget = n
		}
	}
	if budget > repoMapAPIMaxTokens {
		budget = repoMapAPIMaxTokens
	}
	focus := strings.TrimSpace(r.URL.Query().Get("focus"))

	sub := ""
	if p := strings.TrimSpace(r.URL.Query().Get("path")); p != "" && p != "." {
		abs, err := s.ws.Resolve(p)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if info, serr := os.Stat(abs); serr == nil && !info.IsDir() {
			abs = filepath.Dir(abs)
		}
		if rel, rerr := filepath.Rel(s.ws.Root(), abs); rerr == nil {
			sub = filepath.ToSlash(rel)
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	m, err := repomap.Build(ctx, s.ws.Root(), repomap.Options{
		MaxTokens: budget,
		Subpath:   sub,
		Focus:     focus,
		Cache:     s.repoCache,
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"enabled":      true,
		"root":         m.Root,
		"subpath":      m.Subpath,
		"focus":        m.Focus,
		"max_tokens":   budget,
		"files":        m.Files,
		"text":         m.Text,
		"tokens":       m.Tokens,
		"scanned":      m.Scanned,
		"skipped":      m.Skipped,
		"truncated":    m.Truncated,
		"build_ms":     m.BuildMS,
		"cache_hits":   m.CacheHits,
		"cache_misses": m.CacheMisses,
	})
}
