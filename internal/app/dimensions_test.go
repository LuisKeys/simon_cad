package app

import (
	"encoding/json"
	"fmt"
	"math"
	"testing"

	"github.com/accelone/simoncad/internal/dimensions"
)

// dims measures an object through the same query tool the agent and the UI use.
func dims(t *testing.T, a *App, id string) *dimensions.Dimensions {
	t.Helper()
	o := mustOK(t, a.ApplyTool("get_object_dimensions", []byte(fmt.Sprintf(`{"object_id":%q}`, id)), PolicyAuto))
	d, ok := o.Data.(*dimensions.Dimensions)
	if !ok {
		t.Fatalf("get_object_dimensions returned %T", o.Data)
	}
	return d
}

func wantSize(t *testing.T, d *dimensions.Dimensions, w, dp, h float64) {
	t.Helper()
	if math.Abs(d.Width-w) > 1e-6 || math.Abs(d.Depth-dp) > 1e-6 || math.Abs(d.Height-h) > 1e-6 {
		t.Fatalf("%s is %.9g × %.9g × %.9g, want W %g × D %g × H %g", d.Object, d.Width, d.Depth, d.Height, w, dp, h)
	}
}

func near3(a, b [3]float64, tol float64) bool {
	for i := range a {
		if math.Abs(a[i]-b[i]) > tol {
			return false
		}
	}
	return true
}

// The spec's example: 120 × 80 × 40, width → 150 gives 150 × 80 × 40, about the center, as one
// undoable ChangeSet that touches nothing else.
func TestSetObjectDimensionChangesOnlyThatDimension(t *testing.T) {
	a := New("demo", clock)
	defer a.Close()
	mustOK(t, a.ApplyTool("create_box", []byte(`{"id":"b","dx":120,"dy":80,"dz":40}`), PolicyAuto))
	mustOK(t, a.ApplyTool("create_cylinder", []byte(`{"id":"other","radius":5,"height":10,"x":300}`), PolicyAuto))
	before := dims(t, a, "b")
	wantSize(t, before, 120, 80, 40)
	if before.Rotated || before.Origin != [3]float64{0, 0, 0} || before.Center != [3]float64{60, 40, 20} {
		t.Fatalf("unrotated box: %+v", before)
	}
	_, omin, omax := leafProps(t, a, "other")
	hash := a.Graph.Hash()

	o := mustOK(t, a.ApplyTool("set_object_dimension", []byte(`{"object_id":"b","axis":"width","value":150}`), PolicyAuto))
	if !o.Committed {
		t.Fatalf("a single dimension edit should auto-commit: %+v", o.ChangeSet.Warnings)
	}
	leaf := "resize"
	after := dims(t, a, leaf)
	wantSize(t, after, 150, 80, 40)
	if after.Center != before.Center {
		t.Fatalf("resizing about the center moved it: %v → %v", before.Center, after.Center)
	}
	if v, _, _ := leafProps(t, a, leaf); !approx(v, 150*80*40) {
		t.Fatalf("volume %g", v)
	}
	if _, min, max := leafProps(t, a, "other"); min != omin || max != omax {
		t.Fatal("another object changed")
	}

	if _, err := a.Undo(); err != nil {
		t.Fatal(err)
	}
	if a.Graph.Hash() != hash {
		t.Fatal("undo did not restore the model")
	}
	wantSize(t, dims(t, a, "b"), 120, 80, 40)
	if _, err := a.Redo(); err != nil {
		t.Fatal(err)
	}
	wantSize(t, dims(t, a, "resize"), 150, 80, 40)
}

func TestLockProportions(t *testing.T) {
	a := New("demo", clock)
	defer a.Close()
	mustOK(t, a.ApplyTool("create_box", []byte(`{"id":"b","dx":120,"dy":80,"dz":40}`), PolicyAuto))
	if dims(t, a, "b").LockProportions {
		t.Fatal("lock proportions defaults to off")
	}
	geo := a.Graph.GeometryHash()
	o := mustOK(t, a.ApplyTool("set_object_proportional_scaling", []byte(`{"object_id":"b","enabled":true}`), PolicyAuto))
	if !o.Committed || !o.ChangeSet.AppearanceOnly() || a.Graph.GeometryHash() != geo {
		t.Fatal("lock proportions is an editing preference: committed at once, no geometry change")
	}
	mustFail(t, a.ApplyTool("set_object_proportional_scaling", []byte(`{"object_id":"b","enabled":true}`), PolicyAuto), "already on")

	mustOK(t, a.ApplyTool("set_object_dimension", []byte(`{"object_id":"b","axis":"width","value":150}`), PolicyAuto))
	leaf := leaves(t, a)[0]
	wantSize(t, dims(t, a, leaf), 150, 100, 50)
	if !a.Graph.Get(leaf).LockProportions {
		t.Fatal("the resized body keeps the lock")
	}
	// An explicit override for one call, and several explicit dimensions, ignore the lock.
	mustOK(t, a.ApplyTool("set_object_dimension", []byte(`{"object_id":"`+leaf+`","axis":"z","value":20,"proportional":false}`), PolicyAuto))
	wantSize(t, dims(t, a, leaves(t, a)[0]), 150, 100, 20)
	mustOK(t, a.ApplyTool("resize_object", []byte(`{"object_id":"`+leaves(t, a)[0]+`","width":100,"depth":50}`), PolicyAuto))
	wantSize(t, dims(t, a, leaves(t, a)[0]), 100, 50, 20)
}

