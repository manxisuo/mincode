package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/manxisuo/mincode/internal/agent"
	"github.com/manxisuo/mincode/internal/llm"
	"github.com/manxisuo/mincode/internal/observability"
	"github.com/manxisuo/mincode/internal/plan"
	"github.com/manxisuo/mincode/internal/strutil"
)

type planResponse struct {
	Plan *plan.Plan `json:"plan"`
}

func (s *Server) handlePlanGet(w http.ResponseWriter, _ *http.Request) {
	s.planMu.Lock()
	p := s.plans.Current()
	s.planMu.Unlock()
	writeJSON(w, http.StatusOK, planResponse{Plan: p})
}

type planDraftRequest struct {
	Goal string `json:"goal"`
}

func (s *Server) handlePlanDraft(w http.ResponseWriter, r *http.Request) {
	var req planDraftRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json body")
		return
	}
	goal := strings.TrimSpace(req.Goal)
	if goal == "" {
		writeErr(w, http.StatusBadRequest, "goal is required")
		return
	}

	s.planMu.Lock()
	if s.plans.Current() != nil && s.plans.Current().Status == plan.StatusRunning {
		s.planMu.Unlock()
		writeErr(w, http.StatusConflict, "a plan is running")
		return
	}
	s.planMu.Unlock()

	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		writeErr(w, http.StatusConflict, "another turn is already running")
		return
	}
	s.running = true
	s.lastErr = ""
	s.mu.Unlock()

	p, err := s.draftPlan(goal)
	s.mu.Lock()
	s.running = false
	s.mu.Unlock()
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, planResponse{Plan: p})
}

func (s *Server) draftPlan(goal string) (*plan.Plan, error) {
	if s.agent == nil || s.agent.Provider == nil {
		return nil, fmt.Errorf("no provider")
	}
	s.emitPlan(observability.EventPlanCreated, observability.PlanEventData{
		Goal:   goal,
		Status: string(plan.StatusDraft),
		Reason: "generating",
	})
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
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	resp, err := s.agent.Provider.Chat(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("plan generation failed: %w", err)
	}
	titles := plan.ParseStepList(resp.Content)
	if len(titles) == 0 {
		return nil, fmt.Errorf("model returned no parseable steps")
	}
	if len(titles) > 8 {
		titles = titles[:8]
	}
	p := plan.NewPlan("plan-"+time.Now().UTC().Format("150405"), goal, titles)
	s.planMu.Lock()
	s.plans.SetCurrent(p)
	s.planMu.Unlock()
	s.emitPlan(observability.EventPlanCreated, observability.PlanEventData{
		PlanID:    p.ID,
		Goal:      p.Goal,
		Status:    string(p.Status),
		StepCount: len(p.Steps),
	})
	return p, nil
}

func (s *Server) emitPlan(typ observability.EventType, data observability.PlanEventData) {
	if s.bus == nil {
		return
	}
	s.bus.Publish(observability.NewEvent(s.opts.SessionID, s.agentCtxLen(), typ, data))
}

func (s *Server) agentCtxLen() int {
	if s.agent == nil || s.agent.Ctx == nil {
		return 0
	}
	return s.agent.Ctx.Len()
}

func (s *Server) handlePlanApprove(w http.ResponseWriter, _ *http.Request) {
	s.planMu.Lock()
	p := s.plans.Current()
	s.planMu.Unlock()
	if p == nil {
		writeErr(w, http.StatusNotFound, "no active plan")
		return
	}
	if err := p.Approve(); err != nil {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}

	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		writeErr(w, http.StatusConflict, "another turn is already running")
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.cancelTurn = cancel
	s.running = true
	s.lastErr = ""
	s.turnSeq++
	turn := s.turnSeq
	s.mu.Unlock()

	s.emitPlan(observability.EventPlanApproved, observability.PlanEventData{
		PlanID:    p.ID,
		Goal:      p.Goal,
		Status:    string(p.Status),
		StepCount: len(p.Steps),
	})

	go func() {
		defer cancel()
		err := s.runPlanSteps(ctx, p)
		s.saveCurrentSession()
		s.mu.Lock()
		s.running = false
		s.cancelTurn = nil
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
	}()

	writeJSON(w, http.StatusAccepted, map[string]any{
		"ok":     true,
		"plan":   p,
		"status": "approved",
	})
}

