package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/manxisuo/mincode/internal/agent"
	"github.com/manxisuo/mincode/internal/config"
	"github.com/manxisuo/mincode/internal/ctxmgr"
	"github.com/manxisuo/mincode/internal/instruction"
	"github.com/manxisuo/mincode/internal/llm"
	"github.com/manxisuo/mincode/internal/memory"
	"github.com/manxisuo/mincode/internal/observability"
	"github.com/manxisuo/mincode/internal/paths"
	"github.com/manxisuo/mincode/internal/plan"
	"github.com/manxisuo/mincode/internal/repomap"
	"github.com/manxisuo/mincode/internal/session"
	"github.com/manxisuo/mincode/internal/skill"
	"github.com/manxisuo/mincode/internal/tools"
	"github.com/manxisuo/mincode/internal/webfetch"
	"github.com/manxisuo/mincode/internal/websearch"
)

// Options are runtime options from flags.
type Options struct {
	ConfigPath string
	Prompt     string // single-shot mode when non-empty
	Model      string // override
	Provider   string // override
	Workspace  string
	Continue   bool   // restore latest session for workspace
	Replay     string // session id or trace path; non-empty runs replay mode
}

// App wires config, provider, agent, observability and the REPL.
type App struct {
	cfg        config.Config
	provider   llm.Provider
	agent      *agent.Agent
	bus        *observability.Bus
	recorder   *observability.Recorder
	metrics    *observability.MetricsCollector
	sessions   *session.Store
	sessionID  string
	workspace  string
	instr      *instruction.Loader
	skills     *skill.Loader
	mem        *memory.Store
	plans      *plan.Manager
	repoMap    *repomap.Map
	lastResult *agent.Result
	out        io.Writer
	echoTools  atomic.Bool
	// streamEcho prints LLM stream deltas to out while a turn runs.
	streamEcho   atomic.Bool
	streamedText strings.Builder
}

// NewApp constructs the application from options.
func NewApp(opts Options) (*App, error) {
	// Resolve workspace first so mincode.yaml can be loaded from it.
	workspace := opts.Workspace
	if workspace == "" {
		wd, err := os.Getwd()
		if err != nil {
			workspace = "."
		} else {
			workspace = wd
		}
	}
	if abs, err := filepath.Abs(workspace); err == nil {
		workspace = abs
	}

	cfg, err := config.LoadFrom(opts.ConfigPath, workspace)
	if err != nil {
		return nil, err
	}
	if opts.Model != "" {
		cfg.Provider.Model = opts.Model
	}
	if opts.Provider != "" {
		cfg.Provider.Type = opts.Provider
	}

	provider, err := buildProvider(cfg)
	if err != nil {
		return nil, err
	}

	sessionID := time.Now().UTC().Format("20060102-150405") + "-" + observability.NewID()[:8]
	layout := paths.Resolve(workspace, cfg.Data.Location, cfg.Data.Root)
	if err := layout.EnsureDirs(); err != nil {
		return nil, err
	}
	traceDir := layout.TracesDir
	if err := config.EnsureTraceDir(traceDir); err != nil {
		return nil, err
	}
	tracePath := config.TracePath(traceDir, sessionID)
	recorder, err := observability.NewRecorder(tracePath)
	if err != nil {
		return nil, err
	}

	bus := observability.NewBus()
	metrics := observability.NewMetricsCollector()
	bus.Subscribe(func(e observability.Event) {
		if err := recorder.Record(e); err != nil {
			fmt.Fprintf(os.Stderr, "trace write error: %v\n", err)
		}
	})
	bus.Subscribe(metrics.Handle)

	ws, err := tools.NewWorkspace(workspace)
	if err != nil {
		_ = recorder.Close()
		return nil, err
	}

	memStore, err := memory.New(workspace)
	if err != nil {
		_ = recorder.Close()
		return nil, err
	}

	registry := tools.NewRegistry()
	registry.Register(&tools.ReadFile{WS: ws})
	registry.Register(&tools.ListDir{WS: ws})
	registry.Register(&tools.Glob{WS: ws})
	registry.Register(&tools.Grep{WS: ws})
	registry.Register(&tools.WriteFile{WS: ws})
	registry.Register(&tools.EditFile{WS: ws})
	registry.Register(&tools.Shell{WS: ws})
	registry.Register(&tools.WebFetch{Fetcher: webfetch.NewHTTPFetcher(0)})

	if searchProvider, err := buildWebSearch(cfg); err != nil {
		_ = recorder.Close()
		return nil, err
	} else if searchProvider != nil {
		registry.Register(&tools.WebSearch{
			Provider:   searchProvider,
			MaxResults: cfg.WebSearch.MaxResults,
			Timeout:    time.Duration(cfg.WebSearch.TimeoutSec) * time.Second,
		})
	}

	if cfg.RepoMapEnabled() {
		registry.Register(&tools.RepoMap{WS: ws, MaxTokens: cfg.Agent.RepoMapTokens})
	}

	sysPrompt := cfg.Agent.SystemPrompt + config.PlatformShellHint(runtime.GOOS)
	ag := agent.NewWithCompress(provider, registry, bus, sessionID, cfg.Agent.MaxSteps, sysPrompt, cfg.Agent.TokenBudget, cfg.Agent.CompressAt)
	if cfg.Agent.ParallelTools != nil {
		ag.ParallelTools = *cfg.Agent.ParallelTools
	}
	ag.MaxParallel = cfg.Agent.MaxParallel
	ag.Stream = cfg.StreamEnabled()
	if cfg.Agent.Reflection != nil {
		ag.Reflection = *cfg.Agent.Reflection
	}
	ag.MaxReflections = cfg.Agent.MaxReflections

	instrLoader, err := instruction.NewLoader(workspace)
	if err != nil {
		_ = recorder.Close()
		return nil, err
	}
	ag.Instr = instrLoader

	skillLoader, err := skill.NewLoader(workspace)
	if err != nil {
		_ = recorder.Close()
		return nil, err
	}
	if _, err := skillLoader.Discover(); err != nil {
		fmt.Fprintf(os.Stderr, "mincode: discover skills: %v\n", err)
	}

	sessions := session.NewStore(layout.SessionsDir)

	app := &App{
		cfg:       cfg,
		provider:  provider,
		agent:     ag,
		bus:       bus,
		recorder:  recorder,
		metrics:   metrics,
		sessions:  sessions,
		sessionID: sessionID,
		workspace: workspace,
		instr:     instrLoader,
		skills:    skillLoader,
		mem:       memStore,
		plans:     plan.NewManager(),
		out:       os.Stdout,
	}

	registry.Register(&tools.MemoryAdd{
		Store: memStore,
		OnAdded: func(entry, composed string) {
			ag.Ctx.SetMemory(composed)
			app.emit(observability.EventMemoryUpdated, observability.MemoryEventData{
				RelPath: memory.FileName,
				Bytes:   len(composed),
				Entry:   entry,
				Reason:  "agent tool memory_add",
			})
		},
	})

	if opts.Continue {
		if err := app.restoreLatestSession(); err != nil {
			fmt.Fprintf(os.Stderr, "mincode: continue: %v\n", err)
		}
	}

	ag.Approver = NewStdinApprover(app.out, ws)
	bus.Subscribe(func(e observability.Event) {
		if e.Type == observability.EventLLMStreamDelta {
			if data, ok := e.Data.(observability.StreamDeltaData); ok && data.Text != "" {
				app.streamedText.WriteString(data.Text)
				if app.streamEcho.Load() {
					fmt.Fprint(app.out, data.Text)
				}
			}
			return
		}
		if !app.echoTools.Load() {
			return
		}
		echoToolEvent(app.out, e)
	})

	app.emit(observability.EventSessionCreated, observability.SessionCreatedData{
		Workspace: workspace,
		Model:     provider.Model(),
		Provider:  provider.Name(),
	})

	app.loadRootInstructions()
	app.loadMemory()
	app.loadRepoMap()
	return app, nil
}

func buildProvider(cfg config.Config) (llm.Provider, error) {
	switch cfg.Provider.Type {
	case "fake":
		return llm.NewFakeProvider(cfg.Provider.Model,
			"This is a scripted Fake Provider response (offline mode).",
		), nil
	case "openai-compatible", "openai", "":
		return llm.NewCompatibleProvider(
			cfg.Provider.BaseURL,
			cfg.Provider.APIKey,
			cfg.Provider.Model,
			cfg.Provider.Temperature,
			cfg.Provider.MaxTokens,
			cfg.Provider.TimeoutSec,
		), nil
	default:
		return nil, fmt.Errorf("unknown provider type %q", cfg.Provider.Type)
	}
}

// buildWebSearch builds the web search backend from config, or nil when disabled.
func buildWebSearch(cfg config.Config) (websearch.Provider, error) {
	timeout := time.Duration(cfg.WebSearch.TimeoutSec) * time.Second
	switch cfg.WebSearch.Type {
	case "":
		return nil, nil
	case "fake":
		return websearch.NewFakeProvider(websearch.SearchResponse{
			Results: []websearch.Result{
				{Title: "Fake web result", URL: "https://example.com/fake", Snippet: "Offline fake web search result."},
			},
		}), nil
	case "tavily":
		if strings.TrimSpace(cfg.WebSearch.APIKey) == "" {
			return nil, fmt.Errorf("websearch type %q requires api_key (or MINCODE_WEBSEARCH_API_KEY)", cfg.WebSearch.Type)
		}
		return websearch.NewHTTPProvider(cfg.WebSearch.BaseURL, cfg.WebSearch.APIKey, timeout), nil
	case "searxng":
		if strings.TrimSpace(cfg.WebSearch.BaseURL) == "" {
			return nil, fmt.Errorf("websearch type %q requires base_url (e.g. http://localhost:8080)", cfg.WebSearch.Type)
		}
		return websearch.NewSearxNGProvider(cfg.WebSearch.BaseURL, timeout), nil
	default:
		return nil, fmt.Errorf("unknown websearch type %q", cfg.WebSearch.Type)
	}
}

// Close releases the trace file.
func (a *App) Close() error {
	if a.recorder != nil {
		return a.recorder.Close()
	}
	return nil
}

// SessionID returns the current session id.
func (a *App) SessionID() string { return a.sessionID }

// TracePath returns the current JSONL trace path.
func (a *App) TracePath() string { return a.recorder.Path() }

// MetricsSnapshot returns aggregated LLM metrics for this session.
func (a *App) MetricsSnapshot() observability.Metrics {
	if a.metrics == nil {
		return observability.Metrics{}
	}
	return a.metrics.Snapshot()
}

// ProviderInfo returns the active provider name and model.
func (a *App) ProviderInfo() (provider, model string) {
	if a.provider == nil {
		return "", ""
	}
	return a.provider.Name(), a.provider.Model()
}