// Consecutive edits fold into one resize feature; cancelling them out removes it.
func TestDimensionEditsFoldIntoOneResize(t *testing.T) {
	a := New("demo", clock)
	defer a.Close()
	mustOK(t, a.ApplyTool("create_box", []byte(`{"id":"b","dx":120,"dy":80,"dz":40}`), PolicyAuto))
	mustOK(t, a.ApplyTool("set_object_dimension", []byte(`{"object_id":"b","axis":"width","value":150}`), PolicyAuto))
	mustOK(t, a.ApplyTool("set_object_dimension", []byte(`{"axis":"height","value":10,"object_id":"resize"}`), PolicyAuto))
	if l := leaves(t, a); len(l) != 1 || l[0] != "resize" || len(a.Graph.Nodes) != 2 {
		t.Fatalf("edits should fold into one resize feature: leaves %v, %d nodes", l, len(a.Graph.Nodes))
	}
	wantSize(t, dims(t, a, "resize"), 150, 80, 10)
	mustOK(t, a.ApplyTool("resize_object", []byte(`{"object_id":"resize","width":120,"height":40}`), PolicyAuto))
	if l := leaves(t, a); len(l) != 1 || l[0] != "b" || a.Graph.Get("resize") != nil {
		t.Fatalf("back to the original size, the resize disappears: %v", l)
	}
	if _, err := a.Undo(); err != nil {
		t.Fatal(err)
	}
	wantSize(t, dims(t, a, "resize"), 150, 80, 10)

	// A different anchor does not fold: it is a different scale.
	mustOK(t, a.ApplyTool("set_object_dimension", []byte(`{"object_id":"resize","axis":"depth","value":40,"anchor":"object_origin"}`), PolicyAuto))
	if l := leaves(t, a); l[0] == "resize" {
		t.Fatal("an origin-anchored edit should add its own resize")
	}
	// Nor does a resize that another object builds on (a linked copy would change too).
	mustOK(t, a.ApplyTool("duplicate", []byte(`{"features":["resize_2"],"dx":300}`), PolicyAuto))
	mustOK(t, a.ApplyTool("set_object_dimension", []byte(`{"object_id":"resize_2","axis":"depth","value":20}`), PolicyAuto))
	if a.Graph.Get("resize_3") == nil {
		t.Fatal("a resize with dependents must not be edited in place")
	}
	wantSize(t, dims(t, a, "resize_2_copy"), 150, 40, 10)
}

func TestResizeAboutTheObjectOrigin(t *testing.T) {
	a := New("demo", clock)
	defer a.Close()
	mustOK(t, a.ApplyTool("create_box", []byte(`{"id":"b","dx":20,"dy":10,"dz":5,"x":10,"y":20,"z":3}`), PolicyAuto))
	mustOK(t, a.ApplyTool("transform", []byte(`{"target":"b","dx":5,"id":"moved"}`), PolicyAuto))
	if o := dims(t, a, "moved").Origin; o != [3]float64{15, 20, 3} {
		t.Fatalf("object origin follows the move: %v", o)
	}
	mustOK(t, a.ApplyTool("resize_object", []byte(`{"object_id":"moved","width":40,"height":10,"anchor":"object_origin"}`), PolicyAuto))
	_, min, max := leafProps(t, a, leaves(t, a)[0])
	if !near3(min, [3]float64{15, 20, 3}, 1e-6) || !near3(max, [3]float64{55, 30, 13}, 1e-6) {
		t.Fatalf("the origin corner should stay put: min %v max %v", min, max)
	}
}

// A rotated object is measured and resized along its own axes: its width stays its width.
func TestRotatedObjectUsesLocalAxes(t *testing.T) {
	a := New("demo", clock)
	defer a.Close()
	mustOK(t, a.ApplyTool("create_box", []byte(`{"id":"b","dx":120,"dy":80,"dz":40}`), PolicyAuto))
	mustOK(t, a.ApplyTool("transform", []byte(`{"target":"b","angle_deg":30,"ax":0,"ay":0,"az":1,"id":"r1"}`), PolicyAuto))
	mustOK(t, a.ApplyTool("transform", []byte(`{"target":"r1","angle_deg":20,"ax":1,"ay":0,"az":0,"dx":7,"id":"r2"}`), PolicyAuto))
	d := dims(t, a, "r2")
	if !d.Rotated {
		t.Fatal("r2 is rotated")
	}
	wantSize(t, d, 120, 80, 40)
	_, wmin, wmax := leafProps(t, a, "r2")
	if math.Abs((wmax[0]-wmin[0])-120) < 1 {
		t.Fatal("the world box of a rotated object differs from its local one")
	}
	center := d.Center

	mustOK(t, a.ApplyTool("set_object_dimension", []byte(`{"object_id":"r2","axis":"width","value":150}`), PolicyAuto))
	after := dims(t, a, leaves(t, a)[0])
	wantSize(t, after, 150, 80, 40)
	if !near3(after.Center, center, 1e-6) || after.Axes != d.Axes {
		t.Fatalf("orientation and center are preserved: %v %v → %v %v", d.Axes, center, after.Axes, after.Center)
	}
	if v, _, _ := leafProps(t, a, leaves(t, a)[0]); !approx(v, 150*80*40) {
		t.Fatalf("a shear would change the volume: %g", v)
	}
	// The world-axis scale tool still exists and is unaffected.
	mustOK(t, a.ApplyTool("scale", []byte(`{"target":"`+leaves(t, a)[0]+`","factor":2}`), PolicyAuto))
	wantSize(t, dims(t, a, leaves(t, a)[0]), 300, 160, 80)
}

