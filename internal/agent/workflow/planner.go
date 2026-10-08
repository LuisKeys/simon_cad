package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/accelone/simoncad/internal/agent/runtime"
)

// Plan is the planner's structured breakdown of a complex request.
type Plan struct {
	Goal         string       `json:"goal"`
	Analysis     Analysis     `json:"analysis"`
	Steps        []Step       `json:"steps"`
	Expectations Expectations `json:"expectations"`
}

// Analysis is what the planner identified before ordering the steps.
type Analysis struct {
	Primitives      []string `json:"primitives,omitempty"`
	Sketches        []string `json:"sketches,omitempty"`
	Extrusions      []string `json:"extrusions,omitempty"`
	Booleans        []string `json:"booleans,omitempty"`
	Transformations []string `json:"transformations,omitempty"`
	Materials       []string `json:"materials,omitempty"`
	Dimensions      []string `json:"dimensions,omitempty"`
}

// Step is one operation of the plan.
type Step struct {
	N           int    `json:"step"`
	Operation   string `json:"operation"`
	Description string `json:"description"`
	// Ref names the object this step produces so later steps (and the validator) can refer to it.
	Ref       string `json:"ref,omitempty"`
	Target    string `json:"target,omitempty"` // ref of the object the step acts on
	Tool      string `json:"tool,omitempty"`   // ref of the tool object of a boolean
	DependsOn []int  `json:"depends_on,omitempty"`
}

// Expectations describe the requested result; the validator checks them.
type Expectations struct {
	Objects []ObjectExpectation `json:"objects,omitempty"`
	// BodyCount is the number of separate visible bodies the model should have (0: not checked).
	BodyCount int `json:"body_count,omitempty"`
}

// ObjectExpectation is what one referenced object should look like.
type ObjectExpectation struct {
	Ref string `json:"ref"`
	// Size is the overall extent in mm along x, y, z of the object's own body.
	Size map[string]float64 `json:"size,omitempty"`
	// Parameters are expected feature parameters (dx, diameter, radius, height...).
	Parameters map[string]float64 `json:"parameters,omitempty"`
	Min        []float64          `json:"min,omitempty"`    // expected bounding-box min corner
	Center     []float64          `json:"center,omitempty"` // expected bounding-box center
	Material   string             `json:"material,omitempty"`
	Color      string             `json:"color,omitempty"` // #RRGGBB
}

// Operations the planner may use.
var Operations = []string{"primitive", "sketch", "extrude", "boolean_union", "boolean_subtract", "boolean_intersect",
	"hole", "fillet", "chamfer", "pattern", "transform", "material", "appearance", "modify", "delete", "validate", "other"}

// ---- complexity -----------------------------------------------------------------------

// opGroups are the families of CAD operations a request can mention. Construction groups
// build or cut geometry; the rest (appearance, transform, delete) are simple edits.
var opGroups = []struct {
	name         string
	construction bool
	words        []string
}{
	{"primitive", true, []string{"box", "cube", "cylinder", "sphere", "cone", "torus", "plate", "block", "prism", "tube", "bracket", "housing", "enclosure", "flange", "shaft", "wedge", "body", "base", "mount", "case", "lid"}},
	{"sketch", true, []string{"sketch", "profile", "polygon", "outline"}},
	{"extrude", true, []string{"extrud", "revolve", "loft", "sweep"}},
	{"subtract", true, []string{"subtract", "cut", "pocket", "carve", "boolean", "difference"}},
	{"union", true, []string{"union", "join", "merge", "combine", "fuse"}},
	{"hole", true, []string{"hole", "bore", "counterbore", "countersink", "slot"}},
	{"edge", true, []string{"fillet", "round", "chamfer", "bevel"}},
	{"pattern", true, []string{"pattern", "array", "mirror"}},
	{"transform", false, []string{"rotate", "translate", "move", "align", "scale", "position", "center"}},
	{"appearance", false, []string{"color", "colour", "material", "paint", "transparent", "opacity", "hide", "abs", "pla", "aluminum", "aluminium", "steel"}},
	{"delete", false, []string{"delete", "remove"}},
}

var (
	wordRe     = regexp.MustCompile(`[a-z0-9áéíóúñ]+`)
	numberedRe = regexp.MustCompile(`(?m)^\s*(\d+[.)]|[-*•])\s+\S`)
)

