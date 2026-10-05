package rebuild

import (
	"math"
	"testing"

	"github.com/accelone/simoncad/internal/cadir"
	"github.com/accelone/simoncad/internal/geometry/api"
)

func plate(id string, dx, dy, dz float64) *cadir.Node {
	return &cadir.Node{ID: id, Type: "box", Parameters: map[string]any{"dx": dx, "dy": dy, "dz": dz}}
}

func hole(id, target string, dia float64, pts ...[]any) *cadir.Node {
	n := &cadir.Node{ID: id, Type: "hole", Dependencies: []string{target},
		Parameters: map[string]any{"target": target, "diameter": dia}}
	if len(pts) > 0 {
		list := make([]any, len(pts))
		for i, p := range pts {
			list[i] = p
		}
		n.Parameters["positions"] = list
	}
	return n
}

func setup(t *testing.T) (*api.Kernel, *Engine) {
	k := api.New()
	e := New(k)
	t.Cleanup(func() {
		e.Close()
		if n := k.LiveShapes(); n != 0 {
			t.Errorf("leaked %d shapes", n)
		}
		k.Close()
	})
	return k, e
}

func near(a, b float64) bool { return math.Abs(a-b)/math.Max(1, math.Abs(b)) < 1e-6 }

func TestPlateWithHoles(t *testing.T) {
	_, e := setup(t)
	g := cadir.NewGraph()
	g.Nodes = []*cadir.Node{
		plate("plate", 100, 60, 4),
		hole("h", "plate", 3.2, []any{5.0, 5.0}, []any{95.0, 5.0}, []any{5.0, 55.0}, []any{95.0, 55.0}),
	}
	m, err := e.Rebuild(g)
	if err != nil {
		t.Fatal(err)
	}
	if f := m.Failed(); len(f) != 0 {
		t.Fatalf("failed: %v %v", f, m.Nodes[f[0]].Errors)
	}
	want := 100.0*60*4 - 4*math.Pi*1.6*1.6*4
	if got := m.Volume(); !near(got, want) {
		t.Fatalf("volume %v want %v", got, want)
	}
	if len(m.Leaves) != 1 || m.Leaves[0] != "h" {
		t.Fatalf("leaves %v", m.Leaves)
	}
}

func TestHoleKindsAndFaces(t *testing.T) {
	_, e := setup(t)
	cb := hole("cb", "plate", 3.2, []any{10.0, 10.0})
	cb.Parameters["kind"] = "counterbore"
	cb.Parameters["cb_diameter"] = 6.0
	cb.Parameters["cb_depth"] = 1.5
	cs := hole("cs", "cb", 3.2, []any{30.0, 10.0})
	cs.Parameters["kind"] = "countersink"
	cs.Parameters["cs_diameter"] = 6.4
	side := hole("side", "cs", 2.0, []any{0.0, 20.0, 2.0})
	side.References = map[string]string{"face": "face(feature=plate,side=-X)"}
	g := cadir.NewGraph()
	g.Nodes = []*cadir.Node{plate("plate", 60, 40, 4), cb, cs, side}
	m, err := e.Rebuild(g)
	if err != nil {
		t.Fatal(err)
	}
	if f := m.Failed(); len(f) != 0 {
		t.Fatalf("failed %v: %v", f, m.Nodes[f[0]].Errors)
	}
	v := m.Volume()
	full := 60.0 * 40 * 4
	cbVol := math.Pi*1.6*1.6*4 + math.Pi*(3*3-1.6*1.6)*1.5
	h := (6.4 - 3.2) / 2 // 90° countersink: depth = radial difference
	csVol := math.Pi*1.6*1.6*4 + math.Pi*h/3*(3.2*3.2+3.2*1.6+1.6*1.6) - math.Pi*h*1.6*1.6
	sideVol := math.Pi * 1 * 1 * 60 // through the 60 mm X extent, minus overlap with other holes: none
	want := full - cbVol - csVol - sideVol
	if !near(v, want) {
		t.Fatalf("volume %v want %v", v, want)
	}
}

func TestPatternsAndExtrude(t *testing.T) {
	_, e := setup(t)
	lp := &cadir.Node{ID: "lp", Type: "linear_pattern", Dependencies: []string{"h"},
		Parameters: map[string]any{"source": "h", "count": 4.0, "dx": 20.0}}
	cp := &cadir.Node{ID: "cp", Type: "circular_pattern", Dependencies: []string{"h2"},
		Parameters: map[string]any{"source": "h2", "count": 6.0, "cx": 50.0, "cy": 50.0}}
	slot := &cadir.Node{ID: "slot", Type: "extrude", Dependencies: []string{"cp"},
		Parameters: map[string]any{"target": "cp", "mode": "cut", "profile": "slot", "through": true,
			"x1": 20.0, "y1": 90.0, "x2": 80.0, "y2": 90.0, "width": 6.0}}
	g := cadir.NewGraph()
	g.Nodes = []*cadir.Node{
		plate("plate", 100, 100, 4),
		hole("h", "plate", 4, []any{10.0, 10.0}), lp,
		hole("h2", "lp", 3, []any{80.0, 50.0}), cp, slot,
	}
	m, err := e.Rebuild(g)
	if err != nil {
		t.Fatal(err)
	}
	if f := m.Failed(); len(f) != 0 {
		t.Fatalf("failed %v: %v", f, m.Nodes[f[0]].Errors)
	}
	slotArea := 60.0*6 + math.Pi*3*3
	want := 100.0*100*4 - 4*math.Pi*4*4 - 6*math.Pi*1.5*1.5*4 - slotArea*4
	if got := m.Volume(); !near(got, want) {
		t.Fatalf("volume %v want %v", got, want)
	}
}

