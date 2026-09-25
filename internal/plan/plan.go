// Package plan holds the plan-mode task list: draft → approve → step execution.
package plan

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Status is the lifecycle state of a plan or step.
type Status string

const (
	StatusDraft     Status = "draft"
	StatusApproved  Status = "approved"
	StatusRejected  Status = "rejected"
	StatusRunning   Status = "running"
	StatusDone      Status = "done"
	StatusFailed    Status = "failed"
	StatusCancelled Status = "cancelled"
	StatusPending   Status = "pending"
	StatusSkipped   Status = "skipped"
)

// Step is one planned action.
type Step struct {
	Index  int    `json:"index"`
	Title  string `json:"title"`
	Status Status `json:"status"`
	// Result is a short outcome note after execution.
	Result string `json:"result,omitempty"`
	// Error is set when the step failed.
	Error string `json:"error,omitempty"`
}

// Plan is a user-approved task breakdown.
type Plan struct {
	ID        string    `json:"id"`
	Goal      string    `json:"goal"`
	Steps     []Step    `json:"steps"`
	Status    Status    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
	// Current is the 1-based index of the step being executed (0 = not started).
	Current int `json:"current,omitempty"`
	// ReplanCount tracks how many times the plan was re-generated after step failures.
	ReplanCount int `json:"replan_count,omitempty"`
}

// Manager holds the active plan for a session.
type Manager struct {
	current *Plan
}

// NewManager creates an empty plan manager.
func NewManager() *Manager { return &Manager{} }

// Current returns the active plan, or nil.
func (m *Manager) Current() *Plan { return m.current }

// SetCurrent replaces the active plan.
func (m *Manager) SetCurrent(p *Plan) { m.current = p }

// Clear drops the active plan.
func (m *Manager) Clear() { m.current = nil }

// NewPlan builds a draft plan from a goal and step titles.
func NewPlan(id, goal string, titles []string) *Plan {
	p := &Plan{
		ID:        id,
		Goal:      goal,
		Status:    StatusDraft,
		CreatedAt: time.Now().UTC(),
		Steps:     make([]Step, 0, len(titles)),
	}
	for _, t := range titles {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		p.Steps = append(p.Steps, Step{
			Index:  len(p.Steps) + 1,
			Title:  t,
			Status: StatusPending,
		})
	}
	return p
}

// Approve marks a draft plan approved. Returns error if not draft or empty.
func (p *Plan) Approve() error {
	if p == nil {
		return fmt.Errorf("no active plan")
	}
	if p.Status != StatusDraft {
		return fmt.Errorf("plan is %s, cannot approve", p.Status)
	}
	if len(p.Steps) == 0 {
		return fmt.Errorf("plan has no steps")
	}
	p.Status = StatusApproved
	return nil
}

// Reject marks a draft/rejected plan rejected.
func (p *Plan) Reject() error {
	if p == nil {
		return fmt.Errorf("no active plan")
	}
	if p.Status != StatusDraft && p.Status != StatusApproved {
		return fmt.Errorf("plan is %s, cannot reject", p.Status)
	}
	p.Status = StatusRejected
	return nil
}

// Cancel stops a running/approved plan.
func (p *Plan) Cancel() {
	if p == nil {
		return
	}
	if p.Status == StatusApproved || p.Status == StatusRunning {
		p.Status = StatusCancelled
	}
}

// StartStep marks step i (1-based) running and records progress cursor.
func (p *Plan) StartStep(i int) error {
	if err := p.stepIndex(i); err != nil {
		return err
	}
	p.Steps[i-1].Status = StatusRunning
	p.Current = i
	if p.Status == StatusApproved {
		p.Status = StatusRunning
	}
	return nil
}

// CompleteStep marks step i done with an optional short result.
func (p *Plan) CompleteStep(i int, result string) error {
	if err := p.stepIndex(i); err != nil {
		return err
	}
	p.Steps[i-1].Status = StatusDone
	p.Steps[i-1].Result = result
	return nil
}

// FailStep marks step i failed.
func (p *Plan) FailStep(i int, msg string) error {
	if err := p.stepIndex(i); err != nil {
		return err
	}
	p.Steps[i-1].Status = StatusFailed
	p.Steps[i-1].Error = msg
	p.Status = StatusFailed
	return nil
}

