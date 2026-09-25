package server

import (
	"net/http"
	"strings"
)

// handleToolResult returns full tool output for a call_id (T-obs-6).
func (s *Server) handleToolResult(w http.ResponseWriter, r *http.Request) {
	callID := strings.TrimSpace(r.URL.Query().Get("call_id"))
	if callID == "" {
		writeErr(w, http.StatusBadRequest, "call_id required")
		return
	}
	if s.agent == nil || s.agent.Ctx == nil {
		writeErr(w, http.StatusNotFound, "agent unavailable")
		return
	}
	content, ok := s.agent.Ctx.ToolResultByCallID(callID)
	if !ok {
		writeErr(w, http.StatusNotFound, "no tool result for call_id")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"call_id": callID,
		"content": content,
		"bytes":   len(content),
	})
}
