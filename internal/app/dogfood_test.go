package app

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/accelone/simoncad/internal/geometry/api"
)

// Robot-driven dogfooding: the Phase 0 servo bracket, re-created purely through the
// tool → ChangeSet pipeline, must match the hand-built kernel geometry exactly.
func TestDogfoodServoBracket(t *testing.T) {
	raw, err := os.ReadFile("../../testdata/robot/servo_bracket/steps.json")
	if err != nil {
		t.Fatal(err)
	}
	var spec struct {
		Steps []struct {
			Tool string          `json:"tool"`
			Args json.RawMessage `json:"args"`
		} `json:"steps"`
	}
	if err := json.Unmarshal(raw, &spec); err != nil {
		t.Fatal(err)
	}
	a := New("servo_bracket", clock)
	defer a.Close()
	for _, s := range spec.Steps {
		mustOK(t, a.ApplyTool(s.Tool, s.Args, PolicyCommit))
		a.Checkpoint(string(s.Tool), false)
	}
	m, err := a.Model()
	if err != nil {
		t.Fatal(err)
	}
	if f := m.Failed(); len(f) != 0 {
		t.Fatalf("failed features %v", f)
	}

	// Reference: the Phase 0 construction, straight on the kernel.
	k := api.New()
	defer k.Close()
	var live []*api.Shape
	keep := func(s *api.Shape, err error) *api.Shape {
		if err != nil {
			t.Fatal(err)
		}
		live = append(live, s)
		return s
	}
	defer func() {
		for _, s := range live {
			s.Release()
		}
	}()
	plate := keep(keep(k.Box(70, 40, 4)).Fillet(1.5))
	pocket := keep(keep(k.Box(41, 20.5, 6)).Translate(14.5, 9.75, -1))
	part := keep(k.Subtract(plate, pocket))
	for _, sx := range []float64{-1, 1} {
		for _, sy := range []float64{-1, 1} {
			h := keep(keep(k.Cylinder(1.6, 6)).Translate(35+sx*24.75, 20+sy*5, -1))
			part = keep(k.Subtract(part, h))
		}
	}
	want, err := part.Props()
	if err != nil {
		t.Fatal(err)
	}
	got := m.Nodes["ear_holes"].Body.Props
	if rel := math.Abs(got.Volume-want.Volume) / want.Volume; rel > 1e-9 {
		t.Fatalf("tool-built volume %v vs kernel-built %v (rel %.2e)", got.Volume, want.Volume, rel)
	}
	if got.Faces != want.Faces || got.Solids != 1 {
		t.Fatalf("topology differs: %d faces vs %d", got.Faces, want.Faces)
	}

	dir := t.TempDir()
	step := mustOK(t, a.ApplyTool("export_step", []byte(`{"path":"`+filepath.Join(dir, "b.step")+`"}`), PolicyAuto))
	if step.Data.(map[string]any)["reimport_volume_rel_error"].(float64) > 1e-6 {
		t.Fatal("STEP round trip")
	}
	stl := mustOK(t, a.ApplyTool("export_stl", []byte(`{"path":"`+filepath.Join(dir, "b.stl")+`"}`), PolicyAuto))
	if !stl.Data.(map[string]any)["watertight"].(bool) {
		t.Fatal("STL not watertight")
	}

	// Permanent regression project: refresh with UPDATE_ROBOT=1.
	proj := "../../testdata/robot/servo_bracket.scadproj"
	if os.Getenv("UPDATE_ROBOT") == "1" {
		if err := a.Save(proj); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(proj); err == nil {
		b, err := Open(proj, clock)
		if err != nil {
			t.Fatal(err)
		}
		defer b.Close()
		bm, err := b.Model()
		if err != nil || len(bm.Failed()) != 0 {
			t.Fatalf("saved regression project fails to rebuild: %v %v", err, bm.Failed())
		}
		if v := bm.Volume(); math.Abs(v-want.Volume)/want.Volume > 1e-9 {
			t.Fatalf("regression project volume drifted: %v vs %v", v, want.Volume)
		}
		if _, match, _, err := b.Replay(); err != nil || !match {
			t.Fatalf("regression project replay: %v %v", match, err)
		}
	}
}

func runSteps(t *testing.T, a *App, path string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var spec struct {
		Steps []struct {
			Tool string          `json:"tool"`
			Args json.RawMessage `json:"args"`
		} `json:"steps"`
	}
	if err := json.Unmarshal(raw, &spec); err != nil {
		t.Fatal(err)
	}
	for _, s := range spec.Steps {
		mustOK(t, a.ApplyTool(s.Tool, s.Args, PolicyCommit))
	}
}

// Dogfood 2: the elbow clevis, built through the tool pipeline with click-style selectors,
// must match its analytic volume and expose the slot floor as a distinct semantic face.
func TestDogfoodElbowClevis(t *testing.T) {
	a := New("elbow_joint", clock)
	defer a.Close()
	runSteps(t, a, "../../testdata/robot/elbow_joint/steps.json")
	m, err := a.Model()
	if err != nil {
		t.Fatal(err)
	}
	if f := m.Failed(); len(f) != 0 {
		t.Fatalf("failed features %v", f)
	}
	want := 40.0*30*20 - 40*12*14 - math.Pi*16*18 - 4*math.Pi*2.15*2.15*20
	if got := m.Volume(); math.Abs(got-want)/want > 1e-6 {
		t.Fatalf("volume %.6f, want %.6f", got, want)
	}
	// The slot floor is its own face, addressable the way a viewport click would address it.
	if err := a.SetSelection([]string{"face(feature=slot,side=+Z,at=6)"}); err != nil {
		t.Fatal(err)
	}
	e := a.SelectionContext().Entities[0]
	if math.Abs(e.Area-40*12) > 1e-6 || e.Centroid != [3]float64{20, 15, 6} {
		t.Fatalf("slot floor descriptor %+v", e)
	}
	shape, err := m.ResultShape(a.K)
	if err != nil {
		t.Fatal(err)
	}
	defer shape.Release()
	if ok, err := shape.IsValid(); err != nil || !ok {
		t.Fatalf("clevis is not a valid solid: %v", err)
	}
}

// The servo bracket, finished the way a user does it in the viewport: pocket first, then
// round only the four vertical corner edges they selected. This is the gap logged in the
// Phase 1 backlog ("fillet/chamfer act on every edge").
func TestSelectiveFilletOfSelectedEdges(t *testing.T) {
	a := New("bracket_visual", clock)
	defer a.Close()
	mustOK(t, a.ApplyTool("create_box", []byte(`{"id":"plate","dx":70,"dy":40,"dz":4}`), PolicyCommit))
	mustOK(t, a.ApplyTool("extrude_profile", []byte(`{"id":"pocket","target":"plate","mode":"cut","profile":"rectangle","x":14.5,"y":9.75,"w":41,"h":20.5,"through":true}`), PolicyCommit))
	m, _ := a.Model()
	topo, ok := m.Topology("pocket")
	if !ok {
		t.Fatal("no topology")
	}
	// Vertical edges lying on the plate's outer corners: what a user would shift-click.
	var picks []string
	for i, e := range topo.Edges {
		vertical := math.Abs(e.Start[2]-e.End[2]) > 1 && e.Start[0] == e.End[0] && e.Start[1] == e.End[1]
		outer := (e.Start[0] == 0 || e.Start[0] == 70) && (e.Start[1] == 0 || e.Start[1] == 40)
		if vertical && outer {
			picks = append(picks, selectorOfEdge("pocket", i))
		}
	}
	if len(picks) != 4 {
		t.Fatalf("expected 4 outer vertical edges, found %d", len(picks))
	}
	before := m.Volume()
	args, _ := json.Marshal(map[string]any{"id": "rounded", "target": "pocket", "radius": 3, "edges": picks})
	o := mustOK(t, a.ApplyTool("fillet", args, PolicyCommit))
	if !o.Committed {
		t.Fatal("not committed")
	}
	m, _ = a.Model()
	if f := m.Failed(); len(f) != 0 {
		t.Fatalf("failed %v", f)
	}
	removed := 4 * (1 - math.Pi/4) * 3 * 3 * 4
	if got := before - m.Volume(); math.Abs(got-removed) > 1e-6 {
		t.Fatalf("selective fillet removed %.6f mm³, want %.6f (the pocket must stay sharp)", got, removed)
	}

	// Guard rails: foreign feature, wrong kind, out-of-range index, unknown selector.
	for name, edges := range map[string][]string{
		"foreign": {"edge(feature=plate,index=0)"},
		"face":    {"face(feature=pocket,side=+Z)"},
		"range":   {"edge(feature=pocket,index=9999)"},
		"garbage": {"nonsense"},
	} {
		bad, _ := json.Marshal(map[string]any{"target": "pocket", "radius": 1, "edges": edges})
		if o := a.ApplyTool("fillet", bad, PolicyCommit); o.OK {
			t.Fatalf("%s: selective fillet should have been rejected", name)
		}
	}
}

func selectorOfEdge(feature string, i int) string {
	return "edge(feature=" + feature + ",index=" + strconv.Itoa(i) + ")"
}