// Resizing a cylinder across its axis makes it elliptical: expected, not repaired.
func TestNonUniformResizeOfACylinder(t *testing.T) {
	a := New("demo", clock)
	defer a.Close()
	mustOK(t, a.ApplyTool("create_cylinder", []byte(`{"id":"c","radius":10,"height":30}`), PolicyAuto))
	wantSize(t, dims(t, a, "c"), 20, 20, 30)
	mustOK(t, a.ApplyTool("set_object_dimension", []byte(`{"object_id":"c","axis":"width","value":40}`), PolicyAuto))
	wantSize(t, dims(t, a, leaves(t, a)[0]), 40, 20, 30)
	// The scaled cylinder wall is a B-spline, so its volume is the kernel's approximation.
	if v, _, _ := leafProps(t, a, leaves(t, a)[0]); math.Abs(v-math.Pi*20*10*30) > 0.015*v {
		t.Fatalf("ellipse area π·a·b × h, got %g", v)
	}
}

func TestDimensionToolsUseTheSelection(t *testing.T) {
	a := New("demo", clock)
	defer a.Close()
	mustOK(t, a.ApplyTool("create_box", []byte(`{"id":"b","dx":10,"dy":10,"dz":10}`), PolicyAuto))
	mustOK(t, a.ApplyTool("create_box", []byte(`{"id":"c","dx":10,"dy":10,"dz":10,"x":30}`), PolicyAuto))
	mustFail(t, a.ApplyTool("get_object_dimensions", []byte(`{}`), PolicyAuto), "select one object")
	if err := a.SetSelection([]string{"body(feature=b)", "face(feature=c,side=+Z)"}); err != nil {
		t.Fatal(err)
	}
	mustFail(t, a.ApplyTool("set_object_dimension", []byte(`{"axis":"width","value":20}`), PolicyAuto), "2 objects")
	if err := a.SetSelection([]string{"face(feature=c,side=+Z)"}); err != nil {
		t.Fatal(err)
	}
	mustOK(t, a.ApplyTool("set_object_dimension", []byte(`{"axis":"depth","value":20}`), PolicyAuto))
	wantSize(t, dims(t, a, leaves(t, a)[1]), 10, 20, 10)

	mustFail(t, a.ApplyTool("set_object_dimension", []byte(`{"object_id":"c","axis":"width","value":20}`), PolicyAuto), "not a visible body")
	mustFail(t, a.ApplyTool("set_object_dimension", []byte(`{"object_id":"b","axis":"width","value":10}`), PolicyAuto), "already 10 mm")
	mustFail(t, a.ApplyTool("set_object_dimension", []byte(`{"object_id":"b","axis":"width","value":0}`), PolicyAuto), "")
	mustFail(t, a.ApplyTool("resize_object", []byte(`{"object_id":"b"}`), PolicyAuto), "width, depth and/or height")
}

// Replay and persistence: the resize is ordinary CAD-IR (a scale with literal parameters).
func TestResizeIsReplayable(t *testing.T) {
	a := New("demo", clock)
	defer a.Close()
	mustOK(t, a.ApplyTool("create_box", []byte(`{"id":"b","dx":120,"dy":80,"dz":40}`), PolicyAuto))
	mustOK(t, a.ApplyTool("transform", []byte(`{"target":"b","angle_deg":45,"ax":0,"ay":1,"az":1,"id":"r"}`), PolicyAuto))
	mustOK(t, a.ApplyTool("resize_object", []byte(`{"object_id":"r","depth":100,"anchor":"object_origin"}`), PolicyAuto))
	n := a.Graph.Get("resize")
	b, _ := json.Marshal(n.Parameters)
	for _, k := range []string{"frame_ax", "frame_angle_deg", "ox", "sy"} {
		if _, ok := n.Parameters[k]; !ok {
			t.Fatalf("resize parameters %s lack %s", b, k)
		}
	}
	wantSize(t, dims(t, a, "resize"), 120, 100, 40)
	if _, match, _, err := a.Replay(); err != nil || !match {
		t.Fatalf("replay: match=%v err=%v", match, err)
	}
}