// SetOutput redirects REPL/turn stdout (used by experiments).
func (a *App) SetOutput(w io.Writer) {
	if w != nil {
		a.out = w
	}
}

// RunPrompt executes a single-shot turn without entering the REPL.
func (a *App) RunPrompt(ctx context.Context, prompt string) error {
	return a.singleShot(ctx, prompt)
}

// LastResult is the agent.Result from the most recent RunPrompt/turn, if any.
func (a *App) LastResult() *agent.Result { return a.lastResult }

func (a *App) emit(typ observability.EventType, data any) {
	a.bus.Publish(observability.NewEvent(a.sessionID, 0, typ, data))
}

// loadRootInstructions loads workspace/AGENTS.md and pushes it into context.
func (a *App) loadRootInstructions() {
	if a.instr == nil {
		return
	}
	f, err := a.instr.LoadRoot()
	if err != nil {
		fmt.Fprintf(os.Stderr, "mincode: load %s: %v\n", instruction.FileName, err)
		return
	}
	if f == nil {
		return
	}
	a.emitInstructionLoaded(f)
	a.agent.Ctx.SetInstructions(a.instr.Compose())
}

func (a *App) emitInstructionLoaded(f *instruction.File) {
	a.emit(observability.EventInstructionLoaded, observability.InstructionLoadedData{
		Path:    f.Path,
		RelPath: f.RelPath,
		RelDir:  f.RelDir,
		Bytes:   len(f.Content),
	})
}

// loadMemory loads workspace/MEMORY.md into context when present.
func (a *App) loadMemory() {
	if a.mem == nil {
		return
	}
	content, existed, err := a.mem.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "mincode: load memory: %v\n", err)
		return
	}
	if !existed || content == "" {
		return
	}
	a.agent.Ctx.SetMemory(a.mem.Compose())
	a.emit(observability.EventMemoryRetrieved, observability.MemoryEventData{
		RelPath: memory.FileName,
		Bytes:   len(content),
		Entries: countMemoryBullets(content),
		Reason:  "startup",
	})
}

// loadRepoMap builds a compact repository map and pins it into context.
func (a *App) loadRepoMap() {
	if !a.cfg.RepoMapEnabled() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	m, err := repomap.Build(ctx, a.workspace, repomap.Options{MaxTokens: a.cfg.Agent.RepoMapTokens})
	if err != nil {
		fmt.Fprintf(os.Stderr, "mincode: repo map: %v\n", err)
		return
	}
	a.repoMap = m
	a.agent.Ctx.SetRepoMap(m.Text)
	a.emit(observability.EventRepoMapBuilt, observability.RepoMapData{
		Root:      m.Root,
		Subpath:   m.Subpath,
		Focus:     m.Focus,
		Files:     len(m.Files),
		Scanned:   m.Scanned,
		Skipped:   m.Skipped,
		Symbols:   countRepoMapSymbols(m),
		Tokens:    m.Tokens,
		BuildMS:   m.BuildMS,
		Truncated: m.Truncated,
		Reason:    "startup",
	})
}

func countRepoMapSymbols(m *repomap.Map) int {
	n := 0
	for _, f := range m.Files {
		n += len(f.Symbols)
	}
	return n
}

func countMemoryBullets(content string) int {
	n := 0
	for _, line := range strings.Split(content, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "- ") {
			n++
		}
	}
	return n
}

// Run starts the app: replay / single-shot / continue+REPL / REPL.
func (a *App) Run(ctx context.Context, opts Options) error {
	if opts.Replay != "" {
		return a.runReplay(opts.Replay)
	}
	// Single-shot: Ctrl+C cancels the one request, then process exits.
	if opts.Prompt != "" {
		ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
		defer stop()
		return a.singleShot(ctx, opts.Prompt)
	}
	// REPL: SIGINT must cancel only the in-flight turn, not the whole session.
	return a.repl(ctx)
}

// beginTurn returns a context for one REPL turn/plan execution.
// The REPL installs a process-wide SIGINT handler; this only creates a
// cancellable child context so Ctrl+C can abort the in-flight turn.
func beginTurn(parent context.Context) (context.Context, context.CancelFunc) {
	turnCtx, cancel := context.WithCancel(parent)
	return turnCtx, cancel
}

// turnGuard tracks whether a turn is in flight so SIGINT can either cancel it
// or (when idle) just refresh the prompt without exiting the process.
type turnGuard struct {
	mu     sync.Mutex
	cancel context.CancelFunc
}

func (g *turnGuard) begin(parent context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(parent)
	g.mu.Lock()
	g.cancel = cancel
	g.mu.Unlock()
	stop := func() {
		g.mu.Lock()
		if g.cancel != nil {
			g.cancel()
			g.cancel = nil
		}
		g.mu.Unlock()
	}
	return ctx, stop
}

func (g *turnGuard) active() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.cancel != nil
}

func (g *turnGuard) cancelActive() {
	g.mu.Lock()
	if g.cancel != nil {
		g.cancel()
	}
	g.mu.Unlock()
}

func (a *App) restoreLatestSession() error {
	rec, err := a.sessions.Latest(a.workspace)
	if err != nil {
		return err
	}
	entries := make([]ctxmgr.ExportedEntry, 0, len(rec.Entries))
	for _, e := range rec.Entries {
		entries = append(entries, ctxmgr.ExportedEntry{
			Msg: llm.Message{
				Role:       llm.Role(e.Role),
				Content:    e.Content,
				ToolCalls:  e.ToolCalls,
				ToolCallID: e.ToolCallID,
			},
			Source: e.Source,
			Tokens: e.Tokens,
		})
	}
	a.agent.Ctx.RestoreEntries(entries)
	fmt.Fprintf(a.out, "%s session %s  (%d messages, updated %s)\n",
		green("restored"), rec.ID, len(entries),
		rec.UpdatedAt.Local().Format("2006-01-02 15:04"))
	return nil
}

func (a *App) saveSession() {
	if a.sessions == nil {
		return
	}
	entries := a.agent.Ctx.ExportEntries()
	rec := &session.Record{
		ID:           a.sessionID,
		Workspace:    a.workspace,
		Provider:     a.provider.Name(),
		Model:        a.provider.Model(),
		SystemPrompt: a.agent.Ctx.System(),
		CreatedAt:    time.Now().UTC(),
		Entries:      make([]session.Entry, 0, len(entries)),
	}
	for _, e := range entries {
		rec.Entries = append(rec.Entries, session.Entry{
			Role:       string(e.Msg.Role),
			Content:    e.Msg.Content,
			ToolCalls:  e.Msg.ToolCalls,
			ToolCallID: e.Msg.ToolCallID,
			Source:     e.Source,
			Tokens:     e.Tokens,
		})
	}
	rec.Turns = countUserTurns(rec.Entries)
	if err := a.sessions.Save(rec); err != nil {
		fmt.Fprintf(a.out, "%s save session: %v\n", yellow("warn"), err)
	}
}

func countUserTurns(entries []session.Entry) int {
	n := 0
	for _, e := range entries {
		if e.Role == "user" {
			n++
		}
	}
	return n
}

func (a *App) singleShot(ctx context.Context, prompt string) error {
	a.emit(observability.EventAgentStarted, observability.AgentLifecycleData{Reason: "single-shot"})
	a.streamedText.Reset()
	a.streamEcho.Store(true)
	res, err := a.agent.Run(ctx, prompt)
	a.streamEcho.Store(false)
	a.lastResult = res
	a.saveSession()
	if err != nil {
		a.emit(observability.EventAgentFailed, observability.AgentLifecycleData{Reason: err.Error()})
		return err
	}
	if a.streamedText.Len() > 0 {
		fmt.Fprintln(a.out)
	} else if res.Final != "" {
		fmt.Fprintln(a.out, res.Final)
	}
	a.emit(observability.EventAgentFinished, observability.AgentLifecycleData{
		Reason: fmt.Sprintf("single-shot complete steps=%d tools=%d", res.Steps, res.ToolCalls),
	})
	return nil
}

func (a *App) repl(ctx context.Context) error {
	fmt.Fprintf(a.out, "%s  %s\n", bold(cyan("Min Code Agent")), bannerLine("session", a.sessionID))
	fmt.Fprintf(a.out, "%s  %s  %s\n", bannerLine("provider", a.provider.Name()), bannerLine("model", a.provider.Model()), bannerLine("workspace", a.workspace))
	fmt.Fprintf(a.out, "%s\n", bannerLine("tools", strings.Join(a.toolNames(), ", ")))
	fmt.Fprintf(a.out, "%s\n", bannerLine("trace", a.recorder.Path()))
	if n := a.instr.Count(); n > 0 {
		fmt.Fprintf(a.out, "%s\n", bannerLine("instructions", fmt.Sprintf("%d AGENTS.md loaded", n)))
	}
	if n := len(a.skills.Available()); n > 0 {
		fmt.Fprintf(a.out, "%s\n", bannerLine("skills", fmt.Sprintf("%d available (use /skills)", n)))
	}
	if a.mem != nil && a.mem.Exists() {
		fmt.Fprintf(a.out, "%s\n", bannerLine("memory", memory.FileName+" loaded"))
	}
	if a.repoMap != nil {
		fmt.Fprintf(a.out, "%s\n", bannerLine("repo map", fmt.Sprintf("%d files, ~%d tokens", len(a.repoMap.Files), a.repoMap.Tokens)))
	}
	fmt.Fprintf(a.out, "Type a message, or %s for commands.\n\n", bold("/help"))

	a.emit(observability.EventAgentStarted, observability.AgentLifecycleData{Reason: "repl"})

	// One SIGINT handler for the whole REPL: cancel a running turn, or stay
	// alive at the prompt (do not exit the process).
	var guard turnGuard
	var idleInterrupt atomic.Bool
	sigCh := make(chan os.Signal, 8)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigCh)
	go func() {
		for range sigCh {
			if guard.active() {
				guard.cancelActive()
				continue
			}
			// Idle at the prompt: mark interrupt so a failed/aborted Scan
			// does not look like EOF (Windows often returns Scan=false + nil err).
			idleInterrupt.Store(true)
			fmt.Fprintf(a.out, "\n^C\n")
		}
	}()

	in := newStdinScanner()

	for {
		fmt.Fprint(a.out, promptString())
		idleInterrupt.Store(false)
		if !in.Scan() {
			// Windows may deliver the console interrupt to the read before the
			// signal goroutine sets the flag — wait briefly and re-check.
			time.Sleep(20 * time.Millisecond)
			if idleInterrupt.Load() {
				in = newStdinScanner()
				continue
			}
			if err := in.Err(); err != nil && !errors.Is(err, os.ErrClosed) {
				fmt.Fprintf(a.out, "\n")
				in = newStdinScanner()
				continue
			}
			break // real EOF (Ctrl+Z / Ctrl+D)
		}
		line := strings.TrimSpace(in.Text())
		if line == "" {
			continue
		}

		if strings.HasPrefix(line, "/") {
			turnCtx, stopTurn := guard.begin(ctx)
			quit := a.handleCommand(turnCtx, line)
			stopTurn()
			if quit {
				break
			}
			continue
		}

		turnCtx, stopTurn := guard.begin(ctx)
		err := a.runTurn(turnCtx, line)
		stopTurn()
		a.reportTurnError(err)
		a.saveSession()
	}

	a.emit(observability.EventAgentFinished, observability.AgentLifecycleData{Reason: "repl exit"})
	return nil
}

