package ctxmgr

import (
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/manxisuo/mincode/internal/llm"
)

// DefaultBudgetTokens is used when config does not set a budget.
const DefaultBudgetTokens = 32000

// maxToolResultTokens caps a single tool result stored in conversation history.
// Prevents one huge read_file from dominating every subsequent LLM call.
const maxToolResultTokens = 2500

type entry struct {
	msg       llm.Message
	source    Source
	tokens    int
	callID    string
	tool      string
	path      string
	lines     string
	prodStep  int
	enterStep int
}

// Manager owns conversation state and builds budgeted LLM requests.
type Manager struct {
	system       string
	instructions string
	skills       string
	memory       string
	budget       int
	compressAt   int
	entries      []entry
	step         int
	lastSnapshot *Snapshot
	prevSnapshot *Snapshot
	// snapHist keeps recent snapshots by step for historical replay (T-obs-5).
	snapHist []*Snapshot
	cal      *Calibrator
}

// maxSnapHist bounds historical snapshots kept for replay.
const maxSnapHist = 40

// SnapshotByStep returns a stored snapshot for historical replay, or nil.
func (m *Manager) SnapshotByStep(step int) *Snapshot {
	if m == nil {
		return nil
	}
	for _, s := range m.snapHist {
		if s != nil && s.Step == step {
			return s
		}
	}
	return nil
}

// SnapHistory lists stored snapshot steps (ascending).
func (m *Manager) SnapHistory() []int {
	if m == nil {
		return nil
	}
	out := make([]int, 0, len(m.snapHist))
	for _, s := range m.snapHist {
		if s != nil {
			out = append(out, s.Step)
		}
	}
	return out
}

func (m *Manager) pushSnapHist(s *Snapshot) {
	if s == nil {
		return
	}
	m.snapHist = append(m.snapHist, s)
	if len(m.snapHist) > maxSnapHist {
		m.snapHist = m.snapHist[len(m.snapHist)-maxSnapHist:]
	}
}

// New creates a context manager.
func New(systemPrompt, instructions string, budget int) *Manager {
	if budget <= 0 {
		budget = DefaultBudgetTokens
	}
	return &Manager{
		system:       systemPrompt,
		instructions: instructions,
		budget:       budget,
		compressAt:   DefaultCompressAtTokens,
		cal:          NewCalibrator(),
	}
}

func (m *Manager) Budget() int { return m.budget }

func (m *Manager) SetBudget(n int) {
	if n > 0 {
		m.budget = n
	}
}

func (m *Manager) SetInstructions(s string) { m.instructions = s }
func (m *Manager) Instructions() string     { return m.instructions }
func (m *Manager) SetSkills(s string)       { m.skills = s }
func (m *Manager) Skills() string           { return m.skills }
func (m *Manager) SetMemory(s string)       { m.memory = s }
func (m *Manager) Memory() string           { return m.memory }
func (m *Manager) System() string           { return m.system }
func (m *Manager) SetSystem(s string)       { m.system = s }
func (m *Manager) Len() int                 { return len(m.entries) }
func (m *Manager) LastSnapshot() *Snapshot  { return m.lastSnapshot }

// Calibrator exposes the live token-ratio calibrator.
func (m *Manager) Calibrator() *Calibrator { return m.cal }

// est scales the base heuristic by the learned provider ratio.
func (m *Manager) est(s string) int {
	return m.cal.Scale(EstimateTokens(s))
}

// ObserveUsage folds provider-reported prompt_tokens into the calibrator.
// estimated should be snap.TotalTokens + snap.ToolTokens from the same call.
func (m *Manager) ObserveUsage(estimated, actualPromptTokens int) {
	m.cal.Observe(estimated, actualPromptTokens)
	if m.lastSnapshot != nil && actualPromptTokens > 0 {
		m.lastSnapshot.ActualPromptTokens = actualPromptTokens
		m.lastSnapshot.EstimateRatio = m.cal.Ratio()
		m.lastSnapshot.RequestFailed = false
	}
}

// LastStep returns the last BuildRequest step counter.
func (m *Manager) LastStep() int {
	if m == nil {
		return 0
	}
	return m.step
}