// CancelStep marks step i cancelled (user interrupt) and cancels the plan.
func (p *Plan) CancelStep(i int, reason string) error {
	if err := p.stepIndex(i); err != nil {
		return err
	}
	p.Steps[i-1].Status = StatusCancelled
	if reason == "" {
		reason = "cancelled"
	}
	p.Steps[i-1].Error = reason
	p.Status = StatusCancelled
	p.Current = 0
	return nil
}

// Finish marks the whole plan done if every step succeeded.
func (p *Plan) Finish() {
	if p == nil {
		return
	}
	for _, s := range p.Steps {
		if s.Status != StatusDone && s.Status != StatusSkipped {
			return
		}
	}
	p.Status = StatusDone
	p.Current = 0
}

// Progress returns (done, total).
func (p *Plan) Progress() (int, int) {
	if p == nil {
		return 0, 0
	}
	done := 0
	for _, s := range p.Steps {
		if s.Status == StatusDone || s.Status == StatusSkipped {
			done++
		}
	}
	return done, len(p.Steps)
}

// Format renders the plan for the CLI.
func (p *Plan) Format() string {
	if p == nil {
		return "(no plan)"
	}
	var b strings.Builder
	done, total := p.Progress()
	b.WriteString(fmt.Sprintf("Plan %s  [%s]  %d/%d steps\n", p.ID, p.Status, done, total))
	b.WriteString(fmt.Sprintf("Goal: %s\n\n", p.Goal))
	for _, s := range p.Steps {
		b.WriteString(fmt.Sprintf("  %s %s\n", statusMark(s.Status), formatStepLine(s)))
		if s.Result != "" && s.Status == StatusDone {
			b.WriteString(fmt.Sprintf("      %s\n", dimNote(s.Result)))
		}
		if s.Error != "" {
			b.WriteString(fmt.Sprintf("      %s\n", s.Error))
		}
	}
	return b.String()
}

func formatStepLine(s Step) string {
	return fmt.Sprintf("%d. %s", s.Index, s.Title)
}

func statusMark(st Status) string {
	switch st {
	case StatusDone:
		return "[x]"
	case StatusRunning:
		return "[>]"
	case StatusFailed:
		return "[!]"
	case StatusCancelled:
		return "[c]"
	case StatusSkipped:
		return "[-]"
	case StatusPending, StatusDraft:
		return "[ ]"
	default:
		return "[ ]"
	}
}

func dimNote(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	// Truncate on rune boundaries so CJK text is not split mid-character.
	r := []rune(s)
	if len(r) > 80 {
		s = string(r[:80]) + "…"
	}
	return s
}

func (p *Plan) stepIndex(i int) error {
	if p == nil {
		return fmt.Errorf("no active plan")
	}
	if i < 1 || i > len(p.Steps) {
		return fmt.Errorf("step %d out of range 1..%d", i, len(p.Steps))
	}
	return nil
}

// ParseStepList parses a numbered/bulleted list into step titles.
// Accepted markers: "1." "1)" "1、" "- " "* ". Unmarked prose lines are ignored.
func ParseStepList(text string) []string {
	var out []string
	for _, line := range strings.Split(text, "\n") {
		t := strings.TrimSpace(line)
		if t == "" {
			continue
		}
		if strings.HasPrefix(t, "- ") || strings.HasPrefix(t, "* ") {
			t = strings.TrimSpace(t[2:])
		} else {
			marker := false
			for i, r := range t {
				if i > 3 {
					break
				}
				if r == '.' || r == ')' || r == '、' || r == '．' {
					if isDigits(strings.TrimSpace(t[:i])) {
						t = strings.TrimSpace(t[i+utf8LenRune(r):])
						marker = true
					}
					break
				}
				if r < '0' || r > '9' {
					break
				}
			}
			if !marker {
				continue
			}
		}
		t = strings.Trim(t, "*`")
		t = strings.TrimSpace(t)
		if t != "" {
			out = append(out, t)
		}
	}
	return out
}

// utf8LenRune returns the byte length of a single rune encoded as string(r).
func utf8LenRune(r rune) int {
	return len(string(r))
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	_, err := strconv.Atoi(s)
	return err == nil
}