func newStdinScanner() *bufio.Scanner {
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	return in
}

func (a *App) toolNames() []string {
	return []string{"read_file", "list_dir", "glob", "grep", "repo_map", "write_file", "edit_file", "shell", "memory_add"}
}

// reportTurnError prints a user-facing message for a failed/cancelled turn.
func (a *App) reportTurnError(err error) {
	if err == nil {
		return
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		fmt.Fprintf(a.out, "%s cancelled — session still alive, try again\n", yellow("interrupted"))
		return
	}
	fmt.Fprintf(a.out, "error: %v\n", err)
}

// runTurn executes one user turn through the agent and prints tool + final output.
func (a *App) runTurn(ctx context.Context, userText string) error {
	a.echoTools.Store(true)
	a.streamedText.Reset()
	a.streamEcho.Store(true)
	defer func() {
		a.echoTools.Store(false)
		a.streamEcho.Store(false)
	}()

	res, err := a.agent.Run(ctx, userText)
	if err != nil {
		return err
	}
	if a.streamedText.Len() > 0 {
		// Deltas already printed; just close the line.
		fmt.Fprintln(a.out)
		if res.Final != "" && !strings.HasSuffix(strings.TrimRight(a.streamedText.String(), "\n"), strings.TrimSpace(res.Final)) {
			fmt.Fprintln(a.out, res.Final)
			fmt.Fprintln(a.out)
		}
	} else if res.Final != "" {
		fmt.Fprintln(a.out)
		fmt.Fprintln(a.out, res.Final)
		fmt.Fprintln(a.out)
	} else {
		fmt.Fprintln(a.out)
	}
	// Opt-in: propose one durable memory fact after a successful turn.
	a.maybeExtractMemory(ctx, userText, res.Final)
	return nil
}

func echoToolEvent(w io.Writer, e observability.Event) {
	switch e.Type {
	case observability.EventToolStarted:
		if data, ok := toolData(e.Data); ok {
			mark := ""
			if data.Parallel {
				mark = gray("∥ ")
			}
			fmt.Fprintf(w, "  %s %s%s %s\n", cyan("→"), mark, bold(data.Tool), gray(compactJSON(data.Arguments)))
		}
	case observability.EventToolBatchStarted:
		if data, ok := batchData(e.Data); ok {
			fmt.Fprintf(w, "  %s %s size=%d workers=%d %s\n",
				cyan("⇉"), bold("parallel batch"), data.Size, data.MaxWorkers, gray(fmt.Sprint(data.Tools)))
		}
	case observability.EventToolBatchFinished:
		if data, ok := batchData(e.Data); ok {
			fmt.Fprintf(w, "  %s %s ok=%d err=%d %s\n",
				dim("⇇"), bold("batch done"), data.Succeeded, data.Failed,
				gray((time.Duration(data.DurationMS) * time.Millisecond).String()))
		}
	case observability.EventToolFinished, observability.EventToolFailed:
		if data, ok := toolData(e.Data); ok {
			status := green("ok")
			if data.IsError || e.Type == observability.EventToolFailed {
				status = red("error")
			}
			dur := time.Duration(data.DurationMS) * time.Millisecond
			fmt.Fprintf(w, "  %s %s %s %s\n",
				dim("←"),
				status,
				bold(data.Tool),
				gray(fmt.Sprintf("%dB %s", data.ResultSize, dur.Round(time.Millisecond))),
			)
		}
	case observability.EventFileChanged:
		diff := ""
		switch d := e.Data.(type) {
		case observability.FileChangedData:
			diff = d.Diff
		case map[string]any:
			diff, _ = d["diff"].(string)
		}
		if diff != "" {
			fmt.Fprintf(w, "\n%s\n", bold("Diff:"))
			fmt.Fprint(w, colorizeDiff(diff))
			fmt.Fprintln(w)
		}
	}
}

func toolData(v any) (observability.ToolEventData, bool) {
	switch d := v.(type) {
	case observability.ToolEventData:
		return d, true
	case map[string]any:
		out := observability.ToolEventData{}
		if s, ok := d["tool"].(string); ok {
			out.Tool = s
		}
		if s, ok := d["arguments"].(string); ok {
			out.Arguments = s
		}
		if s, ok := d["error"].(string); ok {
			out.Error = s
		}
		if b, ok := d["is_error"].(bool); ok {
			out.IsError = b
		}
		if b, ok := d["parallel"].(bool); ok {
			out.Parallel = b
		}
		if s, ok := d["call_id"].(string); ok {
			out.CallID = s
		}
		out.Index = intFromAny(d["index"])
		out.ResultSize = intFromAny(d["result_size"])
		out.DurationMS = int64(intFromAny(d["duration_ms"]))
		return out, true
	}
	return observability.ToolEventData{}, false
}

func batchData(v any) (observability.ToolBatchData, bool) {
	switch d := v.(type) {
	case observability.ToolBatchData:
		return d, true
	case map[string]any:
		out := observability.ToolBatchData{}
		out.Size = intFromAny(d["size"])
		out.Succeeded = intFromAny(d["succeeded"])
		out.Failed = intFromAny(d["failed"])
		out.MaxWorkers = intFromAny(d["max_workers"])
		out.DurationMS = int64(intFromAny(d["duration_ms"]))
		if b, ok := d["parallel"].(bool); ok {
			out.Parallel = b
		}
		if raw, ok := d["tools"].([]any); ok {
			for _, t := range raw {
				out.Tools = append(out.Tools, fmt.Sprint(t))
			}
		}
		return out, true
	}
	return observability.ToolBatchData{}, false
}

func intFromAny(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	}
	return 0
}

func compactJSON(s string) string {
	if len(s) > 120 {
		s = truncateUTF8(s, 120)
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, []byte(s), "", " "); err != nil {
		return s
	}
	out := strings.ReplaceAll(buf.String(), "\n", " ")
	if len(out) > 120 {
		return truncateUTF8(out, 120)
	}
	return out
}

// truncateUTF8 cuts s to at most max bytes on a rune boundary and appends "...".
func truncateUTF8(s string, max int) string {
	if max <= 0 {
		return ""
	}
	if len(s) <= max {
		return s
	}
	end := max
	for end > 0 && !utf8.RuneStart(s[end]) {
		end--
	}
	return s[:end] + "..."
}

func (a *App) handleCommand(ctx context.Context, line string) (quit bool) {
	fields := strings.Fields(strings.TrimSpace(line))
	name := fields[0]

	switch name {
	case "/help", "/h", "/?":
		fmt.Fprint(a.out, `Commands:
  /help              show this help
  /timeline          show agent execution timeline
  /context           show last context snapshot (what the model saw)
  /instructions      show loaded AGENTS.md project instructions
  /skills            list available skills
  /skill <name>      activate a skill (or /skill -<name> to deactivate)
  /memory            show project memory (MEMORY.md)
  /memory add <fact> append a durable fact to MEMORY.md
  /repomap           show the repository map injected into context
  /export [path]     export conversation as Markdown (default: exports/<session-id>.md)
  /plan <goal>       draft a plan for a task
  /plan              show current plan
  /plan approve      run the approved plan step by step (Ctrl+C to stop mid-run)
  /plan auto <goal>  plan-and-execute: auto-draft, approve, run with failure recovery
  /plan reject       discard the draft plan
  /plan cancel       mark an approved (not yet finished) plan cancelled
  /trace [n]         show last n raw trace events (default 30)
  /metrics           show session token/time metrics
  /clear             clear conversation history
  /exit, /quit       leave the REPL

Trace file:
`)
		fmt.Fprintf(a.out, "  %s\n", a.recorder.Path())
	case "/timeline":
		a.printTimeline()
	case "/context":
		a.printContextSnapshot()
	case "/instructions":
		a.printInstructions()
	case "/skills":
		a.printSkills()
	case "/skill":
		a.handleSkillCommand(fields[1:])
	case "/memory":
		a.handleMemoryCommand(fields[1:])
	case "/repomap":
		a.printRepoMap()
	case "/export":
		a.handleExportCommand(fields[1:])
	case "/plan":
		a.handlePlanCommand(ctx, fields[1:])
	case "/trace":
		n := 30
		if len(fields) > 1 {
			if v, err := atoi(fields[1]); err == nil && v > 0 {
				n = v
			}
		}
		a.printTrace(n)
	case "/metrics":
		fmt.Fprintln(a.out)
		fmt.Fprint(a.out, formatMetrics(a.metrics.Snapshot()))
		fmt.Fprintln(a.out)
	case "/clear":
		a.agent.ClearConversation()
		fmt.Fprintln(a.out, "history cleared (system prompt kept)")
	case "/exit", "/quit":
		return true
	default:
		fmt.Fprintf(a.out, "unknown command %s (try /help)\n", name)
	}
	return false
}

func formatMetrics(m observability.Metrics) string {
	var b strings.Builder
	b.WriteString(bold(cyan("Session Metrics")) + "\n\n")
	b.WriteString(metricRow("LLM Calls", green(fmt.Sprint(m.LLMCalls)), ""))
	errVal := green(fmt.Sprint(m.Errors))
	if m.Errors > 0 {
		errVal = red(fmt.Sprint(m.Errors))
	}
	b.WriteString(metricRow("Errors", errVal, ""))
	b.WriteString(metricRow("Input Tokens", yellow(formatInt(m.InputTokens)), ""))
	b.WriteString(metricRow("Output Tokens", yellow(formatInt(m.OutputTokens)), ""))
	b.WriteString(metricRow("Total Tokens", bold(yellow(formatInt(m.TotalTokens))), ""))
	b.WriteString(metricRow("LLM Time", blue(m.LLMDuration.Round(time.Millisecond).String()), ""))
	return b.String()
}

func metricRow(label, value, unit string) string {
	line := padCol(cyan(label), 18) + value
	if unit != "" {
		line += " " + gray(unit)
	}
	return line + "\n"
}

