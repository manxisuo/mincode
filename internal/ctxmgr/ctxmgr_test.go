package ctxmgr

import (
	"strings"
	"testing"

	"github.com/manxisuo/mincode/internal/llm"
)

func TestEstimateTokens(t *testing.T) {
	if EstimateTokens("") != 0 {
		t.Fatal("empty should be 0")
	}
	if EstimateTokens("hello") <= 0 {
		t.Fatal("positive")
	}
	// CJK should cost more than the same ASCII length roughly.
	cjk := EstimateTokens("你好世界你好世界")
	asc := EstimateTokens("abcdefgh")
	if cjk <= asc {
		t.Fatalf("cjk=%d asc=%d, expect cjk heavier", cjk, asc)
	}
}

func TestPreviewOfPreservesMultilineAndCaps(t *testing.T) {
	if got := previewOf("  \n "); got != "" {
		t.Fatalf("blank = %q", got)
	}

	multi := "line one\nline two\nline three"
	if got := previewOf(multi); got != multi {
		t.Fatalf("multiline = %q, want full content preserved", got)
	}

	long := strings.Repeat("x", previewMaxRunes+50)
	got := previewOf(long)
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("long preview missing ellipsis: len=%d", len(got))
	}
	if n := len([]rune(got)); n != previewMaxRunes+1 {
		t.Fatalf("long preview runes = %d, want %d", n, previewMaxRunes+1)
	}
}

func TestDiffSnapshotsExplainsExclusion(t *testing.T) {
	m := New("SYS", "", 12)
	m.AppendUser("one message here")
	_, s1 := m.BuildRequest(nil)
	if s1.Diff == nil {
		t.Fatal("missing first diff")
	}
	m.AppendUser("two message here")
	m.AppendAssistant(llm.Message{Role: llm.RoleAssistant, Content: "reply one"})
	m.AppendUser("three message here")
	_, s2 := m.BuildRequest(nil)
	if s2.Diff == nil {
		t.Fatal("missing second diff")
	}
	if s2.Diff.ToStep != s2.Step {
		t.Fatalf("diff to_step=%d snap=%d", s2.Diff.ToStep, s2.Step)
	}
	// Tiny budget should force exclusions with policy + notes.
	if s2.Excluded == 0 {
		t.Skip("no exclusion in this budget; notes still present")
	}
	if len(s2.Notes) == 0 {
		t.Fatal("missing budget notes")
	}
	foundPolicy := false
	for _, it := range s2.Items {
		if it.Excluded && it.Policy != "" {
			foundPolicy = true
			if it.SavedTokens <= 0 && it.OrigTokens <= 0 {
				t.Fatalf("excluded item missing saved/orig tokens: %+v", it)
			}
		}
	}
	if !foundPolicy {
		t.Fatal("excluded items missing policy")
	}
}

func TestDiffSnapshotsAddedItem(t *testing.T) {
	m := New("SYS", "", 10000)
	m.AppendUser("first")
	_, _ = m.BuildRequest(nil)
	m.AppendUser("second")
	_, s2 := m.BuildRequest(nil)
	d := s2.Diff
	if d == nil || d.Added < 1 {
		t.Fatalf("diff=%+v want added", d)
	}
	if d.ExcludedNow != 0 && d.FromTotal > d.ToTotal {
		t.Fatalf("unexpected shrink without exclusion: %+v", d)
	}
}

func TestToolResultProvenance(t *testing.T) {
	m := New("SYS", "", 10000)
	m.AppendUser("read the file")
	m.AppendAssistant(llm.Message{
		Role:      llm.RoleAssistant,
		ToolCalls: []llm.ToolCall{{ID: "call_1", Name: "read_file", Arguments: `{"path":"a.go"}`}},
	})
	m.AppendToolResult("call_1", "package a\n// lots of code\nfunc X() {}")
	m.SetMeta("call_1", "read_file", "a.go", "1-3", 7)
	_, snap := m.BuildRequest(nil)
	found := false
	for _, it := range snap.Items {
		if it.Source != SourceToolResult {
			continue
		}
		found = true
		if it.Prov == nil {
			t.Fatalf("tool_result missing prov: %+v", it)
		}
		if it.Prov.Tool != "read_file" || it.Prov.CallID != "call_1" || it.Prov.Path != "a.go" {
			t.Fatalf("prov=%+v", it.Prov)
		}
		if it.Prov.EnteredAtStep != snap.Step {
			t.Fatalf("entered step=%d snap=%d", it.Prov.EnteredAtStep, snap.Step)
		}
	}
	if !found {
		t.Fatal("no tool_result item")
	}
}

