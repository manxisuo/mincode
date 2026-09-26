package agent

import (
	"context"
	"testing"

	"github.com/manxisuo/mincode/internal/ctxmgr"
	"github.com/manxisuo/mincode/internal/llm"
	"github.com/manxisuo/mincode/internal/observability"
)

func toolProvenances(t *testing.T, res *Result) []ctxmgr.Provenance {
	t.Helper()
	if res == nil || res.Snapshot == nil {
		t.Fatal("missing snapshot")
	}
	var out []ctxmgr.Provenance
	for _, it := range res.Snapshot.Items {
		if it.Source != ctxmgr.SourceToolResult {
			continue
		}
		if it.Prov == nil {
			t.Fatalf("tool_result missing provenance: %+v", it)
		}
		out = append(out, *it.Prov)
	}
	return out
}

func TestToolProvenanceSerial(t *testing.T) {
	fake := &llm.FakeProvider{Responses: []llm.ChatResponse{
		{ToolCalls: []llm.ToolCall{{ID: "c1", Name: "read_file", Arguments: `{"path":"cmd/main.go"}`}}},
		{Content: "done"},
	}}
	ag := newAgentWithFake(t, fake)
	res, err := ag.Run(context.Background(), "read it")
	if err != nil {
		t.Fatal(err)
	}
	provs := toolProvenances(t, res)
	if len(provs) != 1 {
		t.Fatalf("provenances = %d, want 1", len(provs))
	}
	p := provs[0]
	if p.Tool != "read_file" || p.CallID != "c1" || p.Path != "cmd/main.go" || p.ProducedAtStep == 0 {
		t.Fatalf("serial provenance incomplete: %+v", p)
	}
}

func TestToolProvenanceParallel(t *testing.T) {
	fake := &llm.FakeProvider{Responses: []llm.ChatResponse{
		{ToolCalls: []llm.ToolCall{
			{ID: "p1", Name: "read_file", Arguments: `{"path":"cmd/main.go"}`},
			{ID: "p2", Name: "list_dir", Arguments: `{"path":"."}`},
			{ID: "p3", Name: "glob", Arguments: `{"pattern":"**/*.go"}`},
		}},
		{Content: "done"},
	}}
	ag := newAgentWithFake(t, fake)
	res, err := ag.Run(context.Background(), "read all")
	if err != nil {
		t.Fatal(err)
	}
	provs := toolProvenances(t, res)
	if len(provs) != 3 {
		t.Fatalf("provenances = %d, want 3", len(provs))
	}
	ids := map[string]bool{}
	for _, p := range provs {
		if p.Tool == "" || p.CallID == "" || p.ProducedAtStep == 0 {
			t.Fatalf("parallel provenance incomplete: %+v", p)
		}
		ids[p.CallID] = true
	}
	for _, want := range []string{"p1", "p2", "p3"} {
		if !ids[want] {
			t.Fatalf("missing call_id %s in %v", want, ids)
		}
	}
}

func TestParallelDecisionRecorded(t *testing.T) {
	fake := &llm.FakeProvider{Responses: []llm.ChatResponse{
		{ToolCalls: []llm.ToolCall{
			{ID: "p1", Name: "read_file", Arguments: `{"path":"cmd/main.go"}`},
			{ID: "p2", Name: "list_dir", Arguments: `{"path":"."}`},
		}},
		{Content: "done"},
	}}
	ag := newAgentWithFake(t, fake)
	var events []observability.Event
	ag.Bus.Subscribe(func(e observability.Event) { events = append(events, e) })
	if _, err := ag.Run(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}

	var perm, par int
	for _, e := range events {
		if e.Type != observability.EventDecision {
			continue
		}
		d, ok := e.Data.(observability.DecisionData)
		if !ok {
			continue
		}
		switch d.Domain {
		case "permission":
			perm++
			if len(d.Evidence) == 0 {
				t.Fatalf("permission decision missing evidence: %+v", d)
			}
		case "parallel":
			par++
		}
	}
	if perm < 2 {
		t.Fatalf("permission decisions = %d, want >= 2 (one per parallel call)", perm)
	}
	if par < 1 {
		t.Fatalf("parallel decisions = %d, want >= 1", par)
	}
}