// NeedsPlan decides whether a request is complex enough for the planning phase. Simple
// edits (change a color, move or delete an object, one feature) go straight to the agent.
func NeedsPlan(intent string) bool {
	text := strings.ToLower(intent)
	words := wordRe.FindAllString(text, -1)
	groups, construction := 0, 0
	for _, g := range opGroups {
		hit := false
		for _, w := range words {
			for _, k := range g.words {
				if strings.HasPrefix(w, k) && (len(k) > 3 || len(w) <= len(k)+2) {
					hit = true
				}
			}
		}
		if hit {
			groups++
			if g.construction {
				construction++
			}
		}
	}
	switch {
	case construction == 0:
		return false
	case len(numberedRe.FindAllString(intent, -1)) >= 2:
		return true
	case construction >= 3:
		return true
	case construction >= 2 && groups >= 3:
		return true
	case construction >= 2 && len(words) >= 35:
		return true
	}
	return false
}

// ---- planner --------------------------------------------------------------------------

// Planner turns a complex request into a Plan with one model call (a submit_plan tool).
type Planner struct {
	Provider   runtime.Provider
	NoThinking bool
	// Tools are the CAD tools the executor will have, listed so the plan stays feasible.
	Tools []runtime.ToolDef
}

// MaxPlanSteps bounds a plan.
const MaxPlanSteps = 15

const plannerPrompt = `You are the planner of the Simon CAD Agent. You do NOT build anything: you break the user's request into an ordered plan that another agent will execute with CAD tools, one step at a time, and you state what the finished model must look like so it can be validated automatically.

Rules
- Call submit_plan exactly once. Do not write any other text.
- First identify what is needed: primitives, sketches, extrusions, boolean operations, transformations, materials, the key dimensions and the dependencies between operations (analysis).
- Each step is ONE operation that the available tools can perform. Order steps so every step only depends on earlier ones; list them in depends_on.
- Give every step that creates an object a short snake_case ref (e.g. servo_body, mounting_hole_1). Later steps use these refs in target/tool.
- Do not add a step for things that already exist in the current model; refer to existing features by their id as ref.
- Units are millimetres; boxes have their min corner at the origin unless the user says otherwise.
- expectations: for each important object give its expected overall size (x, y, z extents of that object's own body) and/or key parameters (dx, dy, dz, diameter, radius, height, depth), position (min corner or center) when the user specified one, and the material id (e.g. black_abs, brushed_aluminum) or color (#RRGGBB) when requested. body_count is the number of separate visible bodies the finished model should have (omit when unsure). Only state what the request implies; never guess.
- The last step may be operation "validate" (it is handled automatically).`

func (p Planner) schema() map[string]any {
	str := map[string]any{"type": "string"}
	strs := map[string]any{"type": "array", "items": str}
	nums := map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "number"}}
	vec := map[string]any{"type": "array", "items": map[string]any{"type": "number"}, "minItems": 3, "maxItems": 3}
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"goal": str,
			"analysis": map[string]any{"type": "object", "properties": map[string]any{
				"primitives": strs, "sketches": strs, "extrusions": strs, "booleans": strs,
				"transformations": strs, "materials": strs, "dimensions": strs}},
			"steps": map[string]any{"type": "array", "minItems": 1, "maxItems": MaxPlanSteps, "items": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"step":        map[string]any{"type": "integer", "minimum": 1},
					"operation":   map[string]any{"type": "string", "enum": Operations},
					"description": str, "ref": str, "target": str, "tool": str,
					"depends_on": map[string]any{"type": "array", "items": map[string]any{"type": "integer"}},
				},
				"required": []string{"step", "operation", "description"},
			}},
			"expectations": map[string]any{"type": "object", "properties": map[string]any{
				"body_count": map[string]any{"type": "integer", "minimum": 0},
				"objects": map[string]any{"type": "array", "items": map[string]any{"type": "object",
					"properties": map[string]any{"ref": str, "size": nums, "parameters": nums, "min": vec, "center": vec, "material": str, "color": str},
					"required":   []string{"ref"}}},
			}},
		},
		"required": []string{"goal", "steps"},
	}
}

