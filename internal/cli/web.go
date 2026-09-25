package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/manxisuo/mincode/internal/agent"
	"github.com/manxisuo/mincode/internal/config"
	"github.com/manxisuo/mincode/internal/experiment"
	"github.com/manxisuo/mincode/internal/instruction"
	"github.com/manxisuo/mincode/internal/memory"
	"github.com/manxisuo/mincode/internal/observability"
	"github.com/manxisuo/mincode/internal/paths"
	"github.com/manxisuo/mincode/internal/permission"
	"github.com/manxisuo/mincode/internal/repomap"
	"github.com/manxisuo/mincode/internal/server"
	"github.com/manxisuo/mincode/internal/session"
	"github.com/manxisuo/mincode/internal/skill"
	"github.com/manxisuo/mincode/internal/tools"
	"github.com/manxisuo/mincode/internal/webfetch"
)

// WebOptions configures `mincode web`.
type WebOptions struct {
	ConfigPath string
	Workspace  string
	Addr       string
	Model      string
	Provider   string
}

// localWebApprover auto-approves Ask-level tools for local single-user web mode.
// Destructive shell commands are still denied by ShellAwarePolicy before Approve.
type localWebApprover struct{}

func (localWebApprover) Approve(req permission.Request) (bool, error) {
	fmt.Fprintf(os.Stderr, "mincode web: auto-approved %s (%s)\n", req.Tool, req.Summary)
	return true, nil
}

