package rebuild

import (
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/accelone/simoncad/internal/cadir"
)

func sketchNode(id string, params map[string]any, refs map[string]string) *cadir.Node {
	n := &cadir.Node{ID: id, Type: "sketch", Parameters: params, References: refs}
	return n
}

func extrudeSketch(id, sketch, target string, depth float64, extra map[string]any) *cadir.Node {
	p := map[string]any{"sketch": sketch, "depth": depth}
	for k, v := range extra {
		p[k] = v
	}
	deps := []string{sketch}
	if target != "" {
		p["target"] = target
		deps = append(deps, target)
	}
	return &cadir.Node{ID: id, Type: "extrude", Parameters: p, Dependencies: deps}
}

func rebuildOK(t *testing.T, e *Engine, g *cadir.Graph) *Model {
	t.Helper()
	m, err := e.Rebuild(g)
	if err != nil {
		t.Fatal(err)
	}
	if f := m.Failed(); len(f) != 0 {
		t.Fatalf("failed: %v %v", f, m.Nodes[f[0]].Errors)
	}
	return m
}

func ent(kind string, kv ...any) map[string]any {
	m := map[string]any{"kind": kind}
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i].(string)] = kv[i+1]
	}
	return m
}

func TestSketchRectangleExtrude(t *testing.T) {
	_, e := setup(t)
	g := cadir.NewGraph()
	rect := ent("rectangle", "id", "e1", "x", 0.0, "y", 0.0, "w", 80.0, "h", 50.0)
	g.Nodes = []*cadir.Node{
		sketchNode("sk", map[string]any{"entities": []any{rect}}, nil),
		extrudeSketch("plate", "sk", "", 5, nil),
	}
	m := rebuildOK(t, e, g)
	if got := m.Volume(); !near(got, 80*50*5) {
		t.Fatalf("volume %v want %v", got, 80*50*5)
	}
	if len(m.Leaves) != 1 || m.Leaves[0] != "plate" || len(m.Sketches) != 0 {
		t.Fatalf("leaves %v sketches %v", m.Leaves, m.Sketches)
	}
}

func TestSketchAloneHasNoSolid(t *testing.T) {
	_, e := setup(t)
	g := cadir.NewGraph()
	g.Nodes = []*cadir.Node{sketchNode("sk", map[string]any{
		"entities": []any{ent("circle", "id", "c", "cx", 0.0, "cy", 0.0, "r", 5.0)}}, nil)}
	m := rebuildOK(t, e, g)
	if len(m.Leaves) != 0 || len(m.Sketches) != 1 || m.Volume() != 0 {
		t.Fatalf("leaves %v sketches %v vol %v", m.Leaves, m.Sketches, m.Volume())
	}
}

func TestSketchRegionWithHoles(t *testing.T) {
	_, e := setup(t)
	g := cadir.NewGraph()
	g.Nodes = []*cadir.Node{
		sketchNode("sk", map[string]any{
			"entities": []any{
				ent("rectangle", "id", "e1", "x", 0.0, "y", 0.0, "w", 80.0, "h", 50.0),
				ent("circle", "id", "e2", "cx", 20.0, "cy", 25.0, "r", 3.0),
				ent("circle", "id", "e3", "cx", 60.0, "cy", 25.0, "r", 3.0),
			},
			"regions": []any{map[string]any{"id": "r1", "outer": []any{"e1"},
				"holes": []any{[]any{"e2"}, []any{"e3"}}}},
		}, nil),
		extrudeSketch("plate", "sk", "", 5, nil),
	}
	m := rebuildOK(t, e, g)
	want := (80*50 - 2*math.Pi*9) * 5
	if got := m.Volume(); !near(got, want) {
		t.Fatalf("volume %v want %v", got, want)
	}
}

