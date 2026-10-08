package app

import (
	"encoding/json"
	"fmt"
	"math"
	"testing"

	"github.com/accelone/simoncad/internal/events"
	"github.com/accelone/simoncad/internal/geometry/features"
	"github.com/accelone/simoncad/internal/tools"
	"github.com/accelone/simoncad/internal/transform"
)

// objectTransform reads an object's transform through the query tool the agent and the UI use.
func objectTransform(t *testing.T, a *App, id string) *transform.Object {
	t.Helper()
	o := mustOK(t, a.ApplyTool("get_object_transform", []byte(fmt.Sprintf(`{"object_id":%q}`, id)), PolicyAuto))
	v, ok := o.Data.(tools.ObjectTransformView)
	if !ok {
		t.Fatalf("get_object_transform returned %T", o.Data)
	}
	return v.Object
}

// lastLeaf is the visible body a move or rotation produced (it replaces the moved body).
func lastLeaf(t *testing.T, a *App) string {
	t.Helper()
	l := leaves(t, a)
	return l[len(l)-1]
}

func boundsOf(t *testing.T, a *App, id string) (min, max [3]float64) {
	t.Helper()
	_, min, max = leafProps(t, a, id)
	return min, max
}

func wantBounds(t *testing.T, a *App, id string, min, max [3]float64) {
	t.Helper()
	gmin, gmax := boundsOf(t, a, id)
	if !near3(gmin, min, 1e-6) || !near3(gmax, max, 1e-6) {
		t.Fatalf("%s bounds = %v..%v, want %v..%v", id, gmin, gmax, min, max)
	}
}

func transformEvents(a *App) int {
	n := 0
	for _, e := range a.Bus.Events() {
		if e.Type == events.ObjectTransformed {
			n++
		}
	}
	return n
}

// Moving along each axis and within each plane is one ChangeSet (one undo step) that adds one
// transform feature; undo restores the original exactly and redo brings the move back.
func TestMoveObjectAxesPlanesUndoRedo(t *testing.T) {
	a := New("demo", clock)
	defer a.Close()
	mustOK(t, a.ApplyTool("create_box", []byte(`{"id":"b","dx":10,"dy":20,"dz":30}`), PolicyAuto))
	h0 := a.Graph.Hash()
	cases := []struct {
		args string
		d    [3]float64
	}{
		{`{"dx":15}`, [3]float64{15, 0, 0}},
		{`{"dy":-7.5}`, [3]float64{0, -7.5, 0}},
		{`{"dz":4}`, [3]float64{0, 0, 4}},
		{`{"dx":1,"dy":2}`, [3]float64{1, 2, 0}},         // XY plane
		{`{"dx":-3,"dz":5}`, [3]float64{-3, 0, 5}},       // XZ plane
		{`{"dy":6,"dz":-2.25}`, [3]float64{0, 6, -2.25}}, // YZ plane
	}
	for _, c := range cases {
		id := lastLeaf(t, a)
		min, max := boundsOf(t, a, id)
		events0 := transformEvents(a)
		var args map[string]any
		_ = json.Unmarshal([]byte(c.args), &args)
		args["object_id"] = id
		raw, _ := json.Marshal(args)
		o := mustOK(t, a.ApplyTool("move_object", raw, PolicyAuto))
		if !o.Committed || len(o.ChangeSet.Commands) != 1 {
			t.Fatalf("%s: a move is one auto-committed command: %+v", c.args, o)
		}
		if transformEvents(a) != events0+1 {
			t.Fatalf("%s: want exactly one ObjectTransformed event", c.args)
		}
		moved := lastLeaf(t, a)
		if moved == id || a.Graph.Get(moved).Type != "transform" {
			t.Fatalf("%s: the move should add a transform feature, leaf=%s", c.args, moved)
		}
		for i := range min {
			min[i] += c.d[i]
			max[i] += c.d[i]
		}
		wantBounds(t, a, moved, min, max)
	}
	for range cases {
		if _, err := a.Undo(); err != nil {
			t.Fatal(err)
		}
	}
	if a.Graph.Hash() != h0 {
		t.Fatal("undoing every move must restore the original state exactly")
	}
	wantBounds(t, a, "b", [3]float64{0, 0, 0}, [3]float64{10, 20, 30})
	if _, err := a.Redo(); err != nil {
		t.Fatal(err)
	}
	wantBounds(t, a, lastLeaf(t, a), [3]float64{15, 0, 0}, [3]float64{25, 20, 30})
}