func (s *Server) runPlanSteps(ctx context.Context, p *plan.Plan) error {
	if s.agent == nil {
		return fmt.Errorf("no agent")
	}
	var stepNotes []string
	for i := range p.Steps {
		if err := ctx.Err(); err != nil {
			p.Cancel()
			s.emitPlan(observability.EventPlanCancelled, observability.PlanEventData{
				PlanID: p.ID, Goal: p.Goal, Status: string(p.Status), Reason: "cancelled",
			})
			return err
		}
		if p.Status == plan.StatusCancelled {
			return nil
		}
		step := p.Steps[i]
		if err := p.StartStep(step.Index); err != nil {
			return err
		}
		done, _ := p.Progress()
		s.emitPlan(observability.EventPlanStepStarted, observability.PlanEventData{
			PlanID: p.ID, StepIndex: step.Index, StepTitle: step.Title,
			StepCount: len(p.Steps), DoneCount: done,
		})

		prompt := planStepPrompt(step.Index, len(p.Steps), p.Goal, step.Title, stepNotes)
		res, err := s.agent.Run(ctx, prompt)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				_ = p.CancelStep(step.Index, "cancelled")
				s.emitPlan(observability.EventPlanStepFailed, observability.PlanEventData{
					PlanID: p.ID, StepIndex: step.Index, StepTitle: step.Title,
					Error: "cancelled", Status: string(plan.StatusCancelled),
				})
				s.emitPlan(observability.EventPlanCancelled, observability.PlanEventData{
					PlanID: p.ID, Status: string(p.Status),
				})
				return err
			}
			if errors.Is(err, agent.MaxStepsExceeded) {
				err = fmt.Errorf("step budget exceeded — step too broad or model thrashing")
			}
			_ = p.FailStep(step.Index, err.Error())
			done, total := p.Progress()
			s.emitPlan(observability.EventPlanStepFailed, observability.PlanEventData{
				PlanID: p.ID, StepIndex: step.Index, StepTitle: step.Title,
				Error: err.Error(), DoneCount: done, StepCount: total,
			})
			return err
		}

		fullFinal := strings.TrimSpace(res.Final)
		if fullFinal == "" {
			fullFinal = "ok"
		}
		// Keep a short note for the next-step prompt; store a longer final
		// so the Plan page can show what each step actually answered.
		note := strutil.TruncateRunes(fullFinal, 120)
		stepFinal := strutil.TruncateRunes(fullFinal, 800)
		_ = p.CompleteStep(step.Index, stepFinal)
		stepNotes = append(stepNotes, fmt.Sprintf("- Step %d (%s): %s", step.Index, step.Title, note))
		done, total := p.Progress()
		s.emitPlan(observability.EventPlanStepFinished, observability.PlanEventData{
			PlanID: p.ID, StepIndex: step.Index, StepTitle: step.Title,
			Result: stepFinal, DoneCount: done, StepCount: total,
		})
	}

	p.Finish()
	done, total := p.Progress()
	s.emitPlan(observability.EventPlanFinished, observability.PlanEventData{
		PlanID: p.ID, Goal: p.Goal, Status: string(p.Status),
		DoneCount: done, StepCount: total,
	})
	return nil
}

func planStepPrompt(index, total int, goal, title string, prior []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "You are executing step %d of %d of an approved plan.\n", index, total)
	fmt.Fprintf(&b, "Overall goal: %s\n", goal)
	fmt.Fprintf(&b, "This step only: %s\n", title)
	if len(prior) > 0 {
		b.WriteString("\nEarlier steps (for context; do not redo):\n")
		for _, n := range prior {
			b.WriteString(n + "\n")
		}
	}
	b.WriteString("\nDo only this step. Reply with a short result summary when finished.")
	return b.String()
}

func (s *Server) handlePlanReject(w http.ResponseWriter, _ *http.Request) {
	s.planMu.Lock()
	p := s.plans.Current()
	s.planMu.Unlock()
	if p == nil {
		writeErr(w, http.StatusNotFound, "no active plan")
		return
	}
	if err := p.Reject(); err != nil {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	s.emitPlan(observability.EventPlanRejected, observability.PlanEventData{
		PlanID: p.ID, Goal: p.Goal, Status: string(p.Status),
	})
	s.planMu.Lock()
	s.plans.Clear()
	s.planMu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "plan": nil})
}

