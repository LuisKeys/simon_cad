package app

import (
	"math"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func leafProps(t *testing.T, a *App, id string) (vol float64, min, max [3]float64) {
	t.Helper()
	m, err := a.Model()
	if err != nil {
		t.Fatal(err)
	}
	r := m.Nodes[id]
	if r == nil || r.Body == nil {
		t.Fatalf("%s has no body (%+v)", id, r)
	}
	p := r.Body.Props
	return p.Volume, [3]float64{p.MinX, p.MinY, p.MinZ}, [3]float64{p.MaxX, p.MaxY, p.MaxZ}
}

func leaves(t *testing.T, a *App) []string {
	t.Helper()
	m, err := a.Model()
	if err != nil {
		t.Fatal(err)
	}
	return m.Leaves
}

func approx(a, b float64) bool { return math.Abs(a-b) < 1e-6*math.Max(1, math.Abs(b)) }

func mustFail(t *testing.T, o Outcome, want string) {
	t.Helper()
	if o.OK || !strings.Contains(o.Error, want) {
		t.Fatalf("want an error containing %q, got ok=%v err=%q", want, o.OK, o.Error)
	}
}

func TestScale(t *testing.T) {
	a := New("demo", clock)
	defer a.Close()
	mustOK(t, a.ApplyTool("create_box", []byte(`{"id":"b","dx":10,"dy":20,"dz":30}`), PolicyAuto))
	mustOK(t, a.ApplyTool("scale", []byte(`{"target":"b","factor":2}`), PolicyAuto))
	v, min, max := leafProps(t, a, "scale")
	if !approx(v, 6000*8) || !approx(min[0], -5) || !approx(max[2], 45) {
		t.Fatalf("uniform scale about the center: vol=%g min=%v max=%v", v, min, max)
	}
	// Editing the source re-scales; base keeps the bottom on z=0.
	mustOK(t, a.ApplyTool("modify_parameter", []byte(`{"feature":"scale","parameter":"about","value":"base"}`), PolicyAuto))
	_, min, _ = leafProps(t, a, "scale")
	if !approx(min[2], 0) {
		t.Fatalf("about=base should keep min z at 0, got %v", min)
	}
	mustOK(t, a.ApplyTool("scale", []byte(`{"target":"scale","sx":0.5,"sz":2,"about":"origin","id":"squash"}`), PolicyAuto))
	v, _, max = leafProps(t, a, "squash")
	if !approx(v, 6000*8) || !approx(max[0], 7.5) {
		t.Fatalf("non-uniform scale: vol=%g max=%v", v, max)
	}
	mustFail(t, a.ApplyTool("scale", []byte(`{"target":"b","factor":2}`), PolicyAuto), "not a visible body")
	mustFail(t, a.ApplyTool("scale", []byte(`{"target":"squash"}`), PolicyAuto), "factor")
	mustFail(t, a.ApplyTool("scale", []byte(`{"target":"squash","factor":1}`), PolicyAuto), "all 1")
	mustFail(t, a.ApplyTool("modify_parameter", []byte(`{"feature":"squash","parameter":"sx","value":0}`), PolicyAuto), "greater than 0")
}

func TestDuplicateIsLinkedAndKeepsSource(t *testing.T) {
	a := New("demo", clock)
	defer a.Close()
	mustOK(t, a.ApplyTool("create_box", []byte(`{"id":"b","dx":10,"dy":10,"dz":10}`), PolicyAuto))
	mustOK(t, a.ApplyTool("create_sphere", []byte(`{"id":"s","radius":2,"x":50}`), PolicyAuto))
	mustOK(t, a.ApplyTool("set_color", []byte(`{"features":["b"],"color":"#FF0000"}`), PolicyAuto))
	o := mustOK(t, a.ApplyTool("duplicate", []byte(`{"features":["b","s"],"dx":20}`), PolicyAuto))
	if !o.Committed || len(o.ChangeSet.Commands) != 2 {
		t.Fatalf("duplicating two bodies is one low-risk ChangeSet that auto-commits: %+v", o)
	}
	if got := strings.Join(leaves(t, a), ","); got != "b,s,b_copy,s_copy" {
		t.Fatalf("sources must stay visible next to their copies, leaves=%s", got)
	}
	if a.Graph.Get("b_copy").Appearance == nil || a.Graph.Get("b_copy").Appearance.Color != "#FF0000" {
		t.Fatalf("the copy keeps the source's appearance: %+v", a.Graph.Get("b_copy").Appearance)
	}
	_, min, _ := leafProps(t, a, "b_copy")
	if !approx(min[0], 20) {
		t.Fatalf("copy offset: min=%v", min)
	}
	mustOK(t, a.ApplyTool("modify_parameter", []byte(`{"feature":"b","parameter":"dx","value":5}`), PolicyAuto))
	if v, _, _ := leafProps(t, a, "b_copy"); !approx(v, 500) {
		t.Fatalf("the copy follows the source: vol=%g", v)
	}
	// Deleting a copy never reports its source as "freed".
	d := mustOK(t, a.ApplyTool("delete_objects", []byte(`{"features":["b_copy"]}`), PolicyCommit))
	for _, w := range d.ChangeSet.Warnings {
		if strings.Contains(w, "no longer consumed") {
			t.Fatalf("unexpected warning %q", w)
		}
	}
	// Review-style preview leaves it proposed.
	if o := a.ApplyTool("duplicate", []byte(`{"features":["s"]}`), PolicyPreview); !o.OK || o.Committed {
		t.Fatalf("preview must not commit: %+v", o)
	}
}

func TestAlign(t *testing.T) {
	a := New("demo", clock)
	defer a.Close()
	mustOK(t, a.ApplyTool("create_box", []byte(`{"id":"a","dx":10,"dy":10,"dz":10}`), PolicyAuto))
	mustOK(t, a.ApplyTool("create_box", []byte(`{"id":"b","dx":4,"dy":4,"dz":4,"x":30,"y":7,"z":5}`), PolicyAuto))
	mustOK(t, a.ApplyTool("create_box", []byte(`{"id":"c","dx":2,"dy":2,"dz":2,"x":-20,"z":-3}`), PolicyAuto))

	o := mustOK(t, a.ApplyTool("align", []byte(`{"features":["a","b","c"],"axes":["z"],"mode":"min"}`), PolicyAuto))
	if !o.Committed || len(o.ChangeSet.Commands) != 2 {
		t.Fatalf("align moves b and c in one auto-committed ChangeSet: %+v", o)
	}
	for _, id := range []string{"align_b", "align_c"} {
		if _, min, _ := leafProps(t, a, id); !approx(min[2], 0) {
			t.Fatalf("%s min z = %g, want 0", id, min[2])
		}
	}
	mustOK(t, a.ApplyTool("align", []byte(`{"features":["align_b"],"axes":["x","y"],"mode":"center","reference":"a"}`), PolicyAuto))
	_, min, max := leafProps(t, a, "align_align_b")
	if !approx((min[0]+max[0])/2, 5) || !approx((min[1]+max[1])/2, 5) {
		t.Fatalf("centered on a: min=%v max=%v", min, max)
	}
	// One body defaults to the origin.
	mustOK(t, a.ApplyTool("align", []byte(`{"features":["a"],"axes":["x"],"mode":"max"}`), PolicyAuto))
	if _, _, max := leafProps(t, a, "align_a"); !approx(max[0], 0) {
		t.Fatalf("max x should be 0, got %v", max)
	}
	mustFail(t, a.ApplyTool("align", []byte(`{"features":["align_a"],"axes":["x"],"mode":"max"}`), PolicyAuto), "already aligned")
	mustFail(t, a.ApplyTool("align", []byte(`{"features":["a"],"axes":["x"]}`), PolicyAuto), "not a visible body")
}

func TestFaceExtrudeInsetOffset(t *testing.T) {
	a := New("demo", clock)
	defer a.Close()
	mustOK(t, a.ApplyTool("create_box", []byte(`{"id":"b","dx":10,"dy":20,"dz":30}`), PolicyAuto))

	mustOK(t, a.ApplyTool("face_extrude", []byte(`{"face":"face(feature=b,side=+Z)","distance":5}`), PolicyAuto))
	if v, _, max := leafProps(t, a, "face_extrude"); !approx(v, 7000) || !approx(max[2], 35) {
		t.Fatalf("pull: vol=%g max=%v", v, max)
	}
	mustOK(t, a.ApplyTool("face_extrude", []byte(`{"face":"face(feature=face_extrude,side=+X)","distance":-2,"id":"push"}`), PolicyAuto))
	if v, _, max := leafProps(t, a, "push"); !approx(v, 7000*0.8) || !approx(max[0], 8) {
		t.Fatalf("push: vol=%g max=%v", v, max)
	}

	// Inset with a negative depth: a 4 x 14 pocket 3 deep in the 8 x 20 top.
	mustOK(t, a.ApplyTool("face_inset", []byte(`{"face":"face(feature=push,side=+Z)","inset":2,"depth":-3,"id":"pocket"}`), PolicyAuto))
	if v, _, _ := leafProps(t, a, "pocket"); !approx(v, 5600-4*16*3) {
		t.Fatalf("inset pocket: vol=%g", v)
	}
	// Imprint only: volume unchanged, one more face.
	mustOK(t, a.ApplyTool("face_inset", []byte(`{"face":"face(feature=pocket,side=-Z)","inset":1,"id":"split"}`), PolicyAuto))
	m, _ := a.Model()
	if v := m.Nodes["split"].Body.Props.Volume; !approx(v, 5600-192) || m.Nodes["split"].Body.Props.Faces != m.Nodes["pocket"].Body.Props.Faces+1 {
		t.Fatalf("imprint: vol=%g faces %d vs %d", v, m.Nodes["split"].Body.Props.Faces, m.Nodes["pocket"].Body.Props.Faces)
	}
	mustFail(t, a.ApplyTool("face_inset", []byte(`{"face":"face(feature=split,side=+X)","inset":20}`), PolicyAuto), "leaves nothing")

	// Offset a planar face outward, then back inward.
	mustOK(t, a.ApplyTool("face_offset", []byte(`{"face":"face(feature=split,side=-Y)","distance":4,"id":"grown"}`), PolicyAuto))
	if _, min, _ := leafProps(t, a, "grown"); !approx(min[1], -4) {
		t.Fatalf("offset face: min=%v", min)
	}
	mustFail(t, a.ApplyTool("face_extrude", []byte(`{"face":"face(feature=grown,side=+Z)","distance":0}`), PolicyAuto), "non-zero")
	mustFail(t, a.ApplyTool("face_extrude", []byte(`{"face":"face(feature=b,side=+Z)","distance":1}`), PolicyAuto), "not a visible body")

	// Signed distances stay editable through modify_parameter.
	mustOK(t, a.ApplyTool("modify_parameter", []byte(`{"feature":"grown","parameter":"distance","value":-1}`), PolicyAuto))
	if _, min, _ := leafProps(t, a, "grown"); !approx(min[1], 1) {
		t.Fatalf("offset face inward: min=%v", min)
	}
}

func TestFaceOffsetCylinder(t *testing.T) {
	a := New("demo", clock)
	defer a.Close()
	mustOK(t, a.ApplyTool("create_cylinder", []byte(`{"id":"c","radius":5,"height":10}`), PolicyAuto))
	m, _ := a.Model()
	topo, _ := m.Topology("c")
	side := -1
	for i, f := range topo.Faces {
		if f.Surface == "cylinder" {
			side = i
		}
	}
	mustOK(t, a.ApplyTool("face_offset", []byte(`{"face":"face(feature=c,index=`+strconv.Itoa(side)+`)","distance":1}`), PolicyAuto))
	if v, _, _ := leafProps(t, a, "face_offset"); !approx(v, math.Pi*36*10) {
		t.Fatalf("radius 5 -> 6: vol=%g want %g", v, math.Pi*36*10)
	}
	mustFail(t, a.ApplyTool("face_extrude", []byte(`{"face":"face(feature=face_offset,index=`+strconv.Itoa(side)+`)","distance":1}`), PolicyAuto), "only planar")

	// A hole's wall faces away from the material, towards the axis: +1 shrinks a 6 mm hole to 4 mm.
	mustOK(t, a.ApplyTool("create_box", []byte(`{"id":"p","dx":20,"dy":20,"dz":10,"x":40}`), PolicyAuto))
	mustOK(t, a.ApplyTool("add_hole", []byte(`{"target":"p","diameter":6,"positions":[[50,10]]}`), PolicyAuto))
	m, _ = a.Model()
	topo, _ = m.Topology("hole")
	wall := -1
	for i, f := range topo.Faces {
		if f.Surface == "cylinder" {
			wall = i
		}
	}
	mustOK(t, a.ApplyTool("face_offset", []byte(`{"face":"face(feature=hole,index=`+strconv.Itoa(wall)+`)","distance":1,"id":"tighter"}`), PolicyAuto))
	if v, _, _ := leafProps(t, a, "tighter"); !approx(v, 4000-math.Pi*4*10) {
		t.Fatalf("hole 6 -> 4 mm: vol=%g want %g", v, 4000-math.Pi*4*10)
	}
}

func TestGroupIsPresentationOnly(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "g.scadproj")
	a := New("demo", clock)
	mustOK(t, a.ApplyTool("create_box", []byte(`{"id":"a","dx":10,"dy":10,"dz":10}`), PolicyAuto))
	mustOK(t, a.ApplyTool("create_box", []byte(`{"id":"b","dx":4,"dy":4,"dz":4,"x":30}`), PolicyAuto))
	geo := a.Graph.GeometryHash()
	misses := a.Engine.Stats.Misses
	o := mustOK(t, a.ApplyTool("group", []byte(`{"features":["a","b"],"name":"Base"}`), PolicyAuto))
	if !o.Committed || !o.ChangeSet.AppearanceOnly() {
		t.Fatalf("grouping auto-commits as presentation: %+v", o)
	}
	if a.Graph.GeometryHash() != geo {
		t.Fatal("grouping changed the geometry hash")
	}
	if _, err := a.Model(); err != nil {
		t.Fatal(err)
	}
	if a.Engine.Stats.Misses != misses {
		t.Fatalf("grouping rebuilt geometry")
	}
	// Features built on a grouped body join its group.
	mustOK(t, a.ApplyTool("fillet", []byte(`{"target":"a","radius":1}`), PolicyAuto))
	if g := a.Graph.Get("fillet").Group; g != "Base" {
		t.Fatalf("fillet group = %q", g)
	}
	mustFail(t, a.ApplyTool("group", []byte(`{"features":["a","b"],"name":"Base"}`), PolicyAuto), "already")
	mustOK(t, a.ApplyTool("ungroup", []byte(`{"features":["b"]}`), PolicyAuto))
	if err := a.Save(dir); err != nil {
		t.Fatal(err)
	}
	a.Close()

	b, err := Open(dir, clock)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if b.Graph.Get("a").Group != "Base" || b.Graph.Get("b").Group != "" {
		t.Fatalf("groups not persisted: %q %q", b.Graph.Get("a").Group, b.Graph.Get("b").Group)
	}
	if _, match, _, err := b.Replay(); err != nil || !match {
		t.Fatalf("replay with groups: match=%v err=%v", match, err)
	}
	mustOK(t, b.ApplyTool("ungroup", []byte(`{"group":"Base"}`), PolicyAuto))
	if b.Graph.Get("a").Group != "" || b.Graph.Get("fillet").Group != "" {
		t.Fatal("dissolving a group must clear every member")
	}
	if _, err := b.Undo(); err != nil {
		t.Fatal(err)
	}
	if b.Graph.Get("a").Group != "Base" {
		t.Fatal("undo must restore the group")
	}
}

