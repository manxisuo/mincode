// Package ctxmgr builds the prompt context for each LLM call and records snapshots.
package ctxmgr

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// Source identifies where a context piece came from.
type Source string

const (
	SourceSystem       Source = "system"
	SourceInstructions Source = "instructions"
	SourceSkills       Source = "skills"
	SourceMemory       Source = "memory"
	SourceHistory      Source = "history"
	SourceToolResult   Source = "tool_result"
	SourceUserInput    Source = "user_input"
	SourcePinned       Source = "pinned"
	SourceSummary      Source = "summary"
)

// Provenance is T-obs-4 data lineage for one context item (L4).
type Provenance struct {
	// Source system | instructions | skills | memory | history | tool_result | user_input | pinned | summary
	Source Source `json:"source"`
	// Tool is the tool name that produced this content (tool_result).
	Tool string `json:"tool,omitempty"`
	// CallID is the LLM tool call id (tool_result).
	CallID string `json:"call_id,omitempty"`
	// Path is the workspace-relative file path when known (tool results).
	Path string `json:"path,omitempty"`
	// Lines is an optional "start-end" range when known.
	Lines string `json:"lines,omitempty"`
	// ProducedAtStep is when the tool/file event was produced (when known).
	ProducedAtStep int `json:"produced_at_step,omitempty"`
	// EnteredAtStep is when the item first entered a context snapshot (when known).
	EnteredAtStep int `json:"entered_at_step,omitempty"`
	// Transformed lists transforms applied (e.g. "truncated 8421→3200").
	Transformed string `json:"transformed,omitempty"`
}

// Item is one context piece with provenance and budget outcome.
type Item struct {
	Source     Source `json:"source"`
	Role       string `json:"role,omitempty"`
	Preview    string `json:"preview"`
	Tokens     int    `json:"token_count"`
	Included   bool   `json:"included"`
	Excluded   bool   `json:"excluded"`
	Truncated  bool   `json:"truncated"`
	Pinned     bool   `json:"pinned"`
	Reason     string `json:"reason,omitempty"`
	ToolCallID string `json:"tool_call_id,omitempty"`
	// Policy names the Runtime rule that excluded/truncated this item (T-obs-2).
	Policy string `json:"policy,omitempty"`
	// OrigTokens is estimated size before exclusion/truncation.
	OrigTokens int `json:"orig_tokens,omitempty"`
	// SavedTokens is how much the budget action saved (exclude/truncate).
	SavedTokens int `json:"saved_tokens,omitempty"`
	// Prov is optional data lineage (T-obs-3).
	Prov *Provenance `json:"prov,omitempty"`
}

// Snapshot is a record of what the model will see on one LLM call.
type Snapshot struct {
	Step        int       `json:"step"`
	Time        time.Time `json:"time"`
	Items       []Item    `json:"items"`
	TotalTokens int       `json:"total_tokens"`
	// ToolTokens is the estimated size of tool JSON schemas sent alongside messages.
	ToolTokens int `json:"tool_tokens"`
	Budget     int `json:"budget"`
	Included   int `json:"included_count"`
	Excluded   int `json:"excluded_count"`
	Truncated  int `json:"truncated_count"`
	// ActualPromptTokens is filled after the provider returns usage.prompt_tokens.
	ActualPromptTokens int `json:"actual_prompt_tokens,omitempty"`
	// EstimateRatio is actual/estimate, learned from provider usage (1.0 until first sample).
	EstimateRatio float64 `json:"estimate_ratio,omitempty"`
	// RequestFailed marks that the LLM call for this snapshot failed (no usage).
	RequestFailed bool `json:"request_failed,omitempty"`
	// Notes are Runtime budget/compaction explanations for this build (T-obs-2).
	Notes []string `json:"notes,omitempty"`
	// Diff vs previous snapshot in the same Manager (nil on first build).
	Diff *SnapshotDiff `json:"diff,omitempty"`
}

// Summary returns a multi-line human-readable breakdown.
func (s Snapshot) Summary() string {
	var b strings.Builder
	b.WriteString("Context Snapshot #" + itoa(s.Step) + "\n\n")
	b.WriteString(pad("Source", 14) + pad("Role", 12) + pad("Tokens", 8) + "Status\n")
	b.WriteString(strings.Repeat("-", 64) + "\n")
	for _, it := range s.Items {
		status := "included"
		switch {
		case it.Excluded:
			status = "excluded"
			if it.Reason != "" {
				status += " (" + it.Reason + ")"
			}
		case it.Truncated:
			status = "truncated"
			if it.Reason != "" {
				status += " (" + it.Reason + ")"
			}
		}
		if it.Pinned && !it.Excluded {
			status += " [pinned]"
		}
		preview := it.Preview
		if preview == "" {
			preview = "-"
		}
		b.WriteString(pad(string(it.Source), 14) + pad(it.Role, 12) + pad(itoa(it.Tokens), 8) + status + "\n")
		b.WriteString("             " + truncate(preview, 56) + "\n")
	}
	b.WriteString(strings.Repeat("-", 64) + "\n")
	b.WriteString(pad("Included", 14) + pad("", 12) + pad(itoa(s.Included), 8) + "items\n")
	b.WriteString(pad("Excluded", 14) + pad("", 12) + pad(itoa(s.Excluded), 8) + "items\n")
	b.WriteString(pad("Truncated", 14) + pad("", 12) + pad(itoa(s.Truncated), 8) + "items\n")
	if s.ToolTokens > 0 {
		b.WriteString(pad("Tool schemas", 14) + pad("", 12) + pad(itoa(s.ToolTokens), 8) + "tokens (not in messages)\n")
	}
	b.WriteString(pad("Total", 14) + pad("", 12) + pad(itoa(s.TotalTokens+s.ToolTokens), 8) + "tokens / budget " + itoa(s.Budget) + "\n")
	if s.ActualPromptTokens > 0 {
		ratio := s.EstimateRatio
		if ratio <= 0 {
			ratio = float64(s.ActualPromptTokens) / float64(s.TotalTokens+s.ToolTokens)
		}
		b.WriteString(pad("API actual", 14) + pad("", 12) + pad(itoa(s.ActualPromptTokens), 8) +
			"prompt_tokens, ratio " + fmt.Sprintf("%.2f", ratio) + "\n")
	}
	return b.String()
}

// EstimateTokens is a conservative token heuristic for budgeting.
// It intentionally over-counts CJK (often ~1 token/char) so budget cuts
// fire before the provider reports over-budget usage.
func EstimateTokens(s string) int {
	if s == "" {
		return 0
	}
	n := utf8.RuneCountInString(s)
	if n <= 0 {
		return 0
	}
	ascii := 0
	for _, r := range s {
		if r < 128 {
			ascii++
		}
	}
	nonASCII := n - ascii
	// ASCII ~4 chars/token; CJK ~1 token/char (DeepSeek/GPT-style BPE).
	// +1 for message framing slack.
	return ascii/4 + nonASCII + 1
}

func pad(s string, n int) string {
	if len(s) >= n {
		return s + " "
	}
	return s + strings.Repeat(" ", n-len(s))
}

func truncate(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", "↵")
	if len(s) <= n {
		return s
	}
	end := n
	for end > 0 && !utf8.RuneStart(s[end]) {
		end--
	}
	return s[:end] + "..."
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
