package server

import (
	"context"
	"strings"
	"time"

	"github.com/manxisuo/mincode/internal/llm"
)

const sessionTitleMaxRunes = 40

// autoNameSession names the active session via a short LLM call when Title is
// empty. Safe to run in a background goroutine; never blocks the agent turn.
func (s *Server) autoNameSession(userMsg string) {
	if s.sessions == nil || s.agent == nil || s.agent.Provider == nil {
		return
	}
	id := s.activeSessionID()
	if id == "" {
		return
	}
	if rec, err := s.sessions.Load(id); err == nil && strings.TrimSpace(rec.Title) != "" {
		return
	}
	title := fallbackSessionTitle(userMsg)
	if t := llmSessionTitle(s.agent.Provider, userMsg); t != "" {
		title = t
	}
	if title == "" {
		return
	}
	_ = s.sessions.SetTitle(id, title)
}

func llmSessionTitle(p llm.Provider, userMsg string) string {
	msg := strings.TrimSpace(userMsg)
	if msg == "" {
		return ""
	}
	if len(msg) > 500 {
		msg = truncateRunes(msg, 500)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	temp := 0.2
	maxTok := 24
	resp, err := p.Chat(ctx, llm.ChatRequest{
		Messages: []llm.Message{
			{
				Role: llm.RoleSystem,
				Content: "Generate a short session title (max 8 words) for the user request. " +
					"Reply with the title only: no quotes, no trailing period, no markdown.",
			},
			{Role: llm.RoleUser, Content: msg},
		},
		Temperature: &temp,
		MaxTokens:   &maxTok,
	})
	if err != nil || resp == nil {
		return ""
	}
	return sanitizeSessionTitle(resp.Content)
}

func sanitizeSessionTitle(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	// First non-empty line only.
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	s = strings.TrimSpace(s)
	s = strings.Trim(s, "`\"'“”‘’")
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "#")
	s = strings.TrimSpace(s)
	s = strings.TrimSuffix(s, ".")
	s = strings.TrimSuffix(s, "。")
	s = strings.Join(strings.Fields(s), " ")
	if s == "" {
		return ""
	}
	return truncateRunes(s, sessionTitleMaxRunes)
}

func fallbackSessionTitle(userMsg string) string {
	t := strings.TrimSpace(userMsg)
	if t == "" {
		return ""
	}
	t = strings.Join(strings.Fields(t), " ")
	return truncateRunes(t, sessionTitleMaxRunes)
}

func truncateRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
