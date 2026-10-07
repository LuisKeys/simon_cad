// Package workflow is the autonomous CAD agent workflow for complex requests:
//
//	User request → Plan → Execute → Validate → Correct → Final result
//
// Each stage is its own component: the Planner decides WHAT must be built (a structured
// plan with expectations), the Executor runs the plan step by step through the ordinary
// agent tool loop (the CAD tool layer still decides HOW geometry is made), the Validator
// deterministically compares the CAD state with the plan, and the Corrector repairs only
// the steps that probably caused a problem, within a bounded number of attempts.
package workflow

// State is where the agent is in the workflow; the UI shows it.
type State string

const (
	StateIdle       State = "IDLE"
	StatePlanning   State = "PLANNING"
	StateExecuting  State = "EXECUTING"
	StateValidating State = "VALIDATING"
	StateCorrecting State = "CORRECTING"
	StateCompleted  State = "COMPLETED"
	StateFailed     State = "FAILED"
)

// Event is one state transition with a human-readable progress message.
type Event struct {
	State   State  `json:"state"`
	Message string `json:"message"`
	Step    int    `json:"step,omitempty"`  // current plan step (1-based) while executing
	Total   int    `json:"total,omitempty"` // number of plan steps
	Attempt int    `json:"attempt,omitempty"`
}

// Terminal reports whether the workflow has finished.
func (s State) Terminal() bool { return s == StateCompleted || s == StateFailed || s == StateIdle }