// SetMeta attaches provenance metadata to the most recent matching entry
// (typically the last tool_result). Used by the agent after tool execution.
func (m *Manager) SetMeta(callID, tool, path, lines string, producedAtStep int) {
	if m == nil || callID == "" {
		return
	}
	for i := len(m.entries) - 1; i >= 0; i-- {
		e := &m.entries[i]
		if e.msg.ToolCallID == callID || (callID != "" && e.callID == callID) {
			if tool != "" {
				e.tool = tool
			}
			if path != "" {
				e.path = path
			}
			if lines != "" {
				e.lines = lines
			}
			e.callID = callID
			e.prodStep = producedAtStep
			return
		}
	}
}

// MarkRequestFailed flags the last snapshot as having no API usage (call failed).
func (m *Manager) MarkRequestFailed() {
	if m.lastSnapshot != nil {
		m.lastSnapshot.RequestFailed = true
	}
}

// AppendUser records a user message.
func (m *Manager) AppendUser(text string) {
	m.entries = append(m.entries, entry{
		msg:    llm.Message{Role: llm.RoleUser, Content: text},
		source: SourceUserInput,
		tokens: m.est(text),
	})
}

// AppendAssistant records an assistant message (may include tool calls).
func (m *Manager) AppendAssistant(msg llm.Message) {
	content := msg.Content
	for _, tc := range msg.ToolCalls {
		content += "\n" + tc.Name + tc.Arguments
	}
	m.entries = append(m.entries, entry{
		msg:    msg,
		source: SourceHistory,
		tokens: m.est(content),
	})
}

// AppendReflection records a self-critique message (user role, own source).
func (m *Manager) AppendReflection(text string) {
	m.entries = append(m.entries, entry{
		msg:    llm.Message{Role: llm.RoleUser, Content: text},
		source: SourceReflection,
		tokens: m.est(text),
	})
}

// AppendToolResult records a tool observation tied to a tool_call_id.
// Oversized results are truncated up-front so they cannot flood later turns.
func (m *Manager) AppendToolResult(toolCallID, content string) {
	if m.est(content) > maxToolResultTokens {
		content = m.truncateToTokens(content, maxToolResultTokens)
	}
	m.entries = append(m.entries, entry{
		msg: llm.Message{
			Role:       llm.RoleTool,
			Content:    content,
			ToolCallID: toolCallID,
		},
		source: SourceToolResult,
		tokens: m.est(content),
	})
}

// ToolResultByCallID returns the full tool result content for a call_id (T-obs-6).
func (m *Manager) ToolResultByCallID(callID string) (string, bool) {
	if m == nil || callID == "" {
		return "", false
	}
	for i := len(m.entries) - 1; i >= 0; i-- {
		e := &m.entries[i]
		if e.msg.ToolCallID == callID || e.callID == callID {
			return e.msg.Content, true
		}
	}
	return "", false
}

// ExportedEntry is a serializable conversation entry.
type ExportedEntry struct {
	Msg    llm.Message `json:"msg"`
	Source string      `json:"source"`
	Tokens int         `json:"tokens"`
}

// ExportEntries returns a copy of conversation state for persistence.
func (m *Manager) ExportEntries() []ExportedEntry {
	out := make([]ExportedEntry, 0, len(m.entries))
	for _, e := range m.entries {
		out = append(out, ExportedEntry{
			Msg:    e.msg,
			Source: string(e.source),
			Tokens: e.tokens,
		})
	}
	return out
}

// RestoreEntries replaces conversation entries (used by --continue).
func (m *Manager) RestoreEntries(in []ExportedEntry) {
	m.entries = nil
	for _, e := range in {
		src := Source(e.Source)
		if src == "" {
			src = SourceHistory
		}
		m.entries = append(m.entries, entry{
			msg:    e.Msg,
			source: src,
			tokens: e.Tokens,
		})
	}
}

// Clear drops conversation entries (keeps system/instructions).
func (m *Manager) Clear() { m.entries = nil }

// DropLastUser removes a trailing user message (used when a turn fails).
func (m *Manager) DropLastUser() {
	if n := len(m.entries); n > 0 && m.entries[n-1].msg.Role == llm.RoleUser {
		m.entries = m.entries[:n-1]
	}
}

type part struct {
	msg       llm.Message
	src       Source
	tok       int
	pin       bool
	order     int // higher = more recent / more important
	excluded  bool
	truncated bool
	origTok   int
	tool      string
	callID    string
	path      string
	lines     string
	prodStep  int
	enterStep int
}

