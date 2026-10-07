package workflow

import (
	"context"
	"fmt"
	"strings"

	"github.com/accelone/simoncad/internal/agent/runtime"
	"github.com/accelone/simoncad/internal/app"
)

// Agent orchestrates the workflow around one runtime. Simple requests (and requests with
// images, which follow the interactive vision protocol) run as a single agent turn,
// exactly like runtime.Run; complex ones are planned, executed step by step, validated
// and corrected.
type Agent struct {
	Runtime   *runtime.Runtime
	Validator Validator
	// MaxCorrections defaults to MaxAutocorrectionAttempts.
	MaxCorrections int
	// NeedsPlan decides whether a request is planned (default: NeedsPlan).
	NeedsPlan func(intent string) bool

	OnState func(Event)
	OnPlan  func(*Plan)
	// OnRevert is told which committed ChangeSets a failed correction undid.
	OnRevert func(ids []string)
}

// Result is the outcome of one workflow run.
type Result struct {
	*runtime.Result
	State      State      `json:"state"`
	Plan       *Plan      `json:"plan,omitempty"`
	Execution  *Execution `json:"execution,omitempty"`
	Validation *Report    `json:"validation,omitempty"`
	Attempts   int        `json:"attempts"`
}

func (ag *Agent) emit(ev Event) {
	if ag.OnState != nil {
		ag.OnState(ev)
	}
}

// Run handles one user intent.
func (ag *Agent) Run(ctx context.Context, a *app.App, intent string, policy app.Policy) (*Result, error) {
	rt := ag.Runtime
	needs := ag.NeedsPlan
	if needs == nil {
		needs = NeedsPlan
	}
	out := &Result{State: StateIdle}
	c, err := rt.Begin(ctx, a, intent, policy)
	out.Result = c.Result()
	if err != nil {
		ag.fail(out, err)
		return out, err
	}
	if len(rt.ImageIDs) > 0 || !needs(intent) {
		return out, ag.single(ctx, c, out)
	}

	ag.emit(Event{State: StatePlanning, Message: "Planning…"})
	_, defs := toolDefs(rt, a)
	plan, usage, err := Planner{Provider: rt.Provider, NoThinking: rt.NoThinking, Tools: defs}.Plan(ctx, c.Msgs, c.Context(), intent)
	c.Res.Usage.InputTokens += usage.InputTokens
	c.Res.Usage.OutputTokens += usage.OutputTokens
	if err != nil {
		ag.fail(out, err)
		return out, err
	}
	if plan == nil || executable(plan) < 2 { // nothing to orchestrate
		return out, ag.single(ctx, c, out)
	}
	out.Plan = plan
	if ag.OnPlan != nil {
		ag.OnPlan(plan)
	}

	ex := NewExecution(plan)
	out.Execution = ex
	g := a.Snapshot()
	for _, s := range plan.Steps { // refs that name existing features
		for _, ref := range []string{s.Ref, s.Target, s.Tool} {
			if ref != "" && plan.Producer(ref) == 0 && g.Get(ref) != nil {
				ex.Refs[ref] = ref
			}
		}
	}
	exe := &Executor{OnStep: func(s Step, total int) {
		ag.emit(Event{State: StateExecuting, Message: progressLabel(s), Step: s.N, Total: total})
	}}
	if err := exe.Run(ctx, c, plan, ex, nil, ""); err != nil {
		ag.fail(out, err)
		return out, err
	}
	if st := ex.Stopped(); st != nil {
		return out, ag.stopped(c, out, st)
	}

	validator := ag.Validator
	if validator == (Validator{}) {
		validator = DefaultValidator
	}
	ag.emit(Event{State: StateValidating, Message: "Validating model…", Total: len(plan.Steps)})
	rep, err := validator.Validate(a, plan, ex)
	if err != nil {
		ag.fail(out, err)
		return out, err
	}
	max := ag.MaxCorrections
	if max == 0 {
		max = MaxAutocorrectionAttempts
	}
	corrector := &Corrector{Executor: exe}
	var reverted []string
	for !rep.OK && out.Attempts < max {
		out.Attempts++
		ag.emit(Event{State: StateCorrecting, Message: correctingLabel(plan, rep), Attempt: out.Attempts, Total: len(plan.Steps)})
		mark := len(c.Res.Committed)
		if err := corrector.Correct(ctx, c, plan, ex, rep, out.Attempts); err != nil {
			ag.fail(out, err)
			return out, err
		}
		if st := ex.Stopped(); st != nil {
			out.Validation = rep
			return out, ag.stopped(c, out, st)
		}
		ag.emit(Event{State: StateValidating, Message: "Validating model…", Attempt: out.Attempts, Total: len(plan.Steps)})
		next, err := validator.Validate(a, plan, ex)
		if err != nil {
			ag.fail(out, err)
			return out, err
		}
		if next.HardCount() > rep.HardCount() {
			// The repair broke the model: return to the latest valid state.
			ids, err := revert(a, c.Res.Committed[mark:])
			reverted = append(reverted, ids...)
			if len(ids) > 0 && ag.OnRevert != nil {
				ag.OnRevert(ids)
			}
			if err != nil {
				ag.fail(out, err)
				return out, err
			}
			c.Res.Committed = c.Res.Committed[:mark]
			continue
		}
		rep = next
	}
	out.Validation = rep
	c.Res.Final = summary(plan, ex, rep, out.Attempts, reverted)
	if rep.OK {
		out.State = StateCompleted
		ag.emit(Event{State: StateCompleted, Message: "Done."})
	} else {
		out.State = StateFailed
		ag.emit(Event{State: StateFailed, Message: "Finished with unresolved problems."})
	}
	c.Finish()
	return out, nil
}

