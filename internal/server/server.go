package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/manxisuo/mincode/internal/agent"
	"github.com/manxisuo/mincode/internal/ctxmgr"
	"github.com/manxisuo/mincode/internal/experiment"
	"github.com/manxisuo/mincode/internal/instruction"
	"github.com/manxisuo/mincode/internal/memory"
	"github.com/manxisuo/mincode/internal/observability"
	"github.com/manxisuo/mincode/internal/plan"
	"github.com/manxisuo/mincode/internal/repomap"
	"github.com/manxisuo/mincode/internal/session"
	"github.com/manxisuo/mincode/internal/skill"
	"github.com/manxisuo/mincode/internal/tools"
	webui "github.com/manxisuo/mincode/web"
)

// Options configures a local Web Inspector server (single user, single session).
type Options struct {
	Addr      string
	Workspace string
	SessionID string
	Provider  string
	Model     string
	// TraceDir is the only directory used for session JSONL traces.
	TraceDir string
	// Resolution is the config cascade for T-obs-7 (optional).
	Resolution any
	// RepoMapEnabled / RepoMapTokens expose the context repo map to the UI.
	RepoMapEnabled bool
	RepoMapTokens  int
	// RepoMapCache shares incremental parsing with the agent's repo_map tool
	// (optional; a private cache is created when nil).
	RepoMapCache *repomap.Cache
}

// Server exposes Agent runtime over HTTP + SSE for the local Web UI.
type Server struct {
	opts         Options
	agent        *agent.Agent
	bus          *observability.Bus
	metrics      *observability.MetricsCollector
	experiments  *experiment.Store
	traceDir     string
	plans        *plan.Manager
	planMu       sync.Mutex
	skills       *skill.Loader
	ws           *tools.Workspace
	instr        *instruction.Loader
	mem          *memory.Store
	sessions     *session.Store
	repoCache    *repomap.Cache
	activeSessID string
	permMu       sync.Mutex
	pendingPerms map[string]*pendingPerm

	hub *eventHub

	mu            sync.Mutex
	running       bool
	cancelTurn    context.CancelFunc
	lastResult    *agent.Result
	lastErr       string
	turnStarted   time.Time
	turnSeq       int
	completedTurn int
}

// New wires an agent + bus into an HTTP server.
func New(opts Options, ag *agent.Agent, bus *observability.Bus, metrics *observability.MetricsCollector, exp *experiment.Store, skills *skill.Loader, ws *tools.Workspace, instr *instruction.Loader) *Server {
	if opts.Addr == "" {
		opts.Addr = "127.0.0.1:8080"
	}
	traceDir := opts.TraceDir
	if traceDir == "" && opts.Workspace != "" {
		traceDir = filepath.Join(opts.Workspace, ".mincode", "traces")
	}
	if traceDir == "" {
		traceDir = ".mincode/traces"
	}
	s := &Server{
		opts:         opts,
		agent:        ag,
		bus:          bus,
		metrics:      metrics,
		experiments:  exp,
		traceDir:     traceDir,
		plans:        plan.NewManager(),
		skills:       skills,
		ws:           ws,
		instr:        instr,
		repoCache:    repoMapCache(opts.RepoMapCache),
		pendingPerms: map[string]*pendingPerm{},
		hub:          newEventHub(),
		activeSessID: opts.SessionID,
	}
	if bus != nil {
		bus.Subscribe(func(e observability.Event) {
			s.hub.publish(e)
		})
	}
	return s
}

// repoMapCache returns the provided cache or a fresh one.
func repoMapCache(c *repomap.Cache) *repomap.Cache {
	if c != nil {
		return c
	}
	return repomap.NewCache()
}

