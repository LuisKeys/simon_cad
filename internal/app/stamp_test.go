package app

import (
	"encoding/json"
	"fmt"
	"math"
	"path/filepath"
	"strings"
	"testing"
)

// an asymmetric glyph ("F"): its mass sits to the left, so a mirror is easy to detect
func glyphPaper(t *testing.T) string {
	p := newPaper(300, 400)
	p.ink(func(x, y float64) bool {
		return (x >= 60 && x <= 110 && y >= 50 && y <= 350) || // stem
			(x >= 60 && x <= 240 && y >= 50 && y <= 100) || // top bar
			(x >= 60 && x <= 200 && y >= 190 && y <= 235) // middle bar
	})
	return p.save(t, "glyph.png")
}

// area of the glyph in px²: stem 50x300, top bar 130x50 beyond the stem, middle bar 90x45 beyond the stem
const glyphAreaPx = 50*300 + 130*50 + 90*45

func stampBounds(t *testing.T, a *App, id string) (lo, hi [3]float64) {
	t.Helper()
	m, err := a.Model()
	if err != nil {
		t.Fatal(err)
	}
	b, ok := m.Bounds(id)
	if !ok {
		t.Fatalf("no body %s", id)
	}
	return b.Min, b.Max
}

func TestCreateStampFromImage(t *testing.T) {
	a := New("demo", clock)
	defer a.Close()
	img := glyphPaper(t)
	o := sw(t, a, "create_stamp", fmt.Sprintf(`{"id":"stamp","path":%q,"width":60,"height":40,"base_thickness":4,"relief_height":2,"margin":4}`, img))
	if !o.Committed {
		t.Fatalf("a clean stamp commits at once: %v", o.ChangeSet.Warnings)
	}
	if !strings.Contains(o.Message, "mirrored") {
		t.Fatalf("message %q", o.Message)
	}
	// every generated node belongs to one group and the final body is the stamp itself
	for _, id := range []string{"stamp_base_sketch", "stamp_base", "stamp_design", "stamp", "stamp_recipe"} {
		n := a.Graph.Get(id)
		if n == nil || n.Group != "stamp" {
			t.Fatalf("node %s: %+v", id, n)
		}
	}
	lo, hi := stampBounds(t, a, "stamp")
	if math.Abs(hi[2]-6) > 1e-5 || math.Abs(lo[2]) > 1e-5 {
		t.Fatalf("z extent %v..%v want 0..6", lo[2], hi[2])
	}
	if math.Abs(lo[0]+30) > 1e-5 || math.Abs(hi[0]-30) > 1e-5 || math.Abs(lo[1]+20) > 1e-5 || math.Abs(hi[1]-20) > 1e-5 {
		t.Fatalf("plate extent %v %v", lo, hi)
	}
	// the design fits the free area (plate minus margin 4): glyph aspect 3:4 → 24 mm wide... limited by height 32 → width 24
	// area of the relief = glyph area scaled to 32 mm tall (400 px → 32 mm)
	scale := 32.0 / 400
	want := 60*40*4 + glyphAreaPx*scale*scale*2
	within(t, "stamp volume", vol(t, a), want, 0.015)
	assertWatertight(t, a, "stamp")

	// the relief is mirrored: the glyph's mass was at -x of the image centre, so its mirror sits at +x
	x, ok := faceCentroidX(t, a, "stamp", 6)
	if !ok || x <= 0 {
		t.Fatalf("mirrored relief should lean to +x, got %v %v", x, ok)
	}
	// the plate is untouched by the mirror: the recipe stays editable and reports itself
	r := resultMap(t, sw(t, a, "get_recipe", `{"recipe":"stamp"}`))
	if r["recipe_id"] != "stamp_recipe" {
		t.Fatalf("%v", r)
	}
}

