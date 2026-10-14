package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/accelone/simoncad/internal/cadir"
	"github.com/accelone/simoncad/internal/persistence"
)

// attachImage stores a 400x300 PNG the way an upload would and returns its id.
func attachImage(t *testing.T, a *App) string {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 400, 300))); err != nil {
		t.Fatal(err)
	}
	im, err := a.Images.Put(b.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	return im.ID
}

func call(t *testing.T, a *App, tool string, args string, policy Policy) Outcome {
	t.Helper()
	return a.ApplyTool(tool, []byte(args), policy)
}

// analyze records a proposal and returns its interpretation id.
func analyze(t *testing.T, a *App, proposal string) string {
	t.Helper()
	o := mustOK(t, call(t, a, "analyze_image", proposal, PolicyCommit))
	if o.ChangeSet != nil {
		t.Fatal("analyze_image must never create a ChangeSet")
	}
	return o.Data.(map[string]any)["interpretation_id"].(string)
}

func commitProposed(t *testing.T, a *App, o Outcome) {
	t.Helper()
	if o.Committed {
		t.Fatal("image-derived ChangeSets must stay proposed until the user commits")
	}
	if _, err := a.Commit(o.ChangeSet.ID); err != nil {
		t.Fatal(err)
	}
}

func jsonf(format string, a ...any) string { return fmt.Sprintf(format, a...) }

func platePlusHoles(img string) string {
	return jsonf(`{"image_id":%q,"image_size":[400,300],"confidence":0.86,
 "entities":[
  {"id":"e1","kind":"rectangle","x":50,"y":50,"w":320,"h":200,"confidence":0.95},
  {"id":"e2","kind":"circle","cx":130,"cy":150,"r":12,"confidence":0.9},
  {"id":"e3","kind":"circle","cx":290,"cy":150,"r":12,"confidence":0.9}],
 "profiles":[{"id":"p1","outer":["e1"],"holes":[["e2"],["e3"]]}],
 "relationships":[{"kind":"equal","entities":["e2","e3"],"confidence":0.9}],
 "dimensions":[
  {"id":"overall_width","kind":"length","binds":"e1.w","source":"missing","confidence":0},
  {"id":"thickness","kind":"depth","source":"missing","confidence":0}]}`, img)
}

func TestImagePlateWithHolesEndToEnd(t *testing.T) {
	a := New("demo", clock)
	defer a.Close()
	a.SetActor(Actor{CreatedBy: "agent", Capability: "vision", Model: "recorded"})
	img := attachImage(t, a)
	id := analyze(t, a, platePlusHoles(img))

	// nothing can be built until the user supplies the missing dimensions
	o := call(t, a, "create_from_image", `{"interpretation_id":"`+id+`"}`, PolicyCommit)
	if o.OK || !strings.Contains(o.Error, "overall_width") || !strings.Contains(o.Error, "thickness") {
		t.Fatalf("expected a refusal naming the missing dimensions, got %+v", o)
	}
	if len(a.Graph.Nodes) != 0 {
		t.Fatal("a refused call must not touch the model")
	}

	// the user answers: "80 mm wide, 5 mm thick"
	mustOK(t, call(t, a, "confirm_detected_dimension", `{"dimension":"overall_width","value":80}`, PolicyCommit))
	mustOK(t, call(t, a, "confirm_detected_dimension", `{"dimension":"thickness","value":5}`, PolicyCommit))

	o = mustOK(t, call(t, a, "create_from_image", `{"id":"plate"}`, PolicyCommit))
	if vol(t, a) != 0 {
		t.Fatal("the preview must not be applied")
	}
	if got := len(o.ChangeSet.Commands); got != 2 {
		t.Fatalf("expected sketch + extrude, got %d commands", got)
	}
	commitProposed(t, a, o)
	if want := (80*50 - 2*math.Pi*9) * 5; !near(vol(t, a), want) {
		t.Fatalf("volume %v want %v", vol(t, a), want)
	}

	// it is ordinary editable CAD-IR with provenance
	sk, ex := a.Graph.Get("plate_sketch"), a.Graph.Get("plate_extrude1")
	if sk == nil || ex == nil || sk.Type != "sketch" || ex.Type != "extrude" {
		t.Fatalf("nodes: %+v %+v", sk, ex)
	}
	for _, n := range []*cadir.Node{sk, ex} {
		pv := n.Provenance
		if pv.CreatedBy != "agent" || pv.Capability != "vision" || pv.SourceImageID != img || pv.InterpretationID != id ||
			pv.ToolCallID == "" || pv.ChangeSet == "" {
			t.Fatalf("provenance incomplete on %s: %+v", n.ID, pv)
		}
	}
	if it, _ := a.Vision.Get(id); len(it.Used) != 2 {
		t.Fatalf("interpretation should record the nodes it produced: %+v", it.Used)
	}
	// editing the dimension after the fact works through the normal parameter tools
	mustOK(t, call(t, a, "modify_parameter", `{"feature":"plate_sketch","parameter":"dim_overall_width","value":100}`, PolicyCommit))
	if want := (100*50 - 2*math.Pi*9) * 5; !near(vol(t, a), want) {
		t.Fatalf("after editing the width: %v want %v", vol(t, a), want)
	}
	mustOK(t, call(t, a, "modify_parameter", `{"feature":"plate_extrude1","parameter":"depth","value":8}`, PolicyCommit))
	if want := (100*50 - 2*math.Pi*9) * 8; !near(vol(t, a), want) {
		t.Fatalf("after editing the depth: %v want %v", vol(t, a), want)
	}
}

