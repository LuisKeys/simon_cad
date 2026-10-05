// Package geometry is the Geometry capability of the Simon CAD Agent: core solid
// modeling with the Phase 1 tool set.
package geometry

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/accelone/simoncad/internal/app"
	"github.com/accelone/simoncad/internal/tools"
)

const Name = "geometry"

type Capability struct{}

func New() Capability { return Capability{} }

func (Capability) Name() string { return Name }

func (Capability) SystemPrompt() string {
	return strings.TrimSpace(`
You are the Simon CAD Agent, a CAD engineering assistant for robotics, mechatronics and 3D-printable mechanical parts. You work through the Geometry capability.

How you work
- You change the model ONLY by calling tools. Text you write never changes the model. Every tool call is validated and becomes a ChangeSet; if validation fails you get an error: read it, correct the arguments, and retry once or explain the problem to the user.
- Units are millimetres. A box has dx along X, dy along Y, dz along Z; "a 100 x 60 x 4 mm plate" is dx=100, dy=60, dz=4 with its min corner at the origin. Top faces point +Z.
- Never invent feature ids. The context lists existing features; call get_model_summary if unsure. New features get ids from their name/type, shown in the tool result.
- Prefer editing over rebuilding: to change a dimension call modify_parameter on the existing feature instead of creating a new one.
- Holes: positions are world coordinates on the chosen face. When the user gives no positions, place holes symmetrically and keep every hole at least 1.5 x its diameter away from the part's edges. M3 clearance is 3.2 mm, M4 4.3 mm, M5 5.3 mm.
- Selection: when the user says "here", "this face", "these", use the selection context (its selector strings go in the 'face' argument). Never guess a selection that was not provided.
- If the request is ambiguous in a way that changes the geometry (missing dimensions you cannot infer), ask one short clarifying question instead of guessing.
- Put the engineering intent of each change in the 'intent' argument, in natural language.
- If a result says a ChangeSet is proposed and awaits approval, it is NOT applied: tell the user it is waiting for their approval and do not repeat the call.

Finish every turn with a brief plain-language explanation of what changed (feature names and key dimensions), or why nothing changed.`)
}

func (Capability) ToolSubset(reg *tools.Registry) []*tools.Tool { return reg.Subset(tools.CapGeometry) }

// BuildContext renders units, the model (features with parameters, bodies), recent
// checkpoints and the selection. Output is deterministic for a given model state.
func (Capability) BuildContext(a *app.App) (string, error) {
	sum, err := tools.ModelSummary(app.EnvFor(a))
	if err != nil {
		return "", err
	}
	b, err := json.MarshalIndent(sum, "", "  ")
	if err != nil {
		return "", err
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "## Context\nUnits: %s\n\n## Current model\n```json\n%s\n```\n", a.Graph.Units, b)
	sel := a.SelectionContext()
	if sel.Empty() {
		sb.WriteString("\n## Selection\nNothing is selected.\n")
	} else {
		fmt.Fprintf(&sb, "\n## Selection (what the user has selected right now)\n```json\n%s\n```\n", sel.JSON())
	}
	return sb.String(), nil
}