// Every feature that modifies a body becomes the visible body, so it must carry on the
// body's appearance: a material set on a box survives scale, fillet, hole, move, face edits
// and booleans, and the visible leaf always shows it.
func TestModifyingFeaturesKeepAppearance(t *testing.T) {
	a := New("demo", clock)
	defer a.Close()
	mustOK(t, a.ApplyTool("create_box", []byte(`{"id":"b","dx":40,"dy":30,"dz":10}`), PolicyAuto))
	mustOK(t, a.ApplyTool("create_box", []byte(`{"id":"tool","dx":5,"dy":5,"dz":20,"x":35}`), PolicyAuto))
	mustOK(t, a.ApplyTool("set_material", []byte(`{"features":["b"],"material_id":"brushed_aluminum"}`), PolicyAuto))
	mustOK(t, a.ApplyTool("set_opacity", []byte(`{"features":["b"],"opacity":0.5}`), PolicyAuto))
	mustOK(t, a.ApplyTool("group", []byte(`{"features":["b"],"name":"Frame"}`), PolicyAuto))
	steps := []struct{ tool, args, id string }{
		{"scale", `{"target":"b","factor":1.5,"id":"s1"}`, "s1"},
		{"fillet", `{"target":"s1","radius":1,"id":"f1"}`, "f1"},
		{"add_hole", `{"target":"f1","diameter":4,"id":"h1"}`, "h1"},
		{"transform", `{"target":"h1","dx":5,"id":"t1"}`, "t1"},
		{"face_extrude", `{"face":"face(feature=t1,side=-Z)","distance":2,"id":"e1"}`, "e1"},
		{"boolean", `{"op":"subtract","target":"e1","tool":"tool","id":"cut1"}`, "cut1"},
	}
	for _, s := range steps {
		mustOK(t, a.ApplyTool(s.tool, []byte(s.args), PolicyCommit))
		n := a.Graph.Get(s.id)
		if n.Appearance == nil || n.Appearance.MaterialID != "brushed_aluminum" || n.Appearance.Opacity == nil || *n.Appearance.Opacity != 0.5 || n.Group != "Frame" {
			t.Fatalf("%s lost the body's appearance/group: %+v group=%q", s.tool, n.Appearance, n.Group)
		}
	}
	if l := leaves(t, a); len(l) != 1 || l[0] != "cut1" {
		t.Fatalf("leaves=%v", l)
	}
	// The tool body of a boolean does not leak its (default) look onto the result.
	mustOK(t, a.ApplyTool("set_color", []byte(`{"features":["cut1"],"color":"#FF0000"}`), PolicyAuto))
	if a.Graph.Get("e1").Appearance.Color != "" {
		t.Fatal("restyling the visible body must not touch its inputs")
	}
}