func TestSketchIrregularLineArcProfile(t *testing.T) {
	_, e := setup(t)
	g := cadir.NewGraph()
	// half disc of radius 10 built from an open line + an open arc, listed out of order
	g.Nodes = []*cadir.Node{
		sketchNode("sk", map[string]any{
			"entities": []any{
				ent("arc", "id", "a1", "p1", []any{10.0, 0.0}, "mid", []any{0.0, 10.0}, "p2", []any{-10.0, 0.0}),
				ent("line", "id", "l1", "p1", []any{10.0, 0.0}, "p2", []any{-10.0, 0.0}), // reversed on purpose
			},
			"regions": []any{map[string]any{"id": "r", "outer": []any{"l1", "a1"}}},
		}, nil),
		extrudeSketch("part", "sk", "", 2, nil),
	}
	m := rebuildOK(t, e, g)
	if got, want := m.Volume(), math.Pi*100/2*2; !near(got, want) {
		t.Fatalf("volume %v want %v", got, want)
	}
	// an open chain must fail as a normal feature failure
	g.Nodes[0].Parameters["entities"] = []any{
		ent("line", "id", "l1", "p1", []any{0.0, 0.0}, "p2", []any{5.0, 0.0}),
		ent("line", "id", "l2", "p1", []any{5.0, 0.0}, "p2", []any{5.0, 5.0}),
	}
	g.Nodes[0].Parameters["regions"] = []any{map[string]any{"id": "r", "outer": []any{"l1", "l2"}}}
	m, _ = e.Rebuild(g)
	if st := m.Nodes["sk"].Status; st != cadir.StatusFailed {
		t.Fatalf("open loop sketch status %q", st)
	}
	if m.Nodes["part"].Status != cadir.StatusFailed {
		t.Fatal("dependent extrude must fail")
	}
}

func TestSketchSlotAndPolygon(t *testing.T) {
	_, e := setup(t)
	g := cadir.NewGraph()
	g.Nodes = []*cadir.Node{
		sketchNode("sk", map[string]any{"entities": []any{
			ent("slot", "id", "s", "x1", 0.0, "y1", 0.0, "x2", 20.0, "y2", 0.0, "width", 6.0),
			ent("polygon", "id", "p", "points", []any{[]any{40.0, 0.0}, []any{50.0, 0.0}, []any{40.0, 10.0}}),
		}}, nil),
		extrudeSketch("part", "sk", "", 1, nil),
	}
	m := rebuildOK(t, e, g)
	want := (20*6 + math.Pi*9 + 50) * 1
	if got := m.Volume(); !near(got, want) {
		t.Fatalf("volume %v want %v", got, want)
	}
}

func TestSketchMultiRegionSelect(t *testing.T) {
	_, e := setup(t)
	g := cadir.NewGraph()
	g.Nodes = []*cadir.Node{
		sketchNode("sk", map[string]any{"entities": []any{
			ent("rectangle", "id", "a", "x", 0.0, "y", 0.0, "w", 10.0, "h", 10.0),
			ent("rectangle", "id", "b", "x", 30.0, "y", 0.0, "w", 10.0, "h", 5.0),
		}}, nil),
		extrudeSketch("only_b", "sk", "", 2, map[string]any{"region": "b"}),
	}
	m := rebuildOK(t, e, g)
	if got := m.Volume(); !near(got, 10*5*2) {
		t.Fatalf("region b volume %v", got)
	}
	g.Nodes[1] = extrudeSketch("both", "sk", "", 2, nil)
	m = rebuildOK(t, e, g)
	if got := m.Volume(); !near(got, (100+50)*2) {
		t.Fatalf("both regions volume %v", got)
	}
	g.Nodes[1] = extrudeSketch("none", "sk", "", 2, map[string]any{"region": "zzz"})
	m, _ = e.Rebuild(g)
	if m.Nodes["none"].Status != cadir.StatusFailed {
		t.Fatal("unknown region must fail")
	}
}