// Absolute positions place the object's origin; omitted axes stay. Units convert at the edge.
func TestMoveObjectAbsoluteAndUnits(t *testing.T) {
	a := New("demo", clock)
	defer a.Close()
	mustOK(t, a.ApplyTool("create_box", []byte(`{"id":"b","dx":10,"dy":10,"dz":10,"x":5,"y":5,"z":5}`), PolicyAuto))
	mustOK(t, a.ApplyTool("move_object", []byte(`{"object_id":"b","x":100,"y":50}`), PolicyAuto))
	if o := objectTransform(t, a, lastLeaf(t, a)); !near3(o.Origin, [3]float64{100, 50, 5}, 1e-9) {
		t.Fatalf("origin = %v, want (100, 50, 5)", o.Origin)
	}
	mustOK(t, a.ApplyTool("move_object", []byte(fmt.Sprintf(`{"object_id":%q,"dz":1,"unit":"in"}`, lastLeaf(t, a))), PolicyAuto))
	if o := objectTransform(t, a, lastLeaf(t, a)); !near3(o.Origin, [3]float64{100, 50, 30.4}, 1e-9) {
		t.Fatalf("1 in = 25.4 mm: origin = %v", o.Origin)
	}
	mustFail(t, a.ApplyTool("move_object", []byte(fmt.Sprintf(`{"object_id":%q,"dx":1,"x":3}`, lastLeaf(t, a))), PolicyAuto), "not both")
	mustFail(t, a.ApplyTool("move_object", []byte(fmt.Sprintf(`{"object_id":%q}`, lastLeaf(t, a))), PolicyAuto), "give a move")
	mustFail(t, a.ApplyTool("move_object", []byte(`{"object_id":"b","dx":1}`), PolicyAuto), "not a visible body")
}

// With snapping on, the moved origin lands on the grid (respecting its origin, negative
// coordinates included) and constrained axes keep their coordinate exactly.
func TestMoveObjectSnapsToTheGrid(t *testing.T) {
	a := New("demo", clock)
	defer a.Close()
	mustOK(t, a.ApplyTool("create_box", []byte(`{"id":"b","dx":4,"dy":4,"dz":4,"x":3,"y":-2,"z":1}`), PolicyAuto))
	mustOK(t, a.ApplyTool("configure_grid", []byte(`{"spacing":10,"origin":[5,0,0]}`), PolicyAuto))
	mustOK(t, a.ApplyTool("configure_snapping", []byte(`{"enabled":true,"grid":true,"translation":true}`), PolicyAuto))
	mustOK(t, a.ApplyTool("move_object", []byte(`{"object_id":"b","dx":-21,"dy":3,"snap":true}`), PolicyAuto))
	o := objectTransform(t, a, lastLeaf(t, a))
	// x: 3 - 21 = -18 → nearest line of 5 + 10k is -15; y: -2 + 3 = 1 → 0; z is not moved.
	if !near3(o.Origin, [3]float64{-15, 0, 1}, 1e-9) {
		t.Fatalf("snapped origin = %v, want (-15, 0, 1)", o.Origin)
	}
}

// Rotating about X, Y and Z keeps the pivot fixed: the origin (default) or the box center.
func TestRotateObjectAxesAndPivots(t *testing.T) {
	a := New("demo", clock)
	defer a.Close()
	mustOK(t, a.ApplyTool("create_box", []byte(`{"id":"b","dx":10,"dy":20,"dz":30,"x":1,"y":2,"z":3}`), PolicyAuto))
	for _, axis := range []string{"x", "y", "z"} {
		for _, pivot := range []string{"origin", "center"} {
			id := lastLeaf(t, a)
			before := objectTransform(t, a, id)
			o := mustOK(t, a.ApplyTool("rotate_object", []byte(fmt.Sprintf(`{"object_id":%q,"angle":90,"axis":%q,"pivot":%q}`, id, axis, pivot)), PolicyAuto))
			if !o.Committed || len(o.ChangeSet.Commands) != 1 {
				t.Fatalf("a rotation is one auto-committed command: %+v", o)
			}
			after := objectTransform(t, a, lastLeaf(t, a))
			fixed, now := before.Origin, after.Origin
			if pivot == "center" {
				fixed, now = before.Center, after.Center
			}
			if !near3(fixed, now, 1e-6) {
				t.Fatalf("rotate about %s through the %s: the pivot moved %v → %v", axis, pivot, fixed, now)
			}
		}
	}
	// Two quarter turns about each of X, Y and Z are half turns, and Rz(180)·Ry(180)·Rx(180)
	// is the identity: the composed orientation must come back exactly unrotated.
	if o := objectTransform(t, a, lastLeaf(t, a)); math.Abs(o.AngleDeg) > 1e-9 {
		t.Fatalf("composed orientation = %g° about %v, want none", o.AngleDeg, o.Axis)
	}
	mustFail(t, a.ApplyTool("rotate_object", []byte(fmt.Sprintf(`{"object_id":%q,"angle":360}`, lastLeaf(t, a))), PolicyAuto), "nothing would change")
	mustFail(t, a.ApplyTool("rotate_object", []byte(fmt.Sprintf(`{"object_id":%q,"angle":5,"pivot":"point"}`, lastLeaf(t, a))), PolicyAuto), "needs `point`")
}

