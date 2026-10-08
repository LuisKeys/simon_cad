package workflow

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/accelone/simoncad/internal/agent/runtime"
)

// StepStatus is the outcome of one plan step.
type StepStatus string

const (
	StepPending  StepStatus = "pending"
	StepSuccess  StepStatus = "success"
	StepFailed   StepStatus = "failed"
	StepSkipped  StepStatus = "skipped"     // a step it depends on failed
	StepNeedInfo StepStatus = "needs_input" // the agent asked the user a question
	StepApproval StepStatus = "awaiting_approval"
)

// StepResult is the structured record of one executed step.
type StepResult struct {
	Step       int        `json:"step"`
	Operation  string     `json:"operation"`
	Target     string     `json:"target,omitempty"`
	Tool       string     `json:"tool,omitempty"`
	Status     StepStatus `json:"status"`
	Objects    []string   `json:"objects,omitempty"` // features the step created
	ChangeSets []string   `json:"changesets,omitempty"`
	Error      string     `json:"error,omitempty"`
	Note       string     `json:"note,omitempty"` // the agent's one-line report
}

// Execution is the state of a plan run: per-step results and the references between
// plan objects and CAD features.
type Execution struct {
	Steps []StepResult `json:"steps"`
	// Refs maps plan refs to the feature ids that realize them.
	Refs map[string]string `json:"refs"`
	// Owner maps a feature id to the step that last created or modified it (for attributing
	// validation problems to a step).
	Owner map[string]int `json:"owner"`
}

// NewExecution prepares an all-pending execution for plan.
func NewExecution(plan *Plan) *Execution {
	ex := &Execution{Refs: map[string]string{}, Owner: map[string]int{}}
	for _, s := range plan.Steps {
		ex.Steps = append(ex.Steps, StepResult{Step: s.N, Operation: s.Operation, Target: s.Target, Tool: s.Tool, Status: StepPending})
	}
	return ex
}

// Result returns the record of step n (1-based).
func (ex *Execution) Result(n int) *StepResult { return &ex.Steps[n-1] }

// Stopped reports a step that halts the workflow (a question for the user or an approval).
func (ex *Execution) Stopped() *StepResult {
	for i := range ex.Steps {
		if s := ex.Steps[i].Status; s == StepNeedInfo || s == StepApproval {
			return &ex.Steps[i]
		}
	}
	return nil
}

// refsText lists the known plan objects for the executor's prompts.
func (ex *Execution) refsText() string {
	if len(ex.Refs) == 0 {
		return ""
	}
	keys := make([]string, 0, len(ex.Refs))
	for k := range ex.Refs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		parts = append(parts, k+" = "+ex.Refs[k])
	}
	return "Objects from earlier steps (plan ref = feature id): " + strings.Join(parts, ", ") + "."
}

// Executor runs plan steps sequentially through the agent conversation. Each step is one
// Send: the model performs exactly that step with CAD tools and reports in one sentence.
type Executor struct {
	// OnStep is called before a step runs.
	OnStep func(s Step, total int)
}

// Run executes the given steps (all pending steps when which is nil). A step whose
// dependencies did not succeed is skipped, never run blindly. It stops at the first step
// that needs the user (a question or an approval). hint is prepended to the first directive.
func (e *Executor) Run(ctx context.Context, c *runtime.Conversation, plan *Plan, ex *Execution, which []int, hint string) error {
	if which == nil {
		for _, s := range plan.Steps {
			which = append(which, s.N)
		}
	}
	first := true
	for _, n := range which {
		s := plan.Steps[n-1]
		r := ex.Result(n)
		if s.Operation == "validate" {
			r.Status, r.Note = StepSuccess, "handled by the automatic validation"
			continue
		}
		if dep := e.blocked(plan, ex, s); dep != 0 {
			r.Status, r.Error = StepSkipped, fmt.Sprintf("not run: step %d it depends on did not succeed", dep)
			continue
		}
		if e.OnStep != nil {
			e.OnStep(s, len(plan.Steps))
		}
		directive := e.directive(plan, ex, s)
		if first && hint != "" {
			directive = hint + "\n\n" + directive
		}
		first = false
		turn, err := c.Send(ctx, directive)
		if err != nil {
			r.Status, r.Error = StepFailed, err.Error()
			return err
		}
		ex.record(s, r, turn)
		if r.Status == StepNeedInfo || r.Status == StepApproval {
			return nil
		}
	}
	return nil
}

