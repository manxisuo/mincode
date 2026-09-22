package agent

import (
	"context"
	"testing"

	"github.com/manxisuo/mincode/internal/llm"
	"github.com/manxisuo/mincode/internal/observability"
	"github.com/manxisuo/mincode/internal/tools"
)

func TestAgentEmitsWireRequestAndRecordsUsage(t *testing.T) {
	fake := llm.NewFakeProvider("m", "ok answer")
	ws := testWorkspace(t)
	reg := tools.NewRegistry()
	reg.Register(&tools.ReadFile{WS: ws})
	bus := observability.NewBus()
	var wireEvents []observability.WireRequestData
	var finished []observability.LLMRequestData
	bus.Subscribe(func(e observability.Event) {
		if e.Type == observability.EventLLMWireRequest {
			if d, ok := e.Data.(observability.WireRequestData); ok {
				wireEvents = append(wireEvents, d)
			}
		}
		if e.Type == observability.EventLLMRequestFinished {
			if d, ok := e.Data.(observability.LLMRequestData); ok {
				finished = append(finished, d)
			}
		}
	})
	ag := New(fake, reg, bus, "wire-s", 3, "system prompt", 8000)
	res, err := ag.Run(context.Background(), "hello wire")
	if err != nil {
		t.Fatal(err)
	}
	if res.Final != "ok answer" {
		t.Fatalf("final=%q", res.Final)
	}
	if len(wireEvents) == 0 {
		t.Fatal("missing llm.wire_request event")
	}
	w := wireEvents[0]
	if w.Stage != "provider_payload" {
		t.Fatalf("stage=%q", w.Stage)
	}
	if w.MessageCount < 2 {
		t.Fatalf("wire messages=%d", w.MessageCount)
	}
	if w.Messages[0].Role != "system" {
		t.Fatalf("first role=%s", w.Messages[0].Role)
	}
	if w.EstimatedPromptTokens <= 0 {
		t.Fatalf("estimated tokens=%d", w.EstimatedPromptTokens)
	}
	rec := ag.LastWire()
	if rec == nil {
		t.Fatal("LastWire nil")
	}
	if len(rec.Messages) != w.MessageCount {
		t.Fatalf("stored messages=%d wire=%d", len(rec.Messages), w.MessageCount)
	}
	if rec.ProviderPromptTokens <= 0 {
		// Fake provider may report usage; if not, skip strict assert.
		if len(finished) > 0 && finished[0].InputTokens > 0 {
			t.Fatalf("wire missing provider tokens but finish has %d", finished[0].InputTokens)
		}
	}
}