func formatInt(n int) string {
	s := fmt.Sprintf("%d", n)
	if n < 1000 {
		return s
	}
	var out []byte
	for i, ch := range []byte(s) {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, ch)
	}
	return string(out)
}

func (a *App) printContextSnapshot() {
	snap := a.agent.Ctx.LastSnapshot()
	if snap == nil {
		fmt.Fprint(a.out, "\n"+yellow("no context snapshot yet")+" — run a prompt first\n\n")
		return
	}
	fmt.Fprintln(a.out)
	fmt.Fprint(a.out, formatSnapshot(*snap))
	fmt.Fprintln(a.out)
}

// printInstructions shows every AGENTS.md currently loaded into context.
func (a *App) printInstructions() {
	fmt.Fprintln(a.out)
	if a.instr == nil || a.instr.Count() == 0 {
		fmt.Fprint(a.out, yellow("no project instructions loaded")+" — add "+bold(instruction.FileName)+" under the workspace\n\n")
		return
	}
	files := a.instr.Files()
	fmt.Fprintf(a.out, "%s  %s\n\n", bold("Project Instructions"),
		gray(fmt.Sprintf("%d file(s)  workspace=%s", len(files), a.workspace)))
	for _, f := range files {
		src := green(f.RelPath)
		if f.RelDir == "." {
			src = bold(green(f.RelPath))
		}
		fmt.Fprintf(a.out, "%s  %s\n", padCol(src, 40), gray(fmt.Sprintf("%d bytes", len(f.Content))))
		// Show a short preview of each file body.
		preview := f.Content
		if lines := strings.Split(preview, "\n"); len(lines) > 6 {
			preview = strings.Join(lines[:6], "\n") + "\n…"
		}
		for _, line := range strings.Split(preview, "\n") {
			fmt.Fprintf(a.out, "  %s\n", dim(line))
		}
		fmt.Fprintln(a.out)
	}
	composed := a.agent.Ctx.Instructions()
	if composed != "" {
		// Token estimate lives in the next context build; show char size here.
		fmt.Fprintf(a.out, "%s  %s\n",
			padCol(cyan("Composed"), 14),
			gray(fmt.Sprintf("%d chars injected as system context", len(composed))))
	}
	fmt.Fprintln(a.out)
}

// printSkills lists discovered skills and which ones are active.
func (a *App) printSkills() {
	fmt.Fprintln(a.out)
	if a.skills == nil {
		fmt.Fprint(a.out, yellow("skills unavailable")+"\n\n")
		return
	}
	available := a.skills.Available()
	if len(available) == 0 {
		fmt.Fprintf(a.out, "%s — put them in %s\n\n",
			yellow("no skills found"), bold(filepath.ToSlash(filepath.Join(skill.DirName, "<name>", skill.FileName))))
		return
	}
	fmt.Fprintf(a.out, "%s  %s\n\n", bold("Skills"),
		gray(fmt.Sprintf("%d available  dir=%s", len(available), skill.DirName+"/")))
	for _, s := range available {
		mark := dim("○")
		state := dim("inactive")
		if a.skills.IsActive(s.Name) {
			mark = green("●")
			state = green("active")
		}
		fmt.Fprintf(a.out, "%s %s  %s\n", mark, padCol(bold(s.Name), 20), state)
		if s.Summary != "" {
			fmt.Fprintf(a.out, "    %s\n", dim(truncateStr(s.Summary, 72)))
		}
		fmt.Fprintf(a.out, "    %s\n", gray(s.RelPath))
	}
	if n := a.skills.CountActive(); n > 0 {
		fmt.Fprintf(a.out, "\n%s  %s\n",
			padCol(cyan("In context"), 14),
			gray(fmt.Sprintf("%d skill(s), %d chars", n, len(a.agent.Ctx.Skills()))))
	}
	fmt.Fprintf(a.out, "%s\n", gray("activate: /skill <name>    deactivate: /skill -<name>"))
	fmt.Fprintln(a.out)
}

// handleSkillCommand activates or deactivates a skill: /skill name | /skill -name
func (a *App) handleSkillCommand(args []string) {
	if len(args) == 0 {
		a.printSkills()
		return
	}
	arg := args[0]
	if strings.HasPrefix(arg, "-") {
		name := strings.TrimPrefix(arg, "-")
		if name == "" {
			fmt.Fprintf(a.out, "%s usage: /skill -<name>\n", yellow("usage:"))
			return
		}
		if !a.skills.Deactivate(name) {
			fmt.Fprintf(a.out, "%s skill %s is not active\n", yellow("warn"), bold(name))
			return
		}
		a.syncSkillsToContext()
		a.emit(observability.EventSkillUnloaded, observability.SkillEventData{
			Name:   name,
			Reason: "user command",
		})
		fmt.Fprintf(a.out, "%s deactivated %s\n", green("ok"), bold(name))
		return
	}

	s, newly, err := a.skills.Activate(arg)
	if err != nil {
		fmt.Fprintf(a.out, "%s %v\n", red("error:"), err)
		return
	}
	a.syncSkillsToContext()
	if newly {
		a.emit(observability.EventSkillLoaded, observability.SkillEventData{
			Name:    s.Name,
			RelPath: s.RelPath,
			Bytes:   len(s.Content),
			Summary: s.Summary,
			Reason:  "user command",
		})
		fmt.Fprintf(a.out, "%s activated %s  (%s)\n", green("ok"), bold(s.Name), gray(s.RelPath))
	} else {
		fmt.Fprintf(a.out, "%s skill %s is already active\n", yellow("info"), bold(s.Name))
	}
}

func (a *App) syncSkillsToContext() {
	if a.skills == nil {
		return
	}
	a.agent.Ctx.SetSkills(a.skills.Compose())
}

// handleMemoryCommand: /memory | /memory add <fact>
func (a *App) handleMemoryCommand(args []string) {
	if a.mem == nil {
		fmt.Fprintf(a.out, "%s memory unavailable\n", yellow("warn"))
		return
	}
	if len(args) == 0 {
		a.printMemory()
		return
	}
	if args[0] != "add" {
		fmt.Fprintf(a.out, "%s usage: /memory  or  /memory add <fact>\n", yellow("usage:"))
		return
	}
	fact := strings.TrimSpace(strings.Join(args[1:], " "))
	if fact == "" {
		fmt.Fprintf(a.out, "%s usage: /memory add <fact>\n", yellow("usage:"))
		return
	}
	content, err := a.mem.Add(fact)
	if err != nil {
		fmt.Fprintf(a.out, "%s %v\n", red("error:"), err)
		return
	}
	a.agent.Ctx.SetMemory(a.mem.Compose())
	a.emit(observability.EventMemoryUpdated, observability.MemoryEventData{
		RelPath: memory.FileName,
		Bytes:   len(content),
		Entries: countMemoryBullets(content),
		Entry:   fact,
		Reason:  "user command",
	})
	fmt.Fprintf(a.out, "%s memory updated (%s)\n", green("ok"), memory.FileName)
}

func (a *App) printMemory() {
	fmt.Fprintln(a.out)
	content := a.mem.Content()
	if content == "" {
		fmt.Fprintf(a.out, "%s — add one with %s\n\n",
			yellow("no project memory yet"), bold("/memory add <fact>"))
		return
	}
	fmt.Fprintf(a.out, "%s  %s\n\n", bold("Project Memory"), gray(a.mem.Path()))
	fmt.Fprintln(a.out, content)
	fmt.Fprintln(a.out)
}

// printRepoMap shows the repository map currently pinned into context.
func (a *App) printRepoMap() {
	fmt.Fprintln(a.out)
	if a.repoMap == nil {
		fmt.Fprintf(a.out, "%s — enable with %s (agent.repo_map)\n\n",
			yellow("repository map disabled or not built"), bold("true"))
		return
	}
	m := a.repoMap
	trunc := ""
	if m.Truncated {
		trunc = "  " + yellow("(truncated)")
	}
	fmt.Fprintf(a.out, "%s  %s%s\n\n", bold("Repository Map"),
		gray(fmt.Sprintf("%d files, %d symbols, ~%d tokens, scanned %d, %dms",
			len(m.Files), countRepoMapSymbols(m), m.Tokens, m.Scanned, m.BuildMS)), trunc)
	fmt.Fprintln(a.out, m.Text)
	fmt.Fprintln(a.out)
}

// handleExportCommand: /export [path]
func (a *App) handleExportCommand(args []string) {
	path := defaultExportPath(a.workspace, a.sessionID)
	if len(args) > 0 && strings.TrimSpace(args[0]) != "" {
		p := strings.TrimSpace(args[0])
		if filepath.IsAbs(p) {
			path = p
		} else {
			// Relative paths resolve inside the workspace.
			abs, err := filepath.Abs(filepath.Join(a.workspace, p))
			if err != nil {
				fmt.Fprintf(a.out, "%s %v\n", red("error:"), err)
				return
			}
			path = abs
		}
	}

	meta := sessionExportMeta{
		SessionID: a.sessionID,
		Workspace: a.workspace,
		Provider:  a.provider.Name(),
		Model:     a.provider.Model(),
		CreatedAt: time.Now().UTC(),
	}
	md := renderSessionMarkdown(meta, a.agent.Ctx.ExportEntries())
	if err := writeExportFile(path, md); err != nil {
		fmt.Fprintf(a.out, "%s export: %v\n", red("error:"), err)
		return
	}
	rel := a.instrRel(path)
	fmt.Fprintf(a.out, "%s exported %s  (%d bytes)\n", green("ok"), bold(rel), len(md))
}

func (a *App) instrRel(path string) string {
	rel, err := filepath.Rel(a.workspace, path)
	if err != nil {
		return path
	}
	return filepath.ToSlash(rel)
}

// handlePlanCommand: /plan [goal | approve | reject | cancel | show]
func (a *App) handlePlanCommand(ctx context.Context, args []string) {
	if len(args) == 0 {
		a.printCurrentPlan()
		return
	}
	switch args[0] {
	case "approve":
		a.approveAndRunPlan(ctx)
	case "reject":
		a.rejectPlan()
	case "cancel":
		a.cancelPlan()
	case "show", "status":
		a.printCurrentPlan()
	case "auto":
		goal := strings.TrimSpace(strings.Join(args[1:], " "))
		a.handleAutoCommand(ctx, goal)
	default:
		goal := strings.TrimSpace(strings.Join(args, " "))
		a.draftPlan(ctx, goal)
	}
}

