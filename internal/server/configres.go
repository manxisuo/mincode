package server

import (
	"net/http"
)

// handleConfigResolution returns the config cascade (T-obs-7).
func (s *Server) handleConfigResolution(w http.ResponseWriter, _ *http.Request) {
	if s.opts.Resolution != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":          true,
			"resolution":  s.opts.Resolution,
			"workspace":   s.opts.Workspace,
			"note":        "winner is last layer: default → file → env → cli",
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":    true,
		"note":  "resolution not captured for this process",
		"resolution": map[string]any{"fields": []any{}},
	})
}
