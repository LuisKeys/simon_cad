package workflow

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/accelone/simoncad/internal/app"
	"github.com/accelone/simoncad/internal/cadir"
	"github.com/accelone/simoncad/internal/geometry/rebuild"
)

// Issue is one validation problem, attributed to the plan step that probably caused it.
type Issue struct {
	Kind    string `json:"kind"`
	Step    int    `json:"step,omitempty"` // 0: not attributable to one step
	Ref     string `json:"ref,omitempty"`
	Feature string `json:"feature,omitempty"`
	Message string `json:"message"`
	// Hard issues are structural (failed or missing geometry); soft ones are mismatches with
	// the requested result (dimensions, position, material, count).
	Hard bool `json:"hard"`
}

// Issue kinds.
const (
	IssueStepFailed   = "step_failed"
	IssueMissing      = "missing_object"
	IssueFailed       = "failed_feature"
	IssueEmpty        = "empty_geometry"
	IssueDangling     = "dangling_reference"
	IssueDimension    = "dimension"
	IssuePosition     = "position"
	IssueAppearance   = "appearance"
	IssueBodyCount    = "body_count"
	IssueNoGeometry   = "no_geometry"
	IssueNotCompleted = "step_not_completed"
)

// Report is the result of one validation pass.
type Report struct {
	OK     bool    `json:"ok"`
	Issues []Issue `json:"issues"`
	Checks int     `json:"checks"`
}

// HardCount is the number of structural issues.
func (r *Report) HardCount() int {
	n := 0
	for _, is := range r.Issues {
		if is.Hard {
			n++
		}
	}
	return n
}

// Text renders the issues as a bullet list.
func (r *Report) Text() string {
	var sb strings.Builder
	for _, is := range r.Issues {
		sb.WriteString("- ")
		if is.Step > 0 {
			fmt.Fprintf(&sb, "[step %d] ", is.Step)
		}
		sb.WriteString(is.Message + "\n")
	}
	return strings.TrimRight(sb.String(), "\n")
}

// Validator compares the CAD state with the plan and its expectations. It is
// deterministic: it reads the CAD-IR and the rebuilt model, never a language model.
type Validator struct {
	// RelTol and AbsTol bound "approximately correct" dimensions: a value passes when it is
	// within max(AbsTol, RelTol*expected) of the expectation.
	RelTol, AbsTol float64
}

// DefaultValidator allows 2% or 0.5 mm, whichever is larger.
var DefaultValidator = Validator{RelTol: 0.02, AbsTol: 0.5}

func (v Validator) tol(want float64) float64 { return math.Max(v.AbsTol, v.RelTol*math.Abs(want)) }

// Validate checks the model after a plan run.
func (v Validator) Validate(a *app.App, plan *Plan, ex *Execution) (*Report, error) {
	m, err := a.Model()
	if err != nil {
		return nil, err
	}
	g := a.Snapshot()
	vc := &vctx{v: v, g: g, m: m, plan: plan, ex: ex, nodes: map[string]*cadir.Node{}, rep: &Report{}}
	for _, n := range g.Nodes {
		vc.nodes[n.ID] = n
	}
	vc.steps()
	vc.references()
	vc.features()
	vc.expectations()
	vc.rep.OK = len(vc.rep.Issues) == 0
	return vc.rep, nil
}

type vctx struct {
	v     Validator
	g     *cadir.Graph
	m     *rebuild.Model
	plan  *Plan
	ex    *Execution
	nodes map[string]*cadir.Node
	rep   *Report
}

func (vc *vctx) add(is Issue) { vc.rep.Issues = append(vc.rep.Issues, is) }

// steps: every step must have succeeded.
func (vc *vctx) steps() {
	for _, r := range vc.ex.Steps {
		vc.rep.Checks++
		switch r.Status {
		case StepFailed:
			vc.add(Issue{Kind: IssueStepFailed, Step: r.Step, Hard: true, Message: fmt.Sprintf("step %d (%s) failed: %s", r.Step, vc.plan.Steps[r.Step-1].Description, r.Error)})
		case StepSkipped, StepPending:
			vc.add(Issue{Kind: IssueNotCompleted, Step: r.Step, Hard: true, Message: fmt.Sprintf("step %d (%s) was not carried out", r.Step, vc.plan.Steps[r.Step-1].Description)})
		}
	}
}

// resolve returns the feature that realizes a plan ref ("" if unknown).
func (vc *vctx) resolve(ref string) string {
	if id, ok := vc.ex.Refs[ref]; ok {
		return id
	}
	if _, ok := vc.nodes[ref]; ok { // the plan referred to an existing feature by id
		return ref
	}
	return ""
}

