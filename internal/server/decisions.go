package server

import (
	"net/http"

	"github.com/manxisuo/mincode/internal/observability"
)

// handleDecisions returns recent Runtime decision traces (T-obs-4).
func (s *Server) handleDecisions(w http.ResponseWriter, r *http.Request) {
	n := 100
	if v := r.URL.Query().Get("limit"); v != "" {
		if x := atoiLimit(v, 200); x > 0 {
			n = x
		}
	}
	var out []observability.DecisionData
	// Prefer hub recent events (includes live SSE path).
	if s.hub != nil {
		evs := s.hub.recent(400)
		for _, e := range evs {
			if e.Type != observability.EventDecision {
				continue
			}
			if d, ok := e.Data.(observability.DecisionData); ok {
				out = append(out, d)
			} else if m, ok := e.Data.(map[string]any); ok {
				// Decode from JSON replay.
				d := observability.DecisionData{}
				if s, ok := m["domain"].(string); ok {
					d.Domain = s
				}
				if s, ok := m["action"].(string); ok {
					d.Action = s
				}
				if s, ok := m["target"].(string); ok {
					d.Target = s
				}
				if s, ok := m["policy"].(string); ok {
					d.Policy = s
				}
				if s, ok := m["reason"].(string); ok {
					d.Reason = s
				}
				out = append(out, d)
			}
		}
	}
	if len(out) > n {
		out = out[len(out)-n:]
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"decisions": out,
		"note":      "Runtime decisions only (policy/parallel/loop) — not model hidden reasoning",
	})
}

func atoiLimit(s string, max int) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int(c-'0')
		if n > max {
			return max
		}
	}
	return n
}
