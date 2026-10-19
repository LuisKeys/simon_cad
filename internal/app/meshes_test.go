package app

import (
	"encoding/binary"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/accelone/simoncad/internal/meshimport"
)

// cubeSTL is a closed, outward-oriented cube of side s at the origin, as a binary STL.
func cubeSTL(s float32) []byte {
	p := [8][3]float32{{0, 0, 0}, {s, 0, 0}, {s, s, 0}, {0, s, 0}, {0, 0, s}, {s, 0, s}, {s, s, s}, {0, s, s}}
	q := [][4]int{{0, 3, 2, 1}, {4, 5, 6, 7}, {0, 1, 5, 4}, {1, 2, 6, 5}, {2, 3, 7, 6}, {3, 0, 4, 7}}
	var tris [][3]int
	for _, f := range q {
		tris = append(tris, [3]int{f[0], f[1], f[2]}, [3]int{f[0], f[2], f[3]})
	}
	return stlOf(p[:], tris)
}

func stlOf(p [][3]float32, tris [][3]int) []byte {
	b := make([]byte, 84+50*len(tris))
	binary.LittleEndian.PutUint32(b[80:], uint32(len(tris)))
	for i, t := range tris {
		for j, v := range t {
			for k := 0; k < 3; k++ {
				binary.LittleEndian.PutUint32(b[84+50*i+12+12*j+4*k:], math.Float32bits(p[v][k]))
			}
		}
	}
	return b
}

