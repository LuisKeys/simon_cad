package app

import (
	"encoding/json"
	"math"
	"path/filepath"
	"testing"
)

func pathPoints(t *testing.T, a *App, id string) [][3]float64 {
	t.Helper()
	n := a.Graph.Get(id)
	if n == nil {
		t.Fatalf("no path %s", id)
	}
	b, _ := json.Marshal(n.Parameters["points"])
	var raw []json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	var out [][3]float64
	for _, r := range raw {
		var arr [3]float64
		if json.Unmarshal(r, &arr) == nil {
			out = append(out, arr)
			continue
		}
		var m struct{ P [3]float64 }
		if err := json.Unmarshal(r, &m); err != nil {
			t.Fatal(err)
		}
		out = append(out, m.P)
	}
	return out
}

const drum = `{"id":"drum","radius":20,"height":60}`

// 7. a path projected onto a cylinder lies on it: every point at r ± offset
func TestProjectPathOntoCylinder(t *testing.T) {
	a := New("demo", clock)
	defer a.Close()
	sw(t, a, "create_cylinder", drum)
	sw(t, a, "create_path", `{"id":"line","points":[[60,-8,30],[60,0,30],[60,8,30]],"type":"catmull_rom"}`)

	sw(t, a, "project_path_to_surface", `{"path":"line","target":"drum","mode":"planar","dir":[-1,0,0]}`)
	pts := pathPoints(t, a, "line_projected")
	if len(pts) != 3 {
		t.Fatalf("%d points", len(pts))
	}
	for i, p := range pts {
		if r := math.Hypot(p[0], p[1]); math.Abs(r-20) > 0.05 {
			t.Errorf("point %d at radius %.4f, want 20", i, r)
		}
		if math.Abs(p[2]-30) > 1e-6 {
			t.Errorf("point %d left its height: z=%v", i, p[2])
		}
	}
	// the source path is untouched
	if src := pathPoints(t, a, "line"); src[0][0] != 60 {
		t.Error("the original path changed")
	}

	// offset lifts along the normal
	sw(t, a, "project_path_to_surface", `{"id":"lifted","path":"line","target":"drum","dir":[-1,0,0],"offset":1.5}`)
	for i, p := range pathPoints(t, a, "lifted") {
		if r := math.Hypot(p[0], p[1]); math.Abs(r-21.5) > 0.05 {
			t.Errorf("lifted point %d at radius %.4f, want 21.5", i, r)
		}
	}

	// normal mode: nearest surface point, no direction
	sw(t, a, "project_path_to_surface", `{"id":"nearest","points":[[30,0,10],[0,25,40]],"target":"drum","mode":"normal"}`)
	for i, p := range pathPoints(t, a, "nearest") {
		if r := math.Hypot(p[0], p[1]); math.Abs(r-20) > 0.05 {
			t.Errorf("nearest point %d at radius %.4f", i, r)
		}
	}
}

func TestConformFollowsTheCurvature(t *testing.T) {
	a := New("demo", clock)
	defer a.Close()
	sw(t, a, "create_cylinder", drum)
	// a wide straight stroke: planar would cut a chord through the drum
	sw(t, a, "create_path", `{"id":"wide","points":[[60,-18,30],[60,18,30]],"type":"linear"}`)
	sw(t, a, "project_path_to_surface", `{"id":"wrapped","path":"wide","target":"drum","mode":"conform","dir":[-1,0,0],"tolerance":0.05}`)
	pts := pathPoints(t, a, "wrapped")
	if len(pts) < 6 {
		t.Fatalf("conform added only %d points", len(pts))
	}
	for i, p := range pts {
		if r := math.Hypot(p[0], p[1]); math.Abs(r-20) > 0.06 {
			t.Errorf("point %d at radius %.4f", i, r)
		}
	}
	// the answer carries the tessellation-limited length of the wrapped arc (~ 2·r·asin(18/20) = 44.9)
	o := sw(t, a, "evaluate_path", `{"path":"wrapped"}`)
	if l := resultMap(t, o)["length_mm"].(float64); math.Abs(l-44.9) > 1 {
		t.Errorf("wrapped length %.2f, want ≈44.9", l)
	}
}

