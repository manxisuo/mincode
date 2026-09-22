package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/manxisuo/mincode/internal/ctxmgr"
	"github.com/manxisuo/mincode/internal/instruction"
	"github.com/manxisuo/mincode/internal/llm"
	"github.com/manxisuo/mincode/internal/observability"
	"github.com/manxisuo/mincode/internal/permission"
	"github.com/manxisuo/mincode/internal/tools"
)

// MaxStepsExceeded is returned when the loop hits the step budget.
var MaxStepsExceeded = errors.New("agent: max steps exceeded")

// LoopDetected is returned when the same tool call repeats too often.
var LoopDetected = errors.New("agent: tool call loop detected")

const (
	defaultMaxSteps = 30
	maxLoopRepeats  = 5
	toolPreviewLen  = 200
)

// truncatePreview cuts s to at most max bytes on a UTF-8 rune boundary.
func truncatePreview(s string, max int) string {
	if len(s) <= max {
		return s
	}
	end := max
	for end > 0 && !utf8.RuneStart(s[end]) {
		end--
	}
	return s[:end] + "..."
}

// Agent coordinates the loop; it does not touch the filesystem or shell directly.
type Agent struct {
	Provider  llm.Provider
	Tools     *tools.Registry
	Bus       *observability.Bus
	SessionID string
	MaxSteps  int

	// Policy and Approver gate write/edit tools. Nil policy uses defaults.
	Policy   permission.Policy
	Approver permission.Approver

	// ParallelTools enables concurrent execution of consecutive read-only
	// tool calls returned in a single LLM response.
	ParallelTools bool
	// MaxParallel caps concurrent read-only tools (default 4).
	MaxParallel int

	// Stream enables streaming LLM output when the provider supports it.
	Stream bool
	// lastTTFT is TTFT of the most recent streamed chat call.
	lastTTFT time.Duration

	// lastWire is the most recent provider-bound request (T-obs-1 wire view).
	lastWireMu sync.Mutex
	lastWire   *WireRecord

	// Ctx builds budgeted prompts and keeps conversation state.
	Ctx   *ctxmgr.Manager
	State State

	// Instr loads hierarchical AGENTS.md instructions (optional).
	Instr *instruction.Loader
}

// WireRecord is one provider-bound LLM request for the wire inspector.
type WireRecord struct {
	Time                  time.Time          `json:"time"`
	Step                  int                `json:"step"`
	Provider              string             `json:"provider"`
	Model                 string             `json:"model"`
	Stream                bool               `json:"stream"`
	Messages              []llm.Message      `json:"messages"`
	Tools                 []WireToolSummary  `json:"tools"`
	Temperature           *float64           `json:"temperature,omitempty"`
	MaxTokens             *int               `json:"max_tokens,omitempty"`
	EstimatedPromptTokens int                `json:"estimated_prompt_tokens"`
	SnapshotTotalTokens   int                `json:"snapshot_total_tokens"`
	SnapshotToolTokens    int                `json:"snapshot_tool_tokens"`
	ProviderPromptTokens  int                `json:"provider_prompt_tokens,omitempty"`
	PromptTokenDelta      int                `json:"prompt_token_delta,omitempty"`
	Summary               llm.RequestSummary `json:"summary"`
}

// WireToolSummary is tool metadata stored with a wire record.
type WireToolSummary struct {
	Name           string `json:"name"`
	SchemaBytes    int    `json:"schema_bytes"`
	DescriptionLen int    `json:"description_len,omitempty"`
}

// New creates an agent with a context manager.
func New(provider llm.Provider, reg *tools.Registry, bus *observability.Bus, sessionID string, maxSteps int, systemPrompt string, tokenBudget int) *Agent {
	return NewWithCompress(provider, reg, bus, sessionID, maxSteps, systemPrompt, tokenBudget, ctxmgr.DefaultCompressAtTokens)
}

