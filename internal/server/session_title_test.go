package server

import (
	"strings"
	"testing"

	"github.com/manxisuo/mincode/internal/agent"
	"github.com/manxisuo/mincode/internal/llm"
	"github.com/manxisuo/mincode/internal/observability"
	"github.com/manxisuo/mincode/internal/session"
	"github.com/manxisuo/mincode/internal/tools"
)

func TestSanitizeSessionTitle(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", ""},
		{"  Fix build error.  ", "Fix build error"},
		{"`Write dog poem`", "Write dog poem"},
		{"\"Hello session\"", "Hello session"},
		{"# Session about web\nmore lines", "Session about web"},
		{"用三句半写狗吃骨头。", "用三句半写狗吃骨头"},
		{strings.Repeat("长", 50), strings.Repeat("长", sessionTitleMaxRunes)},
	}
	for _, c := range cases {
		if got := sanitizeSessionTitle(c.in); got != c.want {
			t.Fatalf("sanitize(%q)=%q want %q", c.in, got, c.want)
		}
	}
}

func TestFallbackSessionTitle(t *testing.T) {
	got := fallbackSessionTitle("  please   fix   the\nweb timeline  ")
	if got != "please fix the web timeline" {
		t.Fatalf("fallback=%q", got)
	}
}

func TestSessionStoreSetTitlePreservedOnSave(t *testing.T) {
	dir := t.TempDir()
	st := session.NewStore(dir)
	id := "sess-title-1"
	if err := st.Save(&session.Record{ID: id, Workspace: "ws"}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetTitle(id, "Web inspector fixes"); err != nil {
		t.Fatal(err)
	}
	rec, err := st.Load(id)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Title != "Web inspector fixes" {
		t.Fatalf("title=%q", rec.Title)
	}
	// Simulate saveCurrentSession preserving title.
	rec.Entries = []session.Entry{{Role: "user", Content: "hi"}}
	if err := st.Save(rec); err != nil {
		t.Fatal(err)
	}
	rec2, err := st.Load(id)
	if err != nil {
		t.Fatal(err)
	}
	if rec2.Title != "Web inspector fixes" {
		t.Fatalf("title after save=%q", rec2.Title)
	}
}

func TestLLMSessionTitleSanitized(t *testing.T) {
	fake := llm.NewFakeProvider("m", "Dog bone poem workshop.")
	got := llmSessionTitle(fake, "用三句半写狗吃骨头")
	if got != "Dog bone poem workshop" {
		t.Fatalf("llm title=%q", got)
	}
}

func TestAutoNameSessionUsesLLMAndSkipsWhenNamed(t *testing.T) {
	dir := t.TempDir()
	st := session.NewStore(dir)
	id := "sess-auto-1"
	if err := st.Save(&session.Record{ID: id}); err != nil {
		t.Fatal(err)
	}
	fake := llm.NewFakeProvider("m", "Dog bone poem workshop.")
	ag := agent.New(fake, tools.NewRegistry(), observability.NewBus(), id, 3, "sys", 8000)
	srv := &Server{agent: ag, sessions: st}
	srv.opts.SessionID = id
	srv.activeSessID = id

	srv.autoNameSession("用三句半写狗吃骨头")
	rec, err := st.Load(id)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Title != "Dog bone poem workshop" {
		t.Fatalf("title=%q want LLM title", rec.Title)
	}

	// Second call must not overwrite an existing title.
	srv.autoNameSession("another message")
	rec2, err := st.Load(id)
	if err != nil {
		t.Fatal(err)
	}
	if rec2.Title != "Dog bone poem workshop" {
		t.Fatalf("title overwritten: %q", rec2.Title)
	}
}