func TestSketchOnPlanesAndOffset(t *testing.T) {
	_, e := setup(t)
	g := cadir.NewGraph()
	r := ent("rectangle", "id", "e", "x", 1.0, "y", 2.0, "w", 4.0, "h", 6.0)
	g.Nodes = []*cadir.Node{
		sketchNode("sk", map[string]any{"plane": "YZ", "offset": 10.0, "entities": []any{r}}, nil),
		extrudeSketch("part", "sk", "", 3, nil),
	}
	m := rebuildOK(t, e, g)
	b := m.Nodes["part"].Body.Bounds()
	// YZ: u→Y, v→Z, extrusion along +X starting at the offset
	want := [6]float64{10, 1, 2, 13, 5, 8}
	got := [6]float64{b.Min[0], b.Min[1], b.Min[2], b.Max[0], b.Max[1], b.Max[2]}
	for i := range want {
		if math.Abs(got[i]-want[i]) > 1e-6 {
			t.Fatalf("bounds %v want %v", got, want)
		}
	}
}

func TestSketchOnPlanarFace(t *testing.T) {
	_, e := setup(t)
	g := cadir.NewGraph()
	// 100x60x4 plate; a 20x10 tab drawn on its +Z face and a through-cut slot from the same face
	g.Nodes = []*cadir.Node{
		plate("plate", 100, 60, 4),
		sketchNode("sk", map[string]any{"entities": []any{
			ent("rectangle", "id", "e", "x", -10.0, "y", -5.0, "w", 20.0, "h", 10.0)}},
			map[string]string{"face": "face(feature=plate,side=+Z)"}),
		extrudeSketch("tab", "sk", "plate", 3, nil),
	}
	g.Nodes[2].Dependencies = append(g.Nodes[2].Dependencies, "plate")
	m := rebuildOK(t, e, g)
	if got, want := m.Volume(), 100.0*60*4+20*10*3; !near(got, want) {
		t.Fatalf("add-on-face volume %v want %v", got, want)
	}
	if b := m.Nodes["tab"].Body.Bounds(); math.Abs(b.Max[2]-7) > 1e-6 {
		t.Fatalf("tab top %v want 7", b.Max[2])
	}

	// cut goes into the material: 2 mm deep pocket
	g.Nodes[2] = extrudeSketch("pocket", "sk", "plate", 2, map[string]any{"mode": "cut"})
	g.Nodes[2].Dependencies = []string{"sk", "plate"}
	m = rebuildOK(t, e, g)
	if got, want := m.Volume(), 100.0*60*4-20*10*2; !near(got, want) {
		t.Fatalf("pocket volume %v want %v", got, want)
	}

	// a sketch on a tilted (non axis-aligned) planar face is placed on that face
	g = cadir.NewGraph()
	g.Nodes = []*cadir.Node{
		plate("plate", 100, 60, 4),
		{ID: "rot", Type: "transform", Dependencies: []string{"plate"}, Parameters: map[string]any{
			"target": "plate", "angle_deg": 30.0, "ay": 1.0, "az": 0.0}},
	}
	m = rebuildOK(t, e, g)
	topo, ok := m.Topology("rot")
	if !ok {
		t.Fatal("no topology")
	}
	n := [3]float64{math.Sin(math.Pi / 6), 0, math.Cos(math.Pi / 6)}
	idx := -1
	for i, f := range topo.Faces {
		if f.Normal[0]*n[0]+f.Normal[1]*n[1]+f.Normal[2]*n[2] > 0.999 && math.Abs(f.Area-6000) < 1 {
			idx = i
		}
	}
	if idx < 0 {
		t.Fatal("tilted face not found")
	}
	g.Nodes = append(g.Nodes,
		sketchNode("sk", map[string]any{"entities": []any{
			ent("rectangle", "id", "e", "x", -10.0, "y", -5.0, "w", 20.0, "h", 10.0)}},
			map[string]string{"face": fmt.Sprintf("face(feature=rot,index=%d)", idx)}),
		extrudeSketch("tab", "sk", "rot", 3, nil))
	g.Nodes[3].Dependencies = []string{"sk", "rot"}
	m = rebuildOK(t, e, g)
	if got, want := m.Volume(), 100.0*60*4+20*10*3; !near(got, want) {
		t.Fatalf("tilted add volume %v want %v", got, want)
	}
	// the tab sticks out of the tilted face by exactly 3 mm along its normal
	sh := m.Nodes["tab"].Body
	if sh.Props.Volume < 1 {
		t.Fatal("tab merged volume missing")
	}
}

