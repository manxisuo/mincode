package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/manxisuo/mincode/internal/llm"
	"github.com/manxisuo/mincode/internal/observability"
	"github.com/manxisuo/mincode/internal/permission"
	"github.com/manxisuo/mincode/internal/tools"
)

const defaultMaxParallel = 4

// parallelSafeTools are read-only tools with no side effects; they can run
// concurrently when the model returns several of them in one response.
var parallelSafeTools = map[string]bool{
	"read_file":  true,
	"list_dir":   true,
	"glob":       true,
	"grep":       true,
	"web_search": true,
	"web_fetch":  true,
}

func isParallelSafe(name string) bool { return parallelSafeTools[name] }

// parallelReason explains why a batch was parallelized or kept serial (T-obs-4).
func parallelReason(batchSize, maxParallel int, allSafe bool) (action, reason string, evidence []string) {
	evidence = []string{
		fmt.Sprintf("batch_size=%d", batchSize),
		fmt.Sprintf("max_parallel=%d", maxParallel),
		fmt.Sprintf("all_parallel_safe=%v", allSafe),
	}
	if allSafe && batchSize > 1 {
		return "parallelize", "all tools ParallelSafe=true and max_parallel allows concurrency", evidence
	}
	if batchSize <= 1 {
		return "serial", "single tool call — no batch", evidence
	}
	return "serial", "at least one tool is not ParallelSafe (write/shell keep serial)", evidence
}

// toolGroup is a batch of tool calls that share one execution strategy.
type toolGroup struct {
	calls    []llm.ToolCall
	parallel bool
}

// partitionToolCalls groups consecutive parallel-safe calls so they can run
// together. Sequential tools (write/edit/shell/unknown) always form
// singleton groups and keep relative order with the parallel groups.
func partitionToolCalls(calls []llm.ToolCall, allowParallel bool, maxParallel int) []toolGroup {
	if len(calls) == 0 {
		return nil
	}
	if !allowParallel || maxParallel <= 1 || len(calls) == 1 {
		out := make([]toolGroup, 0, len(calls))
		for _, tc := range calls {
			out = append(out, toolGroup{calls: []llm.ToolCall{tc}})
		}
		return out
	}

	var groups []toolGroup
	i := 0
	for i < len(calls) {
		if !isParallelSafe(calls[i].Name) {
			groups = append(groups, toolGroup{calls: []llm.ToolCall{calls[i]}})
			i++
			continue
		}
		j := i
		for j < len(calls) && isParallelSafe(calls[j].Name) && j-i < maxParallel {
			j++
		}
		batch := calls[i:j]
		groups = append(groups, toolGroup{calls: batch, parallel: len(batch) > 1})
		i = j
	}
	return groups
}

// toolOutcome pairs a call with its execution result.
type toolOutcome struct {
	call   llm.ToolCall
	result tools.Result
}

// executeToolCalls runs one LLM tool-call batch, parallelizing consecutive
// read-only tools. Results are returned in the original call order. All bus
// emissions and context mutations stay on this goroutine.
func (a *Agent) executeToolCalls(ctx context.Context, calls []llm.ToolCall) ([]toolOutcome, error) {
	groups := partitionToolCalls(calls, a.ParallelTools, a.maxParallel())
	outcomes := make([]toolOutcome, len(calls))

	offset := 0
	for _, g := range groups {
		n, err := a.executeGroup(ctx, g, offset, outcomes)
		offset += n
		if err != nil {
			return outcomes, err
		}
	}
	return outcomes, nil
}

func (a *Agent) maxParallel() int {
	if a.MaxParallel <= 0 {
		return defaultMaxParallel
	}
	return a.MaxParallel
}

func (a *Agent) executeGroup(ctx context.Context, g toolGroup, offset int, outcomes []toolOutcome) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if !g.parallel || a.groupNeedsInteractiveApproval(g) {
		return a.executeGroupSequential(ctx, g, offset, outcomes)
	}
	return a.executeGroupParallel(ctx, g, offset, outcomes)
}