// the projected path feeds the existing relief tools: a groove along the curved surface
func TestEngraveAlongAProjectedPath(t *testing.T) {
	a := New("demo", clock)
	defer a.Close()
	sw(t, a, "create_cylinder", drum)
	base := vol(t, a)
	sw(t, a, "create_path", `{"id":"line","points":[[60,-8,30],[60,8,30]],"type":"linear"}`)
	sw(t, a, "project_path_to_surface", `{"id":"on","path":"line","target":"drum","mode":"conform","dir":[-1,0,0]}`)
	sw(t, a, "engrave_surface", `{"id":"groove","path":"on","target":"drum","width":2,"depth":1,"style":"flat","surface_normal":[1,0,0]}`)
	cut := base - vol(t, a)
	// flat groove 2 x 1 mm over ~16 mm of arc (the surface bends away a little, so allow slack)
	if cut < 20 || cut > 40 {
		t.Errorf("groove removed %.2f mm³, want ≈ 32", cut)
	}
	assertWatertight(t, a, "groove")
}

// text on a curved part: flat-topped letters that start inside the surface merge into it
func TestTextEmbossedOnACylinder(t *testing.T) {
	a := New("demo", clock)
	defer a.Close()
	sw(t, a, "create_cylinder", `{"id":"drum","radius":20,"height":60,"axis":"z"}`)
	base := vol(t, a)
	// the letters lie on the YZ plane tangent to the drum at x = 20; starting 1 mm inside the surface
	sw(t, a, "create_text_geometry", `{"id":"label","text":"Hi","size":8,"plane":"YZ","offset":19,"mode":"emboss","depth":2,"target":"drum","x":0,"y":30,"center":true}`)
	if vol(t, a) <= base {
		t.Fatal("no text was added to the drum")
	}
	assertWatertight(t, a, "label")
}

func TestProjectionErrors(t *testing.T) {
	a := New("demo", clock)
	defer a.Close()
	sw(t, a, "create_cylinder", drum)
	sw(t, a, "create_path", `{"id":"line","points":[[60,-8,30],[60,8,30]]}`)
	sw(t, a, "create_path", `{"id":"high","points":[[60,0,200],[60,5,200]]}`)
	if c := failCode(t, a, "project_path_to_surface", `{"path":"high","target":"drum","dir":[-1,0,0]}`); c != "SURFACE_PROJECTION_FAILED" {
		t.Errorf("code %q", c)
	}
	h := a.Graph.Hash()
	for _, bad := range []string{
		`{"path":"line","target":"drum"}`,                                           // planar needs dir
		`{"path":"line","target":"drum","mode":"normal","dir":[1,0,0]}`,             // normal takes no dir
		`{"target":"drum","dir":[-1,0,0]}`,                                          // no source
		`{"path":"line","points":[[0,0,0],[1,0,0]],"target":"drum","dir":[-1,0,0]}`, // both
		`{"path":"line","target":"nope","dir":[-1,0,0]}`,
		`{"path":"drum","target":"drum","dir":[-1,0,0]}`, // not a path
		`{"path":"line","target":"drum","dir":[0,0,0]}`,
	} {
		if o := a.ApplyTool("project_path_to_surface", []byte(bad), PolicyCommit); o.OK {
			t.Errorf("%s accepted", bad)
		}
	}
	if a.Graph.Hash() != h {
		t.Error("a failed projection changed the graph")
	}
}

func TestProjectedPathSurvivesUndoAndReload(t *testing.T) {
	a := New("demo", clock)
	sw(t, a, "create_cylinder", drum)
	sw(t, a, "create_path", `{"id":"line","points":[[60,-8,30],[60,8,30]]}`)
	h := a.Graph.Hash()
	sw(t, a, "project_path_to_surface", `{"id":"on","path":"line","target":"drum","mode":"conform","dir":[-1,0,0]}`)
	h2 := a.Graph.Hash()
	if _, err := a.Undo(); err != nil || a.Graph.Hash() != h {
		t.Fatalf("undo: %v", err)
	}
	if _, err := a.Redo(); err != nil || a.Graph.Hash() != h2 {
		t.Fatalf("redo: %v", err)
	}
	dir := filepath.Join(t.TempDir(), "p.scadproj")
	if err := a.Save(dir); err != nil {
		t.Fatal(err)
	}
	a.Close()
	b, err := Open(dir, clock)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if len(pathPoints(t, b, "on")) < 2 {
		t.Error("projected path lost on reload")
	}
}