func TestSketchParamsEditableAndCached(t *testing.T) {
	_, e := setup(t)
	g := cadir.NewGraph()
	g.Nodes = []*cadir.Node{
		sketchNode("sk", map[string]any{"w": 80.0, "entities": []any{
			ent("rectangle", "id", "e", "x", 0.0, "y", 0.0, "w", "@w", "h", 50.0)}}, nil),
		extrudeSketch("plate", "sk", "", 5, nil),
	}
	m := rebuildOK(t, e, g)
	if got := m.Volume(); !near(got, 80*50*5) {
		t.Fatalf("volume %v", got)
	}
	hits := e.Stats.Hits
	rebuildOK(t, e, g)
	if e.Stats.Hits <= hits {
		t.Fatal("unchanged graph must hit the cache")
	}
	g.Nodes[0].Parameters["w"] = 100.0
	m = rebuildOK(t, e, g)
	if got := m.Volume(); !near(got, 100*50*5) {
		t.Fatalf("edited volume %v", got)
	}
	// a dangling @reference fails the sketch, not the process
	g.Nodes[0].Parameters["entities"] = []any{ent("circle", "id", "c", "cx", 0.0, "cy", 0.0, "r", "@nope")}
	m, _ = e.Rebuild(g)
	if m.Nodes["sk"].Status != cadir.StatusFailed || !strings.Contains(strings.Join(m.Nodes["sk"].Errors, ";"), "nope") {
		t.Fatalf("dangling @param: %v", m.Nodes["sk"].Errors)
	}
}

func TestBooleanOnSketchFails(t *testing.T) {
	_, e := setup(t)
	g := cadir.NewGraph()
	g.Nodes = []*cadir.Node{
		plate("plate", 10, 10, 10),
		sketchNode("sk", map[string]any{"entities": []any{
			ent("circle", "id", "c", "cx", 5.0, "cy", 5.0, "r", 2.0)}}, nil),
		{ID: "u", Type: "union", Dependencies: []string{"plate", "sk"}, Parameters: map[string]any{"target": "plate", "tool": "sk"}},
	}
	m, _ := e.Rebuild(g)
	if m.Nodes["u"].Status != cadir.StatusFailed {
		t.Fatal("boolean with a sketch operand must fail")
	}
	if !strings.Contains(strings.Join(m.Nodes["u"].Errors, ";"), "sketch") {
		t.Fatalf("error should mention sketch: %v", m.Nodes["u"].Errors)
	}
	if m.Nodes["plate"].Status != cadir.StatusOK {
		t.Fatal("the plate must be unaffected")
	}
}

func TestSketchBooleanChain(t *testing.T) {
	_, e := setup(t)
	g := cadir.NewGraph()
	g.Nodes = []*cadir.Node{
		sketchNode("skA", map[string]any{"entities": []any{ent("rectangle", "id", "a", "x", 0.0, "y", 0.0, "w", 20.0, "h", 10.0)}}, nil),
		sketchNode("skB", map[string]any{"entities": []any{ent("rectangle", "id", "b", "x", 10.0, "y", 0.0, "w", 20.0, "h", 10.0)}}, nil),
		extrudeSketch("A", "skA", "", 4, nil),
		extrudeSketch("B", "skB", "", 4, nil),
		{ID: "uni", Type: "union", Dependencies: []string{"A", "B"}, Parameters: map[string]any{"target": "A", "tool": "B"}},
	}
	m := rebuildOK(t, e, g)
	if got := m.Volume(); !near(got, 30*10*4) {
		t.Fatalf("union volume %v", got)
	}
	g.Nodes[4].Type = "intersect"
	m = rebuildOK(t, e, g)
	if got := m.Volume(); !near(got, 10*10*4) {
		t.Fatalf("intersect volume %v", got)
	}
	g.Nodes[4].Type = "subtract"
	m = rebuildOK(t, e, g)
	if got := m.Volume(); !near(got, 10*10*4) {
		t.Fatalf("subtract volume %v", got)
	}
}
