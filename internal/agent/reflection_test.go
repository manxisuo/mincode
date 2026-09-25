package agent

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/manxisuo/mincode/internal/ctxmgr"
	"github.com/manxisuo/mincode/internal/llm"
	"github.com/manxisuo/mincode/internal/observability"
	"github.com/manxisuo/mincode/internal/tools"
)

func newReflectionAgent(t *testing.T, provider llm.Provider, reflect bool, maxRef int) (*Agent, *[]observability.Event) {
	t.Helper()
	ws := testWorkspace(t)
	reg := tools.NewRegistry()
	reg.Register(&tools.ReadFile{WS: ws})
	bus := observability.NewBus()
	var events []observability.Event
	bus.Subscribe(func(e observability.Event) { events = append(events, e) })
	ag := New(provider, reg, bus, "s", 10, "sys", 32000)
	ag.Reflection = reflect
	ag.MaxReflections = maxRef
	return ag, &events
}

func reflectionVerdicts(events []observability.Event) []string {
	var out []string
	for _, e := range events {
		if e.Type == observability.EventReflectionFinished {
			if d, ok := e.Data.(observability.ReflectionData); ok {
				out = append(out, d.Verdict)
			}
		}
	}
	return out
}

func TestReflectionDone(t *testing.T) {
	fake := &llm.FakeProvider{Responses: []llm.ChatResponse{
		{Content: "first answer"},
		{Content: "VERDICT: DONE"},
	}}
	ag, events := newReflectionAgent(t, fake, true, 2)

	res, err := ag.Run(context.Background(), "hi")
	if err != nil {
		t.Fatal(err)
	}
	if res.Final != "first answer" {
		t.Fatalf("final = %q", res.Final)
	}
	if fake.Calls() != 2 {
		t.Fatalf("calls = %d, want 2 (answer + critique)", fake.Calls())
	}
	if got := reflectionVerdicts(*events); len(got) != 1 || got[0] != "done" {
		t.Fatalf("verdicts = %v", got)
	}
}

func TestReflectionContinueThenDone(t *testing.T) {
	fake := &llm.FakeProvider{Responses: []llm.ChatResponse{
		{Content: "v1"},
		{Content: "VERDICT: CONTINUE\n1. fix x\n2. rerun tests"},
		{Content: "v2"},
		{Content: "VERDICT: DONE"},
	}}
	ag, events := newReflectionAgent(t, fake, true, 2)

	res, err := ag.Run(context.Background(), "do task")
	if err != nil {
		t.Fatal(err)
	}
	if res.Final != "v2" {
		t.Fatalf("final = %q, want v2", res.Final)
	}
	if res.Steps != 2 {
		t.Fatalf("steps = %d, want 2", res.Steps)
	}
	if fake.Calls() != 4 {
		t.Fatalf("calls = %d, want 4", fake.Calls())
	}
	got := reflectionVerdicts(*events)
	if len(got) != 2 || got[0] != "continue" || got[1] != "done" {
		t.Fatalf("verdicts = %v", got)
	}
	// The critique must have entered the conversation.
	if !containsText(ag.Ctx.ExportEntries(), "self-reflection") {
		t.Fatal("reflection message missing from context")
	}
}

func TestReflectionMaxCap(t *testing.T) {
	fake := &llm.FakeProvider{Responses: []llm.ChatResponse{
		{Content: "v1"},
		{Content: "VERDICT: CONTINUE\n1. still broken"},
		{Content: "v2"},
		{Content: "VERDICT: CONTINUE\n1. still broken"},
	}}
	ag, events := newReflectionAgent(t, fake, true, 1)

	res, err := ag.Run(context.Background(), "do task")
	if err != nil {
		t.Fatal(err)
	}
	if res.Final != "v2" {
		t.Fatalf("final = %q, want v2", res.Final)
	}
	// Only one reflection allowed: answer, critique, v2.
	if fake.Calls() != 3 {
		t.Fatalf("calls = %d, want 3 (cap at 1 reflection)", fake.Calls())
	}
	if got := reflectionVerdicts(*events); len(got) != 1 {
		t.Fatalf("verdicts = %v", got)
	}
}

func TestReflectionDisabledByDefault(t *testing.T) {
	fake := &llm.FakeProvider{Responses: []llm.ChatResponse{{Content: "answer"}}}
	ag, events := newReflectionAgent(t, fake, false, 2)

	res, err := ag.Run(context.Background(), "hi")
	if err != nil {
		t.Fatal(err)
	}
	if res.Final != "answer" {
		t.Fatalf("final = %q", res.Final)
	}
	if fake.Calls() != 1 {
		t.Fatalf("calls = %d, want 1 (no reflection)", fake.Calls())
	}
	if len(reflectionVerdicts(*events)) != 0 {
		t.Fatal("no reflection events expected when disabled")
	}
}

// failSecondProvider answers once, then returns an error (reflection failure).
type failSecondProvider struct {
	mu    sync.Mutex
	calls int
}

func (p *failSecondProvider) Name() string  { return "fail-second" }
func (p *failSecondProvider) Model() string { return "m" }
func (p *failSecondProvider) Chat(_ context.Context, _ llm.ChatRequest) (*llm.ChatResponse, error) {
	p.mu.Lock()
	p.calls++
	n := p.calls
	p.mu.Unlock()
	if n == 1 {
		return &llm.ChatResponse{Content: "answer"}, nil
	}
	return nil, errors.New("reflection boom")
}

func TestReflectionErrorAcceptsAnswer(t *testing.T) {
	ag, events := newReflectionAgent(t, &failSecondProvider{}, true, 2)

	res, err := ag.Run(context.Background(), "hi")
	if err != nil {
		t.Fatalf("reflection failure must not fail the turn: %v", err)
	}
	if res.Final != "answer" {
		t.Fatalf("final = %q", res.Final)
	}
	if got := reflectionVerdicts(*events); len(got) != 1 || got[0] != "error" {
		t.Fatalf("verdicts = %v", got)
	}
}

func TestParseReflection(t *testing.T) {
	tests := []struct {
		in      string
		verdict string
		issues  int
	}{
		{"VERDICT: DONE", "done", 0},
		{"verdict: done", "done", 0},
		{"VERDICT: CONTINUE\n1. a\n2. b", "continue", 2},
		{"VERDICT: CONTINUE\n- a\n* b", "continue", 2},
		{"looks fine to me", "done", 0},
		{"", "done", 0},
	}
	for _, tt := range tests {
		v, n := parseReflection(tt.in)
		if v != tt.verdict || n != tt.issues {
			t.Fatalf("parseReflection(%q) = (%s,%d), want (%s,%d)", tt.in, v, n, tt.verdict, tt.issues)
		}
	}
}

func containsText(entries []ctxmgr.ExportedEntry, sub string) bool {
	for _, e := range entries {
		if e.Source == "reflection" && strings.Contains(e.Msg.Content, sub) {
			return true
		}
	}
	return false
}