// NewWithCompress is New with an explicit compaction threshold.
func NewWithCompress(provider llm.Provider, reg *tools.Registry, bus *observability.Bus, sessionID string, maxSteps int, systemPrompt string, tokenBudget, compressAt int) *Agent {
	if maxSteps <= 0 {
		maxSteps = defaultMaxSteps
	}
	mgr := ctxmgr.New(systemPrompt, "", tokenBudget)
	mgr.SetCompressAt(compressAt)
	return &Agent{
		Provider:      provider,
		Tools:         reg,
		Bus:           bus,
		SessionID:     sessionID,
		MaxSteps:      maxSteps,
		Policy:        &permission.ShellAwarePolicy{Inner: permission.NewDefaultPolicy()},
		ParallelTools: true,
		MaxParallel:   defaultMaxParallel,
		Stream:        true,
		Ctx:           mgr,
		State:         StateIdle,
	}
}

// ClearConversation drops user/assistant/tool turns but keeps system prompt.
func (a *Agent) ClearConversation() {
	a.Ctx.Clear()
	a.State = StateIdle
}

func (a *Agent) emit(typ observability.EventType, data any) {
	if a.Bus == nil {
		return
	}
	a.Bus.Publish(observability.NewEvent(a.SessionID, a.Ctx.Len(), typ, data))
}

func (a *Agent) setState(to State) {
	from := a.State
	if from == to {
		return
	}
	a.State = to
	a.emit(observability.EventAgentStateChanged, observability.StateChangedData{
		From: string(from),
		To:   string(to),
	})
}

// Result is the outcome of one user turn.
type Result struct {
	Final     string
	Steps     int
	ToolCalls int
	State     State
	Snapshot  *ctxmgr.Snapshot
}