// references: objects created by succeeded steps still exist, and no feature depends on
// a feature that is gone.
func (vc *vctx) references() {
	refs := make([]string, 0, len(vc.ex.Refs))
	for ref := range vc.ex.Refs {
		refs = append(refs, ref)
	}
	sort.Strings(refs)
	for _, ref := range refs {
		vc.rep.Checks++
		id := vc.ex.Refs[ref]
		if _, ok := vc.nodes[id]; !ok {
			vc.add(Issue{Kind: IssueMissing, Ref: ref, Feature: id, Step: vc.plan.Producer(ref), Hard: true,
				Message: fmt.Sprintf("%s (%s) no longer exists in the model", ref, id)})
		}
	}
	for _, s := range vc.plan.Steps {
		if s.Ref == "" || vc.ex.Steps[s.N-1].Status != StepSuccess || s.Operation == "validate" {
			continue
		}
		if vc.resolve(s.Ref) == "" && producesObject(s.Operation) {
			vc.rep.Checks++
			vc.add(Issue{Kind: IssueMissing, Ref: s.Ref, Step: s.N, Hard: true,
				Message: fmt.Sprintf("step %d should have created %s but no new feature was made", s.N, s.Ref)})
		}
	}
	for _, n := range vc.g.Nodes {
		for _, d := range n.Dependencies {
			vc.rep.Checks++
			if _, ok := vc.nodes[d]; !ok {
				vc.add(Issue{Kind: IssueDangling, Feature: n.ID, Step: vc.ex.Owner[n.ID], Hard: true,
					Message: fmt.Sprintf("%s depends on %s, which does not exist", n.ID, d)})
			}
		}
	}
}

// producesObject: operations expected to add a feature.
func producesObject(op string) bool {
	switch op {
	case "primitive", "sketch", "extrude", "boolean_union", "boolean_subtract", "boolean_intersect", "hole", "fillet", "chamfer", "pattern":
		return true
	}
	return false
}

// features: nothing failed to build and no body is empty.
func (vc *vctx) features() {
	for _, id := range vc.m.Failed() {
		vc.rep.Checks++
		msg := id + " failed to rebuild"
		if r := vc.m.Nodes[id]; r != nil && len(r.Errors) > 0 {
			msg += ": " + strings.Join(r.Errors, "; ")
		}
		vc.add(Issue{Kind: IssueFailed, Feature: id, Step: vc.ex.Owner[id], Hard: true, Message: msg})
	}
	for _, id := range vc.m.Leaves {
		vc.rep.Checks++
		if r := vc.m.Nodes[id]; r == nil || r.Body == nil || r.Body.Props.Volume <= 1e-9 {
			vc.add(Issue{Kind: IssueEmpty, Feature: id, Step: vc.ex.Owner[id], Hard: true, Message: id + " produced an empty body"})
		}
	}
	if len(vc.m.Leaves) == 0 && len(vc.g.Nodes) > 0 {
		vc.rep.Checks++
		vc.add(Issue{Kind: IssueNoGeometry, Step: lastStep(vc.plan), Hard: true, Message: "the model has no solid geometry"})
	}
}

func lastStep(p *Plan) int {
	for i := len(p.Steps) - 1; i >= 0; i-- {
		if p.Steps[i].Operation != "validate" {
			return p.Steps[i].N
		}
	}
	return 0
}

// expectations: dimensions, positions, appearance and body count match the request.
func (vc *vctx) expectations() {
	exp := vc.plan.Expectations
	for _, o := range exp.Objects {
		id := vc.resolve(o.Ref)
		if id == "" {
			if len(o.Size)+len(o.Parameters)+len(o.Min)+len(o.Center) > 0 || o.Material != "" || o.Color != "" {
				vc.rep.Checks++
				vc.add(Issue{Kind: IssueMissing, Ref: o.Ref, Step: vc.plan.Producer(o.Ref), Hard: true,
					Message: fmt.Sprintf("expected object %s was not created", o.Ref)})
			}
			continue
		}
		vc.dimensions(o, id)
		vc.position(o, id)
		vc.appearance(o, id)
	}
	if exp.BodyCount > 0 {
		vc.rep.Checks++
		if got := len(vc.m.Leaves); got != exp.BodyCount {
			vc.add(Issue{Kind: IssueBodyCount, Step: lastStep(vc.plan),
				Message: fmt.Sprintf("the model has %d separate bodies, expected %d (%s)", got, exp.BodyCount, strings.Join(vc.m.Leaves, ", "))})
		}
	}
}

// owner is the step blamed for an object's state: the last step that touched the feature,
// else the step that created the ref.
func (vc *vctx) owner(ref, id string) int {
	if n := vc.ex.Owner[id]; n > 0 {
		return n
	}
	return vc.plan.Producer(ref)
}