// BuildRequest assembles a budgeted ChatRequest and records a Snapshot.
// Call AppendUser first for the current turn.
func (m *Manager) BuildRequest(tools []llm.ToolDefinition) (llm.ChatRequest, Snapshot) {
	m.step++
	now := time.Now().UTC()

	toolTok := m.estimateToolDefTokens(tools)
	// Message budget must leave room for tool schemas that ride along every call.
	msgBudget := m.budget - toolTok
	if msgBudget < m.budget/5 {
		msgBudget = m.budget / 5
		if msgBudget < 256 {
			msgBudget = 256
		}
	}

	var parts []part

	if m.system != "" {
		parts = append(parts, part{
			msg:   llm.Message{Role: llm.RoleSystem, Content: m.system},
			src:   SourceSystem,
			tok:   m.est(m.system),
			pin:   true,
			order: 10000,
		})
	}
	if trimSpace(m.instructions) != "" {
		parts = append(parts, part{
			msg:   llm.Message{Role: llm.RoleSystem, Content: m.instructions},
			src:   SourceInstructions,
			tok:   m.est(m.instructions),
			pin:   true,
			order: 9000,
		})
	}
	if trimSpace(m.skills) != "" {
		parts = append(parts, part{
			msg:   llm.Message{Role: llm.RoleSystem, Content: m.skills},
			src:   SourceSkills,
			tok:   m.est(m.skills),
			pin:   true,
			order: 8500,
		})
	}
	if trimSpace(m.memory) != "" {
		parts = append(parts, part{
			msg:   llm.Message{Role: llm.RoleSystem, Content: m.memory},
			src:   SourceMemory,
			tok:   m.est(m.memory),
			pin:   true,
			order: 8200,
		})
	}
	for i, e := range m.entries {
		src := e.source
		switch {
		case e.msg.Role == llm.RoleTool:
			src = SourceToolResult
		case e.msg.Role == llm.RoleUser && e.source == SourceReflection:
			src = SourceReflection
		case e.msg.Role == llm.RoleUser:
			if i == len(m.entries)-1 {
				src = SourceUserInput
			} else {
				src = SourceHistory
			}
		default:
			src = SourceHistory
		}
		parts = append(parts, part{
			msg:       e.msg,
			src:       src,
			tok:       e.tokens,
			order:     i + 1,
			tool:      e.tool,
			callID:    e.msg.ToolCallID,
			path:      e.path,
			lines:     e.lines,
			prodStep:  e.prodStep,
			enterStep: e.enterStep,
		})
	}

	total := 0
	for _, p := range parts {
		total += p.tok
	}

	if total > msgBudget {
		idxs := make([]int, 0, len(parts))
		for i, p := range parts {
			if p.pin {
				continue
			}
			idxs = append(idxs, i)
		}
		for a := 0; a < len(idxs); a++ {
			for b := a + 1; b < len(idxs); b++ {
				if parts[idxs[b]].order < parts[idxs[a]].order {
					idxs[a], idxs[b] = idxs[b], idxs[a]
				}
			}
		}
		newest := -1
		for _, p := range parts {
			if p.order > newest {
				newest = p.order
			}
		}
		for _, idx := range idxs {
			// Drop assistant-with-tools together with its following tool results
			// so we never leave orphan tool messages behind.
			if total <= msgBudget {
				break
			}
			if parts[idx].src == SourceUserInput && parts[idx].order == newest {
				continue
			}
			if parts[idx].excluded {
				continue
			}
			parts[idx].excluded = true
			total -= parts[idx].tok
			// Cascade: if this is an assistant that requested tools, also exclude
			// the tool results that belong to it (contiguous following tool_result parts).
			if parts[idx].msg.Role == llm.RoleAssistant && len(parts[idx].msg.ToolCalls) > 0 {
				ids := map[string]bool{}
				for _, tc := range parts[idx].msg.ToolCalls {
					ids[tc.ID] = true
				}
				for j := idx + 1; j < len(parts); j++ {
					if parts[j].excluded {
						continue
					}
					if parts[j].msg.Role != llm.RoleTool {
						break
					}
					if ids[parts[j].msg.ToolCallID] || parts[j].src == SourceToolResult {
						parts[j].excluded = true
						total -= parts[j].tok
					}
				}
			}
		}
	}

	if total > msgBudget {
		for i := range parts {
			if total <= msgBudget {
				break
			}
			if parts[i].excluded || parts[i].src != SourceToolResult || parts[i].tok < 200 {
				continue
			}
			need := total - msgBudget
			maxTok := parts[i].tok - need
			if maxTok < 100 {
				maxTok = 100
			}
			newContent := m.truncateToTokens(parts[i].msg.Content, maxTok)
			newTok := m.est(newContent)
			saved := parts[i].tok - newTok
			if saved <= 0 {
				continue
			}
			parts[i].msg.Content = newContent
			parts[i].origTok = parts[i].tok
			parts[i].tok = newTok
			parts[i].truncated = true
			total -= saved
		}
	}

	var (
		messages   []llm.Message
		items      []Item
		systemText string
	)
	finalTotal := 0
	included, excludedN, truncatedN := 0, 0, 0

	for _, p := range parts {
		it := Item{
			Source:     p.src,
			Role:       string(p.msg.Role),
			Preview:    previewOf(p.msg.Content),
			Tokens:     p.tok,
			ToolCallID: p.msg.ToolCallID,
			Pinned:     p.pin,
			OrigTokens: p.tok,
		}
		// T-obs-3: lineage for tool results / history when known.
		if p.src == SourceToolResult || p.tool != "" || p.callID != "" {
			prov := &Provenance{
				Source:         p.src,
				Tool:           p.tool,
				CallID:         p.callID,
				Path:           p.path,
				Lines:          p.lines,
				ProducedAtStep: p.prodStep,
				EnteredAtStep:  p.enterStep,
			}
			if p.enterStep == 0 {
				prov.EnteredAtStep = m.step
			}
			if p.truncated {
				prov.Transformed = fmt.Sprintf("truncated %d→%d tokens (policy=tool_result_max_tokens)", p.origTok, p.tok)
			}
			it.Prov = prov
		}
		if p.excluded {
			it.Included = false
			it.Excluded = true
			it.Reason = "token budget"
			it.Policy = "drop_oldest_non_pinned"
			it.SavedTokens = p.tok
			it.Tokens = 0
			items = append(items, it)
			excludedN++
			continue
		}
		if p.truncated {
			it.Truncated = true
			it.Reason = "tool result budget limit"
			it.Policy = "tool_result_max_tokens"
			// p.tok is after truncate; saved is orig − new when known.
			if p.origTok > 0 && p.origTok > p.tok {
				it.SavedTokens = p.origTok - p.tok
			}
			truncatedN++
		}
		it.Included = true
		items = append(items, it)
		finalTotal += p.tok
		included++

		if p.msg.Role == llm.RoleSystem {
			if systemText != "" {
				systemText += "\n\n"
			}
			systemText += p.msg.Content
			continue
		}
		messages = append(messages, p.msg)
	}
	if systemText != "" {
		messages = append([]llm.Message{{Role: llm.RoleSystem, Content: systemText}}, messages...)
	}

	// Providers reject tool messages that are not paired with a preceding
	// assistant tool_calls message. Repair any budget/history damage.
	messages = sanitizeToolPairs(messages)

	snap := Snapshot{
		Step:        m.step,
		Time:        now,
		Items:       items,
		TotalTokens: finalTotal,
		ToolTokens:  toolTok,
		Budget:      m.budget,
		Included:    included,
		Excluded:    excludedN,
		Truncated:   truncatedN,
	}
	// T-obs-2: explain budget actions and diff vs previous build.
	notes := []string{}
	if excludedN > 0 {
		saved := 0
		for _, it := range items {
			if it.Excluded {
				saved += it.SavedTokens
			}
		}
		notes = append(notes, fmt.Sprintf(
			"excluded %d item(s) policy=drop_oldest_non_pinned, saved≈%d tokens (msg budget=%d, projected was over)",
			excludedN, saved, msgBudget))
	}
	if truncatedN > 0 {
		notes = append(notes, fmt.Sprintf(
			"truncated %d tool_result item(s) policy=tool_result_max_tokens",
			truncatedN))
	}
	if len(notes) == 0 {
		notes = append(notes, fmt.Sprintf("fit in budget (total=%d, msg_budget=%d)", finalTotal, msgBudget))
	}
	snap.Notes = notes
	snap.Diff = DiffSnapshots(m.lastSnapshot, &snap)
	m.lastSnapshot = &snap
	m.pushSnapHist(&snap)

	return llm.ChatRequest{Messages: messages, Tools: tools}, snap
}