// blocked returns a dependency of s that did not succeed (0 if none).
func (e *Executor) blocked(plan *Plan, ex *Execution, s Step) int {
	deps := append([]int(nil), s.DependsOn...)
	for _, ref := range []string{s.Target, s.Tool} {
		if p := plan.Producer(ref); p != 0 && p != s.N {
			deps = append(deps, p)
		}
	}
	for _, d := range deps {
		if d >= 1 && d <= len(ex.Steps) && ex.Steps[d-1].Status != StepSuccess {
			return d
		}
	}
	return 0
}

func (e *Executor) directive(plan *Plan, ex *Execution, s Step) string {
	var sb strings.Builder
	if s.N == 1 || allPending(ex, s.N) {
		fmt.Fprintf(&sb, "## Plan (execute it one step at a time)\n%s\n\n", plan.Text())
	}
	fmt.Fprintf(&sb, "Now carry out ONLY step %d: %s", s.N, s.Description)
	if s.Target != "" {
		fmt.Fprintf(&sb, " (target: %s)", s.Target)
	}
	if s.Tool != "" {
		fmt.Fprintf(&sb, " (tool object: %s)", s.Tool)
	}
	sb.WriteString(".\n")
	if t := ex.refsText(); t != "" {
		sb.WriteString(t + "\n")
	}
	sb.WriteString("Use the feature ids above (never invent ids). Do not start later steps. When this step is done, reply with ONE short sentence saying what you created or changed (feature ids and key dimensions). If it cannot be done, say why in one sentence.")
	return sb.String()
}

func allPending(ex *Execution, upTo int) bool {
	for i := 0; i < upTo-1 && i < len(ex.Steps); i++ {
		if ex.Steps[i].Status != StepPending {
			return false
		}
	}
	return true
}

// record turns a step's tool calls into its structured result and updates the references.
func (ex *Execution) record(s Step, r *StepResult, turn *runtime.Turn) {
	r.Note = oneLine(turn.Final, 240)
	r.Error, r.Objects = "", nil
	failures := map[string]string{} // tool → last unrecovered error
	mutated := false
	for _, call := range turn.Calls {
		o := call.Outcome
		if !o.OK {
			failures[call.Name] = o.Error
			continue
		}
		delete(failures, call.Name)
		if o.ChangeSet == nil {
			continue
		}
		mutated = true
		r.ChangeSets = append(r.ChangeSets, o.ChangeSet.ID)
		if !o.Committed {
			r.Status = StepApproval
			r.Error = "waiting for the user's approval of " + o.ChangeSet.ID
			return
		}
		for _, id := range o.ChangeSet.Effects.Added {
			r.Objects = append(r.Objects, id)
			ex.Owner[id] = s.N
		}
		for _, id := range o.ChangeSet.Effects.Modified {
			ex.Owner[id] = s.N
		}
	}
	switch {
	case turn.Exhausted:
		r.Status, r.Error = StepFailed, "the step did not finish within the tool-call budget"
	case len(failures) > 0:
		var errs []string
		for tool, msg := range failures {
			errs = append(errs, tool+": "+msg)
		}
		sort.Strings(errs)
		r.Status, r.Error = StepFailed, strings.Join(errs, "; ")
	case !mutated && len(turn.Calls) == 0 && strings.Contains(turn.Final, "?"):
		r.Status = StepNeedInfo
	default:
		r.Status = StepSuccess
	}
	if s.Ref != "" && len(r.Objects) > 0 {
		ex.Refs[s.Ref] = r.Objects[0]
	}
}

func oneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}