func TestStampOptions(t *testing.T) {
	img := glyphPaper(t)
	base := func(extra string) *App {
		a := New("demo", clock)
		sw(t, a, "create_stamp", fmt.Sprintf(`{"id":"stamp","path":%q,"width":60,"height":40,"base_thickness":4,"relief_height":2,"margin":4%s}`, img, extra))
		return a
	}
	plain := base("")
	defer plain.Close()
	v0 := vol(t, plain)

	// handle on the back: a cylinder below the plate
	h := base(`,"handle_enabled":true,"handle_height":12,"handle_diameter":16`)
	defer h.Close()
	lo, _ := stampBounds(t, h, "stamp")
	if math.Abs(lo[2]+12) > 1e-6 {
		t.Fatalf("handle should reach z=-12, got %v", lo[2])
	}
	within(t, "handle volume", vol(t, h)-v0, math.Pi*64*12, 0.01)
	assertWatertight(t, h, "stamp")

	// rounded edges remove material and stay printable
	r := base(`,"rounded_edges":true,"edge_radius":1`)
	defer r.Close()
	if !(vol(t, r) < v0) {
		t.Fatal("rounding the edges removes material")
	}
	assertWatertight(t, r, "stamp")

	// shrinkage compensation: scaled by 1/(1-s); no value is ever assumed
	s := base(`,"shrinkage_percent":12`)
	defer s.Close()
	slo, shi := stampBounds(t, s, "stamp")
	within(t, "scaled width", shi[0]-slo[0], 60/0.88, 1e-5)
	within(t, "scaled volume", vol(t, s), v0/math.Pow(0.88, 3), 1e-4)
	plo, phi := stampBounds(t, plain, "stamp")
	if math.Abs(phi[0]-plo[0]-60) > 1e-5 {
		t.Fatalf("without shrinkage the plate is exactly %v", phi[0]-plo[0])
	}

	// a circular plate
	c := New("demo", clock)
	defer c.Close()
	sw(t, c, "create_stamp", fmt.Sprintf(`{"id":"round","shape":"circle","diameter":50,"path":%q,"relief_height":1.5}`, img))
	clo, chi := stampBounds(t, c, "round")
	within(t, "diameter", chi[0]-clo[0], 50, 1e-5)
	// default thickness 4 + relief 1.5 = 5.5, and the assumptions were reported
	within(t, "height", chi[2], 5.5, 1e-5)
	// the glyph stays inside the inscribed square (margin 3): half-diagonal <= 22
	if chi[2] > 5.5+1e-6 {
		t.Fatal("unexpected growth")
	}
}

func TestStampErrorsAreCodedAndNothingIsInvented(t *testing.T) {
	a := New("demo", clock)
	defer a.Close()
	img := glyphPaper(t)
	h := a.Graph.Hash()
	for _, c := range []struct{ args, code string }{
		{fmt.Sprintf(`{"path":%q}`, img), "RECIPE_INVALID"},                                               // no size
		{fmt.Sprintf(`{"path":%q,"width":60,"height":40,"shrinkage_percent":80}`, img), "RECIPE_INVALID"}, // absurd shrinkage
		{fmt.Sprintf(`{"path":%q,"width":60,"height":40,"margin":30}`, img), "RECIPE_INVALID"},
		{`{"width":60,"height":40}`, "RECIPE_INVALID"},                                            // no design
		{fmt.Sprintf(`{"path":%q,"image_id":"x","width":60,"height":40}`, img), "RECIPE_INVALID"}, // two sources
		{fmt.Sprintf(`{"path":%q,"width":60,"height":40,"handle_enabled":true,"handle_diameter":70}`, img), "RECIPE_INVALID"},
	} {
		if got := failCode(t, a, "create_stamp", c.args); got != c.code {
			t.Errorf("%s: code %s want %s", c.args, got, c.code)
		}
	}
	blank := newPaper(100, 100).save(t, "blank.png")
	if got := failCode(t, a, "create_stamp", fmt.Sprintf(`{"path":%q,"width":60,"height":40}`, blank)); got != "VECTORIZE_EMPTY" {
		t.Errorf("blank image: %s", got)
	}
	if a.Graph.Hash() != h {
		t.Fatal("failed stamps must leave no nodes behind")
	}
	// an oversized design is allowed but warned about, so it waits for approval
	o := mustOK(t, a.ApplyTool("create_stamp", []byte(fmt.Sprintf(`{"id":"big","path":%q,"width":60,"height":40,"design_width_mm":90}`, img)), PolicyAuto))
	if o.Committed || len(o.ChangeSet.Warnings) == 0 || !strings.Contains(o.ChangeSet.Warnings[0], "DESIGN_OVERFLOWS") {
		t.Fatalf("overflow must warn and wait: committed=%v %v", o.Committed, o.ChangeSet.Warnings)
	}
}