func (s *Server) handlePlanCancel(w http.ResponseWriter, _ *http.Request) {
	s.planMu.Lock()
	p := s.plans.Current()
	s.planMu.Unlock()
	if p == nil {
		writeErr(w, http.StatusNotFound, "no active plan")
		return
	}
	before := p.Status
	p.Cancel()
	// Also cancel in-flight agent turn if any.
	s.mu.Lock()
	cancel := s.cancelTurn
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	s.emitPlan(observability.EventPlanCancelled, observability.PlanEventData{
		PlanID: p.ID, Goal: p.Goal, Status: string(p.Status),
		Reason: fmt.Sprintf("was %s", before),
	})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "plan": p})
}

const maxServerReplans = 3

func (s *Server) handlePlanAuto(w http.ResponseWriter, r *http.Request) {
	var req planDraftRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json body")
		return
	}
	goal := strings.TrimSpace(req.Goal)
	if goal == "" {
		writeErr(w, http.StatusBadRequest, "goal is required")
		return
	}

	s.planMu.Lock()
	if s.plans.Current() != nil && s.plans.Current().Status == plan.StatusRunning {
		s.planMu.Unlock()
		writeErr(w, http.StatusConflict, "a plan is running")
		return
	}
	s.planMu.Unlock()

	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		writeErr(w, http.StatusConflict, "another turn is already running")
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.cancelTurn = cancel
	s.running = true
	s.lastErr = ""
	s.turnSeq++
	turn := s.turnSeq
	s.mu.Unlock()

	p, err := s.draftPlan(goal)
	if err != nil {
		s.mu.Lock()
		s.running = false
		s.mu.Unlock()
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	if err := p.Approve(); err != nil {
		s.mu.Lock()
		s.running = false
		s.mu.Unlock()
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	s.emitPlan(observability.EventPlanApproved, observability.PlanEventData{
		PlanID: p.ID, Goal: p.Goal, Status: string(p.Status), StepCount: len(p.Steps),
	})

	go func() {
		defer cancel()
		err := s.runAutoPlanSteps(ctx, p)
		s.saveCurrentSession()
		s.mu.Lock()
		s.running = false
		s.cancelTurn = nil
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
	}()

	writeJSON(w, http.StatusAccepted, map[string]any{
		"ok":     true,
		"plan":   p,
		"status": "auto-approved",
	})
}

func (s *Server) runAutoPlanSteps(ctx context.Context, p *plan.Plan) error {
	if s.agent == nil {
		return fmt.Errorf("no agent")
	}
	var stepNotes []string

	for i := 0; i < len(p.Steps); {
		if err := ctx.Err(); err != nil {
			p.Cancel()
			s.emitPlan(observability.EventPlanCancelled, observability.PlanEventData{
				PlanID: p.ID, Goal: p.Goal, Status: string(p.Status), Reason: "cancelled",
			})
			return err
		}
		if p.Status == plan.StatusCancelled {
			return nil
		}
		step := p.Steps[i]
		if err := p.StartStep(step.Index); err != nil {
			return err
		}
		done, _ := p.Progress()
		s.emitPlan(observability.EventPlanStepStarted, observability.PlanEventData{
			PlanID: p.ID, StepIndex: step.Index, StepTitle: step.Title,
			StepCount: len(p.Steps), DoneCount: done, ReplanCount: p.ReplanCount,
		})

		prompt := planStepPrompt(step.Index, len(p.Steps), p.Goal, step.Title, stepNotes)
		res, err := s.agent.Run(ctx, prompt)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				_ = p.CancelStep(step.Index, "cancelled")
				s.emitPlan(observability.EventPlanStepFailed, observability.PlanEventData{
					PlanID: p.ID, StepIndex: step.Index, StepTitle: step.Title,
					Error: "cancelled", Status: string(plan.StatusCancelled),
				})
				return err
			}
			if errors.Is(err, agent.MaxStepsExceeded) {
				err = fmt.Errorf("step budget exceeded")
			}

			// Attempt re-plan on error.
			if p.ReplanCount < maxServerReplans {
				p.ReplanCount++
				s.emitPlan(observability.EventPlanReplan, observability.PlanEventData{
					PlanID: p.ID, StepIndex: step.Index, StepTitle: step.Title,
					Error: err.Error(), ReplanCount: p.ReplanCount,
					Reason: fmt.Sprintf("step %d failed: %v", step.Index, err),
				})
				_ = p.FailStep(step.Index, err.Error()+" [replanning]")
				newPlan, replanErr := s.replanFromFailure(ctx, p, step.Index, err.Error(), stepNotes)
				if replanErr != nil {
					return replanErr
				}
				p = newPlan
				stepNotes = append(stepNotes, fmt.Sprintf("- Step %d (%s): FAILED — %v", step.Index, step.Title, err))
				i = 0
				continue
			}

			_ = p.FailStep(step.Index, err.Error())
			done, total := p.Progress()
			s.emitPlan(observability.EventPlanStepFailed, observability.PlanEventData{
				PlanID: p.ID, StepIndex: step.Index, StepTitle: step.Title,
				Error: err.Error(), DoneCount: done, StepCount: total,
			})
			return err
		}

		fullFinal := strings.TrimSpace(res.Final)
		if fullFinal == "" {
			fullFinal = "ok"
		}

		// Detect step failure: agent returned success but tools reported errors.
		if res.ToolErrors > 0 && p.ReplanCount < maxServerReplans {
			p.ReplanCount++
			errMsg := fmt.Sprintf("%d tool call(s) failed", res.ToolErrors)
			s.emitPlan(observability.EventPlanReplan, observability.PlanEventData{
				PlanID: p.ID, StepIndex: step.Index, StepTitle: step.Title,
				Error: errMsg, ReplanCount: p.ReplanCount,
				Reason: fmt.Sprintf("step %d: %s", step.Index, errMsg),
			})
			_ = p.FailStep(step.Index, errMsg+" [replanning]")
			newPlan, replanErr := s.replanFromFailure(ctx, p, step.Index, errMsg, stepNotes)
			if replanErr != nil {
				return replanErr
			}
			p = newPlan
			stepNotes = append(stepNotes, fmt.Sprintf("- Step %d (%s): FAILED — %s", step.Index, step.Title, errMsg))
			i = 0
			continue
		}

		note := strutil.TruncateRunes(fullFinal, 120)
		stepFinal := strutil.TruncateRunes(fullFinal, 800)
		_ = p.CompleteStep(step.Index, stepFinal)
		stepNotes = append(stepNotes, fmt.Sprintf("- Step %d (%s): %s", step.Index, step.Title, note))
		done, total := p.Progress()
		s.emitPlan(observability.EventPlanStepFinished, observability.PlanEventData{
			PlanID: p.ID, StepIndex: step.Index, StepTitle: step.Title,
			Result: stepFinal, DoneCount: done, StepCount: total,
		})
		i++
	}

	p.Finish()
	done, total := p.Progress()
	s.emitPlan(observability.EventPlanFinished, observability.PlanEventData{
		PlanID: p.ID, Goal: p.Goal, Status: string(p.Status),
		DoneCount: done, StepCount: total, ReplanCount: p.ReplanCount,
	})
	return nil
}

func (s *Server) replanFromFailure(ctx context.Context, oldPlan *plan.Plan, failedStep int, failureMsg string, priorNotes []string) (*plan.Plan, error) {
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
	ctx2, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	resp, err := s.agent.Provider.Chat(ctx2, req)
	if err != nil {
		return nil, err
	}
	titles := plan.ParseStepList(resp.Content)
	if len(titles) == 0 {
		return nil, fmt.Errorf("replan: model returned no parseable steps")
	}
	if len(titles) > 8 {
		titles = titles[:8]
	}

	id := oldPlan.ID + "-r" + fmt.Sprint(oldPlan.ReplanCount)
	p := plan.NewPlan(id, oldPlan.Goal, titles)
	p.ReplanCount = oldPlan.ReplanCount
	s.planMu.Lock()
	s.plans.SetCurrent(p)
	s.planMu.Unlock()
	s.emitPlan(observability.EventPlanCreated, observability.PlanEventData{
		PlanID: p.ID, Goal: p.Goal, Status: string(p.Status),
		StepCount: len(p.Steps), ReplanCount: p.ReplanCount,
		Reason: fmt.Sprintf("replan after step %d failure", failedStep),
	})
	return p, nil
}