// Scaling about an explicit point: giving ox/oy/oz is enough. The agent used to be refused
// with "ox/oy/oz need about=point" and had no way to read what to do instead, so the whole
// turn failed on a request the tool could perfectly well serve.
func TestScaleAboutAPointInfersTheAnchor(t *testing.T) {
	a := New("demo", clock)
	defer a.Close()
	mustOK(t, a.ApplyTool("create_box", []byte(`{"id":"b","dx":10,"dy":20,"dz":30}`), PolicyAuto))
	// Box spans x 0..10; doubling X about x=0 must keep the near face put and send max to 20.
	mustOK(t, a.ApplyTool("scale", []byte(`{"target":"b","sx":2,"ox":0,"oy":0,"oz":0,"id":"s"}`), PolicyAuto))
	_, min, max := leafProps(t, a, "s")
	if !approx(min[0], 0) || !approx(max[0], 20) {
		t.Fatalf("scaled about (0,0,0): min=%v max=%v", min, max)
	}
	// The inferred anchor is recorded on the feature, not left to the default.
	n := a.Snapshot().Get("s")
	if n == nil {
		t.Fatal("feature s is missing")
	}
	if got, _ := n.Parameters["about"].(string); got != "point" {
		t.Fatalf("about = %q, want the inferred point", got)
	}
}