func (a *App) printCurrentPlan() {
	fmt.Fprintln(a.out)
	p := a.plans.Current()
	if p == nil {
		fmt.Fprint(a.out, yellow("no active plan")+" — use "+bold("/plan <goal>")+" to draft one\n\n")
		return
	}
	fmt.Fprint(a.out, p.Format())
	switch p.Status {
	case plan.StatusDraft:
		fmt.Fprintf(a.out, "\n%s  %s  %s\n",
			gray("next:"), bold("/plan approve"), gray("or /plan reject"))
	case plan.StatusApproved, plan.StatusRunning:
		fmt.Fprintf(a.out, "\n%s  %s\n", gray("status:"), "in progress — press Ctrl+C to interrupt the current step")
	}
	fmt.Fprintln(a.out)
}

// draftPlan asks the LLM for a numbered step list and stores a draft plan.
func (a *App) draftPlan(ctx context.Context, goal string) {
	if goal == "" {
		fmt.Fprintf(a.out, "%s usage: /plan <goal>\n", yellow("usage:"))
		return
	}
	fmt.Fprintf(a.out, "%s drafting plan for: %s\n", cyan("plan"), bold(goal))

	req := llm.ChatRequest{
		Messages: []llm.Message{
			{
				Role: llm.RoleSystem,
				Content: `You break a coding task into a short numbered execution plan.
Output ONLY a numbered list (3-8 steps). Each step is one concrete action.
No prose before or after the list. No nested sub-steps.
Example:
1. Read the main router file
2. Add a /health handler
3. Write unit tests for /health
4. Run go test ./...`,
			},
			{Role: llm.RoleUser, Content: goal},
		},
	}

	a.emit(observability.EventPlanCreated, observability.PlanEventData{
		Goal:   goal,
		Status: string(plan.StatusDraft),
		Reason: "generating",
	})

	resp, err := a.provider.Chat(ctx, req)
	if err != nil {
		fmt.Fprintf(a.out, "%s plan generation failed: %v\n", red("error:"), err)
		return
	}
	titles := plan.ParseStepList(resp.Content)
	if len(titles) == 0 {
		fmt.Fprintf(a.out, "%s model returned no parseable steps:\n%s\n", red("error:"), truncateStr(resp.Content, 300))
		return
	}
	if len(titles) > 8 {
		titles = titles[:8]
	}

	id := "plan-" + time.Now().UTC().Format("150405")
	p := plan.NewPlan(id, goal, titles)
	a.plans.SetCurrent(p)

	a.emit(observability.EventPlanCreated, observability.PlanEventData{
		PlanID:    p.ID,
		Goal:      goal,
		Status:    string(p.Status),
		StepCount: len(p.Steps),
	})

	fmt.Fprintln(a.out)
	fmt.Fprint(a.out, p.Format())
	fmt.Fprintf(a.out, "\n%s  %s   %s\n",
		gray("next:"), bold("/plan approve"), gray("to execute   |   /plan reject   to discard"))
	fmt.Fprintln(a.out)
}

func (a *App) rejectPlan() {
	p := a.plans.Current()
	if p == nil {
		fmt.Fprintf(a.out, "%s no active plan\n", yellow("warn"))
		return
	}
	if err := p.Reject(); err != nil {
		fmt.Fprintf(a.out, "%s %v\n", red("error:"), err)
		return
	}
	a.emit(observability.EventPlanRejected, observability.PlanEventData{
		PlanID: p.ID,
		Goal:   p.Goal,
		Status: string(p.Status),
	})
	a.plans.Clear()
	fmt.Fprintf(a.out, "%s plan rejected\n", green("ok"))
}

func (a *App) cancelPlan() {
	p := a.plans.Current()
	if p == nil {
		fmt.Fprintf(a.out, "%s no active plan\n", yellow("warn"))
		return
	}
	before := p.Status
	p.Cancel()
	if p.Status == before {
		fmt.Fprintf(a.out, "%s plan is already %s — nothing to cancel\n", yellow("info"), before)
		return
	}
	a.emit(observability.EventPlanCancelled, observability.PlanEventData{
		PlanID: p.ID,
		Goal:   p.Goal,
		Status: string(p.Status),
	})
	fmt.Fprintf(a.out, "%s plan cancelled (%s)\n", yellow("ok"), p.Status)
}

// approveAndRunPlan approves the draft and executes steps sequentially.
func (a *App) approveAndRunPlan(ctx context.Context) {
	p := a.plans.Current()
	if p == nil {
		fmt.Fprintf(a.out, "%s no active plan — draft one with /plan <goal>\n", yellow("warn"))
		return
	}
	if err := p.Approve(); err != nil {
		fmt.Fprintf(a.out, "%s %v\n", red("error:"), err)
		return
	}
	a.emit(observability.EventPlanApproved, observability.PlanEventData{
		PlanID:    p.ID,
		Goal:      p.Goal,
		Status:    string(p.Status),
		StepCount: len(p.Steps),
	})
	fmt.Fprintf(a.out, "%s executing plan %s (%d steps)\n\n", green("ok"), bold(p.ID), len(p.Steps))

	a.echoTools.Store(true)
	defer a.echoTools.Store(false)

	// Plan steps share agent.MaxSteps (config, default 30). Budget is not lowered:
	// thrashing is deterred by the step prompt, not by starving legitimate work.
	planStepBudget := a.agent.MaxSteps

	var stepNotes []string
	var lastFullResult string

	for i := range p.Steps {
		if err := ctx.Err(); err != nil {
			p.Cancel()
			fmt.Fprintf(a.out, "\n%s interrupted before step %d — session still alive\n", yellow("plan"), i+1)
			return
		}
		if p.Status == plan.StatusCancelled {
			fmt.Fprintf(a.out, "\n%s stopped before step %d\n", yellow("plan"), i+1)
			return
		}

		step := p.Steps[i]
		if err := p.StartStep(step.Index); err != nil {
			fmt.Fprintf(a.out, "%s %v\n", red("error:"), err)
			return
		}
		a.emit(observability.EventPlanStepStarted, observability.PlanEventData{
			PlanID:    p.ID,
			StepIndex: step.Index,
			StepTitle: step.Title,
			StepCount: len(p.Steps),
			DoneCount: i,
		})
		fmt.Fprintf(a.out, "%s step %d/%d %s\n",
			cyan("→"), step.Index, len(p.Steps), bold(step.Title))

		prompt := buildPlanStepPrompt(step.Index, len(p.Steps), p.Goal, step.Title, stepNotes)

		res, err := a.agent.Run(ctx, prompt)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				_ = p.CancelStep(step.Index, "cancelled")
				a.emit(observability.EventPlanStepFailed, observability.PlanEventData{
					PlanID:    p.ID,
					StepIndex: step.Index,
					StepTitle: step.Title,
					Error:     "cancelled",
					Status:    string(plan.StatusCancelled),
				})
				fmt.Fprintf(a.out, "\n%s interrupted at step %d — session still alive\n", yellow("plan"), step.Index)
				return
			}
			if errors.Is(err, agent.MaxStepsExceeded) {
				err = fmt.Errorf("step budget exceeded (%d tool/LLM turns) — step too broad or model thrashing; try a smaller goal or raise agent.max_steps", planStepBudget)
			}
			_ = p.FailStep(step.Index, err.Error())
			done, total := p.Progress()
			a.emit(observability.EventPlanStepFailed, observability.PlanEventData{
				PlanID:    p.ID,
				StepIndex: step.Index,
				StepTitle: step.Title,
				Error:     err.Error(),
				DoneCount: done,
				StepCount: total,
			})
			fmt.Fprintf(a.out, "%s step %d failed: %v\n", red("✗"), step.Index, err)
			return
		}

		full := strings.TrimSpace(res.Final)
		summary := full
		if summary == "" {
			summary = "ok"
		}
		if len(summary) > 120 {
			summary = truncateStr(summary, 120)
		}
		_ = p.CompleteStep(step.Index, summary)
		stepNotes = append(stepNotes, fmt.Sprintf("- Step %d (%s): %s", step.Index, step.Title, summary))
		done, total := p.Progress()
		a.emit(observability.EventPlanStepFinished, observability.PlanEventData{
			PlanID:    p.ID,
			StepIndex: step.Index,
			StepTitle: step.Title,
			Result:    summary,
			DoneCount: done,
			StepCount: total,
		})
		// Print the step's full answer (not the truncated plan-panel summary).
		if full != "" {
			fmt.Fprintln(a.out)
			fmt.Fprintln(a.out, full)
			fmt.Fprintln(a.out)
		}
		fmt.Fprintf(a.out, "%s step %d done\n\n", green("✓"), step.Index)
		lastFullResult = full
	}

	p.Finish()
	done, total := p.Progress()
	a.emit(observability.EventPlanFinished, observability.PlanEventData{
		PlanID:    p.ID,
		Goal:      p.Goal,
		Status:    string(p.Status),
		DoneCount: done,
		StepCount: total,
	})
	fmt.Fprintln(a.out)
	fmt.Fprint(a.out, p.Format())
	if p.Status == plan.StatusDone {
		fmt.Fprintf(a.out, "\n%s plan %s complete\n", green("ok"), bold(p.ID))
		if lastFullResult != "" {
			fmt.Fprintln(a.out)
			fmt.Fprintln(a.out, lastFullResult)
		}
	}
	fmt.Fprintln(a.out)
}

// buildPlanStepPrompt keeps each plan step focused so it cannot burn the whole
// agent step budget on unrelated exploration.
func buildPlanStepPrompt(index, total int, goal, title string, prior []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "You are executing step %d of %d of an approved plan.\n", index, total)
	fmt.Fprintf(&b, "Overall goal: %s\n", goal)
	fmt.Fprintf(&b, "Current step ONLY: %s\n\n", title)
	if len(prior) > 0 {
		b.WriteString("Prior step results (do not redo them):\n")
		for _, n := range prior {
			b.WriteString(n)
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}
	b.WriteString("Rules:\n")
	b.WriteString("- Work only on the current step; do not start later steps.\n")
	b.WriteString("- Do not open .mincode/, traces, or agent runtime files unless the step explicitly asks.\n")
	b.WriteString("- Prefer read_file / list_dir / glob / grep; avoid exploratory shell probes.\n")
	b.WriteString("- Be decisive: a few tool calls, then stop.\n")
	b.WriteString("- Reply with a one-line summary of what you did (no tool calls once done).\n")
	return b.String()
}

const maxReplans = 3

