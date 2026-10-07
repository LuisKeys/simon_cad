package workflow

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/accelone/simoncad/internal/agent/runtime"
)

// MaxAutocorrectionAttempts bounds the correction loop.
const MaxAutocorrectionAttempts = 3

// Corrector repairs what validation found, touching only the steps that probably caused
// each problem: failed steps (and the steps skipped because of them) are re-executed;
// other problems get one targeted repair request naming the responsible steps.
type Corrector struct {
	Executor *Executor
}

// Correct runs one correction attempt.
func (co *Corrector) Correct(ctx context.Context, c *runtime.Conversation, plan *Plan, ex *Execution, rep *Report, attempt int) error {
	rerun := co.stepsToRerun(plan, ex, rep)
	redo := map[int]bool{}
	for _, n := range rerun {
		redo[n] = true
	}
	// Problems attributed to a step being re-executed are its consequences (e.g. its object
	// is missing); the next validation tells whether they remain.
	var other []Issue
	for _, is := range rep.Issues {
		if is.Kind != IssueStepFailed && is.Kind != IssueNotCompleted && !redo[is.Step] {
			other = append(other, is)
		}
	}
	if len(rerun) > 0 {
		var hint strings.Builder
		fmt.Fprintf(&hint, "## Automatic correction (attempt %d of %d)\nValidation found that these steps did not complete:\n", attempt, MaxAutocorrectionAttempts)
		for _, n := range rerun {
			if r := ex.Result(n); r.Status == StepFailed {
				fmt.Fprintf(&hint, "- step %d (%s) failed: %s\n", n, plan.Steps[n-1].Description, r.Error)
			}
		}
		hint.WriteString("Read the errors, correct the arguments and redo only these steps. Do not recreate objects from steps that succeeded.")
		for _, n := range rerun {
			ex.Result(n).Status = StepPending
		}
		if err := co.Executor.Run(ctx, c, plan, ex, rerun, hint.String()); err != nil {
			return err
		}
		if ex.Stopped() != nil {
			return nil
		}
	}
	if len(other) == 0 {
		return nil
	}
	ctxText, err := c.Runtime().BuildContext(c.App())
	if err != nil {
		return err
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "## Automatic correction (attempt %d of %d)\nValidation of the model against the plan found these problems:\n", attempt, MaxAutocorrectionAttempts)
	for _, is := range other {
		sb.WriteString("- ")
		if is.Step > 0 {
			fmt.Fprintf(&sb, "[probably caused by step %d: %s] ", is.Step, plan.Steps[is.Step-1].Description)
		}
		sb.WriteString(is.Message + "\n")
	}
	if t := ex.refsText(); t != "" {
		sb.WriteString(t + "\n")
	}
	sb.WriteString("Strategy: fix ONLY these problems with the smallest change. Edit the features involved (modify_parameter, face edits, set_material, set_color, align...) instead of recreating them; only recreate a feature if it is missing or cannot be repaired. Do not touch features that are correct and never rebuild the whole model.\n\n")
	sb.WriteString(ctxText)
	sb.WriteString("\nWhen done, reply with ONE short sentence describing the fix.")
	turn, err := c.Send(ctx, sb.String())
	if err != nil {
		return err
	}
	co.absorb(plan, ex, other, turn)
	return nil
}

// stepsToRerun returns the failed steps and the steps skipped or left pending because of
// them, in plan order.
func (co *Corrector) stepsToRerun(plan *Plan, ex *Execution, rep *Report) []int {
	set := map[int]bool{}
	for _, is := range rep.Issues {
		if (is.Kind == IssueStepFailed || is.Kind == IssueNotCompleted) && is.Step > 0 {
			set[is.Step] = true
		}
	}
	out := make([]int, 0, len(set))
	for n := range set {
		out = append(out, n)
	}
	sort.Ints(out)
	return out
}

// absorb records what a repair turn changed: ownership of touched features, and a new
// binding for a missing object that the repair recreated.
func (co *Corrector) absorb(plan *Plan, ex *Execution, issues []Issue, turn *runtime.Turn) {
	var added []string
	blame := 0
	for _, is := range issues {
		if is.Step > 0 {
			blame = is.Step
			break
		}
	}
	for _, call := range turn.Calls {
		o := call.Outcome
		if !o.OK || o.ChangeSet == nil || !o.Committed {
			continue
		}
		added = append(added, o.ChangeSet.Effects.Added...)
		if blame > 0 {
			for _, id := range append(append([]string(nil), o.ChangeSet.Effects.Added...), o.ChangeSet.Effects.Modified...) {
				ex.Owner[id] = blame
			}
			r := ex.Result(blame)
			r.ChangeSets = append(r.ChangeSets, o.ChangeSet.ID)
		}
	}
	var missing []Issue
	for _, is := range issues {
		if is.Kind == IssueMissing && is.Ref != "" {
			missing = append(missing, is)
		}
	}
	if len(missing) == 1 && len(added) > 0 {
		ex.Refs[missing[0].Ref] = added[0]
	}
}