// 6. a stamp with mirrored lettering-like glyph: mirror toggles through the recipe and regenerates in place
func TestUpdateRecipeEditsInPlaceAndStructurally(t *testing.T) {
	a := New("demo", clock)
	defer a.Close()
	img := glyphPaper(t)
	sw(t, a, "create_stamp", fmt.Sprintf(`{"id":"stamp","path":%q,"width":60,"height":40,"base_thickness":4,"relief_height":2,"margin":4}`, img))
	v0, h0 := vol(t, a), a.Graph.Hash()
	nodes0 := len(a.Graph.Nodes)
	x0, _ := faceCentroidX(t, a, "stamp", 6)

	// a numeric edit changes parameters in place: same nodes, one ChangeSet
	cs := len(a.Mgr.Log)
	sw(t, a, "update_recipe", `{"recipe":"stamp","changes":{"design":{"relief_height":3}}}`)
	if len(a.Graph.Nodes) != nodes0 || len(a.Mgr.Log) != cs+1 {
		t.Fatalf("in-place edit: %d nodes, %d changesets", len(a.Graph.Nodes), len(a.Mgr.Log)-cs)
	}
	if got := a.Graph.Get("stamp").Parameters["depth"]; got != 3.0 {
		t.Fatalf("relief depth %v", got)
	}
	if !(vol(t, a) > v0) {
		t.Fatal("a taller relief adds volume")
	}
	rec := resultMap(t, sw(t, a, "get_recipe", `{"recipe":"stamp_recipe"}`))["recipe"].(map[string]any)
	if rec["design"].(map[string]any)["relief_height"] != 3.0 {
		t.Fatalf("the stored recipe must follow: %v", rec)
	}

	// mirror off: the relief flips back to the other side
	sw(t, a, "update_recipe", `{"recipe":"stamp","changes":{"mirror":false}}`)
	x1, _ := faceCentroidX(t, a, "stamp", 7)
	if x1 >= 0 || math.Abs(x1+x0) > 0.02 {
		t.Fatalf("unmirrored relief at %v, mirrored was %v", x1, x0)
	}

	// structural edits add nodes: a handle and rounded edges; the final body keeps its id
	sw(t, a, "update_recipe", `{"recipe":"stamp","changes":{"handle":{"enabled":true,"height":10,"diameter":14},"base":{"rounded_edges":true,"edge_radius":0.8}}}`)
	for _, id := range []string{"stamp_handle_sketch", "stamp_base_round", "stamp"} {
		if a.Graph.Get(id) == nil {
			t.Fatalf("missing %s", id)
		}
	}
	if a.Graph.Get("stamp_relief") == nil || a.Graph.Get("stamp").Type != "extrude" {
		t.Fatalf("the handle is now the last node: %+v", a.Graph.Get("stamp"))
	}
	lo, _ := stampBounds(t, a, "stamp")
	if math.Abs(lo[2]+10) > 1e-6 {
		t.Fatalf("handle bottom %v", lo[2])
	}
	assertWatertight(t, a, "stamp")
	// ...and remove them again
	sw(t, a, "update_recipe", `{"recipe":"stamp","changes":{"handle":null,"base":{"rounded_edges":false}}}`)
	if a.Graph.Get("stamp_handle_sketch") != nil || a.Graph.Get("stamp_base_round") != nil || a.Graph.Get("stamp").Type != "relief" {
		t.Fatal("structural removal failed")
	}
	// every edit is one undo step; going all the way back restores the original graph
	for i := 0; i < 4; i++ {
		if _, err := a.Undo(); err != nil {
			t.Fatal(err)
		}
	}
	if a.Graph.Hash() != h0 || !near(vol(t, a), v0) {
		t.Fatal("undo did not restore the first version of the stamp")
	}
	// nothing to change is refused; invalid merged recipes are coded
	if o := a.ApplyTool("update_recipe", []byte(`{"recipe":"stamp","changes":{"mirror":true}}`), PolicyCommit); o.OK {
		t.Fatal("a no-op update must be rejected")
	}
	if c := failCode(t, a, "update_recipe", `{"recipe":"stamp","changes":{"base":{"width":-5}}}`); c != "RECIPE_INVALID" {
		t.Fatalf("code %s", c)
	}
	if c := failCode(t, a, "update_recipe", `{"recipe":"stamp","changes":{"typo":1}}`); c != "RECIPE_INVALID" {
		t.Fatalf("code %s", c)
	}
}

