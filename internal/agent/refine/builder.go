package refine

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	geo "github.com/accelone/simoncad/internal/agent/capabilities/geometry"
	visualcap "github.com/accelone/simoncad/internal/agent/capabilities/visual"
	"github.com/accelone/simoncad/internal/agent/runtime"
	"github.com/accelone/simoncad/internal/app"
	"github.com/accelone/simoncad/internal/tools"
	"github.com/accelone/simoncad/internal/visual"
)

// Strategy is the Planner's modeling strategy.
type Strategy struct {
	ObjectClass  string             `json:"object_class"`
	ParameterSet string             `json:"parameter_set"`
	Parameters   map[string]float64 `json:"parameters"`
	Regions      []struct {
		Name       string   `json:"name"`
		Objects    []string `json:"objects"`
		Parameters []string `json:"parameters"`
	} `json:"regions"`
	Stages map[string][]string `json:"stages"` // stage name → planned elements
	Notes  string              `json:"notes,omitempty"`
}

const plannerPrompt = `You are the Planner of a CAD system. From the user's goal and the analysis of the reference images you write a coarse-to-fine modeling strategy; another agent builds it. Answer only by calling submit_strategy.

- parameter_set + parameters: the design parameters (mm, degrees) that control the major geometry, with values derived from the known dimensions and the reference proportions (e.g. for a car: length, width, height, wheelbase, front_overhang, rear_overhang, ground_clearance, wheel_radius, hood_height, hood_length, roof_height, cabin_start, cabin_end, windshield_angle). When no real dimension is known, choose a plausible real-world scale for the object class and keep the proportions exact.
- regions: the visual regions (roof, front_hood, cabin, wheels, rear_body, ...) with the object ids you plan to create and the parameters that shape each region.
- stages: what to build in BLOCKOUT (main body, cabin, major volumes, wheels only), PRIMARY (windshield, windows, wheel arches, doors, hood, major intakes, bumpers), SECONDARY (lights, mirrors, exhausts, vents, trim) and MATERIALS (paint, glass, rubber, metal, plastic, emissive). Keep BLOCKOUT to simple primitives.`

func strategyTool() runtime.ToolDef {
	return runtime.ToolDef{Name: "submit_strategy", Description: "Submit the modeling strategy.", InputSchema: obj(map[string]any{
		"object_class":  str(),
		"parameter_set": str(),
		"parameters":    map[string]any{"type": "object", "additionalProperties": num()},
		"regions":       arr(obj(map[string]any{"name": str(), "objects": arr(str()), "parameters": arr(str())}, "name")),
		"stages":        map[string]any{"type": "object", "additionalProperties": arr(str())},
		"notes":         str(),
	}, "object_class", "parameter_set", "parameters", "regions", "stages")}
}

// Planner writes the modeling strategy (text only: it reads the reference analysis).
type Planner struct {
	Provider runtime.Provider
	Usage    runtime.Usage
}

// Plan returns the strategy and its raw JSON.
func (p *Planner) Plan(ctx context.Context, goal string, refs []*visual.Reference, geometry map[string]float64, context string) (*Strategy, json.RawMessage, error) {
	var sb strings.Builder
	fmt.Fprintf(&sb, "## Goal\n%s\n\n## Reference analysis\n", goal)
	for _, r := range refs {
		b, _ := json.Marshal(map[string]any{"image": r.ImageID, "object_class": r.ObjectClass, "calibration": r.Calibration, "landmarks": r.Landmarks,
			"known_dimensions_mm": r.Dimensions, "notes": r.Notes, "size_px": []int{r.Width, r.Height}})
		sb.Write(b)
		sb.WriteString("\n")
	}
	b, _ := json.Marshal(geometry)
	fmt.Fprintf(&sb, "\n## Reference proportions (normalized)\n%s\n\n%s", b, context)
	raw, err := structured(ctx, p.Provider, plannerPrompt, []runtime.Block{{Type: "text", Text: sb.String()}}, strategyTool(), &p.Usage)
	if err != nil {
		return nil, nil, err
	}
	var s Strategy
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, nil, fmt.Errorf("strategy: %w", err)
	}
	return &s, raw, nil
}

// Builder is the CADBuilder: it executes geometry operations through the ordinary agent
// tool loop (validation → ChangeSet → commit). Each call is a helper run inside the
// refinement prompt.
type Builder struct {
	Provider   runtime.Provider
	NoThinking bool
	OnStep     func(name string, args json.RawMessage, o app.Outcome)
	Usage      runtime.Usage
}

func (b *Builder) runtime(maxStructural int) *runtime.Runtime {
	return &runtime.Runtime{Provider: b.Provider, Caps: []runtime.Capability{geo.New(), visualcap.New()}, MaxSteps: 60,
		NoThinking: b.NoThinking, MaxStructural: maxStructural, Subtask: true, OnStep: b.OnStep}
}