// Handler returns the HTTP mux.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/health", s.handleHealth)
	mux.HandleFunc("GET /api/session", s.handleSession)
	mux.HandleFunc("POST /api/chat", s.handleChat)
	mux.HandleFunc("POST /api/cancel", s.handleCancel)
	mux.HandleFunc("GET /api/events", s.handleEvents)
	mux.HandleFunc("GET /api/context", s.handleContext)
	mux.HandleFunc("GET /api/wire", s.handleWire)
	mux.HandleFunc("GET /api/decisions", s.handleDecisions)
	mux.HandleFunc("GET /api/tools/result", s.handleToolResult)
	mux.HandleFunc("GET /api/metrics", s.handleMetrics)
	mux.HandleFunc("GET /api/config", s.handleConfigResolution)
	mux.HandleFunc("GET /api/timeline", s.handleTimeline)
	mux.HandleFunc("GET /api/traces", s.handleTraceList)
	mux.HandleFunc("GET /api/traces/{id}", s.handleTraceShow)
	mux.HandleFunc("GET /api/experiments", s.handleExperimentList)
	mux.HandleFunc("GET /api/experiments/{name}", s.handleExperimentShow)
	mux.HandleFunc("GET /api/plan", s.handlePlanGet)
	mux.HandleFunc("POST /api/plan", s.handlePlanDraft)
	mux.HandleFunc("POST /api/plan/approve", s.handlePlanApprove)
	mux.HandleFunc("POST /api/plan/reject", s.handlePlanReject)
	mux.HandleFunc("POST /api/plan/cancel", s.handlePlanCancel)
	mux.HandleFunc("POST /api/plan/auto", s.handlePlanAuto)
	mux.HandleFunc("GET /api/skills", s.handleSkillList)
	mux.HandleFunc("GET /api/skills/{name}", s.handleSkillShow)
	mux.HandleFunc("POST /api/skills/{name}/activate", s.handleSkillActivate)
	mux.HandleFunc("POST /api/skills/{name}/deactivate", s.handleSkillDeactivate)
	mux.HandleFunc("GET /api/instructions", s.handleInstructionList)
	mux.HandleFunc("GET /api/instructions/all", s.handleInstructionShow)
	mux.HandleFunc("POST /api/instructions/reload", s.handleInstructionReload)
	mux.HandleFunc("GET /api/memory", s.handleMemoryGet)
	mux.HandleFunc("POST /api/memory", s.handleMemoryAdd)
	mux.HandleFunc("GET /api/repomap", s.handleRepoMap)
	mux.HandleFunc("GET /api/sessions", s.handleSessionList)
	mux.HandleFunc("GET /api/sessions/current", s.handleSessionCurrent)
	mux.HandleFunc("POST /api/sessions/{id}/load", s.handleSessionLoad)
	mux.HandleFunc("POST /api/sessions/{id}", s.handleSessionUpdate)
	mux.HandleFunc("DELETE /api/sessions/{id}", s.handleSessionDelete)
	mux.HandleFunc("POST /api/export", s.handleExportWrite)
	mux.HandleFunc("GET /api/export", s.handleExportGet)
	mux.HandleFunc("GET /api/export/download", s.handleExportDownload)
	mux.HandleFunc("GET /api/permissions/pending", s.handlePermissionPending)
	mux.HandleFunc("POST /api/permissions/{id}", s.handlePermissionDecide)

	static, err := fs.Sub(webui.FS, "dist")
	if err == nil {
		fileServer := http.FileServer(http.FS(static))
		mux.Handle("GET /", fileServer)
	}
	return mux
}

// ListenAndServe blocks until ctx is cancelled.
func (s *Server) ListenAndServe(ctx context.Context) error {
	srv := &http.Server{
		Addr:              s.opts.Addr,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.ListenAndServe()
	}()
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
		return ctx.Err()
	case err := <-errCh:
		if err == http.ErrServerClosed {
			return nil
		}
		return err
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"error": msg})
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleSession(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	running := s.running
	state := string(s.agent.State)
	lastResult := s.lastResult
	lastErr := s.lastErr
	started := s.turnStarted
	completed := s.completedTurn
	s.mu.Unlock()

	var final string
	var steps, toolCalls int
	if !running && lastResult != nil {
		final = lastResult.Final
		steps = lastResult.Steps
		toolCalls = lastResult.ToolCalls
	}
	var snap any
	if lastResult != nil && lastResult.Snapshot != nil {
		snap = lastResult.Snapshot
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"session_id":     s.opts.SessionID,
		"active_session": s.activeSessionID(),
		"workspace":      s.opts.Workspace,
		"provider":       s.opts.Provider,
		"model":          s.opts.Model,
		"state":          state,
		"running":        running,
		"last_error":     lastErr,
		"turn": map[string]any{
			"id":         completed,
			"final":      final,
			"steps":      steps,
			"tool_calls": toolCalls,
			"started_at": started,
		},
		"snapshot": snap,
	})
}

type chatRequest struct {
	Message string `json:"message"`
}