// RunWeb starts the local Web Inspector (HTTP + SSE) on the workspace.
func RunWeb(ctx context.Context, w WebOptions) error {
	workspace := w.Workspace
	if workspace == "" {
		wd, err := os.Getwd()
		if err != nil {
			return err
		}
		workspace = wd
	}
	if abs, err := filepath.Abs(workspace); err == nil {
		workspace = abs
	}

	cfg, err := config.LoadFrom(w.ConfigPath, workspace)
	if err != nil {
		return err
	}
	if w.Model != "" {
		cfg.Provider.Model = w.Model
	}
	if w.Provider != "" {
		cfg.Provider.Type = w.Provider
	}
	resolution := config.Resolve(w.ConfigPath, workspace, os.Getenv, map[string]string{
		"provider.model": w.Model,
		"provider.type":  w.Provider,
	})

	provider, err := buildProvider(cfg)
	if err != nil {
		return err
	}

	sessionID := time.Now().UTC().Format("20060102-150405") + "-" + observability.NewID()[:8]
	layout := paths.Resolve(workspace, cfg.Data.Location, cfg.Data.Root)
	if err := layout.EnsureDirs(); err != nil {
		return err
	}
	traceDir := layout.TracesDir
	if err := config.EnsureTraceDir(traceDir); err != nil {
		return err
	}
	tracePath := config.TracePath(traceDir, sessionID)
	recorder, err := observability.NewRecorder(tracePath)
	if err != nil {
		return err
	}
	defer func() { _ = recorder.Close() }()

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
		return err
	}
	memStore, err := memory.New(workspace)
	if err != nil {
		return err
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
		return err
	} else if searchProvider != nil {
		registry.Register(&tools.WebSearch{
			Provider:   searchProvider,
			MaxResults: cfg.WebSearch.MaxResults,
			Timeout:    time.Duration(cfg.WebSearch.TimeoutSec) * time.Second,
		})
	}

	// Repository map: shared cache between the agent tool and the Web API.
	repoCache := repomap.NewCache()
	if cfg.RepoMapEnabled() {
		registry.Register(&tools.RepoMap{WS: ws, MaxTokens: cfg.Agent.RepoMapTokens, Cache: repoCache})
	}

	sysPrompt := cfg.Agent.SystemPrompt + config.PlatformShellHint(runtime.GOOS)
	ag := agent.NewWithCompress(provider, registry, bus, sessionID, cfg.Agent.MaxSteps,
		sysPrompt, cfg.Agent.TokenBudget, cfg.Agent.CompressAt)
	if cfg.Agent.ParallelTools != nil {
		ag.ParallelTools = *cfg.Agent.ParallelTools
	}
	ag.MaxParallel = cfg.Agent.MaxParallel
	ag.Stream = cfg.StreamEnabled()
	if cfg.Agent.Reflection != nil {
		ag.Reflection = *cfg.Agent.Reflection
	}
	ag.MaxReflections = cfg.Agent.MaxReflections
	// Approver set after server.New so pending requests can reach the web UI.

	registry.Register(&tools.MemoryAdd{
		Store: memStore,
		OnAdded: func(entry, composed string) {
			ag.Ctx.SetMemory(composed)
		},
	})

	instrLoader, err := instruction.NewLoader(workspace)
	if err == nil {
		ag.Instr = instrLoader
		if f, err := instrLoader.LoadRoot(); err == nil && f != nil {
			ag.Ctx.SetInstructions(instrLoader.Compose())
			bus.Publish(observability.NewEvent(sessionID, 0, observability.EventInstructionLoaded,
				observability.InstructionLoadedData{Path: f.Path, RelPath: f.RelPath, RelDir: f.RelDir, Bytes: len(f.Content)}))
		}
	}
	if skillLoader, err := skill.NewLoader(workspace); err == nil {
		if _, err := skillLoader.Discover(); err != nil {
			fmt.Fprintf(os.Stderr, "mincode web: discover skills: %v\n", err)
		}
	}
	if content, existed, err := memStore.Load(); err == nil && existed && content != "" {
		ag.Ctx.SetMemory(memStore.Compose())
	}

	if cfg.RepoMapEnabled() {
		buildCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if m, err := repomap.Build(buildCtx, workspace, repomap.Options{MaxTokens: cfg.Agent.RepoMapTokens, Cache: repoCache}); err == nil {
			ag.Ctx.SetRepoMap(m.Text)
			bus.Publish(observability.NewEvent(sessionID, 0, observability.EventRepoMapBuilt,
				observability.RepoMapData{
					Files:     len(m.Files),
					Symbols:   countRepoMapSymbols(m),
					Tokens:    m.Tokens,
					Scanned:   m.Scanned,
					Skipped:   m.Skipped,
					BuildMS:   m.BuildMS,
					Truncated: m.Truncated,
					Reason:    "startup",
				}))
		} else {
			fmt.Fprintf(os.Stderr, "mincode web: repo map: %v\n", err)
		}
		cancel()
	}

	addr := w.Addr
	if addr == "" {
		addr = "127.0.0.1:8080"
	}

	expStore, expErr := experiment.NewStore(layout.Experiments)
	if expErr != nil {
		fmt.Fprintf(os.Stderr, "mincode web: experiment store: %v\n", expErr)
		expStore = nil
	}

	skillLoader, _ := skill.NewLoader(workspace)
	if skillLoader != nil {
		_, _ = skillLoader.Discover()
	}

	srv := server.New(server.Options{
		Addr:           addr,
		Workspace:      workspace,
		SessionID:      sessionID,
		Provider:       provider.Name(),
		Model:          provider.Model(),
		TraceDir:       traceDir,
		Resolution:     resolution,
		RepoMapEnabled: cfg.RepoMapEnabled(),
		RepoMapTokens:  cfg.Agent.RepoMapTokens,
		RepoMapCache:   repoCache,
	}, ag, bus, metrics, expStore, skillLoader, ws, instrLoader)
	ag.Approver = srv.WebApprover()
	srv.SetMemory(memStore)
	sessStore := session.NewStore(layout.SessionsDir)
	srv.SetSessions(sessStore)

	bus.Publish(observability.NewEvent(sessionID, 0, observability.EventSessionCreated,
		observability.SessionCreatedData{
			Workspace: workspace,
			Model:     provider.Model(),
			Provider:  provider.Name(),
		}))

	fmt.Printf("MinCode Web Inspector\n")
	fmt.Printf("  workspace  %s\n", workspace)
	fmt.Printf("  provider   %s / %s\n", provider.Name(), provider.Model())
	fmt.Printf("  session    %s\n", sessionID)
	fmt.Printf("  data       %s  project_id=%s\n", layout.Location, layout.ProjectID)
	fmt.Printf("  traces     %s\n", traceDir)
	fmt.Printf("  trace file %s\n", tracePath)
	fmt.Printf("  listening  http://%s\n", addr)
	fmt.Printf("  note       Ask 级操作将在网页审批；危险 shell 仍由策略拒绝\n\n")

	return srv.ListenAndServe(ctx)
}