// run sends one directive and returns the turn and the ChangeSets it committed.
func (b *Builder) run(ctx context.Context, a *app.App, directive string, maxStructural int) (*runtime.Turn, []string, error) {
	c, err := b.runtime(maxStructural).Begin(ctx, a, directive, app.PolicyCommit)
	if err != nil {
		return nil, nil, err
	}
	turn, err := c.Send(ctx, "")
	b.Usage.InputTokens += c.Res.Usage.InputTokens
	b.Usage.OutputTokens += c.Res.Usage.OutputTokens
	return turn, c.Res.Committed, err
}

// Build creates the elements of a stage.
func (b *Builder) Build(ctx context.Context, a *app.App, stage visual.Stage, goal string, strat *Strategy, geometry map[string]float64) (*runtime.Turn, []string, error) {
	var sb strings.Builder
	fmt.Fprintf(&sb, "## Visual modeling — build stage %s\nGoal: %s\nBuild now: %s.\n", stage.Name, goal, stage.Build)
	if stage.Forbidden != "" {
		fmt.Fprintf(&sb, "Do NOT add yet: %s.\n", stage.Forbidden)
	}
	if strat != nil {
		sj, _ := json.Marshal(strat)
		fmt.Fprintf(&sb, "\nModeling strategy (from the planner):\n%s\n", sj)
		if items := strat.Stages[stage.Name]; len(items) > 0 {
			fmt.Fprintf(&sb, "Planned for this stage: %s\n", strings.Join(items, "; "))
		}
	}
	if len(geometry) > 0 {
		gj, _ := json.Marshal(geometry)
		fmt.Fprintf(&sb, "Reference proportions to match: %s\n", gj)
	}
	if stage.Name == visual.Stages[0].Name {
		sb.WriteString("\nSteps: (1) define_parameters with the strategy's parameter set (if it does not exist yet); (2) create each major volume as a simple primitive; (3) bind_parameters so every size and position is an expression over the parameter set; (4) define_region for every region with its objects and parameters. Units are mm; X is the object's length (front towards +X), Y its width (left side towards +Y), Z up, resting on Z=0.\n")
	} else {
		sb.WriteString("\nAdd the stage's elements to the existing model without changing correct global proportions; bind new features to the design parameters where sensible and add them to the matching regions (define_region keeps the existing members only if you list them again). Never modify locked regions.\n")
	}
	sb.WriteString("\n" + visualcap.Rules + "\nWhen done, reply with one short paragraph listing what you created.")
	return b.run(ctx, a, sb.String(), 0)
}

// Apply carries out at most max structural corrections.
func (b *Builder) Apply(ctx context.Context, a *app.App, stage visual.Stage, corrections []visual.Correction, max int) (*runtime.Turn, []string, error) {
	vs := a.Visual
	vs.Lock()
	var sb strings.Builder
	fmt.Fprintf(&sb, "## Visual refinement — stage %s\nApply EXACTLY these corrections, nothing else (at most %d structural changes; the model is re-rendered and measured right after, and changes that make it look worse are reverted automatically):\n", stage.Name, max)
	for i, c := range corrections {
		fmt.Fprintf(&sb, "%d. [%s] %s", i+1, c.Region, c.Problem)
		if c.Parameter != "" {
			fmt.Fprintf(&sb, " — suggested: set %s to %g", c.Parameter, c.Value)
		} else if c.Axis != "" && c.DeltaMM != 0 {
			fmt.Fprintf(&sb, " — suggested: move/resize along %s by %+g mm", c.Axis, c.DeltaMM)
		}
		if r := vs.Region(c.Region); r != nil {
			fmt.Fprintf(&sb, " (region objects %v, parameters %v)", r.Objects, r.Parameters)
		}
		sb.WriteString("\n")
	}
	var locked []string
	for _, r := range vs.Regions {
		if r.Locked {
			locked = append(locked, r.Name)
		}
	}
	vs.Unlock()
	if len(locked) > 0 {
		fmt.Fprintf(&sb, "Locked regions (never modify): %s.\n", strings.Join(locked, ", "))
	}
	params := tools.ParameterValues(a.Graph)
	if len(params) > 0 {
		keys := make([]string, 0, len(params))
		for k := range params {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var ps []string
		for _, k := range keys {
			ps = append(ps, fmt.Sprintf("%s=%g", k, params[k]))
		}
		fmt.Fprintf(&sb, "Current design parameters: %s\n", strings.Join(ps, ", "))
	}
	sb.WriteString("Prefer set_parameter on the region's parameters over editing or rebuilding objects. Do not add details. When done, reply with one sentence per correction saying what you changed.")
	return b.run(ctx, a, sb.String(), max)
}