// Run processes one user message through the agent loop.
func (a *Agent) Run(ctx context.Context, userInput string) (*Result, error) {
	a.Ctx.AppendUser(userInput)

	var (
		steps     int
		toolCalls int
		lastKey   string
		repeats   int
	)

	for step := 0; step < a.MaxSteps; step++ {
		steps = step + 1

		if err := ctx.Err(); err != nil {
			a.setState(StateCancelled)
			a.Ctx.DropLastUser()
			return nil, err
		}

		a.setState(StateBuildingContext)
		a.emit(observability.EventContextBuildStarted, observability.ContextBuiltData{
			Step:   step + 1,
			Budget: a.Ctx.Budget(),
		})

		if cr := a.Ctx.CompactIfNeed(); cr != nil {
			a.emit(observability.EventContextCompactionStart, observability.CompactionData{
				BeforeTokens: cr.BeforeTokens,
			})
			preview := cr.Summary
			if len(preview) > 300 {
				preview = truncatePreview(preview, 300)
			}
			a.emit(observability.EventContextCompacted, observability.CompactionData{
				BeforeTokens:   cr.BeforeTokens,
				AfterTokens:    cr.AfterTokens,
				Compressed:     cr.Compressed,
				Preserved:      cr.Preserved,
				Pinned:         cr.Pinned,
				SavedTokens:    cr.SavedTokens,
				Policy:         cr.Policy,
				Reason:         cr.Reason,
				SummaryPreview: preview,
			})
		}

		defs := a.toolDefinitions()
		req, snap := a.Ctx.BuildRequest(defs)
		a.emit(observability.EventContextBuilt, observability.ContextBuiltData{
			Step:        snap.Step,
			TotalTokens: snap.TotalTokens,
			ToolTokens:  snap.ToolTokens,
			Budget:      snap.Budget,
			Included:    snap.Included,
			Excluded:    snap.Excluded,
			Truncated:   snap.Truncated,
		})
		a.emitContextDiff(&snap)

		a.setState(StateCallingLLM)
		a.emitLLMStarted(req)
		resp, elapsed, streamed, err := a.chatWithProvider(ctx, req)
		if err != nil {
			a.emitLLMFailed(elapsed, err)
			a.Ctx.MarkRequestFailed()
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				a.setState(StateCancelled)
			} else {
				a.setState(StateFailed)
			}
			a.Ctx.DropLastUser()
			return nil, err
		}
		a.emitLLMFinished(req, resp, elapsed, streamed)

		// Calibrate local token estimates against provider-reported prompt_tokens.
		if resp.Usage.PromptTokens > 0 {
			est := snap.TotalTokens + snap.ToolTokens
			a.Ctx.ObserveUsage(est, resp.Usage.PromptTokens)
		}

		a.setState(StateProcessingResponse)

		if len(resp.ToolCalls) == 0 {
			a.Ctx.AppendAssistant(llm.Message{
				Role:    llm.RoleAssistant,
				Content: resp.Content,
			})
			a.setState(StateFinished)
			return &Result{
				Final:     resp.Content,
				Steps:     steps,
				ToolCalls: toolCalls,
				State:     StateFinished,
				Snapshot:  a.Ctx.LastSnapshot(),
			}, nil
		}

		a.Ctx.AppendAssistant(llm.Message{
			Role:      llm.RoleAssistant,
			Content:   resp.Content,
			ToolCalls: resp.ToolCalls,
		})

		// Loop detection across the whole response before any execution.
		for _, tc := range resp.ToolCalls {
			key := tc.Name + "\x00" + tc.Arguments
			if key == lastKey {
				repeats++
			} else {
				lastKey = key
				repeats = 1
			}
			if repeats >= maxLoopRepeats {
				a.emit(observability.EventLoopDetected, observability.LoopDetectedData{
					Tool:      tc.Name,
					Count:     repeats,
					Arguments: tc.Arguments,
				})
				a.emitDecision("loop", "stop", tc.Name, "max_loop_repeats",
					fmt.Sprintf("same tool+args repeated %d times (limit %d)", repeats, maxLoopRepeats),
					[]string{fmt.Sprintf("tool=%s", tc.Name), fmt.Sprintf("count=%d", repeats)})
				a.setState(StateFailed)
				return &Result{State: StateFailed}, fmt.Errorf("%w: %s x%d", LoopDetected, tc.Name, repeats)
			}
		}

		toolCalls += len(resp.ToolCalls)
		_, execErr := a.executeToolCalls(ctx, resp.ToolCalls)
		if execErr != nil {
			if isCancelErr(execErr) {
				a.setState(StateCancelled)
				a.Ctx.DropLastUser()
				return nil, execErr
			}
		}
	}

	a.setState(StateMaxStepsReached)
	return &Result{
		Steps:     steps,
		ToolCalls: toolCalls,
		State:     StateMaxStepsReached,
		Snapshot:  a.Ctx.LastSnapshot(),
	}, fmt.Errorf("%w: %d", MaxStepsExceeded, a.MaxSteps)
}

func (a *Agent) toolDefinitions() []llm.ToolDefinition {
	if a.Tools == nil {
		return nil
	}
	names := a.Tools.Names()
	out := make([]llm.ToolDefinition, 0, len(names))
	for _, name := range names {
		t, ok := a.Tools.Get(name)
		if !ok {
			continue
		}
		out = append(out, llm.ToolDefinition{
			Name:        t.Name(),
			Description: t.Description(),
			Parameters:  t.Schema().Raw(),
		})
	}
	return out
}