func (a *Agent) executeGroupSequential(ctx context.Context, g toolGroup, offset int, outcomes []toolOutcome) (int, error) {
	for i, tc := range g.calls {
		if err := ctx.Err(); err != nil {
			return i, err
		}
		a.setState(StateExecutingTool)
		out, execErr := a.executeTool(ctx, tc)
		if execErr != nil {
			if isCancelErr(execErr) {
				return i + 1, execErr
			}
			out = tools.Result{Content: execErr.Error(), IsError: true}
		}
		a.Ctx.AppendToolResult(tc.ID, out.Content)
		outcomes[offset+i] = toolOutcome{call: tc, result: out}
	}
	return len(g.calls), nil
}

func (a *Agent) executeGroupParallel(ctx context.Context, g toolGroup, offset int, outcomes []toolOutcome) (int, error) {
	a.setState(StateExecutingTool)

	names := make([]string, len(g.calls))
	ids := make([]string, len(g.calls))
	for i, tc := range g.calls {
		names[i] = tc.Name
		ids[i] = tc.ID
	}
	a.emit(observability.EventToolBatchStarted, observability.ToolBatchData{
		Size:       len(g.calls),
		Parallel:   true,
		Tools:      names,
		CallIDs:    ids,
		MaxWorkers: a.maxParallel(),
	})

	prepared := make([]preparedCall, len(g.calls))
	for i, tc := range g.calls {
		p, err := a.prepareToolCall(tc, offset+i)
		if err != nil {
			return i, err
		}
		prepared[i] = p
	}

	type execSlot struct {
		result tools.Result
		err    error
		dur    time.Duration
	}
	slots := make([]execSlot, len(prepared))

	batchStart := time.Now()
	var wg sync.WaitGroup
	sem := make(chan struct{}, a.maxParallel())
	pctx, cancel := context.WithCancel(ctx)
	defer cancel()

	for i := range prepared {
		if prepared[i].denied {
			slots[i] = execSlot{result: prepared[i].result}
			continue
		}
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-pctx.Done():
				slots[i] = execSlot{err: pctx.Err()}
				return
			}
			defer func() { <-sem }()

			start := time.Now()
			args := prepared[i].call.Arguments
			if args == "" {
				args = "{}"
			}
			res, err := a.Tools.Execute(pctx, prepared[i].call.Name, json.RawMessage(args))
			slots[i] = execSlot{result: res, err: err, dur: time.Since(start)}
		}(i)
	}
	wg.Wait()
	batchDur := time.Since(batchStart)

	var succeeded, failed int
	for i := range prepared {
		out := slots[i].result
		if slots[i].err != nil {
			out = tools.Result{Content: slots[i].err.Error(), IsError: true}
		}
		a.finishToolCall(prepared[i], out, slots[i].dur, offset+i)
		if out.IsError {
			failed++
		} else {
			succeeded++
		}
		outcomes[offset+i] = toolOutcome{call: prepared[i].call, result: out}

		if slots[i].err != nil && isCancelErr(slots[i].err) {
			a.emitBatchFinished(g, names, ids, batchDur, succeeded, failed)
			return len(g.calls), slots[i].err
		}
	}

	a.emitBatchFinished(g, names, ids, batchDur, succeeded, failed)
	return len(g.calls), nil
}

func (a *Agent) emitBatchFinished(g toolGroup, names, ids []string, dur time.Duration, succeeded, failed int) {
	a.emit(observability.EventToolBatchFinished, observability.ToolBatchData{
		Size:       len(g.calls),
		Parallel:   true,
		Tools:      names,
		CallIDs:    ids,
		DurationMS: dur.Milliseconds(),
		Succeeded:  succeeded,
		Failed:     failed,
		MaxWorkers: a.maxParallel(),
	})
}

// preparedCall holds a tool call whose permission has already been decided.
type preparedCall struct {
	call    llm.ToolCall
	index   int
	args    string
	summary string
	denied  bool
	result  tools.Result
}