func TestImageMissingOnlyThicknessThenExplicitDepth(t *testing.T) {
	a := New("demo", clock)
	defer a.Close()
	img := attachImage(t, a)
	prop := strings.Replace(platePlusHoles(img),
		`{"id":"overall_width","kind":"length","binds":"e1.w","source":"missing","confidence":0}`,
		`{"id":"overall_width","kind":"length","binds":"e1.w","value":80,"source":"handwritten","confidence":0.93}`, 1)
	id := analyze(t, a, prop)
	rep := a.Vision
	it, _ := rep.Get(id)
	r := it.Report()
	if r.Ready || len(r.Missing) != 1 || r.Missing[0].Dimension != "thickness" || len(r.Unconfirmed) != 0 {
		t.Fatalf("only the thickness may be missing: %+v", r)
	}
	if o := call(t, a, "create_from_image", `{}`, PolicyCommit); o.OK || !strings.Contains(o.Error, "thickness") {
		t.Fatalf("expected a thickness question: %+v", o)
	}
	o := mustOK(t, call(t, a, "create_from_image", `{"extrusion_depth":6}`, PolicyCommit))
	commitProposed(t, a, o)
	if want := (80*50 - 2*math.Pi*9) * 6; !near(vol(t, a), want) {
		t.Fatalf("volume %v want %v", vol(t, a), want)
	}
	// the store was not changed by the plan (the depth was a one-off argument)
	it, _ = rep.Get(id)
	if it.Report().Ready {
		t.Fatal("create_from_image must not rewrite the proposal")
	}
}

func TestImageLowConfidenceDimensionCorrectedByUser(t *testing.T) {
	a := New("demo", clock)
	defer a.Close()
	img := attachImage(t, a)
	prop := strings.Replace(platePlusHoles(img),
		`{"id":"overall_width","kind":"length","binds":"e1.w","source":"missing","confidence":0}`,
		`{"id":"overall_width","kind":"length","binds":"e1.w","value":30,"source":"handwritten","confidence":0.4}`, 1)
	id := analyze(t, a, prop)
	o := call(t, a, "create_from_image", `{"extrusion_depth":5}`, PolicyCommit)
	if o.OK || !strings.Contains(o.Error, "unconfirmed") || !strings.Contains(o.Error, "overall_width") {
		t.Fatalf("a 0.4-confidence reading must need confirmation: %+v", o)
	}
	// "No: it's 40 mm" — an incorrectly detected value corrected by the user
	mustOK(t, call(t, a, "confirm_detected_dimension", `{"interpretation_id":"`+id+`","dimension":"overall_width","value":40}`, PolicyCommit))
	o = mustOK(t, call(t, a, "create_from_image", `{"extrusion_depth":5}`, PolicyCommit))
	commitProposed(t, a, o)
	if want := (40*25 - 2*math.Pi*(1.5*1.5)) * 5; !near(vol(t, a), want) {
		t.Fatalf("volume %v want %v", vol(t, a), want)
	}
}

