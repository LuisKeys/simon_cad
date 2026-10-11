package app

import (
	"encoding/json"
	"math"
	"path/filepath"
	"strings"
	"testing"
)

func resultMap(t *testing.T, o Outcome) map[string]any {
	t.Helper()
	b, _ := json.Marshal(o.Data)
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestPathAndProfileTools(t *testing.T) {
	a := New("demo", clock)
	defer a.Close()

	mustOK(t, a.ApplyTool("create_path", []byte(`{"id":"spine","type":"linear","points":[[0,0,0],[30,0,0],[30,40,0]]}`), PolicyCommit))
	r := resultMap(t, a.ApplyTool("evaluate_path", []byte(`{"path":"spine","t":[0,0.5,1]}`), PolicyCommit))
	if l := r["length_mm"].(float64); math.Abs(l-70) > 1e-9 {
		t.Fatalf("length %v", l)
	}
	if s := r["samples"].([]any); len(s) != 3 {
		t.Fatalf("samples %v", s)
	}

	// edit: insert + move + per-point scale, then convert to bezier keeping the shape
	mustOK(t, a.ApplyTool("update_path", []byte(`{"path":"spine","ops":[
		{"op":"insert","index":1,"point":[15,0,0]},
		{"op":"move","index":3,"position":[30,40,10]},
		{"op":"set","index":0,"w":2,"h":0.5,"twist":45}]}`), PolicyCommit))
	n := a.Graph.Get("spine")
	pts := n.Parameters["points"].([]any)
	if len(pts) != 4 {
		t.Fatalf("points %v", pts)
	}
	first := pts[0].(map[string]any)
	if first["w"] != 2.0 || first["h"] != 0.5 || first["twist"] != 45.0 {
		t.Fatalf("set op lost: %v", first)
	}
	before := resultMap(t, a.ApplyTool("evaluate_path", []byte(`{"path":"spine"}`), PolicyCommit))["length_mm"].(float64)
	mustOK(t, a.ApplyTool("update_path", []byte(`{"path":"spine","type":"bezier"}`), PolicyCommit))
	after := resultMap(t, a.ApplyTool("evaluate_path", []byte(`{"path":"spine"}`), PolicyCommit))["length_mm"].(float64)
	if math.Abs(before-after) > 1e-6 {
		t.Fatalf("bezier conversion changed length %v -> %v", before, after)
	}
	mustOK(t, a.ApplyTool("update_path", []byte(`{"path":"spine","closed":true}`), PolicyCommit))
	mustOK(t, a.ApplyTool("update_path", []byte(`{"path":"spine","reverse":true}`), PolicyCommit))

	// profiles
	mustOK(t, a.ApplyTool("create_profile", []byte(`{"id":"pipe","shape":"hollow_circle","outer_radius":3,"inner_radius":2}`), PolicyCommit))
	mustOK(t, a.ApplyTool("update_profile", []byte(`{"profile":"pipe","outer_radius":4}`), PolicyCommit))
	if got := a.Graph.Get("pipe").Parameters["outer_radius"]; got != 4.0 {
		t.Fatalf("outer_radius %v", got)
	}
	mustOK(t, a.ApplyTool("create_profile", []byte(`{"id":"cust","shape":"custom","contours":[
		{"points":[[0,0],[10,0],[10,10],[0,10]]},{"points":[[2,2],[8,2],[8,8],[2,8]],"hole":true}]}`), PolicyCommit))

	// data nodes are not geometry
	if v := vol(t, a); v != 0 {
		t.Fatalf("paths/profiles have no volume, got %v", v)
	}
	m, err := a.Model()
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Leaves) != 0 || len(m.Sketches) != 0 {
		t.Fatalf("data nodes must not be bodies: %v %v", m.Leaves, m.Sketches)
	}
	for _, id := range []string{"spine", "pipe", "cust"} {
		if m.Nodes[id].Status != "ok" {
			t.Fatalf("%s status %v %v", id, m.Nodes[id].Status, m.Nodes[id].Errors)
		}
	}
}