func (vc *vctx) dimensions(o ObjectExpectation, id string) {
	n := vc.nodes[id]
	for _, k := range sortedKeys(o.Parameters) {
		want := o.Parameters[k]
		raw, ok := n.Parameters[k]
		if !ok {
			continue // a parameter this feature type does not have: checked through size instead
		}
		got, ok := raw.(float64)
		if !ok {
			continue
		}
		vc.rep.Checks++
		if math.Abs(got-want) > vc.v.tol(want) {
			vc.add(Issue{Kind: IssueDimension, Ref: o.Ref, Feature: id, Step: vc.owner(o.Ref, id),
				Message: fmt.Sprintf("%s (%s): %s is %.3g mm, expected %.3g mm", o.Ref, id, k, got, want)})
		}
	}
	if len(o.Size) == 0 {
		return
	}
	b, ok := vc.bounds(id)
	if !ok {
		return
	}
	for i, axis := range []string{"x", "y", "z"} {
		want, ok := o.Size[axis]
		if !ok {
			continue
		}
		vc.rep.Checks++
		if got := b[1][i] - b[0][i]; math.Abs(got-want) > vc.v.tol(want) {
			vc.add(Issue{Kind: IssueDimension, Ref: o.Ref, Feature: id, Step: vc.owner(o.Ref, id),
				Message: fmt.Sprintf("%s (%s): size along %s is %.3g mm, expected %.3g mm", o.Ref, id, axis, got, want)})
		}
	}
}

func (vc *vctx) position(o ObjectExpectation, id string) {
	if len(o.Min) != 3 && len(o.Center) != 3 {
		return
	}
	b, ok := vc.bounds(id)
	if !ok {
		return
	}
	check := func(label string, want []float64, got [3]float64) {
		if len(want) != 3 {
			return
		}
		vc.rep.Checks++
		for i := range got {
			if math.Abs(got[i]-want[i]) > vc.v.tol(1) {
				vc.add(Issue{Kind: IssuePosition, Ref: o.Ref, Feature: id, Step: vc.owner(o.Ref, id),
					Message: fmt.Sprintf("%s (%s): %s is (%.3g, %.3g, %.3g), expected (%.3g, %.3g, %.3g)", o.Ref, id, label, got[0], got[1], got[2], want[0], want[1], want[2])})
				return
			}
		}
	}
	check("min corner", o.Min, b[0])
	check("center", o.Center, [3]float64{(b[0][0] + b[1][0]) / 2, (b[0][1] + b[1][1]) / 2, (b[0][2] + b[1][2]) / 2})
}

// appearance looks at the object and at the visible bodies built from it (a material set
// on the final body counts for the objects it consumed).
func (vc *vctx) appearance(o ObjectExpectation, id string) {
	if o.Material == "" && o.Color == "" {
		return
	}
	chain := vc.descendants(id)
	has := func(f func(*cadir.Appearance) bool) bool {
		for _, n := range chain {
			if ap := vc.nodes[n].Appearance; ap != nil && f(ap) {
				return true
			}
		}
		return false
	}
	step := vc.appearanceStep(o.Ref, id)
	if o.Material != "" {
		vc.rep.Checks++
		if !has(func(ap *cadir.Appearance) bool { return strings.EqualFold(ap.MaterialID, o.Material) }) {
			vc.add(Issue{Kind: IssueAppearance, Ref: o.Ref, Feature: id, Step: step,
				Message: fmt.Sprintf("%s (%s) does not have material %s", o.Ref, id, o.Material)})
		}
	}
	if o.Color != "" {
		vc.rep.Checks++
		if !has(func(ap *cadir.Appearance) bool { return strings.EqualFold(ap.Color, o.Color) }) {
			vc.add(Issue{Kind: IssueAppearance, Ref: o.Ref, Feature: id, Step: step,
				Message: fmt.Sprintf("%s (%s) is not colored %s", o.Ref, id, o.Color)})
		}
	}
}

// appearanceStep blames the material/appearance step aimed at ref, else the last one.
func (vc *vctx) appearanceStep(ref, id string) int {
	last := 0
	for _, s := range vc.plan.Steps {
		if s.Operation == "material" || s.Operation == "appearance" {
			if s.Target == ref || s.Target == id {
				return s.N
			}
			last = s.N
		}
	}
	if last > 0 {
		return last
	}
	return vc.owner(ref, id)
}

// descendants returns id and every feature that (transitively) consumes it.
func (vc *vctx) descendants(id string) []string {
	out, seen := []string{id}, map[string]bool{id: true}
	for i := 0; i < len(out); i++ {
		for _, d := range vc.g.Dependents(out[i]) {
			if !seen[d] {
				seen[d] = true
				out = append(out, d)
			}
		}
	}
	return out
}

// bounds is the bounding box of a feature's own body.
func (vc *vctx) bounds(id string) ([2][3]float64, bool) {
	r := vc.m.Nodes[id]
	if r == nil || r.Body == nil {
		return [2][3]float64{}, false
	}
	p := r.Body.Props
	return [2][3]float64{{p.MinX, p.MinY, p.MinZ}, {p.MaxX, p.MaxY, p.MaxZ}}, true
}

func sortedKeys(m map[string]float64) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
