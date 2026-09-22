// DecisionLevel is the result of policy evaluation for one tool call.
package permission

const (
	// Allow runs the tool without prompting.
	Allow DecisionLevel = "allow"
	// Ask requires interactive approval (web / stdin).
	Ask DecisionLevel = "ask"
	// Deny blocks the tool without calling the provider.
	Deny DecisionLevel = "deny"
)