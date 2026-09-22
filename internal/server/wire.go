package server

import (
	"net/http"
	"time"
)

// handleWire returns the last provider-bound LLM request (T-obs-1).
func (s *Server) handleWire(w http.ResponseWriter, _ *http.Request) {
	if s.agent == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"wire":   nil,
			"note":   "agent unavailable",
			"stages": []string{"context_build", "sanitize_tool_pairs", "provider_payload"},
		})
		return
	}
	rec := s.agent.LastWire()
	if rec == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"wire":   nil,
			"note":   "no LLM request yet",
			"stages": []string{"context_build", "sanitize_tool_pairs", "provider_payload"},
		})
		return
	}
	// Copy under agent lock semantics: LastWire returns the pointer; marshal
	// is single-threaded enough for local inspector use.
	writeJSON(w, http.StatusOK, map[string]any{
		"wire": rec,
		"chain": []map[string]string{
			{"stage": "context_build", "desc": "Context Builder (budget, instructions, skills, memory)"},
			{"stage": "sanitize_tool_pairs", "desc": "Repair tool/tool_call pairing before provider"},
			{"stage": "provider_payload", "desc": "ChatRequest sent to Provider (messages + tool schemas)"},
		},
		"fetched_at": time.Now().UTC().Format(time.RFC3339),
	})
}