func TestImageIrregularClosedProfile(t *testing.T) {
	a := New("demo", clock)
	defer a.Close()
	img := attachImage(t, a)
	id := analyze(t, a, jsonf(`{"image_id":%q,"image_size":[400,300],"confidence":0.7,
	 "entities":[
	  {"id":"l1","kind":"line","p1":[50,250],"p2":[250,251],"confidence":0.8},
	  {"id":"a1","kind":"arc","p1":[252,249],"mid":[200,100],"p2":[53,52],"confidence":0.7},
	  {"id":"l2","kind":"line","p1":[51,50],"p2":[49,248],"confidence":0.8}],
	 "profiles":[{"id":"p1","outer":["l1","a1","l2"]}],
	 "dimensions":[{"id":"w","kind":"length","binds":"l1.length","value":50,"source":"handwritten","confidence":0.9}]}`, img))
	_ = id
	o := mustOK(t, call(t, a, "create_from_image", `{"extrusion_depth":3,"id":"cam"}`, PolicyCommit))
	commitProposed(t, a, o)
	m, err := a.Model()
	if err != nil || len(m.Failed()) != 0 {
		t.Fatalf("failed: %v %v", m.Failed(), err)
	}
	if v := vol(t, a); v < 2000 || v > 10000 {
		t.Fatalf("implausible volume %v", v)
	}
	// a hand-drawn loop stays editable geometry: lines and an arc, welded at the joints
	sk := a.Graph.Get("cam_sketch")
	ents := sk.Parameters["entities"].([]any)
	if len(ents) != 3 {
		t.Fatalf("entities: %v", ents)
	}
}

func TestImageNoScaleNeverGuessed(t *testing.T) {
	a := New("demo", clock)
	defer a.Close()
	img := attachImage(t, a)
	prop := strings.Replace(platePlusHoles(img), `"dimensions":[
  {"id":"overall_width","kind":"length","binds":"e1.w","source":"missing","confidence":0},
  {"id":"thickness","kind":"depth","source":"missing","confidence":0}]`, `"dimensions":[]`, 1)
	analyze(t, a, prop)
	o := call(t, a, "create_from_image", `{"extrusion_depth":5}`, PolicyCommit)
	if o.OK || !strings.Contains(o.Error, "missing") {
		t.Fatalf("without any real dimension nothing may be built: %+v", o)
	}
	// the user gives an explicit scale instead of a dimension
	mustOK(t, call(t, a, "set_sketch_scale", `{"pixels":320,"mm":80}`, PolicyCommit))
	o = mustOK(t, call(t, a, "create_from_image", `{"extrusion_depth":5}`, PolicyCommit))
	commitProposed(t, a, o)
	if want := (80*50 - 2*math.Pi*9) * 5; !near(vol(t, a), want) {
		t.Fatalf("volume %v want %v", vol(t, a), want)
	}
}

func TestImageUnknownImageAndBadProposals(t *testing.T) {
	a := New("demo", clock)
	defer a.Close()
	h := a.Graph.Hash()
	if o := call(t, a, "analyze_image", platePlusHoles("img_deadbeefdeadbeef"), PolicyCommit); o.OK {
		t.Fatal("an unattached image must be refused")
	}
	img := attachImage(t, a)
	bad := strings.Replace(platePlusHoles(img), `"image_size":[400,300]`, `"image_size":[100,100]`, 1)
	if o := call(t, a, "analyze_image", bad, PolicyCommit); o.OK {
		t.Fatal("a wrong image size must be refused")
	}
	if o := call(t, a, "create_from_image", `{}`, PolicyCommit); o.OK {
		t.Fatal("no interpretation yet")
	}
	if a.Graph.Hash() != h || len(a.Mgr.Log) != 0 {
		t.Fatal("failures must never touch the model")
	}
}