func isCancelErr(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// groupNeedsInteractiveApproval is true when any member would prompt the user.
func (a *Agent) groupNeedsInteractiveApproval(g toolGroup) bool {
	if a.Approver == nil {
		return false
	}
	for _, tc := range g.calls {
		args := tc.Arguments
		if args == "" {
			args = "{}"
		}
		level := permission.Ask
		if a.Policy != nil {
			level = a.Policy.Evaluate(permission.Request{
				Tool:      tc.Name,
				Arguments: args,
				Summary:   summarizeToolCall(tc.Name, args),
			})
		}
		if level == permission.Ask {
			return true
		}
	}
	return false
}

// prepareToolCall evaluates permission and emits request/start events.
// Denied calls short-circuit with an error result (no Execute).
func (a *Agent) prepareToolCall(tc llm.ToolCall, index int) (preparedCall, error) {
	args := tc.Arguments
	if args == "" {
		args = "{}"
	}
	p := preparedCall{call: tc, index: index, args: args, summary: summarizeToolCall(tc.Name, args)}

	req := permission.Request{
		Tool:      tc.Name,
		Arguments: args,
		Summary:   p.summary,
	}
	level := permission.Ask
	if a.Policy != nil {
		level = a.Policy.Evaluate(req)
	}
	a.emit(observability.EventPermissionRequested, observability.PermissionData{
		Tool:      tc.Name,
		Arguments: args,
		Summary:   p.summary,
		Level:     level.String(),
	})

	denied, perr := permission.Evaluate(a.Policy, a.Approver, req)
	if denied {
		reason := "denied"
		if perr != nil {
			reason = perr.Error()
		}
		a.emit(observability.EventPermissionDenied, observability.PermissionData{
			Tool:     tc.Name,
			Summary:  p.summary,
			Level:    level.String(),
			Decision: "denied",
			Reason:   reason,
		})
		p.denied = true
		p.result = tools.Result{
			Content: "permission denied: " + reason,
			IsError: true,
			Meta:    map[string]any{"permission": "denied"},
		}
		return p, nil
	}
	a.emit(observability.EventPermissionApproved, observability.PermissionData{
		Tool:     tc.Name,
		Summary:  p.summary,
		Level:    level.String(),
		Decision: "approved",
	})

	a.emit(observability.EventToolRequested, observability.ToolEventData{
		Tool:      tc.Name,
		Arguments: args,
		CallID:    tc.ID,
		Index:     index,
		Parallel:  true,
	})
	a.emit(observability.EventToolStarted, observability.ToolEventData{
		Tool:      tc.Name,
		Arguments: args,
		CallID:    tc.ID,
		Index:     index,
		Parallel:  true,
	})
	return p, nil
}

// finishToolCall emits finished events, file.changed, instructions, and
// appends the tool result. Always called on the agent's main goroutine.
func (a *Agent) finishToolCall(p preparedCall, result tools.Result, dur time.Duration, index int) {
	if metaPath, ok := result.Meta["path"].(string); ok && (p.call.Name == "write_file" || p.call.Name == "edit_file") {
		if !result.IsError {
			op, _ := result.Meta["operation"].(string)
			diff, _ := result.Meta["diff"].(string)
			a.emit(observability.EventFileChanged, observability.FileChangedData{
				Path:      metaPath,
				Operation: op,
				Bytes:     len(result.Content),
				Diff:      diff,
			})
		}
	}

	a.loadInstructionsForTool(p.call, result)

	preview := result.Content
	if len(preview) > toolPreviewLen {
		preview = truncatePreview(preview, toolPreviewLen)
	}
	data := observability.ToolEventData{
		Tool:          p.call.Name,
		Arguments:     p.args,
		CallID:        p.call.ID,
		Index:         index,
		Parallel:      true,
		DurationMS:    dur.Milliseconds(),
		ResultSize:    len(result.Content),
		IsError:       result.IsError,
		OutputPreview: preview,
	}
	if result.IsError {
		data.Error = result.Content
	}
	a.emit(observability.EventToolFinished, data)
	a.Ctx.AppendToolResult(p.call.ID, result.Content)
}