func TestApplyRecipeMatchesCreateStampAndRoundTrips(t *testing.T) {
	img := glyphPaper(t)
	a := New("demo", clock)
	defer a.Close()
	sw(t, a, "create_stamp", fmt.Sprintf(`{"id":"s1","path":%q,"width":60,"height":40,"base_thickness":4,"relief_height":2,"margin":4,"rounded_edges":true,"edge_radius":1}`, img))
	rec := resultMap(t, sw(t, a, "get_recipe", `{"recipe":"s1"}`))["recipe"]

	// the stored recipe is reusable: apply it again under another id
	b := New("demo", clock)
	defer b.Close()
	args, _ := json.Marshal(map[string]any{"id": "s1", "recipe": rec})
	mustOK(t, b.ApplyTool("apply_recipe", args, PolicyCommit))
	within(t, "same geometry", vol(t, b), vol(t, a), 1e-12)
	if len(b.Graph.Nodes) != len(a.Graph.Nodes) {
		t.Fatalf("nodes %d vs %d", len(b.Graph.Nodes), len(a.Graph.Nodes))
	}
	// deterministic: identical node parameters
	for _, n := range a.Graph.Nodes {
		m := b.Graph.Get(n.ID)
		if m == nil || fmt.Sprint(m.Parameters) != fmt.Sprint(n.Parameters) {
			t.Fatalf("node %s differs", n.ID)
		}
	}
	// an invalid recipe object is coded
	bad, _ := json.Marshal(map[string]any{"recipe": map[string]any{"type": "stamp", "base": map[string]any{"width": 10}}})
	o := b.ApplyTool("apply_recipe", bad, PolicyCommit)
	if o.OK || o.Code() != "RECIPE_INVALID" {
		t.Fatalf("%v %s", o.OK, o.Code())
	}
}

func TestStampSurvivesUndoAndReload(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "p.scadproj")
	img := glyphPaper(t)
	a := New("demo", clock)
	h0 := a.Graph.Hash()
	sw(t, a, "create_stamp", fmt.Sprintf(`{"id":"stamp","path":%q,"width":50,"height":50,"relief_height":1.5,"handle_enabled":true}`, img))
	h1, v1 := a.Graph.Hash(), vol(t, a)
	if _, err := a.Undo(); err != nil || a.Graph.Hash() != h0 || len(a.Graph.Nodes) != 0 {
		t.Fatalf("undo: %v (%d nodes)", err, len(a.Graph.Nodes))
	}
	if _, err := a.Redo(); err != nil || a.Graph.Hash() != h1 {
		t.Fatalf("redo: %v", err)
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
	if !near(vol(t, b), v1) {
		t.Fatal("reloaded stamp differs")
	}
	// still editable after reload: the recipe regenerates it
	sw(t, b, "update_recipe", `{"recipe":"stamp","changes":{"design":{"relief_height":2.5}}}`)
	if vol(t, b) <= v1 {
		t.Fatal("editing after reload did not regenerate")
	}
}

