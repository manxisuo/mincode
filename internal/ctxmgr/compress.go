package ctxmgr

import (
	"fmt"
	"strings"

	"github.com/manxisuo/mincode/internal/llm"
)

// DefaultCompressAtTokens triggers compaction when estimated history exceeds this.
const DefaultCompressAtTokens = 18000

// keepRecentEntries is how many trailing conversation entries survive compaction.
const keepRecentEntries = 6

// CompactionResult describes one compaction run for inspectors/events.
type CompactionResult struct {
	BeforeTokens int    `json:"before_tokens"`
	AfterTokens  int    `json:"after_tokens"`
	Dropped      int    `json:"dropped"`
	Compressed   int    `json:"compressed"`
	Preserved    int    `json:"preserved"`
	Pinned       int    `json:"pinned"`
	SavedTokens  int    `json:"saved_tokens,omitempty"`
	Policy       string `json:"policy,omitempty"`
	Reason       string `json:"reason,omitempty"`
	Summary      string `json:"summary"`
}

// CompactIfNeed folds old history into a structured summary when over threshold.
// Returns nil if no compaction was needed.
func (m *Manager) CompactIfNeed() *CompactionResult {
	if m.compressAt <= 0 {
		return nil
	}
	if m.historyTokens() < m.compressAt {
		return nil
	}
	return m.Compact()
}

// Compact always runs a compaction pass (used by tests and manual triggers).
func (m *Manager) Compact() *CompactionResult {
	if len(m.entries) == 0 {
		return nil
	}

	keep := keepRecentEntries
	if keep > len(m.entries) {
		keep = len(m.entries)
	}
	// Always keep at least the last user turn if present.
	split := len(m.entries) - keep
	if split <= 0 {
		return nil
	}

	old := m.entries[:split]
	recent := m.entries[split:]

	before := 0
	for _, e := range m.entries {
		before += e.tokens
	}
	pinned := 1 // system
	if strings.TrimSpace(m.instructions) != "" {
		pinned++
	}

	summary := BuildStructuredSummary(old)
	sumTok := EstimateTokens(summary)

	// Replace old entries with a single summary entry + recent tail.
	m.entries = append([]entry{{
		msg:    llm.Message{Role: llm.RoleSystem, Content: summary},
		source: SourceSummary,
		tokens: sumTok,
	}}, recent...)

	after := sumTok
	for _, e := range recent {
		after += e.tokens
	}

	res := &CompactionResult{
		BeforeTokens: before,
		AfterTokens:  after,
		Compressed:   len(old),
		Preserved:    len(recent),
		Pinned:       pinned,
		Summary:      summary,
		SavedTokens:  before - after,
		Policy:       "compact_old_history_keep_recent",
		Reason: fmt.Sprintf(
			"history tokens %d exceeded compress_at=%d; folded %d older entry(ies) into summary, kept %d recent",
			before, m.compressAt, len(old), len(recent)),
	}
	return res
}

func (m *Manager) historyTokens() int {
	total := 0
	for _, e := range m.entries {
		total += e.tokens
	}
	return total
}

// SetCompressAt sets the token threshold for auto-compaction (0 disables).
func (m *Manager) SetCompressAt(n int) { m.compressAt = n }

// CompressAt returns the current threshold.
func (m *Manager) CompressAt() int { return m.compressAt }

// BuildStructuredSummary extracts a compact narrative from old entries.
// Phase 7 v1 is extractive (no extra LLM call) so it is offline-testable.
func BuildStructuredSummary(entries []entry) string {
	var (
		goals       []string
		files       = map[string]bool{}
		tools       = map[string]int{}
		lastAsst    string
		lastUser    string
		errSnippets []string
	)

	for _, e := range entries {
		content := e.msg.Content
		switch e.msg.Role {
		case llm.RoleUser:
			if s := firstLine(content); s != "" {
				lastUser = s
				goals = appendUniqueLimited(goals, s, 8)
			}
		case llm.RoleAssistant:
			if s := firstLine(content); s != "" {
				lastAsst = s
			}
		case llm.RoleTool:
			// tool names live on the preceding assistant; scan content for paths
			for _, p := range extractPaths(content) {
				files[p] = true
			}
			low := strings.ToLower(content)
			if strings.Contains(low, "exit_code=1") || strings.Contains(low, "error") ||
				strings.Contains(low, "failed") || strings.Contains(low, "not found") {
				errSnippets = appendUniqueLimited(errSnippets, firstLine(content), 5)
			}
		}
		for _, tc := range e.msg.ToolCalls {
			tools[tc.Name]++
		}
	}

	var b strings.Builder
	b.WriteString("[Conversation Summary]\n")
	if len(goals) > 0 {
		b.WriteString("Goals / user asks:\n")
		for _, g := range goals {
			b.WriteString("  - " + g + "\n")
		}
	}
	if lastUser != "" {
		b.WriteString("Latest user ask: " + lastUser + "\n")
	}
	if lastAsst != "" {
		b.WriteString("Latest assistant note: " + truncateRunes(lastAsst, 160) + "\n")
	}
	if len(files) > 0 {
		b.WriteString("Files mentioned:\n")
		n := 0
		for f := range files {
			b.WriteString("  - " + f + "\n")
			n++
			if n >= 12 {
				break
			}
		}
	}
	if len(tools) > 0 {
		b.WriteString("Tools used:")
		for name, c := range tools {
			fmt.Fprintf(&b, " %s×%d", name, c)
		}
		b.WriteString("\n")
	}
	if len(errSnippets) > 0 {
		b.WriteString("Errors / failures:\n")
		for _, e := range errSnippets {
			b.WriteString("  - " + e + "\n")
		}
	}
	b.WriteString("Pinned: system prompt + recent tail preserved.\n")
	return b.String()
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return truncateRunes(strings.TrimSpace(s), 120)
}

func extractPaths(s string) []string {
	var out []string
	for _, tok := range strings.FieldsFunc(s, func(r rune) bool {
		return r == ' ' || r == '\n' || r == '\t' || r == '"' || r == '\'' || r == '`'
	}) {
		if looksLikePath(tok) {
			out = appendUniqueLimited(out, tok, 20)
		}
	}
	return out
}

func looksLikePath(tok string) bool {
	if len(tok) < 3 || len(tok) > 120 {
		return false
	}
	if strings.ContainsAny(tok, ":/\\") &&
		(strings.Contains(tok, ".go") || strings.Contains(tok, ".md") ||
			strings.Contains(tok, ".json") || strings.Contains(tok, ".yaml") ||
			strings.Contains(tok, ".yml") || strings.Contains(tok, ".txt") ||
			strings.Contains(tok, "/") || strings.Contains(tok, "\\")) {
		// skip URLs-ish and lone slashes
		if strings.HasPrefix(tok, "http") {
			return false
		}
		return true
	}
	return false
}

func appendUniqueLimited(list []string, s string, max int) []string {
	if s == "" {
		return list
	}
	for _, x := range list {
		if x == s {
			return list
		}
	}
	if len(list) >= max {
		return list
	}
	return append(list, s)
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "..."
}