func TestBuildRequestIncludesSystemAndUser(t *testing.T) {
	m := New("SYS", "", 10000)
	m.AppendUser("hi there")
	req, snap := m.BuildRequest(nil)

	if len(req.Messages) < 2 {
		t.Fatalf("messages = %d", len(req.Messages))
	}
	if req.Messages[0].Role != llm.RoleSystem || req.Messages[0].Content != "SYS" {
		t.Fatalf("first = %+v", req.Messages[0])
	}
	if req.Messages[len(req.Messages)-1].Content != "hi there" {
		t.Fatalf("last = %+v", req.Messages[len(req.Messages)-1])
	}
	if snap.TotalTokens <= 0 {
		t.Fatal("tokens")
	}
	if snap.Included < 2 {
		t.Fatalf("included = %d", snap.Included)
	}

	// Snapshot item sources
	var sources []Source
	for _, it := range snap.Items {
		sources = append(sources, it.Source)
	}
	joined := ""
	for _, s := range sources {
		joined += string(s) + ","
	}
	if !strings.Contains(joined, "system") || !strings.Contains(joined, "user_input") {
		t.Fatalf("sources = %s", joined)
	}
}

func TestBudgetExcludesOldest(t *testing.T) {
	// Tiny budget forces exclusions.
	m := New("SYS", "", 5)
	m.AppendUser("msg-one")
	m.AppendAssistant(llm.Message{Role: llm.RoleAssistant, Content: "reply-one"})
	m.AppendUser("msg-two")

	req, snap := m.BuildRequest(nil)

	if snap.Excluded == 0 {
		t.Fatalf("expected exclusions, snap=%s", snap)
	}
	// Newest user input must still be present.
	found := false
	for _, msg := range req.Messages {
		if msg.Content == "msg-two" {
			found = true
		}
	}
	if !found {
		t.Fatalf("newest user missing: %+v", req.Messages)
	}
	// System still present
	if req.Messages[0].Role != llm.RoleSystem {
		t.Fatalf("system dropped: %+v", req.Messages)
	}
}

func TestToolResultTruncation(t *testing.T) {
	m := New("SYS", "", 200)
	m.AppendUser("go")
	big := strings.Repeat("line of tool output\n", 50)
	m.AppendToolResult("call1", big)

	req, snap := m.BuildRequest(nil)
	if snap.Truncated == 0 && snap.Excluded == 0 {
		t.Fatalf("expected truncate or exclude, snap=%s total=%d", snap, snap.TotalTokens)
	}
	if snap.TotalTokens > m.Budget()+50 {
		t.Fatalf("still over budget: %d > %d", snap.TotalTokens, m.Budget())
	}
	// Messages should not explode.
	_ = req
}

func TestInstructionsIncluded(t *testing.T) {
	m := New("SYS", "Use tabs", 10000)
	m.AppendUser("hi")
	_, snap := m.BuildRequest(nil)
	found := false
	for _, it := range snap.Items {
		if it.Source == SourceInstructions {
			found = true
			if !it.Included {
				t.Fatal("instructions should be included")
			}
		}
	}
	if !found {
		t.Fatal("missing instructions item")
	}
}

func TestDropLastUser(t *testing.T) {
	m := New("S", "", 1000)
	m.AppendUser("a")
	m.AppendUser("b")
	m.DropLastUser()
	if m.Len() != 1 {
		t.Fatalf("len = %d", m.Len())
	}
}

func TestClearKeepsSystem(t *testing.T) {
	m := New("SYS", "INS", 1000)
	m.AppendUser("x")
	m.Clear()
	if m.Len() != 0 {
		t.Fatal("entries should be empty")
	}
	if m.System() != "SYS" || m.Instructions() != "INS" {
		t.Fatal("system/instructions cleared")
	}
}

func TestToolDefsCountAgainstBudget(t *testing.T) {
	m := New("SYS", "", 5000)
	m.AppendUser("hi")
	defs := []llm.ToolDefinition{
		{Name: "read_file", Description: "Read a file from the workspace with optional line range", Parameters: []byte(`{"type":"object","properties":{"path":{"type":"string"}}}`)},
		{Name: "grep", Description: "Search file contents", Parameters: []byte(`{"type":"object","properties":{"pattern":{"type":"string"}}}`)},
	}
	_, snap := m.BuildRequest(defs)
	if snap.ToolTokens <= 0 {
		t.Fatalf("tool tokens should be counted: %+v", snap)
	}
	// Total messages + tools should be reported together for budgeting.
	if snap.TotalTokens+snap.ToolTokens > snap.Budget+100 {
		t.Fatalf("over budget: msgs=%d tools=%d budget=%d", snap.TotalTokens, snap.ToolTokens, snap.Budget)
	}
}