// The default axis is the grid plane's normal; radians and angular snapping convert at the edge.
func TestRotateObjectDefaultsUnitsAndSnap(t *testing.T) {
	a := New("demo", clock)
	defer a.Close()
	mustOK(t, a.ApplyTool("create_box", []byte(`{"id":"b","dx":10,"dy":10,"dz":10}`), PolicyAuto))
	mustOK(t, a.ApplyTool("configure_grid", []byte(`{"plane":"XZ"}`), PolicyAuto))
	mustOK(t, a.ApplyTool("configure_snapping", []byte(`{"enabled":true,"rotation":true,"rotation_increment":15}`), PolicyAuto))
	mustOK(t, a.ApplyTool("rotate_object", []byte(fmt.Sprintf(`{"object_id":"b","angle":%v,"angle_unit":"rad","snap":true}`, 50*math.Pi/180)), PolicyAuto))
	n := a.Graph.Get(lastLeaf(t, a))
	if n.Parameters["angle_deg"] != 45.0 || n.Parameters["ay"] != 1.0 || n.Parameters["az"] != 0.0 { // az defaults to 1, so 0 is stored
		t.Fatalf("50° snapped to 45° about Y (the XZ grid's normal): %v", n.Parameters)
	}
}

// A rotation crossing ±180° is stored as the equivalent angle and lands exactly where the
// opposite rotation does.
func TestRotationAcrossHalfTurnMatches(t *testing.T) {
	a := New("demo", clock)
	defer a.Close()
	mustOK(t, a.ApplyTool("create_box", []byte(`{"id":"b","dx":10,"dy":4,"dz":2,"x":3}`), PolicyAuto))
	mustOK(t, a.ApplyTool("rotate_object", []byte(`{"object_id":"b","angle":190}`), PolicyAuto))
	n := a.Graph.Get(lastLeaf(t, a))
	if n.Parameters["angle_deg"] != -170.0 {
		t.Fatalf("190° is stored as -170°: %v", n.Parameters)
	}
	min1, max1 := boundsOf(t, a, n.ID)
	b := New("demo", clock)
	defer b.Close()
	mustOK(t, b.ApplyTool("create_box", []byte(`{"id":"b","dx":10,"dy":4,"dz":2,"x":3}`), PolicyAuto))
	mustOK(t, b.ApplyTool("rotate_object", []byte(`{"object_id":"b","angle":-170}`), PolicyAuto))
	min2, max2 := boundsOf(t, b, lastLeaf(t, b))
	if !near3(min1, min2, 1e-6) || !near3(max1, max2, 1e-6) {
		t.Fatalf("190° and -170° differ: %v..%v vs %v..%v", min1, max1, min2, max2)
	}
}

// The agent's low-level transform tool and the TransformService tools (what the viewport
// commits) produce the same geometry for the same motion.
func TestAgentAndInteractiveTransformsAgree(t *testing.T) {
	run := func(tool, args string) ([3]float64, [3]float64) {
		a := New("demo", clock)
		defer a.Close()
		mustOK(t, a.ApplyTool("create_box", []byte(`{"id":"b","dx":10,"dy":20,"dz":5,"x":2,"y":3}`), PolicyAuto))
		mustOK(t, a.ApplyTool(tool, []byte(args), PolicyAuto))
		return boundsOf(t, a, lastLeaf(t, a))
	}
	pairs := [][2][2]string{
		{{"transform", `{"target":"b","dx":20,"dz":-4}`}, {"move_object", `{"object_id":"b","dx":20,"dz":-4}`}},
		{{"transform", `{"target":"b","angle_deg":30,"ax":1,"ay":0,"az":0,"ox":2,"oy":3}`}, {"rotate_object", `{"object_id":"b","angle":30,"axis":"x"}`}},
		{{"transform", `{"target":"b","angle_deg":-45,"ox":7,"oy":13,"oz":2.5}`}, {"rotate_object", `{"object_id":"b","angle":-45,"axis":"z","pivot":"center"}`}},
	}
	for _, p := range pairs {
		min1, max1 := run(p[0][0], p[0][1])
		min2, max2 := run(p[1][0], p[1][1])
		if !near3(min1, min2, 1e-6) || !near3(max1, max2, 1e-6) {
			t.Fatalf("%s vs %s: %v..%v vs %v..%v", p[0][1], p[1][1], min1, max1, min2, max2)
		}
	}
}