func TestImageProposalCorrections(t *testing.T) {
	a := New("demo", clock)
	defer a.Close()
	img := attachImage(t, a)
	// the model first saw the two circles as separate solids (each its own profile)
	id := analyze(t, a, jsonf(`{"image_id":%q,"image_size":[400,300],"confidence":0.8,
 "entities":[
  {"id":"e1","kind":"rectangle","x":50,"y":50,"w":320,"h":200,"confidence":0.95},
  {"id":"e2","kind":"circle","cx":130,"cy":150,"r":12,"confidence":0.9}],
 "profiles":[{"id":"p1","outer":["e1"]},{"id":"p2","outer":["e2"]}],
 "dimensions":[{"id":"w","kind":"length","binds":"e1.w","value":80,"source":"printed","confidence":0.95},
               {"id":"t","kind":"depth","value":5,"source":"printed","confidence":0.9}]}`, img))
	if it, _ := a.Vision.Get(id); len(it.Report().Ambiguous) != 1 {
		t.Fatal("a circle nested in a profile without a role is ambiguous")
	}
	o := call(t, a, "create_from_image", `{}`, PolicyCommit)
	if o.OK || !strings.Contains(o.Error, "ambiguous") || !strings.Contains(o.Error, "Join") {
		t.Fatalf("ambiguity must be asked, not guessed: %+v", o)
	}
	// "That circle should be a hole."
	mustOK(t, call(t, a, "update_sketch_proposal", `{"edits":[
	  {"op":"set_profile","id":"p1","value":{"holes":[["e2"]]}},
	  {"op":"remove_profile","id":"p2"}]}`, PolicyCommit))
	o = mustOK(t, call(t, a, "create_from_image", `{}`, PolicyCommit))
	commitProposed(t, a, o)
	if want := (80*50 - math.Pi*9) * 5; !near(vol(t, a), want) {
		t.Fatalf("volume %v want %v", vol(t, a), want)
	}
	// a failing correction batch changes nothing
	before, _ := a.Vision.Get(id)
	if o := call(t, a, "update_sketch_proposal", `{"edits":[{"op":"set_entity","id":"e2","value":{"r":99}},{"op":"remove_entity","id":"nope"}]}`, PolicyCommit); o.OK {
		t.Fatal("expected failure")
	}
	after, _ := a.Vision.Get(id)
	if after.Revision != before.Revision {
		t.Fatal("failed edits created a revision")
	}
}

func TestImageMultiContourSketch(t *testing.T) {
	a := New("demo", clock)
	defer a.Close()
	img := attachImage(t, a)
	analyze(t, a, jsonf(`{"image_id":%q,"image_size":[400,300],"confidence":0.8,
 "entities":[
  {"id":"e1","kind":"rectangle","x":20,"y":20,"w":100,"h":100,"confidence":0.9},
  {"id":"e2","kind":"circle","cx":250,"cy":70,"r":40,"confidence":0.9},
  {"id":"e3","kind":"slot","x1":60,"y1":200,"x2":340,"y2":200,"width":40,"confidence":0.9}],
 "profiles":[{"id":"p1","outer":["e1"]},{"id":"p2","outer":["e2"]},{"id":"p3","outer":["e3"]}],
 "dimensions":[{"id":"w","kind":"length","binds":"e1.w","value":50,"source":"printed","confidence":0.95}]}`, img))
	o := mustOK(t, call(t, a, "create_from_image", `{"extrusion_depth":2,"id":"multi"}`, PolicyCommit))
	commitProposed(t, a, o)
	s := 0.5 // 50 mm / 100 px
	want := (100*s*100*s + math.Pi*(40*s)*(40*s) + (280*s*40*s + math.Pi*(20*s)*(20*s))) * 2
	if !near(vol(t, a), want) {
		t.Fatalf("volume %v want %v", vol(t, a), want)
	}
	m, _ := a.Model()
	if len(m.Leaves) != 1 {
		t.Fatalf("all contours extrude together as one body, got %v", m.Leaves)
	}
}