// handleAutoCommand: /plan auto <goal> — plan-and-execute with automatic failure recovery.
func (a *App) handleAutoCommand(ctx context.Context, goal string) {
	if goal == "" {
		fmt.Fprintf(a.out, "%s usage: /auto <goal>\n", yellow("usage:"))
		return
	}

	// Step 1: draft plan via LLM.
	fmt.Fprintf(a.out, "%s auto: planning for: %s\n", cyan("auto"), bold(goal))
	p, err := a.draftPlanForAuto(ctx, goal)
	if err != nil {
		fmt.Fprintf(a.out, "%s plan generation failed: %v\n", red("error:"), err)
		return
	}

	// Step 2: auto-approve.
	if err := p.Approve(); err != nil {
		fmt.Fprintf(a.out, "%s %v\n", red("error:"), err)
		return
	}
	a.emit(observability.EventPlanApproved, observability.PlanEventData{
		PlanID:    p.ID,
		Goal:      p.Goal,
		Status:    string(p.Status),
		StepCount: len(p.Steps),
	})
	fmt.Fprintf(a.out, "%s plan approved (%d steps) — executing\n\n", green("ok"), len(p.Steps))

	// Step 3: execute with failure recovery.
	a.autoPlanAndRun(ctx, p)
}

// draftPlanForAuto generates a plan via LLM and stores it as the active plan.
func (a *App) draftPlanForAuto(ctx context.Context, goal string) (*plan.Plan, error) {
	req := llm.ChatRequest{
		Messages: []llm.Message{
			{
				Role: llm.RoleSystem,
				Content: `You break a coding task into a short numbered execution plan.
Output ONLY a numbered list (3-8 steps). Each step is one concrete action.
No prose before or after the list. No nested sub-steps.
Example:
1. Read the main router file
2. Add a /health handler
3. Write unit tests for /health
4. Run go test ./...`,
			},
			{Role: llm.RoleUser, Content: goal},
		},
	}

	a.emit(observability.EventPlanCreated, observability.PlanEventData{
		Goal:   goal,
		Status: string(plan.StatusDraft),
		Reason: "auto-generating",
	})

	resp, err := a.provider.Chat(ctx, req)
	if err != nil {
		return nil, err
	}
	titles := plan.ParseStepList(resp.Content)
	if len(titles) == 0 {
		return nil, fmt.Errorf("model returned no parseable steps")
	}
	if len(titles) > 8 {
		titles = titles[:8]
	}

	id := "plan-" + time.Now().UTC().Format("150405")
	p := plan.NewPlan(id, goal, titles)
	a.plans.SetCurrent(p)

	a.emit(observability.EventPlanCreated, observability.PlanEventData{
		PlanID:    p.ID,
		Goal:      goal,
		Status:    string(p.Status),
		StepCount: len(p.Steps),
	})

	fmt.Fprintln(a.out)
	fmt.Fprint(a.out, p.Format())
	fmt.Fprintln(a.out)
	return p, nil
}

// autoPlanAndRun executes a plan step by step, re-planning on failure.
func (a *App) autoPlanAndRun(ctx context.Context, p *plan.Plan) {
	a.echoTools.Store(true)
	defer a.echoTools.Store(false)

	planStepBudget := a.agent.MaxSteps
	var stepNotes []string
	var lastFullResult string

	for i := 0; i < len(p.Steps); {
		if err := ctx.Err(); err != nil {
			p.Cancel()
			fmt.Fprintf(a.out, "\n%s interrupted before step %d — session still alive\n", yellow("auto"), i+1)
			return
		}
		if p.Status == plan.StatusCancelled {
			fmt.Fprintf(a.out, "\n%s stopped before step %d\n", yellow("auto"), i+1)
			return
		}

		step := p.Steps[i]
		if err := p.StartStep(step.Index); err != nil {
			fmt.Fprintf(a.out, "%s %v\n", red("error:"), err)
			return
		}
		a.emit(observability.EventPlanStepStarted, observability.PlanEventData{
			PlanID:      p.ID,
			StepIndex:   step.Index,
			StepTitle:   step.Title,
			StepCount:   len(p.Steps),
			DoneCount:   i,
			ReplanCount: p.ReplanCount,
		})
		fmt.Fprintf(a.out, "%s step %d/%d %s\n",
			cyan("→"), step.Index, len(p.Steps), bold(step.Title))

		prompt := buildPlanStepPrompt(step.Index, len(p.Steps), p.Goal, step.Title, stepNotes)

		res, err := a.agent.Run(ctx, prompt)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				_ = p.CancelStep(step.Index, "cancelled")
				a.emit(observability.EventPlanStepFailed, observability.PlanEventData{
					PlanID:    p.ID,
					StepIndex: step.Index,
					StepTitle: step.Title,
					Error:     "cancelled",
					Status:    string(plan.StatusCancelled),
				})
				fmt.Fprintf(a.out, "\n%s interrupted at step %d — session still alive\n", yellow("auto"), step.Index)
				return
			}
			if errors.Is(err, agent.MaxStepsExceeded) {
				err = fmt.Errorf("step budget exceeded (%d tool/LLM turns)", planStepBudget)
			}

			// Attempt re-plan on failure.
			if p.ReplanCount < maxReplans {
				p.ReplanCount++
				a.emit(observability.EventPlanReplan, observability.PlanEventData{
					PlanID:      p.ID,
					StepIndex:   step.Index,
					StepTitle:   step.Title,
					Error:       err.Error(),
					ReplanCount: p.ReplanCount,
					Reason:      fmt.Sprintf("step %d failed: %v", step.Index, err),
				})
				fmt.Fprintf(a.out, "%s step %d failed, re-planning (attempt %d/%d): %v\n",
					yellow("replan"), step.Index, p.ReplanCount, maxReplans, err)

				_ = p.FailStep(step.Index, err.Error()+" [replanning]")
				newPlan, replanErr := a.replanFromFailure(ctx, p, step.Index, err.Error(), stepNotes)
				if replanErr != nil {
					fmt.Fprintf(a.out, "%s re-plan failed: %v\n", red("error:"), replanErr)
					fmt.Fprintf(a.out, "%s plan aborted\n", red("✗"))
					return
				}
				// Resume with the new plan from the same position.
				p = newPlan
				stepNotes = append(stepNotes, fmt.Sprintf("- Step %d (%s): FAILED — %v", step.Index, step.Title, err))
				continue
			}

			_ = p.FailStep(step.Index, err.Error())
			done, total := p.Progress()
			a.emit(observability.EventPlanStepFailed, observability.PlanEventData{
				PlanID:    p.ID,
				StepIndex: step.Index,
				StepTitle: step.Title,
				Error:     err.Error(),
				DoneCount: done,
				StepCount: total,
			})
			fmt.Fprintf(a.out, "%s step %d failed (no more replans): %v\n", red("✗"), step.Index, err)
			return
		}

		full := strings.TrimSpace(res.Final)
		summary := full
		if summary == "" {
			summary = "ok"
		}

		// Detect step failure: agent returned success but tools reported errors.
		stepFailed := res.ToolErrors > 0
		if stepFailed && p.ReplanCount < maxReplans {
			p.ReplanCount++
			errMsg := fmt.Sprintf("%d tool call(s) failed", res.ToolErrors)
			a.emit(observability.EventPlanReplan, observability.PlanEventData{
				PlanID:      p.ID,
				StepIndex:   step.Index,
				StepTitle:   step.Title,
				Error:       errMsg,
				ReplanCount: p.ReplanCount,
				Reason:      fmt.Sprintf("step %d: %s", step.Index, errMsg),
			})
			fmt.Fprintf(a.out, "%s step %d had errors, re-planning (attempt %d/%d): %s\n",
				yellow("replan"), step.Index, p.ReplanCount, maxReplans, errMsg)

			_ = p.FailStep(step.Index, errMsg+" [replanning]")
			newPlan, replanErr := a.replanFromFailure(ctx, p, step.Index, errMsg, stepNotes)
			if replanErr != nil {
				fmt.Fprintf(a.out, "%s re-plan failed: %v\n", red("error:"), replanErr)
				fmt.Fprintf(a.out, "%s plan aborted\n", red("✗"))
				return
			}
			p = newPlan
			stepNotes = append(stepNotes, fmt.Sprintf("- Step %d (%s): FAILED — %s", step.Index, step.Title, errMsg))
			i = 0
			continue
		}

		if len(summary) > 120 {
			summary = truncateStr(summary, 120)
		}
		_ = p.CompleteStep(step.Index, summary)
		stepNotes = append(stepNotes, fmt.Sprintf("- Step %d (%s): %s", step.Index, step.Title, summary))
		done, total := p.Progress()
		a.emit(observability.EventPlanStepFinished, observability.PlanEventData{
			PlanID:    p.ID,
			StepIndex: step.Index,
			StepTitle: step.Title,
			Result:    summary,
			DoneCount: done,
			StepCount: total,
		})
		if full != "" {
			fmt.Fprintln(a.out)
			fmt.Fprintln(a.out, full)
			fmt.Fprintln(a.out)
		}
		fmt.Fprintf(a.out, "%s step %d done\n\n", green("✓"), step.Index)
		lastFullResult = full
		i++
	}

	p.Finish()
	done, total := p.Progress()
	a.emit(observability.EventPlanFinished, observability.PlanEventData{
		PlanID:      p.ID,
		Goal:        p.Goal,
		Status:      string(p.Status),
		DoneCount:   done,
		StepCount:   total,
		ReplanCount: p.ReplanCount,
	})
	fmt.Fprintln(a.out)
	fmt.Fprint(a.out, p.Format())
	if p.Status == plan.StatusDone {
		extra := ""
		if p.ReplanCount > 0 {
			extra = fmt.Sprintf(" (replanned %d time(s))", p.ReplanCount)
		}
		fmt.Fprintf(a.out, "\n%s plan %s complete%s\n", green("ok"), bold(p.ID), extra)
		// Print the last step's full result so the user sees the final answer.
		if lastFullResult != "" {
			fmt.Fprintln(a.out)
			fmt.Fprintln(a.out, lastFullResult)
		}
	}
	fmt.Fprintln(a.out)
}