func writeTemp(t *testing.T, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func importArgs(t *testing.T, m map[string]any) []byte {
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestImportSTLValidatesThenBuildsASolid(t *testing.T) {
	a := New("scan", clock)
	defer a.Close()
	path := writeTemp(t, "part.stl", cubeSTL(10))
	o := mustOK(t, a.ApplyTool("import_stl", importArgs(t, map[string]any{"path": path, "units": "cm"}), PolicyAuto))
	if !o.Committed {
		t.Fatalf("a valid import should commit: %+v", o)
	}
	n := a.Graph.Get("part")
	if n == nil || n.Type != "import_mesh" || n.Parameters["source"] != "part.stl" {
		t.Fatalf("node: %+v", n)
	}
	if !near(vol(t, a), 1000*1000) {
		t.Fatalf("volume %v: 10 cm cube is 1e6 mm³", vol(t, a))
	}

	// Combine with ordinary tools.
	mustOK(t, a.ApplyTool("create_box", importArgs(t, map[string]any{"name": "cutter", "dx": 50, "dy": 50, "dz": 200}), PolicyAuto))
	mustOK(t, a.ApplyTool("boolean", importArgs(t, map[string]any{"op": "subtract", "target": "part", "tool": "cutter"}), PolicyAuto))
	if !near(vol(t, a), 1000*1000-50*50*100) {
		t.Fatalf("volume after boolean %v", vol(t, a))
	}

	// Undo removes the node and the model goes back to empty.
	for range 2 {
		if _, err := a.Undo(); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := a.Undo(); err != nil {
		t.Fatal(err)
	}
	if a.Graph.Get("part") != nil {
		t.Fatal("undo should remove the imported body")
	}
	if _, err := a.Redo(); err != nil {
		t.Fatal(err)
	}
	if !near(vol(t, a), 1000*1000) {
		t.Fatal("redo should bring the mesh back")
	}
}

func TestImportSTLRefusesBadFilesAndLeavesTheModelAlone(t *testing.T) {
	a := New("scan", clock)
	defer a.Close()
	p := [][3]float32{{0, 0, 0}, {1, 0, 0}, {1, 1, 0}, {0, 1, 0}, {0.5, 0.5, 1}}
	open := stlOf(p, [][3]int{{0, 2, 1}, {0, 3, 2}, {0, 1, 4}, {1, 2, 4}, {2, 3, 4}}) // a pyramid without its last side
	for name, data := range map[string][]byte{
		"holes.stl":   open,
		"garbage.stl": []byte("definitely not a mesh, but long enough to not be truncated........................."),
		"empty.stl":   {},
	} {
		o := a.ApplyTool("import_stl", importArgs(t, map[string]any{"path": writeTemp(t, name, data)}), PolicyAuto)
		if o.OK {
			t.Errorf("%s: accepted", name)
		}
		if len(a.Graph.Nodes) != 0 {
			t.Fatalf("%s: the model changed", name)
		}
	}
	o := a.ApplyTool("import_stl", importArgs(t, map[string]any{"path": writeTemp(t, "holes.stl", open)}), PolicyAuto)
	if !strings.Contains(o.Error, "not watertight") {
		t.Fatalf("the user should be told why: %q", o.Error)
	}
	for name, args := range map[string]map[string]any{
		"both":       {"path": "a.stl", "mesh": "mesh_0123456789abcdef01234567"},
		"neither":    {},
		"not stl":    {"path": "/etc/passwd"},
		"outside":    {"path": "/etc/shadow.stl"},
		"unknown id": {"mesh": "mesh_0123456789abcdef01234567"},
	} {
		if o := a.ApplyTool("import_stl", importArgs(t, args), PolicyAuto); o.OK {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestImportedMeshSurvivesSaveAndOpen(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "scan.scadproj")
	a := New("scan", clock)
	mustOK(t, a.ApplyTool("import_stl", importArgs(t, map[string]any{"path": writeTemp(t, "part.stl", cubeSTL(10)), "placement": "base"}), PolicyAuto))
	// A mesh nothing references must not be written.
	if _, err := a.StoreMesh(cubeSTL(7), meshimport.Options{}); err != nil {
		t.Fatal(err)
	}
	if err := a.Save(dir); err != nil {
		t.Fatal(err)
	}
	a.Close()
	files, _ := filepath.Glob(filepath.Join(dir, "assets", "meshes", "*.stl"))
	if len(files) != 1 {
		t.Fatalf("expected exactly the referenced mesh on disk, got %v", files)
	}

	b, err := Open(dir, clock)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if !near(vol(t, b), 1000) {
		t.Fatalf("reopened volume %v", vol(t, b))
	}
	// A corrupted stored mesh is refused rather than rebuilt into something else.
	data, _ := os.ReadFile(files[0])
	data[100] ^= 0x40
	if err := os.WriteFile(files[0], data, 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := Open(dir, clock)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	m, err := c.Model()
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Failed()) != 1 || !strings.Contains(strings.Join(m.Nodes["part"].Errors, " "), "does not match its id") {
		t.Fatalf("tampered mesh: failed=%v errors=%v", m.Failed(), m.Nodes["part"].Errors)
	}
}

// What this build exports, it can import back: same volume, same size.
func TestExportedSTLImportsBackAsTheSameSolid(t *testing.T) {
	a := New("rt", clock)
	defer a.Close()
	mustOK(t, a.ApplyTool("create_box", importArgs(t, map[string]any{"id": "plate", "dx": 30, "dy": 20, "dz": 6}), PolicyAuto))
	mustOK(t, a.ApplyTool("add_hole", importArgs(t, map[string]any{"target": "plate", "diameter": 5, "positions": [][]float64{{10, 10}}}), PolicyAuto))
	want := vol(t, a)
	out := filepath.Join(t.TempDir(), "plate.stl")
	mustOK(t, a.ApplyTool("export_stl", importArgs(t, map[string]any{"path": out, "deflection_mm": 0.01}), PolicyAuto))

	b := New("rt2", clock)
	defer b.Close()
	mustOK(t, b.ApplyTool("import_stl", importArgs(t, map[string]any{"path": out}), PolicyAuto))
	// The hole is a tessellated cylinder, so the volume is the faceted one: within 0.5 %.
	if got := vol(t, b); math.Abs(got-want)/want > 0.005 {
		t.Fatalf("round trip volume %v, want %v", got, want)
	}
}

// A component built from an imported mesh carries the mesh with it: it is resolved from the
// component's own directory, not from the project that created it.
func TestComponentOfAnImportedMeshCarriesTheMesh(t *testing.T) {
	lib := t.TempDir()
	a := newInLibrary(t, "scan", lib)
	defer a.Close()
	mustOK(t, a.ApplyTool("import_stl", importArgs(t, map[string]any{"path": writeTemp(t, "knob.stl", cubeSTL(10))}), PolicyAuto))
	mustOK(t, a.ApplyTool("create_component", []byte(`{"name":"Knob","features":["knob"]}`), PolicyCommit))
	if files, _ := filepath.Glob(filepath.Join(lib, "*", "assets", "meshes", "*.stl")); len(files) != 1 {
		t.Fatalf("the component should own its mesh, found %v", files)
	}
	if !near(vol(t, a), 1000) {
		t.Fatalf("instance volume %v", vol(t, a))
	}
	mustOK(t, a.ApplyTool("insert_component", []byte(`{"component":"Knob","dx":50}`), PolicyCommit))
	if !near(vol(t, a), 2000) {
		t.Fatalf("two instances: volume %v", vol(t, a))
	}
}

// A cube whose bottom face is missing: refused, then imported when the holes are to be closed.
func TestImportSTLCanCloseHolesOnRequest(t *testing.T) {
	a := New("scan", clock)
	defer a.Close()
	p := [][3]float32{{0, 0, 0}, {10, 0, 0}, {10, 10, 0}, {0, 10, 0}, {0, 0, 10}, {10, 0, 10}, {10, 10, 10}, {0, 10, 10}}
	q := [][4]int{{4, 5, 6, 7}, {0, 1, 5, 4}, {1, 2, 6, 5}, {2, 3, 7, 6}, {3, 0, 4, 7}}
	var tris [][3]int
	for _, f := range q {
		tris = append(tris, [3]int{f[0], f[1], f[2]}, [3]int{f[0], f[2], f[3]})
	}
	path := writeTemp(t, "open.stl", stlOf(p, tris))

	o := a.ApplyTool("import_stl", importArgs(t, map[string]any{"path": path}), PolicyAuto)
	if o.OK || !strings.Contains(o.Error, "not watertight") || len(a.Graph.Nodes) != 0 {
		t.Fatalf("holes must be refused by default: %+v", o)
	}
	o = mustOK(t, a.ApplyTool("import_stl", importArgs(t, map[string]any{"path": path, "fill_holes": true}), PolicyAuto))
	if !o.Committed {
		t.Fatalf("a repaired import commits like any other: %+v", o)
	}
	if !near(vol(t, a), 1000) {
		t.Fatalf("closed cube volume %v", vol(t, a))
	}
	if !strings.Contains(o.ChangeSet.Explanation, "1 holes were closed") {
		t.Fatalf("the repair should be reported: %q", o.ChangeSet.Explanation)
	}
}

// gridCubeSTL is a cube whose faces are n x n quads: lots of triangles, all of them redundant.
func gridCubeSTL(s float32, n int) []byte {
	var p [][3]float32
	var tris [][3]int
	idx := map[[3]float32]int{}
	vert := func(v [3]float32) int {
		if i, ok := idx[v]; ok {
			return i
		}
		idx[v] = len(p)
		p = append(p, v)
		return len(p) - 1
	}
	face := func(o, du, dv [3]float32) {
		at := func(a, b int) [3]float32 {
			f, g := float32(a)/float32(n), float32(b)/float32(n)
			return [3]float32{o[0] + du[0]*f + dv[0]*g, o[1] + du[1]*f + dv[1]*g, o[2] + du[2]*f + dv[2]*g}
		}
		for i := 0; i < n; i++ {
			for j := 0; j < n; j++ {
				a, b, c, d := vert(at(i, j)), vert(at(i+1, j)), vert(at(i+1, j+1)), vert(at(i, j+1))
				tris = append(tris, [3]int{a, b, c}, [3]int{a, c, d})
			}
		}
	}
	x, y, z := [3]float32{s, 0, 0}, [3]float32{0, s, 0}, [3]float32{0, 0, s}
	face([3]float32{}, y, x)
	face(z, x, y)
	face([3]float32{}, x, z)
	face(y, z, x)
	face([3]float32{}, z, y)
	face(x, y, z)
	return stlOf(p, tris)
}

func TestDecimateMeshReallyRemovesFacesAndUndoRestoresThem(t *testing.T) {
	a := New("scan", clock)
	defer a.Close()
	path := writeTemp(t, "heavy.stl", gridCubeSTL(10, 16)) // 3072 triangles
	mustOK(t, a.ApplyTool("import_stl", importArgs(t, map[string]any{"path": path}), PolicyAuto))
	before := a.Graph.Get("heavy").Parameters["mesh"].(string)
	info, _ := a.MeshInfo(before)
	if info.Triangles != 3072 {
		t.Fatalf("imported %d triangles", info.Triangles)
	}

	o := mustOK(t, a.ApplyTool("decimate_mesh", importArgs(t, map[string]any{"feature": "heavy", "ratio": 0.05}), PolicyAuto))
	if !o.Committed || !strings.Contains(o.ChangeSet.Explanation, "Reduced heavy from 3072") {
		t.Fatalf("outcome: %+v", o)
	}
	after := a.Graph.Get("heavy").Parameters["mesh"].(string)
	if after == before {
		t.Fatal("the node should point at the reduced mesh")
	}
	if ai, _ := a.MeshInfo(after); ai.Triangles > 200 || ai.Triangles < 12 {
		t.Fatalf("reduced to %d triangles", ai.Triangles)
	}
	if !near(vol(t, a), 1000) {
		t.Fatalf("a flat solid keeps its volume: %v", vol(t, a))
	}
	m, _ := a.Model()
	if p := m.Nodes["heavy"].Body.Props; p.Faces != 6 {
		t.Fatalf("the cube should have its six faces, has %d", p.Faces)
	}

	if _, err := a.Undo(); err != nil {
		t.Fatal(err)
	}
	if a.Graph.Get("heavy").Parameters["mesh"] != before || !near(vol(t, a), 1000) {
		t.Fatal("undo should bring the full mesh back")
	}

	for name, args := range map[string]map[string]any{
		"neither":          {"feature": "heavy"},
		"more than it has": {"feature": "heavy", "target_triangles": 5000},
		"too few":          {"feature": "heavy", "target_triangles": 6},
		"not a mesh":       {"feature": "nothing", "ratio": 0.5},
	} {
		if o := a.ApplyTool("decimate_mesh", importArgs(t, args), PolicyAuto); o.OK {
			t.Errorf("%s: accepted", name)
		}
	}
	// Models send both fields, one of them zero or empty; the one that means something is used.
	for name, args := range map[string]map[string]any{
		"ratio and a zero count":   {"feature": "heavy", "ratio": 0.1, "target_triangles": 0},
		"count and a zero ratio":   {"feature": "heavy", "target_triangles": 300, "ratio": 0},
		"both set: the count wins": {"feature": "heavy", "target_triangles": 300, "ratio": 0.9},
	} {
		o := a.ApplyTool("decimate_mesh", importArgs(t, args), PolicyAuto)
		if !o.OK {
			t.Fatalf("%s: %s", name, o.Error)
		}
		if _, err := a.Undo(); err != nil {
			t.Fatal(err)
		}
	}
	mustOK(t, a.ApplyTool("create_box", importArgs(t, map[string]any{"id": "plate", "dx": 5, "dy": 5, "dz": 5}), PolicyAuto))
	if o := a.ApplyTool("decimate_mesh", importArgs(t, map[string]any{"feature": "plate", "ratio": 0.5}), PolicyAuto); o.OK || !strings.Contains(o.Error, "not an imported mesh") {
		t.Fatalf("a box has no triangles to reduce: %+v", o)
	}
}

// The same tolerance for import_stl: a model that fills `mesh` with "" while giving a path.
func TestImportSTLIgnoresEmptyFields(t *testing.T) {
	a := New("scan", clock)
	defer a.Close()
	path := writeTemp(t, "cube.stl", cubeSTL(10))
	mustOK(t, a.ApplyTool("import_stl", importArgs(t, map[string]any{"path": path, "mesh": "", "units": "", "placement": "", "decimate_to": 0, "fill_holes": false}), PolicyAuto))
	if !near(vol(t, a), 1000) {
		t.Fatalf("volume %v", vol(t, a))
	}
}

func TestImportSTLCanReduceWhileImporting(t *testing.T) {
	a := New("scan", clock)
	defer a.Close()
	path := writeTemp(t, "heavy.stl", gridCubeSTL(10, 16))
	o := mustOK(t, a.ApplyTool("import_stl", importArgs(t, map[string]any{"path": path, "decimate_to": 150}), PolicyAuto))
	if !strings.Contains(o.ChangeSet.Explanation, "Reduced from 3072") {
		t.Fatalf("the reduction should be reported: %q", o.ChangeSet.Explanation)
	}
	id := a.Graph.Get("heavy").Parameters["mesh"].(string)
	if info, _ := a.MeshInfo(id); info.Triangles > 170 {
		t.Fatalf("%d triangles", info.Triangles)
	}
	if !near(vol(t, a), 1000) {
		t.Fatalf("volume %v", vol(t, a))
	}
}
