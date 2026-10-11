package app

import (
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Reference models: each is a deterministic tool script whose result (volume, bounding box, piece
// and face counts) is pinned in testdata/regression/models.json. A change to the kernel, the
// sweep planner, the tracer or the text engine that moves any of them shows up here.
// Regenerate deliberately with: go test ./internal/app -run Regression -update-regression
var updateRegression = flag.Bool("update-regression", false, "rewrite testdata/regression/models.json")

type refCall struct {
	Tool string          `json:"tool"`
	Args json.RawMessage `json:"args"`
}

type refModel struct {
	Name   string    `json:"name"`
	Script []refCall `json:"script"`
	Expect *refStats `json:"expect,omitempty"`
}

type refStats struct {
	Volume float64    `json:"volume_mm3"`
	Min    [3]float64 `json:"min"`
	Max    [3]float64 `json:"max"`
	Solids int        `json:"solids"`
	Faces  int        `json:"faces"`
}

func step(tool, args string) refCall { return refCall{tool, json.RawMessage(args)} }

func referenceScripts(glyph string) []refModel {
	return []refModel{
		{Name: "helix_tube", Script: []refCall{step("create_tube", `{"id":"m","points":[[10,0,0],[0,10,3],[-10,0,6],[0,-10,9],[10,0,12],[0,10,15],[-10,0,18]],"outer_diameter":3}`)}},
		{Name: "torus_ring", Script: []refCall{step("create_tube", `{"id":"m","points":[[20,0,0],[0,20,0],[-20,0,0],[0,-20,0]],"closed":true,"outer_diameter":4}`)}},
		{Name: "hollow_pipe", Script: []refCall{step("create_tube", `{"id":"m","points":[[0,0,0],[30,0,0],[60,15,0]],"outer_diameter":8,"inner_diameter":5}`)}},
		{Name: "flat_ribbon", Script: []refCall{step("create_ribbon", `{"id":"m","points":[[0,0,0],[30,10,0],[60,0,0]],"width":10,"thickness":1.5}`)}},
		{Name: "extruded_text", Script: []refCall{step("create_text_geometry", `{"id":"m","text":"Simon","size":12,"mode":"extrude","depth":3}`)}},
		{Name: "embossed_label", Script: []refCall{
			step("create_box", `{"id":"plate","dx":60,"dy":30,"dz":4,"x":-30,"y":-15}`),
			step("create_text_geometry", `{"id":"m","text":"CAD","size":10,"mode":"emboss","depth":1.5,"target":"plate","offset":4,"center":true}`)}},
		{Name: "engraved_groove", Script: []refCall{
			step("create_box", `{"id":"plate","dx":50,"dy":50,"dz":6,"x":-25,"y":-25}`),
			step("engrave_surface", `{"id":"m","target":"plate","points":[[-20,-10,6],[0,10,6],[20,-10,6]],"width":2,"style":"round"}`)}},
		{Name: "glyph_stamp", Script: []refCall{
			step("create_stamp", fmt.Sprintf(`{"id":"m","path":%q,"width":50,"height":50,"relief_height":1.5,"handle_enabled":true}`, glyph))}},
		{Name: "projected_groove", Script: []refCall{
			step("create_cylinder", `{"id":"drum","radius":20,"height":60}`),
			step("create_path", `{"id":"line","points":[[60,-8,30],[60,8,30]],"type":"linear"}`),
			step("project_path_to_surface", `{"id":"on","path":"line","target":"drum","mode":"conform","dir":[-1,0,0]}`),
			step("engrave_surface", `{"id":"m","path":"on","target":"drum","width":2,"depth":1,"style":"flat","surface_normal":[1,0,0]}`)}},
	}
}

func runReference(t *testing.T, m refModel) *refStats {
	t.Helper()
	a := New("regression", clock)
	defer a.Close()
	for _, c := range m.Script {
		if o := a.ApplyTool(c.Tool, c.Args, PolicyCommit); !o.OK {
			t.Fatalf("%s: %s: %s", m.Name, c.Tool, o.Error)
		}
	}
	mod, err := a.Model()
	if err != nil {
		t.Fatal(err)
	}
	if len(mod.Leaves) != 1 {
		t.Fatalf("%s: %d visible bodies %v, want 1", m.Name, len(mod.Leaves), mod.Leaves)
	}
	p := mod.Nodes[mod.Leaves[0]].Body.Props
	r6 := func(v float64) float64 { return math.Round(v*1e4) / 1e4 }
	return &refStats{Volume: r6(p.Volume), Min: [3]float64{r6(p.MinX), r6(p.MinY), r6(p.MinZ)}, Max: [3]float64{r6(p.MaxX), r6(p.MaxY), r6(p.MaxZ)}, Solids: p.Solids, Faces: p.Faces}
}

func TestRegressionReferenceModels(t *testing.T) {
	file := filepath.Join("..", "..", "testdata", "regression", "models.json")
	models := referenceScripts("glyph.png")
	if *updateRegression {
		glyph := glyphPaper(t)
		for i := range models {
			for j := range models[i].Script {
				models[i].Script[j].Args = json.RawMessage(strings.ReplaceAll(string(models[i].Script[j].Args), `"glyph.png"`, fmt.Sprintf("%q", glyph)))
			}
			models[i].Expect = runReference(t, models[i])
			// the stored script must not depend on this machine's temp dir
			for j := range models[i].Script {
				models[i].Script[j].Args = json.RawMessage(strings.ReplaceAll(string(models[i].Script[j].Args), fmt.Sprintf("%q", glyph), `"glyph.png"`))
			}
		}
		b, _ := json.MarshalIndent(models, "", " ")
		if err := os.WriteFile(file, append(b, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("%v (create it with -update-regression)", err)
	}
	var want []refModel
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	if len(want) != len(models) {
		t.Fatalf("models.json has %d models, the test defines %d: regenerate with -update-regression", len(want), len(models))
	}
	glyph := glyphPaper(t)
	for _, w := range want {
		t.Run(w.Name, func(t *testing.T) {
			m := w
			m.Script = append([]refCall(nil), w.Script...)
			for j := range m.Script {
				m.Script[j].Args = json.RawMessage(strings.ReplaceAll(string(m.Script[j].Args), `"glyph.png"`, fmt.Sprintf("%q", glyph)))
			}
			got := runReference(t, m)
			e := w.Expect
			if math.Abs(got.Volume-e.Volume) > 1e-3*math.Max(1, e.Volume) {
				t.Errorf("volume %.4f, reference %.4f", got.Volume, e.Volume)
			}
			for i := 0; i < 3; i++ {
				if math.Abs(got.Min[i]-e.Min[i]) > 0.02 || math.Abs(got.Max[i]-e.Max[i]) > 0.02 {
					t.Errorf("bounding box axis %d [%.3f, %.3f], reference [%.3f, %.3f]", i, got.Min[i], got.Max[i], e.Min[i], e.Max[i])
				}
			}
			if got.Solids != e.Solids {
				t.Errorf("%d solids, reference %d", got.Solids, e.Solids)
			}
			if got.Faces != e.Faces {
				t.Errorf("%d faces, reference %d", got.Faces, e.Faces)
			}
		})
	}
}