// A component instance is moved through its own placement (its local transform in the parent):
// no feature is added, the component is not edited, a rotated or scaled instance composes
// correctly with world motions, and undo restores the placement.
func TestTransformComponentInstancePlacement(t *testing.T) {
	lib := t.TempDir()
	a := newInLibrary(t, "car", lib)
	defer a.Close()
	mustOK(t, a.ApplyTool("create_box", []byte(`{"id":"wheel","dx":10,"dy":4,"dz":2}`), PolicyAuto))
	mustOK(t, a.ApplyTool("create_component", []byte(`{"name":"Wheel","features":["wheel"]}`), PolicyCommit))
	mustOK(t, a.ApplyTool("insert_component", []byte(`{"component":"Wheel","name":"Wheel FR","dx":100,"angle_deg":90,"ax":1,"ay":0,"az":0,"sx":2}`), PolicyCommit))
	var inst string
	for _, n := range a.Graph.Nodes {
		if n.Type == features.TypeComponentInstance && n.Metadata.Name == "Wheel FR" {
			inst = n.ID
		}
	}
	if inst == "" {
		t.Fatal("no instance")
	}
	src := a.Graph.Get(inst).Parameters[features.ParamContentHash]
	nodes := len(a.Graph.Nodes)
	o0 := objectTransform(t, a, inst)
	if o0.Write != transform.WriteInstance {
		t.Fatalf("an instance is moved through its placement, got %q", o0.Write)
	}
	min0, max0 := boundsOf(t, a, inst)

	mustOK(t, a.ApplyTool("move_object", []byte(fmt.Sprintf(`{"object_id":%q,"dy":7}`, inst)), PolicyAuto))
	if len(a.Graph.Nodes) != nodes {
		t.Fatal("moving an instance must not add features")
	}
	min1, max1 := boundsOf(t, a, inst)
	if !near3(min1, [3]float64{min0[0], min0[1] + 7, min0[2]}, 1e-6) || !near3(max1, [3]float64{max0[0], max0[1] + 7, max0[2]}, 1e-6) {
		t.Fatalf("world move of a rotated, scaled instance: %v..%v from %v..%v", min1, max1, min0, max0)
	}
	before := objectTransform(t, a, inst)
	o := mustOK(t, a.ApplyTool("rotate_object", []byte(fmt.Sprintf(`{"object_id":%q,"angle":45,"axis":"z","pivot":"center"}`, inst)), PolicyAuto))
	if !o.Committed {
		t.Fatalf("an instance placement update is one low-risk ChangeSet: %+v", o)
	}
	after := objectTransform(t, a, inst)
	if !near3(before.Center, after.Center, 1e-6) {
		t.Fatalf("pivot=center moved: %v → %v", before.Center, after.Center)
	}
	if a.Graph.Get(inst).Parameters[features.ParamContentHash] != src {
		t.Fatal("the component reference must not change")
	}
	if s := a.Graph.Get(inst).Parameters["sx"]; s != 2.0 {
		t.Fatalf("the instance's own scale is kept, sx=%v", s)
	}
	if _, err := a.Undo(); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Undo(); err != nil {
		t.Fatal(err)
	}
	wantBounds(t, a, inst, min0, max0)
}

// A locked object is refused before anything is proposed.
func TestLockedObjectCannotBeMoved(t *testing.T) {
	a := New("demo", clock)
	defer a.Close()
	mustOK(t, a.ApplyTool("create_box", []byte(`{"id":"b","dx":10,"dy":10,"dz":10}`), PolicyAuto))
	mustOK(t, a.ApplyTool("define_region", []byte(`{"name":"base","objects":["b"]}`), PolicyAuto))
	mustOK(t, a.ApplyTool("lock_region", []byte(`{"name":"base"}`), PolicyAuto))
	if o := objectTransform(t, a, "b"); o.Transformable {
		t.Fatal("a locked object is not transformable")
	}
	mustFail(t, a.ApplyTool("move_object", []byte(`{"object_id":"b","dx":1}`), PolicyAuto), "locked region")
}