func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	var req chatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json body")
		return
	}
	msg := strings.TrimSpace(req.Message)
	if msg == "" {
		writeErr(w, http.StatusBadRequest, "message is required")
		return
	}

	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		writeErr(w, http.StatusConflict, "a turn is already running")
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.cancelTurn = cancel
	s.running = true
	s.lastErr = ""
	s.turnStarted = time.Now().UTC()
	s.turnSeq++
	turn := s.turnSeq
	s.lastResult = nil
	s.mu.Unlock()

	go func() {
		defer cancel()
		res, err := s.agent.Run(ctx, msg)
		s.saveCurrentSession()
		s.mu.Lock()
		s.running = false
		s.cancelTurn = nil
		s.lastResult = res
		s.completedTurn = turn
		switch {
		case err == nil:
			s.lastErr = ""
		case errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded):
			s.lastErr = "cancelled by user"
		default:
			s.lastErr = err.Error()
		}
		s.mu.Unlock()
		// Name the session after the first turn; async so the agent is not blocked.
		if msg != "" {
			go s.autoNameSession(msg)
		}
	}()

	writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "running": true})
}

func (s *Server) handleCancel(w http.ResponseWriter, _ *http.Request) {
	s.denyAllPending()
	s.mu.Lock()
	cancel := s.cancelTurn
	s.mu.Unlock()
	if cancel == nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "cancelled": false})
		return
	}
	cancel()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "cancelled": true})
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	ch := s.hub.subscribe()
	defer s.hub.unsubscribe(ch)

	fmt.Fprintf(w, "event: hello\ndata: %s\n\n", mustJSON(map[string]any{
		"session_id": s.opts.SessionID,
		"time":       time.Now().UTC(),
	}))
	flusher.Flush()

	ctx := r.Context()
	ticker := time.NewTicker(20 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			fmt.Fprintf(w, ": keepalive\n\n")
			flusher.Flush()
		case e, ok := <-ch:
			if !ok {
				return
			}
			payload := map[string]any{
				"id":      e.ID,
				"time":    e.Time,
				"session": e.SessionID,
				"step":    e.Step,
				"type":    e.Type,
				"data":    e.Data,
			}
			fmt.Fprintf(w, "event: runtime\ndata: %s\n\n", mustJSON(payload))
			flusher.Flush()
		}
	}
}

func (s *Server) handleContext(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	res := s.lastResult
	s.mu.Unlock()
	var snap *ctxmgr.Snapshot
	// T-obs-5: ?step=N returns the historical snapshot for replay.
	if q := r.URL.Query().Get("step"); q != "" {
		step := 0
		for _, c := range q {
			if c < '0' || c > '9' {
				writeErr(w, http.StatusBadRequest, "invalid step")
				return
			}
			step = step*10 + int(c-'0')
		}
		if s.agent != nil && s.agent.Ctx != nil {
			snap = s.agent.Ctx.SnapshotByStep(step)
		}
		if snap == nil {
			writeErr(w, http.StatusNotFound, "no snapshot for step")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"snapshot": snap, "replay": true, "step": step})
		return
	}
	if res != nil && res.Snapshot != nil {
		snap = res.Snapshot
	} else if s.agent != nil && s.agent.Ctx != nil {
		snap = s.agent.Ctx.LastSnapshot()
	}
	if snap == nil {
		writeJSON(w, http.StatusOK, map[string]any{"items": []any{}, "total_tokens": 0})
		return
	}
	steps := []int{}
	if s.agent != nil && s.agent.Ctx != nil {
		steps = s.agent.Ctx.SnapHistory()
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items":           snap.Items,
		"step":            snap.Step,
		"total_tokens":    snap.TotalTokens,
		"tool_tokens":     snap.ToolTokens,
		"budget":          snap.Budget,
		"included_count":  snap.Included,
		"excluded_count":  snap.Excluded,
		"truncated_count": snap.Truncated,
		"notes":           snap.Notes,
		"diff":            snap.Diff,
		"history_steps":   steps,
	})
}

func (s *Server) handleMetrics(w http.ResponseWriter, _ *http.Request) {
	m := observability.Metrics{}
	if s.metrics != nil {
		m = s.metrics.Snapshot()
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"llm_calls":           m.LLMCalls,
		"errors":              m.Errors,
		"input_tokens":        m.InputTokens,
		"output_tokens":       m.OutputTokens,
		"total_tokens":        m.TotalTokens,
		"llm_duration_ms":     m.LLMDuration.Milliseconds(),
		"parallel_batches":    m.ParallelBatches,
		"parallel_tool_calls": m.ParallelToolCalls,
		"stream_calls":        m.StreamCalls,
		"stream_deltas":       m.StreamDeltas,
		"last_ttft_ms":        m.LastTTFTMS,
	})
}

func (s *Server) handleTimeline(w http.ResponseWriter, _ *http.Request) {
	events := s.hub.recent(200)
	writeJSON(w, http.StatusOK, map[string]any{"events": events})
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return `{"error":"marshal"}`
	}
	return string(b)
}