// single runs the request as one ordinary agent turn.
func (ag *Agent) single(ctx context.Context, c *runtime.Conversation, out *Result) error {
	ag.emit(Event{State: StateExecuting, Message: "Working…"})
	turn, err := c.Send(ctx, "")
	if err != nil {
		ag.fail(out, err)
		return err
	}
	c.Res.Final = turn.Final
	c.Finish()
	out.State = StateCompleted
	ag.emit(Event{State: StateCompleted, Message: "Done."})
	return nil
}

// stopped ends the run at a step that needs the user: the agent's question, or the note
// that a ChangeSet waits for approval.
func (ag *Agent) stopped(c *runtime.Conversation, out *Result, st *StepResult) error {
	if st.Status == StepApproval {
		c.Res.Final = fmt.Sprintf("Step %d of the plan produced a ChangeSet that is waiting for your approval; the remaining steps continue after you commit it and ask again. %s", st.Step, st.Note)
	} else {
		c.Res.Final = st.Note
	}
	c.Finish()
	out.State = StateCompleted
	ag.emit(Event{State: StateCompleted, Message: "Waiting for you."})
	return nil
}

func (ag *Agent) fail(out *Result, err error) {
	out.State = StateFailed
	ag.emit(Event{State: StateFailed, Message: err.Error()})
}

// revert undoes the given committed ChangeSets, newest first, as long as they are the
// latest ones on the undo stack.
func revert(a *app.App, ids []string) ([]string, error) {
	var done []string
	for i := len(ids) - 1; i >= 0; i-- {
		cs, err := a.Undo()
		if err != nil {
			return done, err
		}
		if cs.ID != ids[i] {
			if _, rerr := a.Redo(); rerr != nil {
				return done, rerr
			}
			return done, fmt.Errorf("the model changed during correction; could not revert %s", ids[i])
		}
		done = append(done, cs.ID)
	}
	return done, nil
}

func toolDefs(rt *runtime.Runtime, a *app.App) (map[string]bool, []runtime.ToolDef) {
	allowed := map[string]bool{}
	var defs []runtime.ToolDef
	for _, cp := range rt.Caps {
		for _, t := range cp.ToolSubset(a.Reg) {
			if !allowed[t.Name] {
				allowed[t.Name] = true
				defs = append(defs, runtime.ToolDef{Name: t.Name, Description: t.Description, InputSchema: t.Schema})
			}
		}
	}
	return allowed, defs
}

