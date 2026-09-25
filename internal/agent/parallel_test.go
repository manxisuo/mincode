package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/manxisuo/mincode/internal/llm"
	"github.com/manxisuo/mincode/internal/observability"
	"github.com/manxisuo/mincode/internal/tools"
)

func writeTestFile(ws *tools.Workspace, rel, content string) error {
	return os.WriteFile(filepath.Join(ws.Root(), rel), []byte(content), 0o644)
}

func TestParallelSafeIncludesWebSearch(t *testing.T) {
	for _, name := range []string{"read_file", "list_dir", "glob", "grep", "web_search", "web_fetch"} {
		if !isParallelSafe(name) {
			t.Fatalf("%s should be parallel-safe", name)
		}
	}
	for _, name := range []string{"write_file", "edit_file", "shell", "memory_add", "unknown_tool"} {
		if isParallelSafe(name) {
			t.Fatalf("%s must not be parallel-safe", name)
		}
	}
}

// blockingReadTool is a parallel-safe fake that records concurrency.
type blockingReadTool struct {
	inflight *atomic.Int32
	max      *atomic.Int32
	started  chan struct{}
	block    chan struct{}
	release  chan struct{}
}

func (t *blockingReadTool) Name() string        { return "read_file" }
func (t *blockingReadTool) Description() string { return "blocking test tool" }
func (t *blockingReadTool) Schema() tools.JSONSchema {
	return tools.JSONSchema{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}}}
}

func (t *blockingReadTool) Execute(ctx context.Context, args json.RawMessage) (tools.Result, error) {
	if t.started != nil {
		select {
		case t.started <- struct{}{}:
		default:
		}
	}
	if t.inflight != nil {
		n := t.inflight.Add(1)
		for {
			m := t.max.Load()
			if n <= m || t.max.CompareAndSwap(m, n) {
				break
			}
		}
		defer t.inflight.Add(-1)
	}
	if t.block != nil {
		select {
		case <-t.block:
		case <-ctx.Done():
			return tools.Result{Content: "cancelled", IsError: true}, ctx.Err()
		}
	}
	if t.release != nil {
		select {
		case <-t.release:
		case <-ctx.Done():
			return tools.Result{Content: "cancelled", IsError: true}, ctx.Err()
		}
	}
	return tools.Result{Content: "ok:" + string(args)}, nil
}

func TestPartitionToolCalls(t *testing.T) {
	calls := []llm.ToolCall{
		{ID: "1", Name: "read_file", Arguments: `{"path":"a"}`},
		{ID: "2", Name: "glob", Arguments: `{"pattern":"*"}`},
		{ID: "3", Name: "write_file", Arguments: `{"path":"b","content":"x"}`},
		{ID: "4", Name: "grep", Arguments: `{"pattern":"x"}`},
		{ID: "5", Name: "read_file", Arguments: `{"path":"c"}`},
	}

	groups := partitionToolCalls(calls, true, 4)
	if len(groups) != 3 {
		t.Fatalf("groups = %d, want 3: %+v", len(groups), groups)
	}
	if !groups[0].parallel || len(groups[0].calls) != 2 {
		t.Fatalf("group0 = %+v", groups[0])
	}
	if groups[1].parallel || groups[1].calls[0].Name != "write_file" {
		t.Fatalf("group1 = %+v", groups[1])
	}
	if !groups[2].parallel || len(groups[2].calls) != 2 {
		t.Fatalf("group2 = %+v", groups[2])
	}

	groups = partitionToolCalls(calls, false, 4)
	if len(groups) != 5 {
		t.Fatalf("disabled groups = %d", len(groups))
	}
	for _, g := range groups {
		if g.parallel {
			t.Fatal("disabled should not parallelize")
		}
	}

	groups = partitionToolCalls(calls, true, 1)
	if len(groups) != 5 {
		t.Fatalf("max=1 groups = %d", len(groups))
	}

	many := []llm.ToolCall{
		{ID: "a", Name: "read_file"},
		{ID: "b", Name: "read_file"},
		{ID: "c", Name: "read_file"},
		{ID: "d", Name: "read_file"},
		{ID: "e", Name: "read_file"},
	}
	groups = partitionToolCalls(many, true, 2)
	if len(groups) != 3 {
		t.Fatalf("capped groups = %d: %+v", len(groups), groups)
	}
	if len(groups[0].calls) != 2 || len(groups[1].calls) != 2 || len(groups[2].calls) != 1 {
		t.Fatalf("capped sizes: %+v", groups)
	}
}