// An explicit anchor computes its own fixed point, so coordinates passed alongside it have
// nothing to do. That reading is unambiguous, so the call succeeds instead of failing a turn
// over a redundant argument — but the explanation says what was disregarded, and the unused
// coordinates are not stored where a later parameter edit would appear to honour them.
func TestScaleIgnoresCoordinatesUnderAnExplicitAnchor(t *testing.T) {
	a := New("demo", clock)
	defer a.Close()
	mustOK(t, a.ApplyTool("create_box", []byte(`{"id":"b","dx":10,"dy":20,"dz":30}`), PolicyAuto))
	o := mustOK(t, a.ApplyTool("scale", []byte(`{"target":"b","sx":2,"about":"center","ox":5,"oy":10,"oz":15,"id":"s"}`), PolicyAuto))
	// Scaled about the bounding-box center (x 0..10 → -5..15), not about x=5.
	_, min, max := leafProps(t, a, "s")
	if !approx(min[0], -5) || !approx(max[0], 15) {
		t.Fatalf("scaled about the center: min=%v max=%v", min, max)
	}
	for _, want := range []string{"about its center", "(5, 10, 15) was not used"} {
		if !strings.Contains(o.ChangeSet.Explanation, want) {
			t.Fatalf("the explanation does not say %q: %s", want, o.ChangeSet.Explanation)
		}
	}
	n := a.Snapshot().Get("s")
	if n == nil {
		t.Fatal("feature s is missing")
	}
	for _, k := range []string{"ox", "oy", "oz"} {
		if _, ok := n.Parameters[k]; ok {
			t.Fatalf("the unused %s was stored on the feature: %v", k, n.Parameters)
		}
	}
	// about=point with coordinates stays valid, and so does an anchor with no coordinates.
	mustOK(t, a.ApplyTool("scale", []byte(`{"target":"s","sx":1.5,"about":"point","ox":5,"id":"p"}`), PolicyAuto))
	mustOK(t, a.ApplyTool("scale", []byte(`{"target":"p","sx":1.5,"about":"center","id":"c"}`), PolicyAuto))
}