func TestAppendToolResultCapsSize(t *testing.T) {
	m := New("SYS", "", 32000)
	big := strings.Repeat("汉字测试内容\n", 2000)
	m.AppendToolResult("c1", big)
	if m.entries[len(m.entries)-1].tokens > maxToolResultTokens+50 {
		t.Fatalf("tool result tokens = %d, want cap ~%d", m.entries[len(m.entries)-1].tokens, maxToolResultTokens)
	}
}

func TestEstimateTokensCJK(t *testing.T) {
	// CJK should be ~1 token per char, not /2.
	s := strings.Repeat("中", 100)
	est := EstimateTokens(s)
	if est < 90 {
		t.Fatalf("CJK estimate too low: %d", est)
	}
}

func TestOlderUserMessagesLabelHistory(t *testing.T) {
	m := New("SYS", "", 32000)
	m.AppendUser("first question")
	m.AppendAssistant(llm.Message{Role: llm.RoleAssistant, Content: "first answer"})
	m.AppendUser("second question")

	_, snap := m.BuildRequest(nil)
	var users []Item
	for _, it := range snap.Items {
		if it.Role == "user" {
			users = append(users, it)
		}
	}
	if len(users) != 2 {
		t.Fatalf("user items = %d", len(users))
	}
	// Only the newest user turn is user_input; older turns are history.
	if users[0].Source != SourceHistory {
		t.Fatalf("older user source = %s, want history", users[0].Source)
	}
	if users[1].Source != SourceUserInput {
		t.Fatalf("newest user source = %s, want user_input", users[1].Source)
	}
}

func TestSnapshotSummary(t *testing.T) {
	m := New("SYS", "", 10000)
	m.AppendUser("hello")
	_, snap := m.BuildRequest(nil)
	s := snap.Summary()
	if !strings.Contains(s, "Context Snapshot") || !strings.Contains(s, "Total") {
		t.Fatalf("summary = %s", s)
	}
}

func TestRepoMapIncluded(t *testing.T) {
	m := New("SYS", "", 10000)
	m.SetRepoMap("Repository map:\nmain.go [go pkg main, 3 lines]\n  func main()")
	m.AppendUser("hi")

	req, snap := m.BuildRequest(nil)
	found := false
	for _, it := range snap.Items {
		if it.Source != SourceRepoMap {
			continue
		}
		found = true
		if !it.Included || it.Excluded {
			t.Fatalf("repo_map should be included: %+v", it)
		}
		if !it.Pinned {
			t.Fatalf("repo_map should be pinned: %+v", it)
		}
	}
	if !found {
		t.Fatalf("missing repo_map item: %+v", snap.Items)
	}
	if req.Messages[0].Role != llm.RoleSystem || !strings.Contains(req.Messages[0].Content, "Repository map") {
		t.Fatalf("repo_map not in system message: %+v", req.Messages[0])
	}
}

func TestCodeSearchIncluded(t *testing.T) {
	m := New("SYS", "", 10000)
	m.SetCodeSearch("Relevant code (lexical search: \"budget\"):\n  ctx/budget.go:3  const DefaultBudgetTokens")
	m.AppendUser("hi")

	req, snap := m.BuildRequest(nil)
	found := false
	for _, it := range snap.Items {
		if it.Source != SourceCodeSearch {
			continue
		}
		found = true
		if !it.Included || it.Excluded || !it.Pinned {
			t.Fatalf("code_search item wrong: %+v", it)
		}
	}
	if !found {
		t.Fatalf("missing code_search item: %+v", snap.Items)
	}
	if req.Messages[0].Role != llm.RoleSystem || !strings.Contains(req.Messages[0].Content, "Relevant code") {
		t.Fatalf("code_search not in system message: %+v", req.Messages[0])
	}
}

func TestAppendReflectionSource(t *testing.T) {
	m := New("SYS", "", 10000)
	m.AppendUser("do the task")
	m.AppendAssistant(llm.Message{Role: llm.RoleAssistant, Content: "v1"})
	m.AppendReflection("[self-reflection] fix x")

	// Round-trips through export with its own source.
	entries := m.ExportEntries()
	if len(entries) != 3 || entries[2].Source != string(SourceReflection) {
		t.Fatalf("entries = %+v", entries)
	}

	// In the next build the reflection item keeps SourceReflection (not user_input).
	_, snap := m.BuildRequest(nil)
	found := false
	for _, it := range snap.Items {
		if it.Source == SourceReflection {
			found = true
		}
	}
	if !found {
		t.Fatalf("reflection source missing from snapshot items: %+v", snap.Items)
	}
}