// 9. a printable stamp exports to a watertight STL; the printability check reads the same model
func TestStampPrintabilityAndSTLExport(t *testing.T) {
	a := New("demo", clock)
	defer a.Close()
	img := glyphPaper(t)
	sw(t, a, "create_stamp", fmt.Sprintf(`{"id":"stamp","path":%q,"width":60,"height":40,"base_thickness":4,"relief_height":2,"margin":4,"rounded_edges":true,"handle_enabled":true,"handle_height":10}`, img))
	r := resultMap(t, sw(t, a, "validate_printability", `{"object":"stamp","nozzle_mm":0.4,"bed":[220,220,250]}`))
	if r["printable"] != true {
		t.Fatalf("%v", r)
	}
	body := r["bodies"].([]any)[0].(map[string]any)
	th := body["thickness"].(map[string]any)
	if th["min_mm"].(float64) < 0.8 {
		t.Fatalf("the glyph stem is %v mm wide at least", th["min_mm"])
	}
	stl := filepath.Join(t.TempDir(), "stamp.stl")
	e := resultMap(t, sw(t, a, "export_stl", fmt.Sprintf(`{"path":%q}`, stl)))
	if e["watertight"] != true {
		t.Fatalf("stl: %v", e)
	}
	// too big for a small bed
	small := resultMap(t, sw(t, a, "validate_printability", `{"object":"stamp","bed":[50,50,50]}`))
	if small["printable"] != false {
		t.Fatalf("%v", small)
	}
	if issues := small["bodies"].([]any)[0].(map[string]any)["issues"].([]any); !strings.Contains(fmt.Sprint(issues), "TOO_LARGE") {
		t.Fatalf("%v", issues)
	}
}

// thin features are found by casting rays through the walls
func TestPrintabilityFindsThinWalls(t *testing.T) {
	a := New("demo", clock)
	defer a.Close()
	sw(t, a, "create_box", `{"id":"slab","dx":40,"dy":40,"dz":0.5}`)
	sw(t, a, "create_box", `{"id":"post","dx":0.2,"dy":20,"dz":10,"x":10,"y":10}`)
	sw(t, a, "boolean", `{"op":"union","target":"slab","tool":"post","id":"part"}`)
	r := resultMap(t, sw(t, a, "validate_printability", `{"object":"part","nozzle_mm":0.4}`))
	if r["printable"] != false {
		t.Fatalf("a 0.2 mm wall cannot be printed with a 0.4 mm nozzle: %v", r)
	}
	if !strings.Contains(fmt.Sprint(r["bodies"]), "NOT_PRINTABLE") {
		t.Fatalf("%v", r)
	}
	// with a 0.1 mm nozzle the same wall is thin but printable (a warning)
	w := resultMap(t, sw(t, a, "validate_printability", `{"object":"part","nozzle_mm":0.1,"min_wall_mm":0.4}`))
	if w["printable"] != true || !strings.Contains(fmt.Sprint(w["bodies"]), "THIN_FEATURE") {
		t.Fatalf("%v", w)
	}
	// a chunky part has no findings
	sw(t, a, "create_box", `{"id":"block","dx":20,"dy":20,"dz":10,"x":100}`)
	ok := resultMap(t, sw(t, a, "validate_printability", `{"object":"block"}`))
	if ok["printable"] != true || len(ok["bodies"].([]any)[0].(map[string]any)["issues"].([]any)) != 0 {
		t.Fatalf("%v", ok)
	}
}