// replanFromFailure asks the LLM to generate a fresh plan that accounts for
// the failure and completed steps. Returns a new approved plan ready to resume.
func (a *App) replanFromFailure(ctx context.Context, oldPlan *plan.Plan, failedStep int, failureMsg string, priorNotes []string) (*plan.Plan, error) {
	var contextBuilder strings.Builder
	fmt.Fprintf(&contextBuilder, "Previous plan for goal: %s\n", oldPlan.Goal)
	fmt.Fprintf(&contextBuilder, "Failed at step %d: %s\n", failedStep, failureMsg)
	if len(priorNotes) > 0 {
		contextBuilder.WriteString("\nCompleted steps before failure:\n")
		for _, n := range priorNotes {
			contextBuilder.WriteString(n + "\n")
		}
	}
	contextBuilder.WriteString("\nGenerate a NEW numbered plan that:")
	contextBuilder.WriteString("\n1. Accounts for what was already completed")
	contextBuilder.WriteString("\n2. Handles the failure (try a different approach)")
	contextBuilder.WriteString("\n3. Completes the remaining work")

	req := llm.ChatRequest{
		Messages: []llm.Message{
			{
				Role:    llm.RoleSystem,
				Content: "You re-plan a failed coding task. Output ONLY a numbered list (3-8 steps). No prose.",
			},
			{Role: llm.RoleUser, Content: contextBuilder.String()},
		},
	}

	resp, err := a.provider.Chat(ctx, req)
	if err != nil {
		return nil, err
	}
	titles := plan.ParseStepList(resp.Content)
	if len(titles) == 0 {
		return nil, fmt.Errorf("model returned no parseable steps")
	}
	if len(titles) > 8 {
		titles = titles[:8]
	}

	id := oldPlan.ID + "-r" + fmt.Sprint(oldPlan.ReplanCount)
	p := plan.NewPlan(id, oldPlan.Goal, titles)
	p.ReplanCount = oldPlan.ReplanCount
	a.plans.SetCurrent(p)

	a.emit(observability.EventPlanCreated, observability.PlanEventData{
		PlanID:      p.ID,
		Goal:        p.Goal,
		Status:      string(p.Status),
		StepCount:   len(p.Steps),
		ReplanCount: p.ReplanCount,
		Reason:      fmt.Sprintf("replan after step %d failure", failedStep),
	})

	fmt.Fprintln(a.out)
	fmt.Fprintf(a.out, "%s new plan (%d steps):\n", cyan("replan"), len(p.Steps))
	fmt.Fprint(a.out, p.Format())
	fmt.Fprintln(a.out)

	if err := p.Approve(); err != nil {
		return nil, err
	}
	return p, nil
}

func formatSnapshot(s ctxmgr.Snapshot) string {
	var b strings.Builder
	b.WriteString(bold("Context Snapshot #"+fmt.Sprint(s.Step)) + "\n\n")
	b.WriteString(padCol(cyan("Source"), 14) + padCol(cyan("Role"), 12) + padCol(cyan("Tokens"), 8) + cyan("Status") + "\n")
	b.WriteString(gray(strings.Repeat("─", 64)) + "\n")

	for _, it := range s.Items {
		src := paintSource(string(it.Source))
		role := gray(it.Role)
		toks := fmt.Sprint(it.Tokens)
		status := green("included")
		switch {
		case it.Excluded:
			status = yellow("excluded")
			if it.Reason != "" {
				status += gray(" (" + it.Reason + ")")
			}
		case it.Truncated:
			status = yellow("truncated")
			if it.Reason != "" {
				status += gray(" (" + it.Reason + ")")
			}
		}
		if it.Pinned && !it.Excluded {
			status += blue(" [pinned]")
		}
		b.WriteString(padCol(src, 14) + padCol(role, 12) + padCol(toks, 8) + status + "\n")
		preview := it.Preview
		if preview == "" {
			preview = "-"
		}
		// Flatten newlines so multi-line context previews stay one row in CLI.
		preview = strings.ReplaceAll(preview, "\r\n", " ")
		preview = strings.ReplaceAll(preview, "\n", " ")
		b.WriteString("             " + dim(truncateStr(preview, 56)) + "\n")
	}
	b.WriteString(gray(strings.Repeat("─", 64)) + "\n")
	b.WriteString(padCol("Included", 14) + padCol("", 12) + padCol(fmt.Sprint(s.Included), 8) + "items\n")
	b.WriteString(padCol(yellow("Excluded"), 14) + padCol("", 12) + padCol(fmt.Sprint(s.Excluded), 8) + "items\n")
	b.WriteString(padCol(yellow("Truncated"), 14) + padCol("", 12) + padCol(fmt.Sprint(s.Truncated), 8) + "items\n")
	if s.ToolTokens > 0 {
		b.WriteString(padCol(magenta("Tool schemas"), 14) + padCol("", 12) + padCol(magenta(fmt.Sprint(s.ToolTokens)), 8) +
			gray("tokens (sent every call, not chat messages)") + "\n")
	}
	grand := s.TotalTokens + s.ToolTokens
	totalCol := green(fmt.Sprint(grand))
	if grand > s.Budget*9/10 {
		totalCol = yellow(fmt.Sprint(grand))
	}
	b.WriteString(padCol(bold("Total"), 14) + padCol("", 12) + padCol(totalCol, 8) +
		"tokens / budget " + fmt.Sprint(s.Budget) + "\n")
	if s.ActualPromptTokens > 0 {
		estTotal := grand
		ratio := s.EstimateRatio
		if ratio <= 0 && estTotal > 0 {
			ratio = float64(s.ActualPromptTokens) / float64(estTotal)
		}
		delta := s.ActualPromptTokens - estTotal
		sign := "+"
		if delta < 0 {
			sign = ""
		}
		b.WriteString(padCol(blue("API actual"), 14) + padCol("", 12) + padCol(blue(fmt.Sprint(s.ActualPromptTokens)), 8) +
			gray(fmt.Sprintf("prompt_tokens  ratio=%.2f  Δ%s%d", ratio, sign, delta)) + "\n")
	} else if s.RequestFailed {
		b.WriteString(padCol(red("API actual"), 14) + padCol("", 12) + padCol(red("-"), 8) +
			gray("(request failed — no usage)") + "\n")
	}
	return b.String()
}

func paintSource(src string) string {
	switch src {
	case "system":
		return bold(magenta(src))
	case "instructions":
		return magenta(src)
	case "skills":
		return yellow(src)
	case "memory":
		return blue(src)
	case "user_input":
		return green(src)
	case "history":
		return cyan(src)
	case "tool_result":
		return blue(src)
	case "summary":
		return yellow(src)
	case "pinned":
		return yellow(src)
	default:
		return src
	}
}

func padCol(s string, n int) string {
	// Pad using visible width (strip ANSI).
	vis := len(stripANSI(s))
	if vis >= n {
		return s + " "
	}
	return s + strings.Repeat(" ", n-vis)
}

func (a *App) printTimeline() {
	events, err := observability.ReadEvents(a.recorder.Path())
	if err != nil {
		fmt.Fprintf(a.out, "%s %v\n", red("read trace:"), err)
		return
	}
	fmt.Fprintf(a.out, "\n%s  %s\n\n", bold("Timeline"), gray(a.recorder.Path()))
	seq := 0
	for _, e := range events {
		line, ok := timelineLine(e)
		if !ok {
			continue
		}
		seq++
		fmt.Fprintf(a.out, "%s  %s\n", gray(fmt.Sprintf("%3d", seq)), line)
	}
	fmt.Fprintln(a.out)
}