func TestParallelReadOnlyToolsExecute(t *testing.T) {
	ws := testWorkspace(t)
	if err := writeTestFile(ws, "a.txt", "AAA"); err != nil {
		t.Fatal(err)
	}
	if err := writeTestFile(ws, "b.txt", "BBB"); err != nil {
		t.Fatal(err)
	}

	reg := tools.NewRegistry()
	reg.Register(&tools.ReadFile{WS: ws})
	reg.Register(&tools.Glob{WS: ws})

	bus := observability.NewBus()
	var mu sync.Mutex
	var events []observability.Event
	bus.Subscribe(func(e observability.Event) {
		mu.Lock()
		events = append(events, e)
		mu.Unlock()
	})

	fake := &llm.FakeProvider{
		Responses: []llm.ChatResponse{
			{
				ToolCalls: []llm.ToolCall{
					{ID: "c1", Name: "read_file", Arguments: `{"path":"a.txt"}`},
					{ID: "c2", Name: "read_file", Arguments: `{"path":"b.txt"}`},
					{ID: "c3", Name: "glob", Arguments: `{"pattern":"*.txt"}`},
				},
			},
			{Content: "done"},
		},
	}

	ag := New(fake, reg, bus, "par-session", 5, "sys", 8000)
	res, err := ag.Run(context.Background(), "read both")
	if err != nil {
		t.Fatal(err)
	}
	if res.Final != "done" {
		t.Fatalf("final = %q", res.Final)
	}
	if res.ToolCalls != 3 {
		t.Fatalf("toolCalls = %d", res.ToolCalls)
	}

	reqs := fake.Requests()
	if len(reqs) != 2 {
		t.Fatalf("requests = %d", len(reqs))
	}
	var toolMsgs []llm.Message
	for _, m := range reqs[1].Messages {
		if m.Role == llm.RoleTool {
			toolMsgs = append(toolMsgs, m)
		}
	}
	if len(toolMsgs) != 3 {
		t.Fatalf("tool msgs = %d", len(toolMsgs))
	}
	if !strings.Contains(toolMsgs[0].Content, "AAA") || !strings.Contains(toolMsgs[1].Content, "BBB") {
		t.Fatalf("order broken: %q | %q", toolMsgs[0].Content, toolMsgs[1].Content)
	}

	var batchStart, batchFinish, parallelStarts int
	for _, e := range events {
		switch e.Type {
		case observability.EventToolBatchStarted:
			batchStart++
		case observability.EventToolBatchFinished:
			batchFinish++
		case observability.EventToolStarted:
			if data, ok := e.Data.(observability.ToolEventData); ok && data.Parallel {
				parallelStarts++
			}
		}
	}
	if batchStart != 1 || batchFinish != 1 {
		t.Fatalf("batch events start=%d finish=%d", batchStart, batchFinish)
	}
	if parallelStarts != 3 {
		t.Fatalf("parallel starts = %d", parallelStarts)
	}
}

func TestParallelActuallyConcurrent(t *testing.T) {
	var inflight, maxInflight atomic.Int32
	release := make(chan struct{})

	slow := &blockingReadTool{inflight: &inflight, max: &maxInflight, release: release}

	reg := tools.NewRegistry()
	reg.Register(slow)
	bus := observability.NewBus()
	fake := &llm.FakeProvider{
		Responses: []llm.ChatResponse{
			{
				ToolCalls: []llm.ToolCall{
					{ID: "1", Name: "read_file", Arguments: `{"path":"x"}`},
					{ID: "2", Name: "read_file", Arguments: `{"path":"y"}`},
					{ID: "3", Name: "read_file", Arguments: `{"path":"z"}`},
				},
			},
			{Content: "ok"},
		},
	}
	ag := New(fake, reg, bus, "par", 5, "sys", 8000)

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = ag.Run(context.Background(), "go")
	}()

	deadline := time.After(2 * time.Second)
	for maxInflight.Load() < 3 {
		select {
		case <-deadline:
			t.Fatalf("max inflight = %d, want 3", maxInflight.Load())
		case <-time.After(5 * time.Millisecond):
		}
	}
	close(release)
	<-done

	if maxInflight.Load() < 3 {
		t.Fatalf("tools did not overlap: max=%d", maxInflight.Load())
	}
}