func TestStampImpressionPreview(t *testing.T) {
	a := New("demo", clock)
	defer a.Close()
	img := glyphPaper(t)
	sw(t, a, "create_stamp", fmt.Sprintf(`{"id":"stamp","path":%q,"width":60,"height":40}`, img))
	r := resultMap(t, sw(t, a, "stamp_impression", `{"stamp":"stamp"}`))
	if r["mirrored"] != true || r["impression_reads_like_source"] != true {
		t.Fatalf("%v", r)
	}
	relief, impr := r["relief_svg"].(string), r["impression_svg"].(string)
	if !strings.Contains(relief, "<svg") || relief == impr {
		t.Fatal("the physical stamp and its impression are different drawings")
	}
	// the impression is the mirror image: every x coordinate of one is the negative of the other
	if !strings.Contains(relief, "M") || strings.Count(relief, "C") != strings.Count(impr, "C") {
		t.Fatal("both drawings hold the same curves")
	}
	sw(t, a, "update_recipe", `{"recipe":"stamp","changes":{"mirror":false}}`)
	r2 := resultMap(t, sw(t, a, "stamp_impression", `{"stamp":"stamp_recipe"}`))
	if r2["mirrored"] != false || !strings.Contains(r2["note"].(string), "reversed") {
		t.Fatalf("%v", r2)
	}
	// with mirror off the physical stamp shows the source drawing: relief and impression swap
	if r2["relief_svg"] != r["impression_svg"] || r2["impression_svg"] != r["relief_svg"] {
		t.Fatal("toggling mirror swaps the two drawings")
	}
	if o := a.ApplyTool("stamp_impression", []byte(`{"stamp":"nope"}`), PolicyCommit); o.OK {
		t.Fatal("unknown stamp")
	}
}

// a hand-drawn flower as raised ridges: the strokes are drawn with the ridge width in 2D, merged
// where they cross and raised as one relief
func TestFlowerStampFromCenterlines(t *testing.T) {
	a := New("demo", clock)
	defer a.Close()
	p := newPaper(500, 600)
	p.stroke([][2]float64{{250, 560}, {245, 400}, {255, 300}}, 8)
	for k := 0; k < 5; k++ {
		ang := float64(k) * 2 * math.Pi / 5
		p.stroke(ellipsePts(250+85*math.Cos(ang), 230+85*math.Sin(ang), 55, 28, ang), 7)
	}
	img := p.save(t, "flower.png")
	o := sw(t, a, "create_stamp", fmt.Sprintf(`{"id":"flower","path":%q,"mode":"centerline","width":60,"height":60,"stroke_width":1.6,"relief_height":1.2,"base_thickness":3,"margin":4}`, img))
	if !o.Committed {
		t.Fatalf("not committed: %v", o.ChangeSet.Warnings)
	}
	if a.Graph.Get("flower_design") == nil || a.Graph.Get("flower").Type != "relief" {
		t.Fatal("the ridges are one relief of one design sketch")
	}
	m, _ := a.Model()
	if m.Nodes["flower"].Status != "ok" {
		t.Fatalf("%v", m.Nodes["flower"].Errors)
	}
	assertWatertight(t, a, "flower")
	// the ridges add roughly stroke_width x relief_height x total stroke length (strokes drawn at 7-8 px of 500 over 60 mm)
	extra := vol(t, a) - 60*60*3
	if extra < 60*0.8*1.2 || extra > 60*60*1.2*0.25 {
		t.Fatalf("ridge volume %v is implausible", extra)
	}
	// the stem crosses petals: crossings merge, so the design is a handful of regions, not one per stroke
	r := resultMap(t, sw(t, a, "validate_printability", `{"object":"flower","nozzle_mm":0.4}`))
	if r["printable"] != true {
		t.Fatalf("%v", r)
	}
	// ridges narrower than the nozzle are reported through the recipe
	bad := resultMap(t, sw(t, a, "validate_printability", `{"object":"flower","nozzle_mm":2}`))
	if bad["printable"] != false || !strings.Contains(fmt.Sprint(bad["bodies"]), "NOT_PRINTABLE") {
		t.Fatalf("%v", bad)
	}
	// a thinner ridge regenerates in place and a taller relief is flagged fragile
	sw(t, a, "update_recipe", `{"recipe":"flower","changes":{"design":{"stroke_width":0.9,"relief_height":4}}}`)
	assertWatertight(t, a, "flower")
	frag := resultMap(t, sw(t, a, "validate_printability", `{"object":"flower","nozzle_mm":0.4}`))
	if !strings.Contains(fmt.Sprint(frag["bodies"]), "FRAGILE_RELIEF") {
		t.Fatalf("%v", frag)
	}
	imp := resultMap(t, sw(t, a, "stamp_impression", `{"stamp":"flower"}`))
	if !strings.Contains(imp["relief_svg"].(string), "fill-rule") {
		t.Fatal("ridge preview missing")
	}
}