func timelineLine(e observability.Event) (string, bool) {
	ts := gray(e.Time.Local().Format("15:04:05.000"))
	switch e.Type {
	case observability.EventSessionCreated:
		return ts + "  " + bold(blue("Session Created")), true
	case observability.EventAgentStarted:
		return ts + "  " + bold(green("Agent Started")), true
	case observability.EventAgentFinished:
		return ts + "  " + bold(green("Agent Finished")), true
	case observability.EventAgentFailed:
		return ts + "  " + bold(red("Agent Failed")), true
	case observability.EventAgentStateChanged:
		data := mapFromAny(e.Data)
		to, _ := data["to"].(string)
		switch to {
		case "BUILDING_CONTEXT", "CALLING_LLM", "PROCESSING_RESPONSE", "EXECUTING_TOOL",
			"FINISHED", "FAILED", "CANCELLED", "MAX_STEPS_REACHED":
			return ts + "  " + dim("State →") + " " + paintState(to), true
		}
		return "", false
	case observability.EventLLMRequestStarted:
		data := mapFromAny(e.Data)
		return fmt.Sprintf("%s  %s %s %s",
			ts, magenta("LLM Request"), fmt.Sprint(data["model"]), gray(fmt.Sprint("messages=", data["message_count"]))), true
	case observability.EventContextBuilt:
		data := mapFromAny(e.Data)
		est := fmt.Sprint(data["total_tokens"])
		if v, ok := data["excluded_count"].(float64); ok && v > 0 {
			est = yellow(est)
		} else {
			est = green(est)
		}
		toolsTok := data["tool_tokens"]
		if toolsTok == nil {
			toolsTok = 0
		}
		return fmt.Sprintf("%s  %s msgs=%s %s tools=%v excl=%v trunc=%v",
			ts, cyan("Context Built"), est, gray("budget="+fmt.Sprint(data["budget"])),
			toolsTok, data["excluded_count"], data["truncated_count"]), true
	case observability.EventContextCompacted:
		data := mapFromAny(e.Data)
		return fmt.Sprintf("%s  %s %v→%v tokens  compressed=%v preserved=%v",
			ts, yellow("Compacted"), data["before_tokens"], data["after_tokens"],
			data["compressed"], data["preserved"]), true
	case observability.EventLLMStreamDelta:
		// Printed live via the bus subscriber when streamEcho is on.
	case observability.EventLLMRequestFinished:
		data := mapFromAny(e.Data)
		preview, _ := data["content_preview"].(string)
		// Flatten newlines so a multi-line poem cannot break the timeline grid.
		preview = strings.ReplaceAll(preview, "\r\n", "\n")
		preview = strings.ReplaceAll(preview, "\n", " ↵ ")
		preview = truncateStr(preview, 72)
		streamTag := ""
		if b, ok := data["streamed"].(bool); ok && b {
			ttft := data["ttft_ms"]
			if ttft == nil {
				ttft = 0
			}
			streamTag = gray(fmt.Sprintf(" stream ttft=%vms", ttft))
		}
		head := fmt.Sprintf("%s  %s %s %s %s%s",
			ts, magenta("LLM Response"),
			gray(fmt.Sprintf("in=%v out=%v", data["input_tokens"], data["output_tokens"])),
			gray(fmt.Sprintf("%vms", data["duration_ms"])),
			dim("…"), streamTag)
		if preview == "" {
			return head, true
		}
		// Preview on its own indented line.
		return head + "\n" + strings.Repeat(" ", 22) + dim(preview), true
	case observability.EventLLMRequestFailed:
		data := mapFromAny(e.Data)
		return fmt.Sprintf("%s  %s %v", ts, red("LLM Failed"), data["error"]), true
	case observability.EventToolStarted:
		data := mapFromAny(e.Data)
		tool, _ := data["tool"].(string)
		args, _ := data["arguments"].(string)
		par := ""
		if b, ok := data["parallel"].(bool); ok && b {
			par = " ∥"
		}
		return fmt.Sprintf("%s  %s %s %s%s", ts, cyan("Tool Start"), bold(tool), gray(truncateStr(compactJSON(args), 50)), par), true
	case observability.EventToolBatchStarted:
		data := mapFromAny(e.Data)
		return fmt.Sprintf("%s  %s size=%v workers=%v %v",
			ts, cyan("Batch Start"), data["size"], data["max_workers"],
			gray(fmt.Sprint(data["tools"]))), true
	case observability.EventToolBatchFinished:
		data := mapFromAny(e.Data)
		return fmt.Sprintf("%s  %s size=%v ok=%v err=%v %v",
			ts, green("Batch Done"), data["size"], data["succeeded"], data["failed"],
			gray(fmt.Sprintf("%vms", data["duration_ms"]))), true
	case observability.EventToolFinished:
		data := mapFromAny(e.Data)
		tool, _ := data["tool"].(string)
		return fmt.Sprintf("%s  %s %s %s %s",
			ts, green("Tool Done"), bold(tool),
			gray(fmt.Sprintf("%v bytes", data["result_size"])),
			gray(fmt.Sprintf("%vms", data["duration_ms"]))), true
	case observability.EventToolFailed:
		data := mapFromAny(e.Data)
		tool, _ := data["tool"].(string)
		return fmt.Sprintf("%s  %s %s %v", ts, red("Tool Failed"), bold(tool), data["error"]), true
	case observability.EventPermissionRequested:
		data := mapFromAny(e.Data)
		return fmt.Sprintf("%s  %s %v", ts, yellow("Permission"), data["summary"]), true
	case observability.EventPermissionApproved:
		data := mapFromAny(e.Data)
		return fmt.Sprintf("%s  %s %v", ts, green("Perm OK"), data["summary"]), true
	case observability.EventPermissionDenied:
		data := mapFromAny(e.Data)
		return fmt.Sprintf("%s  %s %v", ts, red("Perm Deny"), data["summary"]), true
	case observability.EventFileChanged:
		data := mapFromAny(e.Data)
		return fmt.Sprintf("%s  %s %v %v", ts, magenta("File Changed"), data["operation"], data["path"]), true
	case observability.EventLoopDetected:
		data := mapFromAny(e.Data)
		return fmt.Sprintf("%s  %s %v x%v", ts, red("Loop Detected"), data["tool"], data["count"]), true
	case observability.EventInstructionLoaded:
		data := mapFromAny(e.Data)
		rel, _ := data["rel_path"].(string)
		return fmt.Sprintf("%s  %s %s", ts, magenta("Instructions"), bold(rel)), true
	case observability.EventSkillLoaded:
		data := mapFromAny(e.Data)
		name, _ := data["name"].(string)
		return fmt.Sprintf("%s  %s %s", ts, yellow("Skill Loaded"), bold(name)), true
	case observability.EventSkillUnloaded:
		data := mapFromAny(e.Data)
		name, _ := data["name"].(string)
		return fmt.Sprintf("%s  %s %s", ts, dim("Skill Unloaded"), bold(name)), true
	case observability.EventMemoryRetrieved:
		data := mapFromAny(e.Data)
		return fmt.Sprintf("%s  %s %v entries", ts, blue("Memory"), data["entries"]), true
	case observability.EventMemoryUpdated:
		data := mapFromAny(e.Data)
		entry, _ := data["entry"].(string)
		return fmt.Sprintf("%s  %s %s", ts, blue("Memory Updated"), gray(truncateStr(entry, 48))), true
	case observability.EventPlanCreated:
		data := mapFromAny(e.Data)
		goal, _ := data["goal"].(string)
		if id, _ := data["plan_id"].(string); id != "" {
			return fmt.Sprintf("%s  %s %s", ts, yellow("Plan Created"), bold(id)) + gray(" "+truncateStr(goal, 48)), true
		}
		return fmt.Sprintf("%s  %s %s", ts, yellow("Plan Drafting"), gray(truncateStr(goal, 48))), true
	case observability.EventPlanApproved:
		data := mapFromAny(e.Data)
		id, _ := data["plan_id"].(string)
		return fmt.Sprintf("%s  %s %s steps=%v", ts, green("Plan Approved"), bold(id), data["step_count"]), true
	case observability.EventPlanRejected:
		data := mapFromAny(e.Data)
		id, _ := data["plan_id"].(string)
		return fmt.Sprintf("%s  %s %s", ts, red("Plan Rejected"), id), true
	case observability.EventPlanCancelled:
		data := mapFromAny(e.Data)
		id, _ := data["plan_id"].(string)
		return fmt.Sprintf("%s  %s %s", ts, yellow("Plan Cancelled"), id), true
	case observability.EventPlanStepStarted:
		data := mapFromAny(e.Data)
		return fmt.Sprintf("%s  %s %v/%v %v", ts, cyan("Plan Step"),
			data["step_index"], data["step_count"], data["step_title"]), true
	case observability.EventPlanStepFinished:
		data := mapFromAny(e.Data)
		return fmt.Sprintf("%s  %s %v/%v %v", ts, green("Plan Step OK"),
			data["step_index"], data["step_count"], data["step_title"]), true
	case observability.EventPlanStepFailed:
		data := mapFromAny(e.Data)
		return fmt.Sprintf("%s  %s %v/%v %v", ts, red("Plan Step Fail"),
			data["step_index"], data["step_count"], data["step_title"]), true
	case observability.EventPlanFinished:
		data := mapFromAny(e.Data)
		return fmt.Sprintf("%s  %s %v status=%v", ts, bold("Plan Finished"),
			data["plan_id"], data["status"]), true
	}
	return "", false
}

func paintState(state string) string {
	switch state {
	case "FINISHED":
		return green(state)
	case "FAILED", "CANCELLED", "MAX_STEPS_REACHED":
		return red(state)
	case "EXECUTING_TOOL", "CALLING_LLM":
		return yellow(state)
	case "BUILDING_CONTEXT", "PROCESSING_RESPONSE":
		return cyan(state)
	default:
		return state
	}
}

func mapFromAny(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return map[string]any{}
}

func truncateStr(s string, n int) string {
	if n <= 0 || len(s) <= n {
		return s
	}
	end := n
	for end > 0 && !utf8.RuneStart(s[end]) {
		end--
	}
	return s[:end] + "..."
}

func (a *App) printTrace(n int) {
	events, err := observability.ReadEvents(a.recorder.Path())
	if err != nil {
		fmt.Fprintf(a.out, "%s %v\n", red("read trace:"), err)
		return
	}
	fmt.Fprintf(a.out, "\n%s %s  %s\n\n", bold("Trace"), gray(a.recorder.Path()),
		gray(fmt.Sprintf("(%d events, showing last %d)", len(events), n)))
	start := 0
	if len(events) > n {
		start = len(events) - n
	}
	for i, e := range events[start:] {
		seq := start + i + 1
		fmt.Fprintf(a.out, "%s  %s\n", gray(fmt.Sprintf("%3d", seq)), formatEvent(e))
	}
	fmt.Fprintln(a.out)
}

func formatEvent(e observability.Event) string {
	ts := gray(e.Time.Local().Format("15:04:05.000"))
	typ := paintEventType(string(e.Type))
	base := fmt.Sprintf("%s  %s", ts, typ)

	data := mapFromAny(e.Data)
	if len(data) == 0 && e.Data != nil {
		b, _ := json.Marshal(e.Data)
		_ = json.Unmarshal(b, &data)
	}

	switch e.Type {
	case observability.EventLLMRequestFinished:
		return fmt.Sprintf("%s  in=%v out=%v total=%v  %vms",
			base, data["input_tokens"], data["output_tokens"], data["total_tokens"], data["duration_ms"])
	case observability.EventLLMRequestStarted:
		return fmt.Sprintf("%s  model=%v messages=%v", base, data["model"], data["message_count"])
	case observability.EventLLMRequestFailed:
		return fmt.Sprintf("%s  %v", base, red(fmt.Sprint(data["error"])))
	case observability.EventSessionCreated:
		return fmt.Sprintf("%s  model=%v provider=%v", base, data["model"], data["provider"])
	case observability.EventAgentStateChanged:
		to, _ := data["to"].(string)
		return fmt.Sprintf("%s  %v → %s", base, data["from"], paintState(to))
	case observability.EventToolStarted, observability.EventToolRequested:
		return fmt.Sprintf("%s  %v %v", base, data["tool"], gray(truncateStr(fmt.Sprint(data["arguments"]), 60)))
	case observability.EventToolFinished:
		errPart := green("ok")
		if v, ok := data["is_error"].(bool); ok && v {
			errPart = red("error")
		}
		return fmt.Sprintf("%s  %v  %v bytes  %vms %s", base, data["tool"], data["result_size"], data["duration_ms"], errPart)
	case observability.EventToolFailed:
		return fmt.Sprintf("%s  %v  %v", base, data["tool"], red(fmt.Sprint(data["error"])))
	case observability.EventInstructionLoaded:
		return fmt.Sprintf("%s  %v  %v bytes", base, data["rel_path"], data["bytes"])
	case observability.EventSkillLoaded:
		return fmt.Sprintf("%s  %v  %v bytes", base, data["name"], data["bytes"])
	case observability.EventSkillUnloaded:
		return fmt.Sprintf("%s  %v", base, data["name"])
	}
	return base
}

func paintEventType(typ string) string {
	switch {
	case typ == "session.created":
		return blue(typ)
	case strings.HasPrefix(typ, "agent.finished"):
		return green(typ)
	case strings.HasPrefix(typ, "agent.failed") || strings.HasPrefix(typ, "llm.request_failed") ||
		strings.HasPrefix(typ, "tool.failed") || typ == "loop.detected" ||
		strings.HasPrefix(typ, "permission.denied"):
		return red(typ)
	case strings.HasPrefix(typ, "permission.approved"):
		return green(typ)
	case strings.HasPrefix(typ, "permission."):
		return yellow(typ)
	case strings.HasPrefix(typ, "file."):
		return magenta(typ)
	case typ == "instruction.loaded":
		return magenta(typ)
	case typ == "skill.loaded" || typ == "skill.unloaded":
		return yellow(typ)
	case strings.HasPrefix(typ, "memory."):
		return blue(typ)
	case strings.HasPrefix(typ, "plan."):
		return yellow(typ)
	case strings.HasPrefix(typ, "llm."):
		return magenta(typ)
	case strings.HasPrefix(typ, "tool."):
		return cyan(typ)
	case strings.HasPrefix(typ, "context."):
		return yellow(typ)
	case strings.HasPrefix(typ, "agent."):
		return bold(typ)
	default:
		return typ
	}
}

func atoi(s string) (int, error) {
	n := 0
	if s == "" {
		return 0, fmt.Errorf("empty")
	}
	for _, ch := range s {
		if ch < '0' || ch > '9' {
			return 0, fmt.Errorf("not a number")
		}
		n = n*10 + int(ch-'0')
	}
	return n, nil
}