func TestImageOnSelectedPlanarFace(t *testing.T) {
	a := New("demo", clock)
	defer a.Close()
	mustOK(t, call(t, a, "create_box", `{"id":"plate","dx":100,"dy":60,"dz":10}`, PolicyCommit))
	img := attachImage(t, a)
	analyze(t, a, jsonf(`{"image_id":%q,"image_size":[400,300],"confidence":0.8,
 "entities":[{"id":"e1","kind":"circle","cx":200,"cy":150,"r":50,"confidence":0.9}],
 "profiles":[{"id":"p1","outer":["e1"]}],
 "dimensions":[{"id":"d","kind":"diameter","binds":"e1.diameter","value":20,"source":"printed","confidence":0.95}],
 "operations":[{"id":"o1","kind":"cut","profile":"p1","target":"selection","through":true}]}`, img))

	// without a selection "selection" has nothing to refer to
	if o := call(t, a, "create_from_image", `{"face":"selection"}`, PolicyCommit); o.OK {
		t.Fatal("expected an error without a selection")
	}
	if err := a.SetSelection([]string{"face(feature=plate,side=+Z)"}); err != nil {
		t.Fatal(err)
	}
	o := mustOK(t, call(t, a, "create_from_image", `{"face":"selection","id":"hole"}`, PolicyCommit))
	commitProposed(t, a, o)
	if want := 100.0*60*10 - math.Pi*100*10; !near(vol(t, a), want) {
		t.Fatalf("volume %v want %v", vol(t, a), want)
	}
	sk := a.Graph.Get("hole_sketch")
	if sk == nil || sk.References["face"] != "face(feature=plate,side=+Z)" {
		t.Fatalf("the sketch must use the selected face as its placement plane: %v", sk.References)
	}
	if _, has := sk.Parameters["plane"]; has {
		t.Fatal("a face-placed sketch carries no world plane")
	}
	// the drawing is centred on the face: the hole sits in the middle of the plate
	m, _ := a.Model()
	topo, _ := m.Topology("hole_o1")
	cx, cy := 0.0, 0.0
	var n float64
	for _, f := range topo.Faces {
		if f.Surface == "cylinder" {
			cx += f.Centroid[0]
			cy += f.Centroid[1]
			n++
		}
	}
	if n == 0 || math.Abs(cx/n-50) > 1e-3 || math.Abs(cy/n-30) > 1e-3 {
		t.Fatalf("hole centre (%v,%v) want (50,30)", cx/n, cy/n)
	}
}

// Three profiles: a base plate, an overlapping side tab and a mounting hole.
func tabbedPlate(img string, ops string) string {
	return jsonf(`{"image_id":%q,"image_size":[400,300],"confidence":0.85,
 "entities":[
  {"id":"base","kind":"rectangle","x":50,"y":50,"w":200,"h":100,"confidence":0.95},
  {"id":"tab","kind":"rectangle","x":150,"y":80,"w":200,"h":100,"confidence":0.9},
  {"id":"hole","kind":"circle","cx":100,"cy":100,"r":10,"confidence":0.9}],
 "profiles":[{"id":"pb","outer":["base"]},{"id":"pt","outer":["tab"]},{"id":"ph","outer":["hole"]}],
 "dimensions":[{"id":"w","kind":"length","binds":"base.w","value":100,"source":"printed","confidence":0.95},
               {"id":"t","kind":"depth","value":4,"source":"user","confidence":1}],
 "operations":%s}`, img, ops)
}

func TestImageBooleanOperations(t *testing.T) {
	// scale 0.5 mm/px: base 100x50, tab 100x50, overlap 50x35 = 1750 mm²
	cases := []struct {
		kind string
		want float64
	}{
		{"union", (5000 + 5000 - 1750) * 4},
		{"intersect", 1750 * 4},
		{"subtract", (5000 - 1750) * 4},
	}
	for _, c := range cases {
		t.Run(c.kind, func(t *testing.T) {
			a := New("demo", clock)
			defer a.Close()
			img := attachImage(t, a)
			analyze(t, a, tabbedPlate(img, `[
			 {"id":"o1","kind":"extrude","profile":"pb","depth_dim":"t"},
			 {"id":"o2","kind":"extrude","profile":"pt","depth_dim":"t"},
			 {"id":"o3","kind":"`+c.kind+`","a":"o1","b":"o2"}]`))
			o := mustOK(t, call(t, a, "create_from_image", `{"id":"bool"}`, PolicyCommit))
			if len(o.ChangeSet.Commands) != 4 {
				t.Fatalf("sketch + 2 extrudes + boolean, got %d", len(o.ChangeSet.Commands))
			}
			commitProposed(t, a, o)
			if !near(vol(t, a), c.want) {
				t.Fatalf("%s volume %v want %v", c.kind, vol(t, a), c.want)
			}
			if n := a.Graph.Get("bool_o3"); n == nil || n.Type != c.kind || n.Parameters["target"] != "bool_o1" || n.Parameters["tool"] != "bool_o2" {
				t.Fatalf("boolean must be an explicit, named DAG node: %+v", n)
			}
			m, _ := a.Model()
			if len(m.Leaves) != 1 {
				t.Fatalf("one final solid expected, got %v", m.Leaves)
			}
		})
	}
}