func TestParallelDisabledFallsBackSequential(t *testing.T) {
	ws := testWorkspace(t)
	reg := tools.NewRegistry()
	reg.Register(&tools.ReadFile{WS: ws})
	bus := observability.NewBus()
	var batchStarts int
	bus.Subscribe(func(e observability.Event) {
		if e.Type == observability.EventToolBatchStarted {
			batchStarts++
		}
	})

	fake := &llm.FakeProvider{
		Responses: []llm.ChatResponse{
			{
				ToolCalls: []llm.ToolCall{
					{ID: "1", Name: "read_file", Arguments: `{"path":"go.mod"}`},
					{ID: "2", Name: "read_file", Arguments: `{"path":"go.mod"}`},
				},
			},
			{Content: "ok"},
		},
	}
	ag := New(fake, reg, bus, "seq", 5, "sys", 8000)
	ag.ParallelTools = false

	if _, err := ag.Run(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
	if batchStarts != 0 {
		t.Fatalf("batch starts with parallel disabled = %d", batchStarts)
	}
}

func TestParallelMixedWriteStaysOrdered(t *testing.T) {
	ws := testWorkspace(t)
	reg := tools.NewRegistry()
	reg.Register(&tools.ReadFile{WS: ws})
	reg.Register(&tools.WriteFile{WS: ws})

	bus := observability.NewBus()
	fake := &llm.FakeProvider{
		Responses: []llm.ChatResponse{
			{
				ToolCalls: []llm.ToolCall{
					{ID: "r1", Name: "read_file", Arguments: `{"path":"go.mod"}`},
					{ID: "r2", Name: "read_file", Arguments: `{"path":"go.mod"}`},
					{ID: "w1", Name: "write_file", Arguments: `{"path":"out.txt","content":"hi"}`},
					{ID: "r3", Name: "read_file", Arguments: `{"path":"out.txt"}`},
				},
			},
			{Content: "wrote"},
		},
	}
	ag := New(fake, reg, bus, "mix", 5, "sys", 8000)
	ag.Approver = allowAll{}

	res, err := ag.Run(context.Background(), "mix ops")
	if err != nil {
		t.Fatal(err)
	}
	if res.Final != "wrote" {
		t.Fatalf("final = %q", res.Final)
	}

	reqs := fake.Requests()
	var toolMsgs []llm.Message
	for _, m := range reqs[1].Messages {
		if m.Role == llm.RoleTool {
			toolMsgs = append(toolMsgs, m)
		}
	}
	if len(toolMsgs) != 4 {
		t.Fatalf("tool msgs = %d", len(toolMsgs))
	}
	if !strings.Contains(toolMsgs[3].Content, "hi") {
		t.Fatalf("write/read order broken: %q", toolMsgs[3].Content)
	}
}

func TestParallelCancel(t *testing.T) {
	started := make(chan struct{}, 8)
	block := make(chan struct{})
	tb := &blockingReadTool{started: started, block: block}

	reg := tools.NewRegistry()
	reg.Register(tb)
	bus := observability.NewBus()
	fake := &llm.FakeProvider{
		Responses: []llm.ChatResponse{
			{
				ToolCalls: []llm.ToolCall{
					{ID: "1", Name: "read_file", Arguments: `{"path":"a"}`},
					{ID: "2", Name: "read_file", Arguments: `{"path":"b"}`},
				},
			},
		},
	}
	ag := New(fake, reg, bus, "cancel", 5, "sys", 8000)

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		_, err := ag.Run(ctx, "go")
		errCh <- err
	}()

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("tool never started")
	}
	cancel()
	close(block)

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("expected cancel error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
}