func executable(p *Plan) int {
	n := 0
	for _, s := range p.Steps {
		if s.Operation != "validate" {
			n++
		}
	}
	return n
}

// gerunds turn the leading verb of a step description into a progress verb.
var gerunds = map[string]string{
	"create": "Creating", "add": "Adding", "make": "Making", "build": "Building", "draw": "Drawing", "sketch": "Sketching",
	"extrude": "Extruding", "revolve": "Revolving", "subtract": "Subtracting", "cut": "Cutting", "drill": "Drilling",
	"join": "Joining", "union": "Joining", "combine": "Combining", "merge": "Merging", "intersect": "Intersecting",
	"fillet": "Filleting", "round": "Rounding", "chamfer": "Chamfering", "pattern": "Patterning", "mirror": "Mirroring",
	"move": "Moving", "position": "Positioning", "place": "Placing", "rotate": "Rotating", "align": "Aligning", "scale": "Scaling",
	"apply": "Applying", "assign": "Assigning", "set": "Setting", "color": "Coloring", "paint": "Painting",
	"modify": "Modifying", "change": "Changing", "resize": "Resizing", "delete": "Deleting", "remove": "Removing",
	"group": "Grouping", "validate": "Validating", "check": "Checking", "duplicate": "Duplicating", "copy": "Copying",
}

// progressLabel turns a step into a progress message ("Create base body" → "Creating base body…").
func progressLabel(s Step) string {
	d := strings.TrimRight(strings.TrimSpace(s.Description), ".")
	if d == "" {
		return fmt.Sprintf("Step %d…", s.N)
	}
	first, rest, _ := strings.Cut(d, " ")
	if g, ok := gerunds[strings.ToLower(first)]; ok {
		return strings.TrimSpace(g+" "+rest) + "…"
	}
	return d + "…"
}

func correctingLabel(p *Plan, rep *Report) string {
	for _, is := range rep.Issues {
		if is.Step > 0 {
			d := strings.TrimRight(p.Steps[is.Step-1].Description, ".")
			return fmt.Sprintf("Correcting step %d (%s)…", is.Step, strings.ToLower(d[:1])+d[1:])
		}
	}
	return "Correcting the model…"
}

// summary is the final explanation of a planned run.
func summary(plan *Plan, ex *Execution, rep *Report, attempts int, reverted []string) string {
	var sb strings.Builder
	if plan.Goal != "" {
		sb.WriteString(strings.TrimRight(plan.Goal, ".") + ".\n")
	}
	for _, s := range plan.Steps {
		r := ex.Result(s.N)
		mark := map[StepStatus]string{StepSuccess: "✓", StepFailed: "✗", StepSkipped: "–"}[r.Status]
		if mark == "" {
			mark = "·"
		}
		fmt.Fprintf(&sb, "%s %d. %s", mark, s.N, s.Description)
		if len(r.Objects) > 0 {
			fmt.Fprintf(&sb, " → %s", strings.Join(r.Objects, ", "))
		}
		if r.Note != "" && r.Status == StepSuccess && s.Operation != "validate" {
			fmt.Fprintf(&sb, " — %s", r.Note)
		} else if r.Error != "" {
			fmt.Fprintf(&sb, " — %s", r.Error)
		}
		sb.WriteString("\n")
	}
	switch {
	case rep.OK && attempts == 0:
		fmt.Fprintf(&sb, "Validation passed (%d checks).", rep.Checks)
	case rep.OK:
		fmt.Fprintf(&sb, "Validation passed after %d automatic correction(s) (%d checks).", attempts, rep.Checks)
	default:
		fmt.Fprintf(&sb, "Unresolved after %d automatic correction attempt(s); the model is left in its latest valid state:\n%s", attempts, rep.Text())
	}
	if len(reverted) > 0 {
		fmt.Fprintf(&sb, "\nReverted correction changes that made the model worse: %s.", strings.Join(reverted, ", "))
	}
	return sb.String()
}
