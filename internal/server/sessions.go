package server

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/manxisuo/mincode/internal/ctxmgr"
	"github.com/manxisuo/mincode/internal/llm"
	"github.com/manxisuo/mincode/internal/session"
)

// SetSessions wires the session store for list/switch APIs.
func (s *Server) SetSessions(st *session.Store) {
	s.sessions = st
}

type sessionListItem struct {
	ID           string `json:"id"`
	Title        string `json:"title,omitempty"`
	Note         string `json:"note,omitempty"`
	Workspace    string `json:"workspace"`
	Provider     string `json:"provider"`
	Model        string `json:"model"`
	Turns        int    `json:"turns"`
	MessageCount int    `json:"message_count"`
	UpdatedAt    string `json:"updated_at"`
	IsCurrent    bool   `json:"is_current"`
}

type chatMessageOut struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

func (s *Server) activeSessionID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.activeSessID == "" {
		return s.opts.SessionID
	}
	return s.activeSessID
}

func (s *Server) handleSessionList(w http.ResponseWriter, _ *http.Request) {
	if s.sessions == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"sessions": []sessionListItem{},
			"current":  s.opts.SessionID,
			"note":     "sessions unavailable",
		})
		return
	}
	recs, err := s.sessions.List()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	cur := s.activeSessionID()
	list := make([]sessionListItem, 0, len(recs))
	// newest first
	for i := len(recs) - 1; i >= 0; i-- {
		r := recs[i]
		if s.opts.Workspace != "" && r.Workspace != "" && !sameWorkspace(r.Workspace, s.opts.Workspace) {
			continue
		}
		turns := r.Turns
		if turns == 0 {
			turns = countUserTurns(r.Entries)
		}
		list = append(list, sessionListItem{
			ID:           r.ID,
			Title:        r.Title,
			Note:         r.Note,
			Workspace:    r.Workspace,
			Provider:     r.Provider,
			Model:        r.Model,
			Turns:        turns,
			MessageCount: len(r.Entries),
			UpdatedAt:    r.UpdatedAt.UTC().Format(timeFormat),
			IsCurrent:    r.ID == cur,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"sessions":     list,
		"current":      cur,
		"process_id":   s.opts.SessionID,
		"sessions_dir": s.sessions.Dir,
		"workspace":    s.opts.Workspace,
	})
}