func (a *Agent) executeTool(ctx context.Context, tc llm.ToolCall) (tools.Result, error) {
	args := tc.Arguments
	if args == "" {
		args = "{}"
	}

	summary := summarizeToolCall(tc.Name, args)
	req := permission.Request{
		Tool:      tc.Name,
		Arguments: args,
		Summary:   summary,
	}
	level := permission.Ask
	if a.Policy != nil {
		level = a.Policy.Evaluate(req)
	}
	a.emit(observability.EventPermissionRequested, observability.PermissionData{
		Tool:      tc.Name,
		Arguments: args,
		Summary:   summary,
		Level:     level.String(),
	})
	a.emitDecision("permission", level.String(), tc.Name, shellPolicyName(tc.Name), decisionReason(tc.Name, level), []string{"summary=" + summary})

	denied, perr := permission.Evaluate(a.Policy, a.Approver, req)
	if denied {
		reason := "denied"
		if perr != nil {
			reason = perr.Error()
		}
		a.emit(observability.EventPermissionDenied, observability.PermissionData{
			Tool:     tc.Name,
			Summary:  summary,
			Level:    level.String(),
			Decision: "denied",
			Reason:   reason,
		})
		msg := "permission denied: " + reason
		return tools.Result{Content: msg, IsError: true, Meta: map[string]any{"permission": "denied"}}, nil
	}
	a.emit(observability.EventPermissionApproved, observability.PermissionData{
		Tool:     tc.Name,
		Summary:  summary,
		Level:    level.String(),
		Decision: "approved",
	})

	a.emit(observability.EventToolRequested, observability.ToolEventData{
		Tool:      tc.Name,
		Arguments: args,
	})
	a.emit(observability.EventToolStarted, observability.ToolEventData{
		Tool:      tc.Name,
		Arguments: args,
	})

	start := time.Now()
	result, err := a.Tools.Execute(ctx, tc.Name, json.RawMessage(args))
	elapsed := time.Since(start)

	if err != nil {
		a.emit(observability.EventToolFailed, observability.ToolEventData{
			Tool:       tc.Name,
			Arguments:  args,
			DurationMS: elapsed.Milliseconds(),
			Error:      err.Error(),
		})
		return result, err
	}

	metaPath := ""
	if p, ok := result.Meta["path"].(string); ok && (tc.Name == "write_file" || tc.Name == "edit_file") {
		metaPath = p
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

	a.loadInstructionsForTool(tc, result)
	// Called after AppendToolResult so the meta sticks to the tool_result entry.
	a.Ctx.SetMeta(tc.Name, tc.Name, metaPath, "", a.Ctx.LastStep())

	preview := result.Content
	if len(preview) > toolPreviewLen {
		preview = truncatePreview(preview, toolPreviewLen)
	}
	data := observability.ToolEventData{
		Tool:          tc.Name,
		Arguments:     args,
		DurationMS:    elapsed.Milliseconds(),
		ResultSize:    len(result.Content),
		IsError:       result.IsError,
		OutputPreview: preview,
	}
	if result.IsError {
		data.Error = result.Content
	}
	// T-obs-3: attach lineage meta to the tool_result entry (after Append).
	a.Ctx.SetMeta(tc.Name, tc.Name, metaPath, "", a.Ctx.LastStep()+1)
	a.emit(observability.EventToolFinished, data)
	return result, nil
}

// loadInstructionsForTool pulls in AGENTS.md for directories the tool just
// touched, so nested project rules enter the next context build.
func (a *Agent) loadInstructionsForTool(tc llm.ToolCall, result tools.Result) {
	if a.Instr == nil || result.IsError {
		return
	}
	var path string
	if p, ok := result.Meta["path"].(string); ok && p != "" {
		path = p
	} else {
		path = pathFromArgs(tc.Arguments)
	}
	if path == "" {
		if tc.Name == "list_dir" {
			path = "."
		} else {
			return
		}
	}
	added, err := a.Instr.LoadForPath(path)
	if err != nil || len(added) == 0 {
		return
	}
	for _, f := range added {
		a.emit(observability.EventInstructionLoaded, observability.InstructionLoadedData{
			Path:    f.Path,
			RelPath: f.RelPath,
			RelDir:  f.RelDir,
			Bytes:   len(f.Content),
		})
	}
	a.Ctx.SetInstructions(a.Instr.Compose())
}

// pathFromArgs extracts a "path" field from a tool-arguments JSON object.
func pathFromArgs(args string) string {
	if args == "" {
		return ""
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(args), &m); err != nil {
		return ""
	}
	p, _ := m["path"].(string)
	return p
}

func summarizeToolCall(name, args string) string {
	var m map[string]any
	if err := json.Unmarshal([]byte(args), &m); err != nil {
		return name
	}
	path, _ := m["path"].(string)
	switch name {
	case "write_file":
		return fmt.Sprintf("write_file %s", path)
	case "edit_file":
		return fmt.Sprintf("edit_file %s", path)
	case "read_file":
		return fmt.Sprintf("read_file %s", path)
	case "memory_add":
		entry, _ := m["entry"].(string)
		if entry != "" {
			return "memory_add " + truncatePreview(entry, 60)
		}
		return "memory_add"
	case "shell":
		cmd, _ := m["command"].(string)
		return "shell " + cmd
	default:
		if path != "" {
			return name + " " + path
		}
		return name
	}
}

func (a *Agent) emitLLMStarted(req llm.ChatRequest) {
	a.recordWire(req)
	a.emit(observability.EventLLMRequestStarted, observability.LLMRequestData{
		Provider:     a.Provider.Name(),
		Model:        a.Provider.Model(),
		MessageCount: len(req.Messages),
	})
	a.emitWireRequest(req)
}

// emitDecision records one Runtime decision (T-obs-4). Never model thoughts.
func (a *Agent) emitDecision(domain, action, target, policy, reason string, evidence []string) {
	if a.Bus == nil {
		return
	}
	a.emit(observability.EventDecision, observability.DecisionData{
		Domain:   domain,
		Action:   action,
		Target:   target,
		Policy:   policy,
		Reason:   reason,
		Evidence: evidence,
	})
}

func shellPolicyName(tool string) string {
	if tool == "shell" {
		return "shell_classify"
	}
	return "default_tool_level"
}

func decisionReason(tool string, lvl permission.Level) string {
	switch tool {
	case "shell":
		return "ClassifyShell → " + lvl.String()
	default:
		return "DefaultPolicy.Evaluate → " + lvl.String()
	}
}

// emitContextDiff publishes T-obs-2 causal diff for the latest snapshot.
func (a *Agent) emitContextDiff(snap *ctxmgr.Snapshot) {
	if a.Bus == nil || snap == nil || snap.Diff == nil {
		return
	}
	d := snap.Diff
	lines := make([]string, 0, len(d.Entries))
	for _, e := range d.Entries {
		if e.Change == ctxmgr.DiffKept {
			continue
		}
		lines = append(lines, fmt.Sprintf("%s %s %s", e.Change, e.Source, e.Reason))
	}
	a.emit(observability.EventContextDiff, observability.ContextDiffData{
		FromStep:      d.FromStep,
		ToStep:        d.ToStep,
		Added:         d.Added,
		Removed:       d.Removed,
		Kept:          d.Kept,
		ExcludedNow:   d.ExcludedNow,
		TruncatedNow:  d.TruncatedNow,
		Reincluded:    d.Reincluded,
		SavedTokens:   d.SavedTokens,
		FromTotal:     d.FromTotal,
		ToTotal:       d.ToTotal,
		Budget:        d.Budget,
		Notes:         d.Notes,
		ChangePreview: lines,
	})
}

// recordWire stores the provider-bound request after Context Builder.
func (a *Agent) recordWire(req llm.ChatRequest) {
	est, snapTotal, snapTools := a.wireTokenEstimate()
	sum := llm.DescribeRequest(req, a.Provider.Name(), a.Provider.Model())
	rec := &WireRecord{
		Time:                  time.Now().UTC(),
		Step:                  a.stepForWire(),
		Provider:              a.Provider.Name(),
		Model:                 sum.Model,
		Messages:              append([]llm.Message(nil), req.Messages...),
		Temperature:           req.Temperature,
		MaxTokens:             req.MaxTokens,
		EstimatedPromptTokens: est,
		SnapshotTotalTokens:   snapTotal,
		SnapshotToolTokens:    snapTools,
		Summary:               sum,
	}
	rec.Tools = make([]WireToolSummary, 0, len(sum.Tools))
	for _, t := range sum.Tools {
		rec.Tools = append(rec.Tools, WireToolSummary{
			Name:           t.Name,
			SchemaBytes:    t.SchemaBytes,
			DescriptionLen: t.DescriptionLen,
		})
	}
	a.lastWireMu.Lock()
	a.lastWire = rec
	a.lastWireMu.Unlock()
}

func (a *Agent) wireTokenEstimate() (est, total, tools int) {
	if a.Ctx == nil {
		return 0, 0, 0
	}
	snap := a.Ctx.LastSnapshot()
	if snap == nil {
		return 0, 0, 0
	}
	return snap.TotalTokens + snap.ToolTokens, snap.TotalTokens, snap.ToolTokens
}

func (a *Agent) stepForWire() int {
	if a.Ctx == nil {
		return 0
	}
	if snap := a.Ctx.LastSnapshot(); snap != nil {
		return snap.Step
	}
	return 0
}

// LastWire returns a copy-safe pointer to the last wire record (may be nil).
func (a *Agent) LastWire() *WireRecord {
	a.lastWireMu.Lock()
	defer a.lastWireMu.Unlock()
	return a.lastWire
}

func (a *Agent) emitWireRequest(req llm.ChatRequest) {
	if a.Bus == nil {
		return
	}
	est, snapTotal, snapTools := a.wireTokenEstimate()
	sum := llm.DescribeRequest(req, a.Provider.Name(), a.Provider.Model())
	data := observability.WireRequestData{
		Provider:              sum.Provider,
		Model:                 sum.Model,
		Stream:                a.Stream,
		Stage:                 "provider_payload",
		MessageCount:          sum.MessageCount,
		ToolCount:             sum.ToolCount,
		ContentBytes:          sum.ContentBytes,
		SchemaBytes:           sum.SchemaBytes,
		Temperature:           sum.Temperature,
		MaxTokens:             sum.MaxTokens,
		EstimatedPromptTokens: est,
		SnapshotTotalTokens:   snapTotal,
		SnapshotToolTokens:    snapTools,
		Messages:              make([]observability.WireMessageInfo, 0, len(sum.Messages)),
		Tools:                 make([]observability.WireToolInfo, 0, len(sum.Tools)),
	}
	for _, m := range sum.Messages {
		data.Messages = append(data.Messages, observability.WireMessageInfo{
			Index:          m.Index,
			Role:           m.Role,
			ContentLen:     m.ContentLen,
			ContentPreview: m.ContentPreview,
			ToolCallID:     m.ToolCallID,
			ToolCalls:      m.ToolCalls,
		})
	}
	for _, t := range sum.Tools {
		data.Tools = append(data.Tools, observability.WireToolInfo{
			Name:           t.Name,
			SchemaBytes:    t.SchemaBytes,
			DescriptionLen: t.DescriptionLen,
		})
	}
	step := data.SnapshotTotalTokens // placeholder avoid unused; step from snapshot
	if a.Ctx != nil {
		if snap := a.Ctx.LastSnapshot(); snap != nil {
			step = snap.Step
		}
	}
	_ = step
	a.emit(observability.EventLLMWireRequest, data)
}

// finishWireUsage records provider prompt_tokens on the last wire record.
func (a *Agent) finishWireUsage(promptTokens int) {
	a.lastWireMu.Lock()
	rec := a.lastWire
	a.lastWireMu.Unlock()
	if rec == nil || promptTokens <= 0 {
		return
	}
	rec.ProviderPromptTokens = promptTokens
	rec.PromptTokenDelta = promptTokens - rec.EstimatedPromptTokens
}

// chatWithProvider uses StreamingProvider when Stream is on; otherwise Chat.
// Returns full aggregated response even when deltas were delivered.
func (a *Agent) chatWithProvider(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, time.Duration, bool, error) {
	if !a.Stream {
		start := time.Now()
		resp, err := a.Provider.Chat(ctx, req)
		return resp, time.Since(start), false, err
	}
	sp, ok := llm.IsStreamingProvider(a.Provider)
	if !ok {
		start := time.Now()
		resp, err := a.Provider.Chat(ctx, req)
		return resp, time.Since(start), false, err
	}

	start := time.Now()
	var (
		ttft   time.Duration
		deltas int
		length int
	)
	a.emit(observability.EventLLMStreamStarted, observability.LLMRequestData{
		Provider:     a.Provider.Name(),
		Model:        a.Provider.Model(),
		MessageCount: len(req.Messages),
		Streamed:     true,
	})
	resp, err := sp.ChatStream(ctx, req, func(text string) {
		if text == "" {
			return
		}
		if deltas == 0 {
			ttft = time.Since(start)
		}
		deltas++
		length += len(text)
		a.emit(observability.EventLLMStreamDelta, observability.StreamDeltaData{
			Text:     text,
			Index:    deltas,
			TTFTMS:   ttft.Milliseconds(),
			TotalLen: length,
		})
	})
	elapsed := time.Since(start)
	if err != nil {
		return nil, elapsed, true, err
	}
	a.lastTTFT = ttft
	a.emit(observability.EventLLMStreamFinished, observability.LLMRequestData{
		Provider:       a.Provider.Name(),
		Model:          a.Provider.Model(),
		MessageCount:   len(req.Messages),
		Streamed:       true,
		DurationMS:     elapsed.Milliseconds(),
		TTFTMS:         ttft.Milliseconds(),
		Deltas:         deltas,
		OutputTokens:   resp.Usage.CompletionTokens,
		ContentPreview: truncatePreview(resp.Content, toolPreviewLen),
	})
	return resp, elapsed, true, nil
}

func (a *Agent) emitLLMFinished(req llm.ChatRequest, resp *llm.ChatResponse, elapsed time.Duration, streamed bool) {
	preview := resp.Content
	if len(preview) > toolPreviewLen {
		preview = truncatePreview(preview, toolPreviewLen)
	}
	if preview == "" && len(resp.ToolCalls) > 0 {
		preview = fmt.Sprintf("tool_calls: %d", len(resp.ToolCalls))
	}
	est, _, _ := a.wireTokenEstimate()
	providerIn := 0
	if resp != nil {
		providerIn = resp.Usage.PromptTokens
	}
	delta := 0
	if est > 0 && providerIn > 0 {
		delta = providerIn - est
	}
	a.finishWireUsage(providerIn)
	a.emit(observability.EventLLMRequestFinished, observability.LLMRequestData{
		Provider:              a.Provider.Name(),
		Model:                 a.Provider.Model(),
		MessageCount:          len(req.Messages),
		InputTokens:           resp.Usage.PromptTokens,
		OutputTokens:          resp.Usage.CompletionTokens,
		TotalTokens:           resp.Usage.TotalTokens,
		DurationMS:            elapsed.Milliseconds(),
		ContentPreview:        preview,
		Streamed:              streamed,
		TTFTMS:                a.lastTTFT.Milliseconds(),
		EstimatedPromptTokens: est,
		PromptTokenDelta:      delta,
		WireMessageCount:      len(req.Messages),
		WireToolCount:         len(req.Tools),
	})
}

func (a *Agent) emitLLMFailed(elapsed time.Duration, err error) {
	a.emit(observability.EventLLMRequestFailed, observability.LLMRequestData{
		Provider:   a.Provider.Name(),
		Model:      a.Provider.Model(),
		DurationMS: elapsed.Milliseconds(),
		Error:      err.Error(),
	})
}