func TestMirrorTransformFillet(t *testing.T) {
	_, e := setup(t)
	g := cadir.NewGraph()
	g.Nodes = []*cadir.Node{
		plate("a", 10, 10, 10),
		{ID: "t", Type: "transform", Dependencies: []string{"a"}, Parameters: map[string]any{"target": "a", "dx": 20.0}},
		{ID: "m", Type: "mirror", Dependencies: []string{"t"}, Parameters: map[string]any{"target": "t", "plane": "YZ"}},
		{ID: "f", Type: "fillet", Dependencies: []string{"m"}, Parameters: map[string]any{"target": "m", "radius": 1.0}},
	}
	m, err := e.Rebuild(g)
	if err != nil {
		t.Fatal(err)
	}
	if f := m.Failed(); len(f) != 0 {
		t.Fatalf("failed %v: %v", f, m.Nodes[f[0]].Errors)
	}
	b, _ := m.Bounds("m")
	if !near(b.Min[0], -30) || !near(b.Max[0], 30) {
		t.Fatalf("mirror bounds %v", b)
	}
	if v := m.Volume(); v >= 2000 || v < 1900 {
		t.Fatalf("filleted volume %v", v)
	}
}

func TestExpressionsAndIncrementalCache(t *testing.T) {
	_, e := setup(t)
	h := hole("h", "plate", 3.2, []any{10.0, 10.0})
	h.Expressions = map[string]string{"depth": "plate.dz * 0.5"}
	g := cadir.NewGraph()
	g.Nodes = []*cadir.Node{plate("plate", 50, 50, 4), plate("other", 5, 5, 5), h}
	if _, err := e.Rebuild(g); err != nil {
		t.Fatal(err)
	}
	if e.Stats.Misses != 3 || e.Stats.Hits != 0 {
		t.Fatalf("cold stats %+v", e.Stats)
	}
	e.Stats = Stats{}
	if _, err := e.Rebuild(g); err != nil {
		t.Fatal(err)
	}
	if e.Stats.Misses != 0 || e.Stats.Hits != 3 {
		t.Fatalf("warm stats %+v", e.Stats)
	}
	// Editing the plate invalidates plate and hole but not "other".
	e.Stats = Stats{}
	g.Get("plate").Parameters["dz"] = 6.0
	m, err := e.Rebuild(g)
	if err != nil {
		t.Fatal(err)
	}
	if e.Stats.Misses != 2 || e.Stats.Hits != 1 {
		t.Fatalf("incremental stats %+v", e.Stats)
	}
	if d := m.Nodes["h"].Params["depth"].(float64); d != 3 {
		t.Fatalf("expression result %v", d)
	}
}

func TestFailurePropagationAndSuppression(t *testing.T) {
	_, e := setup(t)
	bad := hole("bad", "plate", 3.2, []any{500.0, 500.0}) // outside face
	next := &cadir.Node{ID: "f", Type: "fillet", Dependencies: []string{"bad"}, Parameters: map[string]any{"target": "bad", "radius": 0.5}}
	g := cadir.NewGraph()
	g.Nodes = []*cadir.Node{plate("plate", 50, 50, 4), bad, next}
	m, err := e.Rebuild(g)
	if err != nil {
		t.Fatal(err)
	}
	if m.Nodes["bad"].Status != cadir.StatusFailed || m.Nodes["f"].Status != cadir.StatusFailed {
		t.Fatalf("statuses: %v %v", m.Nodes["bad"].Status, m.Nodes["f"].Status)
	}
	if got := m.Nodes["f"].Errors; len(got) < 2 {
		t.Fatalf("expected cause chain, got %v", got)
	}
	// Suppressing the bad feature passes the target through and heals the chain.
	g.Get("bad").Suppressed = true
	m, _ = e.Rebuild(g)
	if len(m.Failed()) != 0 || m.Nodes["bad"].Status != cadir.StatusSuppressed {
		t.Fatalf("after suppress: %v", m.Failed())
	}
	if !near(m.Nodes["f"].Body.Props.Volume, m.Nodes["f"].Body.Props.Volume) || m.Nodes["f"].Body.Props.Volume > 50*50*4 {
		t.Fatalf("volume %v", m.Nodes["f"].Body.Props.Volume)
	}
}

func TestUnresolvedSelectorIsDependencyFailure(t *testing.T) {
	_, e := setup(t)
	h := hole("h", "plate", 3, []any{5.0, 5.0})
	h.References = map[string]string{"face": "face(feature=ghost,side=+Z)"}
	g := cadir.NewGraph()
	g.Nodes = []*cadir.Node{plate("plate", 20, 20, 4), h}
	if _, err := e.Rebuild(g); err == nil {
		t.Fatal("expected graph-level error for selector on unknown feature")
	}
	g.Nodes[1].References["face"] = "face(feature=plate,side=sideways)"
	m, err := e.Rebuild(g)
	if err != nil {
		t.Fatal(err)
	}
	if m.Nodes["h"].Status != cadir.StatusFailed {
		t.Fatal("bad side should fail")
	}
}

func TestDeterministicOrderTieBreak(t *testing.T) {
	g := cadir.NewGraph()
	g.Nodes = []*cadir.Node{plate("b", 1, 1, 1), plate("a", 1, 1, 1), hole("h", "a", 0.2)}
	o, err := g.TopoOrder()
	if err != nil {
		t.Fatal(err)
	}
	if o[0].ID != "b" || o[1].ID != "a" || o[2].ID != "h" {
		t.Fatalf("order %v %v %v", o[0].ID, o[1].ID, o[2].ID)
	}
}
