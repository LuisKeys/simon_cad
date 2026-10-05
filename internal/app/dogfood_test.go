package app

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
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