func TestImageSequentialBooleansUndoRedoSaveLoad(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "p.scadproj")
	a := New("demo", clock)
	a.SetActor(Actor{CreatedBy: "agent", Capability: "vision", Model: "recorded"})
	img := attachImage(t, a)
	analyze(t, a, tabbedPlate(img, `[
	 {"id":"plate","kind":"extrude","profile":"pb","depth_dim":"t"},
	 {"id":"tab","kind":"extrude","profile":"pt","depth_dim":"t"},
	 {"id":"joined","kind":"union","a":"plate","b":"tab"},
	 {"id":"holes","kind":"extrude","profile":"ph","depth_dim":"t"},
	 {"id":"final","kind":"subtract","a":"joined","b":"holes"}]`))
	before := a.Graph.Hash()
	o := mustOK(t, call(t, a, "create_from_image", `{"id":"p"}`, PolicyCommit))
	commitProposed(t, a, o)
	after := a.Graph.Hash()
	want := (5000 + 5000 - 1750 - math.Pi*25) * 4
	if !near(vol(t, a), want) {
		t.Fatalf("volume %v want %v", vol(t, a), want)
	}
	// the history stays editable, node by node
	for _, id := range []string{"p_sketch", "p_plate", "p_tab", "p_joined", "p_holes", "p_final"} {
		if a.Graph.Get(id) == nil {
			t.Fatalf("missing feature %s", id)
		}
	}
	mustOK(t, call(t, a, "modify_parameter", `{"feature":"p_holes","parameter":"depth","value":2}`, PolicyCommit))
	if !near(vol(t, a), (5000+5000-1750)*4-math.Pi*25*2) {
		t.Fatalf("editing one feature in the chain: %v", vol(t, a))
	}
	if _, err := a.Undo(); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Undo(); err != nil {
		t.Fatal(err)
	}
	if a.Graph.Hash() != before || vol(t, a) != 0 {
		t.Fatal("undo must remove the whole boolean chain")
	}
	if _, err := a.Redo(); err != nil {
		t.Fatal(err)
	}
	if a.Graph.Hash() != after || !near(vol(t, a), want) {
		t.Fatal("redo must restore the whole chain")
	}
	if err := a.Save(dir); err != nil {
		t.Fatal(err)
	}
	a.Close()

	b, err := Open(dir, clock)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if !near(vol(t, b), want) {
		t.Fatalf("reloaded volume %v want %v", vol(t, b), want)
	}
	if b.Graph.Hash() != after {
		t.Fatal("reloaded graph differs")
	}
	if _, match, _, err := b.Replay(); err != nil || !match {
		t.Fatalf("replay match=%v err=%v", match, err)
	}
	if n := b.Graph.Get("p_final"); n == nil || n.Provenance.Capability != "vision" || n.Provenance.SourceImageID != img {
		t.Fatalf("provenance after reload: %+v", n)
	}
}

func TestImageCutThroughExistingBody(t *testing.T) {
	a := New("demo", clock)
	defer a.Close()
	mustOK(t, call(t, a, "create_box", `{"id":"block","dx":100,"dy":60,"dz":20}`, PolicyCommit))
	img := attachImage(t, a)
	analyze(t, a, jsonf(`{"image_id":%q,"image_size":[400,300],"confidence":0.8,
 "entities":[{"id":"s","kind":"slot","x1":120,"y1":150,"x2":280,"y2":150,"width":60,"confidence":0.9}],
 "profiles":[{"id":"p1","outer":["s"]}],
 "dimensions":[{"id":"sw","kind":"length","binds":"s.width","value":12,"source":"printed","confidence":0.95}],
 "operations":[{"id":"o1","kind":"cut","profile":"p1","target":"feature:block","through":true}]}`, img))
	o := mustOK(t, call(t, a, "create_from_image", `{"id":"slotcut","plane":"XY","offset":10}`, PolicyCommit))
	commitProposed(t, a, o)
	// slot: width 12, length between centres 160 px * 0.2 = 32 -> area 12*32 + pi*36
	want := 100.0*60*20 - (12*32+math.Pi*36)*20
	if !near(vol(t, a), want) {
		t.Fatalf("volume %v want %v", vol(t, a), want)
	}
	n := a.Graph.Get("slotcut_o1")
	if n.Parameters["mode"] != "cut" || n.Parameters["through"] != true || n.Parameters["target"] != "block" {
		t.Fatalf("cut feature: %+v", n.Parameters)
	}
	// undo/redo around it
	h := a.Graph.Hash()
	if _, err := a.Undo(); err != nil {
		t.Fatal(err)
	}
	if !near(vol(t, a), 100*60*20) {
		t.Fatal("undo must restore the untouched block")
	}
	if _, err := a.Redo(); err != nil || a.Graph.Hash() != h {
		t.Fatal("redo")
	}
	// the selected body works as the target too
	if err := a.SetSelection([]string{"body(feature=slotcut_o1)"}); err != nil {
		t.Fatal(err)
	}
}

