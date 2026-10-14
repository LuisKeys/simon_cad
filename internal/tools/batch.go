package tools

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/accelone/simoncad/internal/cadir"
	"github.com/accelone/simoncad/internal/commands"
)

// CapUI tags tools that only the UI and the CLI call. No agent capability includes them, so
// the agent never sees them (see batch).
const CapUI = "ui"

// maxBatchCalls bounds one batch.
const maxBatchCalls = 100

// batchEnv plans one call of a batch against the graph as the earlier calls of the batch left
// it, so the ids they generate stay unique. Geometry queries (Model) still see the model as it
// is now: a call can act on what exists, not on something an earlier call of the batch creates.
type batchEnv struct {
	Env
	g *cadir.Graph
}

func (e batchEnv) Graph() *cadir.Graph { return e.g }

// batchTool builds the batch tool over r. It runs several model-editing tool calls as ONE
// ChangeSet, so an operation on many objects (suppress, scale, extrude several faces) is a
// single undo step and either applies completely or not at all, instead of N independent
// changes that can fail half way. It is for user gestures in the UI and CLI; it has the UI
// capability, so the agent cannot use it to get around the limit on structural changes per
// iteration.
func batchTool(r *Registry) *Tool {
	return &Tool{
		Name: "batch",
		Description: "Run several model-editing tool calls as one change: one ChangeSet, one undo step, all or nothing. " +
			"Each call acts on the model as it is now (plus the ids earlier calls of the batch generate); " +
			"a call cannot refer to a feature created by an earlier call of the same batch.",
		Capability: CapUI,
		Schema: object(props{
			"calls": array("The calls, in order", object(props{
				"tool": text("Name of a tool that edits the model"),
				"args": map[string]any{"type": "object", "description": "Arguments of that tool"},
			}, "tool"), 1, maxBatchCalls),
			"intent": text("Why"),
		}, "calls"),
		Plan: func(env Env, a Args) (*Plan, error) {
			calls, _ := a["calls"].([]any)
			g := env.Graph().Clone()
			out := &Plan{LowRisk: true}
			var explained []string
			for i, c := range calls {
				m, _ := c.(map[string]any)
				name, _ := m["tool"].(string)
				label := fmt.Sprintf("call %d (%s)", i+1, name)
				t, ok := r.Get(name)
				switch {
				case !ok:
					return nil, fmt.Errorf("%s: unknown tool", label)
				case !t.Mutating():
					return nil, fmt.Errorf("%s: only tools that edit the model can be batched", label)
				case t.Capability == CapUI:
					return nil, fmt.Errorf("%s: a batch cannot contain a batch", label)
				}
				raw, err := json.Marshal(m["args"])
				if err != nil {
					return nil, fmt.Errorf("%s: %w", label, err)
				}
				if m["args"] == nil {
					raw = []byte("{}")
				}
				res, err := r.Invoke(batchEnv{env, g}, name, raw)
				if err != nil {
					return nil, fmt.Errorf("%s: %w", label, err)
				}
				p := res.Plan
				for _, cmd := range p.Commands {
					if _, err := cmd.Apply(g); err != nil {
						return nil, fmt.Errorf("%s: %w", label, err)
					}
				}
				out.Commands = append(out.Commands, p.Commands...)
				out.Warnings = append(out.Warnings, p.Warnings...)
				out.RequireApproval = out.RequireApproval || p.RequireApproval
				out.LowRisk = out.LowRisk && lowRiskPlan(p)
				if p.Explanation != "" {
					explained = append(explained, p.Explanation)
				}
			}
			out.Intent = intentOr(a, fmt.Sprintf("Apply %d changes together", len(calls)))
			out.Explanation = strings.Join(explained, " ")
			return out, nil
		},
	}
}

// lowRiskPlan reports whether a plan alone would commit without approval under the auto
// policy: the plan says so, or it is a single non-destructive command (the rule of
// ChangeSet.AutoCommitEligible). A batch is low risk only if every one of its calls is.
func lowRiskPlan(p *Plan) bool {
	if p.RequireApproval || len(p.Warnings) > 0 {
		return false
	}
	if p.LowRisk {
		return true
	}
	if len(p.Commands) != 1 {
		return false
	}
	return !destructive(p.Commands[0])
}

func destructive(c commands.Command) bool {
	switch c.Type() {
	case "RemoveFeature", "RestoreSnapshot":
		return true
	}
	return false
}