func (s *Server) handleSessionLoad(w http.ResponseWriter, r *http.Request) {
	if s.sessions == nil || s.agent == nil || s.agent.Ctx == nil {
		writeErr(w, http.StatusNotFound, "sessions unavailable")
		return
	}
	id := r.PathValue("id")
	if id == "" || strings.Contains(id, "..") || strings.ContainsAny(id, `/\`) {
		writeErr(w, http.StatusBadRequest, "invalid session id")
		return
	}
	// Persist current conversation first (same id as last active).
	s.saveCurrentSession()

	rec, err := s.sessions.Load(id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "session not found: "+id)
		return
	}
	entries := make([]ctxmgr.ExportedEntry, 0, len(rec.Entries))
	msgs := make([]chatMessageOut, 0, len(rec.Entries))
	for _, e := range rec.Entries {
		entries = append(entries, ctxmgr.ExportedEntry{
			Msg: llm.Message{
				Role:       llm.Role(e.Role),
				Content:    e.Content,
				ToolCalls:  e.ToolCalls,
				ToolCallID: e.ToolCallID,
			},
			Source: e.Source,
			Tokens: e.Tokens,
		})
		if e.Role == "user" || e.Role == "assistant" {
			if strings.TrimSpace(e.Content) != "" {
				msgs = append(msgs, chatMessageOut{Role: e.Role, Content: e.Content})
			}
		}
	}
	s.agent.Ctx.RestoreEntries(entries)
	// Rebuild a snapshot so the Inspector shows the loaded session's context
	// immediately (not the previous turn's).
	if _, snap := s.agent.Ctx.BuildRequest(nil); snap.Step > 0 {
		_ = snap
	}

	s.mu.Lock()
	s.activeSessID = rec.ID
	s.lastResult = nil
	s.lastErr = ""
	s.mu.Unlock()

	// Keep provider/model labels if the record has them.
	prov, model := s.opts.Provider, s.opts.Model
	if rec.Provider != "" {
		prov = rec.Provider
	}
	if rec.Model != "" {
		model = rec.Model
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":       true,
		"id":       rec.ID,
		"provider": prov,
		"model":    model,
		"turns":    countUserTurns(rec.Entries),
		"messages": msgs,
		"entries":  len(rec.Entries),
	})
}

func (s *Server) handleSessionCurrent(w http.ResponseWriter, _ *http.Request) {
	id := s.activeSessionID()
	entryCount := 0
	if s.agent != nil && s.agent.Ctx != nil {
		entryCount = s.agent.Ctx.Len()
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id":           id,
		"process_id":   s.opts.SessionID,
		"entry_count":  entryCount,
		"sessions_dir": "",
	})
}

// saveCurrentSession writes agent context under the active session id.
func (s *Server) saveCurrentSession() {
	if s.sessions == nil || s.agent == nil || s.agent.Ctx == nil {
		return
	}
	id := s.activeSessionID()
	if id == "" {
		return
	}
	entries := s.agent.Ctx.ExportEntries()
	rec := &session.Record{
		ID:           id,
		Workspace:    s.opts.Workspace,
		Provider:     s.opts.Provider,
		Model:        s.opts.Model,
		SystemPrompt: s.agent.Ctx.System(),
		Entries:      make([]session.Entry, 0, len(entries)),
	}
	// Preserve auto-generated title/note across saves.
	if prev, err := s.sessions.Load(id); err == nil && prev != nil {
		rec.Title = prev.Title
		rec.Note = prev.Note
		rec.CreatedAt = prev.CreatedAt
	}
	for _, e := range entries {
		rec.Entries = append(rec.Entries, session.Entry{
			Role:       string(e.Msg.Role),
			Content:    e.Msg.Content,
			ToolCalls:  e.Msg.ToolCalls,
			ToolCallID: e.Msg.ToolCallID,
			Source:     e.Source,
			Tokens:     e.Tokens,
		})
	}
	rec.Turns = countUserTurns(rec.Entries)
	_ = s.sessions.Save(rec)
}

func countUserTurns(entries []session.Entry) int {
	n := 0
	for _, e := range entries {
		if e.Role == "user" {
			n++
		}
	}
	return n
}

func sameWorkspace(a, b string) bool {
	a = strings.TrimRight(strings.ReplaceAll(a, "/", `\`), `\`)
	b = strings.TrimRight(strings.ReplaceAll(b, "/", `\`), `\`)
	return strings.EqualFold(a, b)
}

func validSessionID(id string) bool {
	if id == "" || strings.Contains(id, "..") || strings.ContainsAny(id, `/\`) {
		return false
	}
	return true
}

type sessionUpdateRequest struct {
	// Title/note are optional. Send a field to update it; omit to leave unchanged.
	Title *string `json:"title"`
	Note  *string `json:"note"`
}

// handleSessionUpdate renames a session and/or updates its note.
func (s *Server) handleSessionUpdate(w http.ResponseWriter, r *http.Request) {
	if s.sessions == nil {
		writeErr(w, http.StatusNotFound, "sessions unavailable")
		return
	}
	id := r.PathValue("id")
	if !validSessionID(id) {
		writeErr(w, http.StatusBadRequest, "invalid session id")
		return
	}
	var req sessionUpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json body")
		return
	}
	if req.Title == nil && req.Note == nil {
		writeErr(w, http.StatusBadRequest, "title or note required")
		return
	}
	if err := s.sessions.SetMeta(id, req.Title, req.Note); err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	rec, _ := s.sessions.Load(id)
	title, note := "", ""
	if rec != nil {
		title, note = rec.Title, rec.Note
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":    true,
		"id":    id,
		"title": title,
		"note":  note,
	})
}

// handleSessionDelete removes a stored session (not the active one).
func (s *Server) handleSessionDelete(w http.ResponseWriter, r *http.Request) {
	if s.sessions == nil {
		writeErr(w, http.StatusNotFound, "sessions unavailable")
		return
	}
	id := r.PathValue("id")
	if !validSessionID(id) {
		writeErr(w, http.StatusBadRequest, "invalid session id")
		return
	}
	if id == s.activeSessionID() {
		writeErr(w, http.StatusConflict, "cannot delete the active session — switch first")
		return
	}
	if err := s.sessions.Delete(id); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": id})
}

const timeFormat = "2006-01-02T15:04:05Z"