func TestPathToolErrorsAreActionableAndCoded(t *testing.T) {
	a := New("demo", clock)
	defer a.Close()
	mustOK(t, a.ApplyTool("create_path", []byte(`{"id":"p","points":[[0,0,0],[1,0,0]]}`), PolicyCommit))
	mustOK(t, a.ApplyTool("create_profile", []byte(`{"id":"pr","shape":"circle","radius":1}`), PolicyCommit))
	h := a.Graph.Hash()
	for _, bad := range []struct{ tool, args, code string }{
		{"create_path", `{"points":[[0,0,0]]}`, ""},
		{"create_path", `{"points":[[0,0],[1,1]]}`, "PATH_INVALID"},
		{"create_path", `{"points":[[0,0,0],[1,1,1]],"closed":true}`, "PATH_INVALID"},
		{"create_profile", `{"shape":"hollow_circle","outer_radius":1,"inner_radius":2}`, "PROFILE_INVALID"},
		{"create_profile", `{"shape":"custom","contours":[{"points":[[0,0],[2,2],[2,0],[0,2]]}]}`, "PROFILE_INVALID"},
		{"update_path", `{"path":"pr","reverse":true}`, ""},
		{"update_path", `{"path":"p","ops":[{"op":"remove","index":0}]}`, ""},
		{"update_path", `{"path":"p","ops":[{"op":"move","index":9,"position":[0,0,0]}]}`, ""},
		{"update_path", `{"path":"p"}`, ""},
		{"update_profile", `{"profile":"pr"}`, ""},
		{"update_profile", `{"profile":"pr","radius":-1}`, ""},
		{"delete_path", `{"path":"pr"}`, ""},
	} {
		o := a.ApplyTool(bad.tool, []byte(bad.args), PolicyCommit)
		if o.OK {
			t.Errorf("%s %s should fail", bad.tool, bad.args)
			continue
		}
		if bad.code != "" {
			var m map[string]any
			_ = json.Unmarshal([]byte(o.JSON()), &m)
			if m["code"] != bad.code {
				t.Errorf("%s %s: code %v want %s (error %q)", bad.tool, bad.args, m["code"], bad.code, o.Error)
			}
		}
		if strings.TrimSpace(o.Error) == "" {
			t.Errorf("%s %s: empty error", bad.tool, bad.args)
		}
	}
	if a.Graph.Hash() != h {
		t.Fatal("rejected calls mutated the model")
	}
}

func TestPathUndoRedoSaveLoad(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "p.scadproj")
	a := New("demo", clock)
	mustOK(t, a.ApplyTool("create_path", []byte(`{"id":"p","type":"catmull_rom","points":[[0,0,0],[10,5,0],[20,0,3],[30,5,0]]}`), PolicyCommit))
	h0 := a.Graph.Hash()
	mustOK(t, a.ApplyTool("update_path", []byte(`{"path":"p","ops":[{"op":"move","index":2,"position":[20,-5,3]}]}`), PolicyCommit))
	h1 := a.Graph.Hash()
	if h1 == h0 {
		t.Fatal("edit did not change the graph")
	}
	if _, err := a.Undo(); err != nil || a.Graph.Hash() != h0 {
		t.Fatalf("undo: %v", err)
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
	if b.Graph.Hash() != h1 {
		t.Fatal("reopened path differs")
	}
	r := resultMap(t, b.ApplyTool("evaluate_path", []byte(`{"path":"p"}`), PolicyCommit))
	if r["length_mm"].(float64) < 30 {
		t.Fatalf("length %v", r)
	}
	// deleting the path works once nothing uses it, and undo brings it back
	mustOK(t, b.ApplyTool("delete_path", []byte(`{"path":"p"}`), PolicyCommit))
	if b.Graph.Get("p") != nil {
		t.Fatal("path not deleted")
	}
	if _, err := b.Undo(); err != nil || b.Graph.Get("p") == nil {
		t.Fatalf("undo delete: %v", err)
	}
}