func TestImageFailedBooleanNeverCorruptsModel(t *testing.T) {
	a := New("demo", clock)
	defer a.Close()
	img := attachImage(t, a)
	// two disjoint rectangles: their intersection is empty
	analyze(t, a, jsonf(`{"image_id":%q,"image_size":[400,300],"confidence":0.85,
 "entities":[
  {"id":"a","kind":"rectangle","x":10,"y":10,"w":100,"h":100,"confidence":0.95},
  {"id":"b","kind":"rectangle","x":250,"y":150,"w":100,"h":100,"confidence":0.9}],
 "profiles":[{"id":"pa","outer":["a"]},{"id":"pb","outer":["b"]}],
 "dimensions":[{"id":"w","kind":"length","binds":"a.w","value":50,"source":"printed","confidence":0.95},
               {"id":"t","kind":"depth","value":3,"source":"user","confidence":1}],
 "operations":[
  {"id":"o1","kind":"extrude","profile":"pa","depth_dim":"t"},
  {"id":"o2","kind":"extrude","profile":"pb","depth_dim":"t"},
  {"id":"o3","kind":"intersect","a":"o1","b":"o2"}]}`, img))
	mustOK(t, call(t, a, "create_box", `{"id":"keep","dx":5,"dy":5,"dz":5}`, PolicyCommit))
	h := a.Graph.Hash()
	o := call(t, a, "create_from_image", `{}`, PolicyCommit)
	if o.OK {
		t.Fatalf("an empty boolean result must be rejected: %+v", o)
	}
	if a.Graph.Hash() != h || !near(vol(t, a), 125) {
		t.Fatal("the model must be untouched after a failed interpretation")
	}
}

func TestImageSketchOnlyThenExtrudeDetectedProfile(t *testing.T) {
	a := New("demo", clock)
	defer a.Close()
	img := attachImage(t, a)
	analyze(t, a, platePlusHoles(img))
	mustOK(t, call(t, a, "confirm_detected_dimension", `{"dimension":"overall_width","value":80}`, PolicyCommit))
	// no sketch yet
	if o := call(t, a, "extrude_detected_profile", `{"extrusion_depth":5}`, PolicyCommit); o.OK {
		t.Fatal("extrude_detected_profile needs a sketch first")
	}
	o := mustOK(t, call(t, a, "create_sketch_from_image", `{"id":"s","extrusion_depth":5}`, PolicyCommit))
	if len(o.ChangeSet.Commands) != 1 {
		t.Fatalf("sketch only: %d commands", len(o.ChangeSet.Commands))
	}
	commitProposed(t, a, o)
	if vol(t, a) != 0 {
		t.Fatal("a sketch has no volume")
	}
	o = mustOK(t, call(t, a, "extrude_detected_profile", `{"id":"s","extrusion_depth":5}`, PolicyCommit))
	if len(o.ChangeSet.Commands) != 1 {
		t.Fatalf("extrude only: %d commands", len(o.ChangeSet.Commands))
	}
	commitProposed(t, a, o)
	if want := (80*50 - 2*math.Pi*9) * 5; !near(vol(t, a), want) {
		t.Fatalf("volume %v want %v", vol(t, a), want)
	}
}

func TestImageStoresRoundTripJSON(t *testing.T) {
	a := New("demo", clock)
	defer a.Close()
	id := analyze(t, a, platePlusHoles(attachImage(t, a)))
	b, err := json.Marshal(a.Vision)
	if err != nil || !strings.Contains(string(b), id) {
		t.Fatalf("%v %s", err, b)
	}
}