// Plan asks the model for a plan. history is the prior conversation; contextText is the
// live model context. A nil plan (and no error) means the model declined to plan, e.g.
// because it needs to ask the user something first.
func (p Planner) Plan(ctx context.Context, history []runtime.Message, contextText, intent string) (*Plan, runtime.Usage, error) {
	var sb strings.Builder
	sb.WriteString(plannerPrompt)
	if len(p.Tools) > 0 {
		sb.WriteString("\n\nTools available to the executor:\n")
		for _, t := range p.Tools {
			desc := t.Description
			if i := strings.Index(desc, ". "); i > 0 {
				desc = desc[:i+1]
			}
			fmt.Fprintf(&sb, "- %s: %s\n", t.Name, desc)
		}
	}
	msgs := append(append([]runtime.Message(nil), history...), runtime.Message{Role: "user", Content: []runtime.Block{
		{Type: "text", Text: contextText + "## User request\n" + intent + "\n\nCall submit_plan with the plan for this request."}}})
	resp, err := p.Provider.Complete(ctx, runtime.Request{System: sb.String(), Messages: msgs, NoThinking: p.NoThinking,
		Tools: []runtime.ToolDef{{Name: "submit_plan", Description: "Submit the structured plan for the request.", InputSchema: p.schema()}}})
	if err != nil {
		return nil, runtime.Usage{}, err
	}
	for _, u := range resp.ToolUses() {
		if u.Name != "submit_plan" {
			continue
		}
		var plan Plan
		if err := json.Unmarshal(u.Input, &plan); err != nil {
			return nil, resp.Usage, fmt.Errorf("planner returned an invalid plan: %w", err)
		}
		plan.normalize()
		if len(plan.Steps) == 0 {
			return nil, resp.Usage, nil
		}
		return &plan, resp.Usage, nil
	}
	return nil, resp.Usage, nil
}

// normalize renumbers steps 1..n, drops forward/unknown dependencies, adds the implicit
// dependencies given by target/tool refs and trims to MaxPlanSteps.
func (pl *Plan) normalize() {
	if len(pl.Steps) > MaxPlanSteps {
		pl.Steps = pl.Steps[:MaxPlanSteps]
	}
	renum := map[int]int{}
	for i := range pl.Steps {
		renum[pl.Steps[i].N] = i + 1
		pl.Steps[i].N = i + 1
		pl.Steps[i].Ref = strings.TrimSpace(pl.Steps[i].Ref)
	}
	producer := map[string]int{}
	for i := range pl.Steps {
		s := &pl.Steps[i]
		seen := map[int]bool{}
		var deps []int
		add := func(d int) {
			if d >= 1 && d < s.N && !seen[d] {
				seen[d] = true
				deps = append(deps, d)
			}
		}
		for _, d := range s.DependsOn {
			add(renum[d])
		}
		for _, ref := range []string{s.Target, s.Tool} {
			if n, ok := producer[ref]; ok && ref != "" {
				add(n)
			}
		}
		s.DependsOn = deps
		if s.Ref != "" {
			if _, dup := producer[s.Ref]; !dup {
				producer[s.Ref] = s.N
			}
		}
	}
}

// Producer returns the step that creates ref (0 if none).
func (pl *Plan) Producer(ref string) int {
	for _, s := range pl.Steps {
		if s.Ref == ref && ref != "" {
			return s.N
		}
	}
	return 0
}

// Text renders the plan as the numbered list shown to the user and the executor.
func (pl *Plan) Text() string {
	var sb strings.Builder
	for _, s := range pl.Steps {
		fmt.Fprintf(&sb, "%d. %s", s.N, s.Description)
		var tags []string
		if s.Ref != "" {
			tags = append(tags, "ref="+s.Ref)
		}
		if len(s.DependsOn) > 0 {
			tags = append(tags, fmt.Sprintf("after %v", s.DependsOn))
		}
		if len(tags) > 0 {
			fmt.Fprintf(&sb, "  [%s: %s]", s.Operation, strings.Join(tags, ", "))
		} else {
			fmt.Fprintf(&sb, "  [%s]", s.Operation)
		}
		sb.WriteString("\n")
	}
	return strings.TrimRight(sb.String(), "\n")
}