// estimateToolDefTokens estimates the token cost of tool JSON schemas.
// These are sent on every LLM call but are not chat messages.
func (m *Manager) estimateToolDefTokens(defs []llm.ToolDefinition) int {
	if len(defs) == 0 {
		return 0
	}
	total := 0
	for _, d := range defs {
		total += m.est(d.Name)
		total += m.est(d.Description)
		if len(d.Parameters) > 0 {
			total += m.est(string(d.Parameters))
		}
		total += 12 // wire envelope / type markers
	}
	return total
}

func (m *Manager) truncateToTokens(s string, maxTok int) string {
	return truncateToTokensScaled(s, maxTok, m.est)
}

// previewMaxRunes caps stored context-item previews so snapshots stay bounded
// while still allowing the inspector to expand a useful amount of content.
const previewMaxRunes = 2000

// previewOf returns a bounded multi-line preview of message content.
// Newlines are preserved so the web inspector can expand the full preview.
func previewOf(s string) string {
	s = trimSpace(s)
	if s == "" {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= previewMaxRunes {
		return s
	}
	return string(runes[:previewMaxRunes]) + "…"
}

func truncateToTokensScaled(s string, maxTok int, est func(string) int) string {
	if est(s) <= maxTok {
		return s
	}
	n := utf8.RuneCountInString(s)
	if n == 0 {
		return s
	}
	e := est(s)
	keep := n * maxTok / e
	if keep < 20 {
		keep = 20
	}
	if keep > n {
		keep = n
	}
	count := 0
	for i := range s {
		if count == keep {
			return s[:i] + "\n... (truncated, token budget)\n"
		}
		count++
	}
	return s
}

func truncateToTokens(s string, maxTok int) string {
	return truncateToTokensScaled(s, maxTok, EstimateTokens)
}

func trimSpace(s string) string {
	start, end := 0, len(s)
	for start < end && (s[start] == ' ' || s[start] == '\n' || s[start] == '\t' || s[start] == '\r') {
		start++
	}
	for end > start && (s[end-1] == ' ' || s[end-1] == '\n' || s[end-1] == '\t' || s[end-1] == '\r') {
		end--
	}
	return s[start:end]
}

// sanitizeToolPairs ensures every tool message follows an assistant message
// that declared a matching tool_call id. Orphan tool messages are dropped;
// unfulfilled tool_calls get a placeholder tool result so the provider
// accepts the transcript (OpenAI-compatible rule).
func sanitizeToolPairs(msgs []llm.Message) []llm.Message {
	out := make([]llm.Message, 0, len(msgs))
	pending := map[string]bool{} // tool_call ids awaiting results

	flushPending := func() {
		for id := range pending {
			out = append(out, llm.Message{
				Role:       llm.RoleTool,
				Content:    "(tool result missing after context rebuild)",
				ToolCallID: id,
			})
		}
		pending = map[string]bool{}
	}

	for _, m := range msgs {
		switch m.Role {
		case llm.RoleTool:
			if pending[m.ToolCallID] {
				out = append(out, m)
				delete(pending, m.ToolCallID)
			}
			// else: orphan tool message — drop
		case llm.RoleAssistant:
			// Previous assistant still waiting for tool results.
			if len(pending) > 0 {
				flushPending()
			}
			out = append(out, m)
			if len(m.ToolCalls) > 0 {
				pending = map[string]bool{}
				for _, tc := range m.ToolCalls {
					if tc.ID != "" {
						pending[tc.ID] = true
					}
				}
			}
		default: // user / system
			if len(pending) > 0 {
				flushPending()
			}
			out = append(out, m)
		}
	}
	if len(pending) > 0 {
		flushPending()
	}
	return out
}

// FormatSnapshot renders a snapshot for the CLI.
func FormatSnapshot(s Snapshot) string { return s.Summary() }

func (s Snapshot) String() string {
	return fmt.Sprintf("Snapshot#%d tokens=%d budget=%d items=%d", s.Step, s.TotalTokens, s.Budget, len(s.Items))
}
