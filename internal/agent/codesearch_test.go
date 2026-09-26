package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/manxisuo/mincode/internal/llm"
	"github.com/manxisuo/mincode/internal/observability"
)

type stubCodeSearch struct {
	text     string
	summary  []string
	err      error
	gotQuery string
	gotK     int
}

func (s *stubCodeSearch) SearchContext(_ context.Context, query string, k int) (string, []string, error) {
	s.gotQuery = query
	s.gotK = k
	return s.text, s.summary, s.err
}

func TestAgentInjectsCodeSearch(t *testing.T) {
	fake := llm.NewFakeProvider("m", "final")
	ag := newAgentWithFake(t, fake)
	stub := &stubCodeSearch{
		text:    "Relevant code (lexical search: \"find a\"):\n  a.go:1  func A()",
		summary: []string{"a.go:1 1.00"},
	}
	ag.CodeSearch = stub
	ag.CodeSearchTopK = 4

	var events []observability.Event
	ag.Bus.Subscribe(func(e observability.Event) { events = append(events, e) })

	res, err := ag.Run(context.Background(), "find a")
	if err != nil {
		t.Fatal(err)
	}
	if stub.gotQuery != "find a" || stub.gotK != 4 {
		t.Fatalf("query=%q k=%d", stub.gotQuery, stub.gotK)
	}
	if got := ag.Ctx.CodeSearch(); !strings.Contains(got, "Relevant code") {
		t.Fatalf("code search not pinned: %q", got)
	}
	if res.Snapshot == nil {
		t.Fatal("missing snapshot")
	}
	hasSource := false
	for _, it := range res.Snapshot.Items {
		if it.Source == "code_search" {
			hasSource = true
		}
	}
	if !hasSource {
		t.Fatalf("snapshot missing code_search source: %+v", res.Snapshot.Items)
	}

	var injected *observability.CodeSearchData
	for _, e := range events {
		if e.Type != observability.EventCodeSearchInjected {
			continue
		}
		if d, ok := e.Data.(observability.CodeSearchData); ok {
			injected = &d
		}
	}
	if injected == nil || injected.Hits != 1 || injected.TopK != 4 || injected.Query != "find a" {
		t.Fatalf("injected event = %+v", injected)
	}
}

type stubBackendCodeSearch struct{ stubCodeSearch }

func (s *stubBackendCodeSearch) Backend() string { return "embedding" }

func TestAgentCodeSearchBackendReported(t *testing.T) {
	fake := llm.NewFakeProvider("m", "final")
	ag := newAgentWithFake(t, fake)
	ag.CodeSearch = &stubBackendCodeSearch{stubCodeSearch{text: "Relevant code:\n  a.go", summary: []string{"a.go 1.0"}}}

	var events []observability.Event
	ag.Bus.Subscribe(func(e observability.Event) { events = append(events, e) })
	if _, err := ag.Run(context.Background(), "q"); err != nil {
		t.Fatal(err)
	}
	got := ""
	for _, e := range events {
		if e.Type == observability.EventCodeSearchInjected {
			if d, ok := e.Data.(observability.CodeSearchData); ok {
				got = d.Backend
			}
		}
	}
	if got != "embedding" {
		t.Fatalf("backend = %q, want embedding", got)
	}
}

func TestAgentClearsStaleCodeSearch(t *testing.T) {
	fake := llm.NewFakeProvider("m", "final")
	ag := newAgentWithFake(t, fake)
	ag.CodeSearch = &stubCodeSearch{text: "   ", summary: nil}
	ag.Ctx.SetCodeSearch("stale from previous turn")

	if _, err := ag.Run(context.Background(), "nothing relevant"); err != nil {
		t.Fatal(err)
	}
	if ag.Ctx.CodeSearch() != "" {
		t.Fatalf("stale code search not cleared: %q", ag.Ctx.CodeSearch())
	}
}

func TestAgentCodeSearchFailedIsBestEffort(t *testing.T) {
	fake := llm.NewFakeProvider("m", "final")
	ag := newAgentWithFake(t, fake)
	ag.CodeSearch = &stubCodeSearch{err: context.DeadlineExceeded}
	if _, err := ag.Run(context.Background(), "hi"); err != nil {
		t.Fatalf("search failure should not fail the turn: %v", err)
	}
}