func TestRetainedImagesAndInterpretationsPersist(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "p.scadproj")
	a := New("demo", clock)
	img := attachImage(t, a)
	other := func() string { // a second, never retained image
		var b bytes.Buffer
		_ = png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 20, 20)))
		im, err := a.Images.Put(b.Bytes())
		if err != nil {
			t.Fatal(err)
		}
		return im.ID
	}()
	id := analyze(t, a, platePlusHoles(img))
	mustOK(t, call(t, a, "confirm_detected_dimension", `{"dimension":"overall_width","value":80}`, PolicyCommit))
	o := mustOK(t, call(t, a, "create_from_image", `{"extrusion_depth":5,"id":"plate"}`, PolicyCommit))
	commitProposed(t, a, o)
	if err := a.Save(dir); err != nil {
		t.Fatal(err)
	}
	if entries, err := os.ReadDir(filepath.Join(dir, "assets")); err == nil && len(entries) > 0 {
		t.Fatalf("nothing is stored unless explicitly retained: %v", entries)
	}
	if err := a.Images.Retain(img, true); err != nil {
		t.Fatal(err)
	}
	if err := a.Save(dir); err != nil {
		t.Fatal(err)
	}
	files, _ := filepath.Glob(filepath.Join(dir, "assets", "images", "*"))
	if len(files) != 1 || !strings.Contains(files[0], img) || strings.Contains(files[0], other) {
		t.Fatalf("only the retained image belongs in assets/images: %v", files)
	}
	var meta struct {
		V int `json:"schema_version"`
	}
	b, _ := os.ReadFile(filepath.Join(dir, "project.json"))
	_ = json.Unmarshal(b, &meta)
	if meta.V != persistence.SchemaVersion {
		t.Fatalf("schema version %d", meta.V)
	}
	a.Close()

	b2, err := Open(dir, clock)
	if err != nil {
		t.Fatal(err)
	}
	defer b2.Close()
	if im, ok := b2.Images.Get(img); !ok || !im.Retained || im.Width != 400 || im.Height != 300 {
		t.Fatalf("retained image must be restored: %+v", im)
	}
	if it, ok := b2.Vision.Get(id); !ok || it.Proposal.ImageID != img {
		t.Fatalf("interpretation must be restored: %+v", it)
	}
	if b2.Graph.Get("plate_extrude1") == nil {
		t.Fatal("geometry must be restored")
	}
	// un-retaining drops the file on the next save
	_ = b2.Images.Retain(img, false)
	if err := b2.Save(dir); err != nil {
		t.Fatal(err)
	}
	if files, _ := filepath.Glob(filepath.Join(dir, "assets", "images", "*")); len(files) != 0 {
		t.Fatalf("stale image kept: %v", files)
	}
}

// A project made before schema v2 (no assets, no interpretations) still opens and rebuilds.
func TestV1ProjectOpensUnderV2(t *testing.T) {
	b, err := Open("../../testdata/robot/servo_bracket.scadproj", clock)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if vol(t, b) <= 0 {
		t.Fatal("v1 project must rebuild")
	}
	if len(b.Vision.List()) != 0 || len(b.Images.All()) != 0 {
		t.Fatal("nothing image-related in a v1 project")
	}
}

func TestUnusedInterpretationClosesWhenTheModelIsBuiltAnotherWay(t *testing.T) {
	a := New("demo", clock)
	defer a.Close()
	id := analyze(t, a, platePlusHoles(attachImage(t, a)))
	if !a.Vision.HasOpen() {
		t.Fatal("a fresh interpretation must be open")
	}

	// presentation-only edits do not count as building the model
	mustOK(t, call(t, a, "create_box", `{"id":"plate","dx":80,"dy":60,"dz":5}`, PolicyCommit))
	if a.Vision.HasOpen() {
		t.Fatal("a box built by hand must close the pending interpretation")
	}
	it, _ := a.Vision.Get(id)
	if !it.Closed || it.Open() {
		t.Fatalf("interpretation should be closed, got %+v", it)
	}
	// a closed interpretation survives save/load
	b, _ := json.Marshal(a.Vision)
	if !strings.Contains(string(b), `"closed":true`) {
		t.Fatalf("closed flag not persisted: %s", b)
	}
}

func TestAppearanceChangeKeepsInterpretationOpen(t *testing.T) {
	a := New("demo", clock)
	defer a.Close()
	mustOK(t, call(t, a, "create_box", `{"id":"plate","dx":80,"dy":60,"dz":5}`, PolicyCommit))
	analyze(t, a, platePlusHoles(attachImage(t, a)))
	mustOK(t, call(t, a, "set_color", `{"features":["plate"],"color":"#2255ff"}`, PolicyCommit))
	if !a.Vision.HasOpen() {
		t.Fatal("a color change must not close the interpretation")
	}
}
