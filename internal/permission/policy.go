// Package permission decides whether a tool invocation is allowed.
package permission

import "fmt"

// Level is the decision for a tool action.
type Level int

const (
	Allow Level = iota
	Ask
	Deny
)

func (l Level) String() string {
	switch l {
	case Allow:
		return "allow"
	case Ask:
		return "ask"
	case Deny:
		return "deny"
	default:
		return "unknown"
	}
}

// Request describes an action that needs a decision.
type Request struct {
	Tool      string
	Arguments string
	// Summary is a short human-readable description (e.g. file path + op).
	Summary string
}

// Policy returns a decision level for a tool call.
type Policy interface {
	Evaluate(req Request) Level
}

// Approver asks the user (or an automation) when Policy returns Ask.
// Returns true if approved.
type Approver interface {
	Approve(req Request) (bool, error)
}

// DefaultPolicy: read-only tools allow; write/edit ask; unknown deny.
type DefaultPolicy struct {
	// Overrides maps tool name → level. Unset tools use built-in defaults.
	Overrides map[string]Level
}

// NewDefaultPolicy builds the Phase 4 baseline policy.
func NewDefaultPolicy() *DefaultPolicy {
	return &DefaultPolicy{
		Overrides: map[string]Level{
			"read_file":   Allow,
			"list_dir":    Allow,
			"glob":        Allow,
			"grep":        Allow,
			"repo_map":    Allow, // read-only structural map of the workspace
			"code_search": Allow, // read-only lexical code retrieval
			"web_search":  Allow, // read-only network query; backend must be configured
			"web_fetch":   Allow, // read-only URL fetch; SSRF guard blocks private targets
			"write_file":  Ask,
			"edit_file":   Ask,
			"shell":       Ask, // Phase 5; reserved
			"memory_add":  Ask, // durable fact write — user confirms
		},
	}
}

func (p *DefaultPolicy) Evaluate(req Request) Level {
	if p == nil {
		return Ask
	}
	if lvl, ok := p.Overrides[req.Tool]; ok {
		return lvl
	}
	// Unknown tools: do not allow silently.
	return Deny
}

// Set overrides one tool's level.
func (p *DefaultPolicy) Set(tool string, lvl Level) {
	if p.Overrides == nil {
		p.Overrides = map[string]Level{}
	}
	p.Overrides[tool] = lvl
}

// DeniedError is returned when a tool call is not permitted.
type DeniedError struct {
	Tool   string
	Reason string
}

func (e *DeniedError) Error() string {
	return fmt.Sprintf("permission denied for %s: %s", e.Tool, e.Reason)
}

// Evaluate runs policy + optional approver.
// denied=true means the call must not execute.
func Evaluate(policy Policy, approver Approver, req Request) (denied bool, err error) {
	if policy == nil {
		policy = NewDefaultPolicy()
	}
	switch policy.Evaluate(req) {
	case Allow:
		return false, nil
	case Deny:
		return true, &DeniedError{Tool: req.Tool, Reason: "policy deny"}
	case Ask:
		if approver == nil {
			return true, &DeniedError{Tool: req.Tool, Reason: "ask but no approver"}
		}
		ok, err := approver.Approve(req)
		if err != nil {
			return true, err
		}
		if !ok {
			return true, &DeniedError{Tool: req.Tool, Reason: "user rejected"}
		}
		return false, nil
	}
	return true, &DeniedError{Tool: req.Tool, Reason: "unknown level"}
}
