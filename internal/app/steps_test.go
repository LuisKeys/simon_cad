package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// plateSTEP exports a 30 x 20 x 6 plate with a 5 mm hole as a STEP file and returns its path and
// volume: a body with real planes and a real cylinder.
func plateSTEP(t *testing.T) (string, float64) {
	t.Helper()
	a := New("src", clock)
	defer a.Close()
	mustOK(t, a.ApplyTool("create_box", importArgs(t, map[string]any{"id": "plate", "dx": 30, "dy": 20, "dz": 6}), PolicyAuto))
	mustOK(t, a.ApplyTool("add_hole", importArgs(t, map[string]any{"target": "plate", "diameter": 5, "positions": [][]float64{{10, 10}}}), PolicyAuto))
	out := filepath.Join(t.TempDir(), "plate.step")
	mustOK(t, a.ApplyTool("export_step", importArgs(t, map[string]any{"path": out}), PolicyAuto))
	return out, vol(t, a)
}

func TestImportSTEPKeepsTheExactSolid(t *testing.T) {
	path, want := plateSTEP(t)
	a := New("dst", clock)
	defer a.Close()
	mustOK(t, a.ApplyTool("import_step", importArgs(t, map[string]any{"path": path, "placement": "base"}), PolicyAuto))
	n := a.Graph.Get("plate")
	if n == nil || n.Type != "import_step" || n.Parameters["source"] != "plate.step" {
		t.Fatalf("node: %+v", n)
	}
	if got := vol(t, a); !near(got, want) {
		t.Fatalf("volume %v, want %v (exact: no tessellation error)", got, want)
	}
	m, err := a.Model()
	if err != nil {
		t.Fatal(err)
	}
	p, err := m.Nodes["plate"].Body.Shape.Props()
	if err != nil {
		t.Fatal(err)
	}
	if p.MinZ != 0 || p.MinX+p.MaxX != 0 || p.MinY+p.MaxY != 0 {
		t.Fatalf("placement base should centre X/Y and rest on Z=0: %+v", p)
	}

	// Exact geometry: ordinary editing tools work on it, a fillet of every edge removes volume.
	mustOK(t, a.ApplyTool("fillet", importArgs(t, map[string]any{"target": "plate", "radius": 1}), PolicyAuto))
	if got := vol(t, a); got >= want {
		t.Fatalf("fillet should remove material: %v >= %v", got, want)
	}
	// And so does the usual undo/redo.
	if _, err := a.Undo(); err != nil {
		t.Fatal(err)
	}
	if !near(vol(t, a), want) {
		t.Fatal("undo should restore the imported solid")
	}
}

func TestImportSTEPConvertsTheFilesUnits(t *testing.T) {
	path, want := plateSTEP(t)
	b, _ := os.ReadFile(path)
	if !strings.Contains(string(b), "SI_UNIT(.MILLI.,.METRE.)") {
		t.Skip("exported file does not declare millimetres this way")
	}
	cm := writeTemp(t, "plate_cm.step", []byte(strings.Replace(string(b), "SI_UNIT(.MILLI.,.METRE.)", "SI_UNIT(.CENTI.,.METRE.)", 1)))
	a := New("dst", clock)
	defer a.Close()
	mustOK(t, a.ApplyTool("import_step", importArgs(t, map[string]any{"path": cm}), PolicyAuto))
	if got := vol(t, a); !near(got, want*1000) {
		t.Fatalf("a file in centimetres should be 10x larger: volume %v, want %v", got, want*1000)
	}
}

func TestImportSTEPRefusesBadFilesAndLeavesTheModelAlone(t *testing.T) {
	good, _ := plateSTEP(t)
	data, _ := os.ReadFile(good)

	// A STEP holding one face and no solid.
	src := New("src", clock)
	defer src.Close()
	box, err := src.K.Box(10, 10, 10)
	if err != nil {
		t.Fatal(err)
	}
	face, err := box.Face(1)
	if err != nil {
		t.Fatal(err)
	}
	faceOnly := filepath.Join(t.TempDir(), "face.step")
	if err := face.ExportSTEP(faceOnly); err != nil {
		t.Fatal(err)
	}
	faceData, _ := os.ReadFile(faceOnly)

	a := New("dst", clock)
	defer a.Close()
	for name, c := range map[string]struct {
		data []byte
		want string
	}{
		"empty.step":   {nil, "empty"},
		"text.step":    {[]byte("this is not a STEP file at all"), "not a STEP file"},
		"cut.step":     {data[:len(data)/2], ""},
		"surface.step": {faceData, "no solid"},
	} {
		o := a.ApplyTool("import_step", importArgs(t, map[string]any{"path": writeTemp(t, name, c.data)}), PolicyAuto)
		if o.OK || !strings.Contains(o.Error, c.want) {
			t.Errorf("%s: ok=%v error=%q, want a refusal mentioning %q", name, o.OK, o.Error, c.want)
		}
		if len(a.Graph.Nodes) != 0 {
			t.Fatalf("%s: the model changed", name)
		}
	}
	if o := a.ApplyTool("import_step", importArgs(t, map[string]any{"path": writeTemp(t, "plate.stl", data)}), PolicyAuto); o.OK || !strings.Contains(o.Error, ".step") {
		t.Errorf("wrong extension should be refused: %+v", o)
	}
	if o := a.ApplyTool("import_step", importArgs(t, map[string]any{}), PolicyAuto); o.OK {
		t.Error("neither path nor step should be refused")
	}
	// Empty fields count as absent.
	mustOK(t, a.ApplyTool("import_step", importArgs(t, map[string]any{"path": good, "step": "", "placement": "", "filename": ""}), PolicyAuto))
}

func TestImportedSTEPSurvivesSaveAndOpen(t *testing.T) {
	path, want := plateSTEP(t)
	dir := filepath.Join(t.TempDir(), "p.scadproj")
	a := New("p", clock)
	mustOK(t, a.ApplyTool("import_step", importArgs(t, map[string]any{"path": path}), PolicyAuto))
	// A file nothing references must not be written.
	other, _ := os.ReadFile(writeTemp(t, "x.step", mustRead(t, path)))
	if _, err := a.StoreStep(append(other[:len(other):len(other)], []byte("\n")...)); err != nil {
		t.Fatal(err)
	}
	if err := a.Save(dir); err != nil {
		t.Fatal(err)
	}
	a.Close()
	files, _ := filepath.Glob(filepath.Join(dir, "assets", "steps", "*.step"))
	if len(files) != 1 {
		t.Fatalf("expected exactly the referenced file on disk, got %v", files)
	}

	b, err := Open(dir, clock)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if !near(vol(t, b), want) {
		t.Fatalf("reopened volume %v, want %v", vol(t, b), want)
	}
	// A modified stored file is refused rather than rebuilt into something else.
	data := mustRead(t, files[0])
	data = append(data, '\n')
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
	if len(m.Failed()) != 1 || !strings.Contains(strings.Join(m.Nodes["plate"].Errors, " "), "has been modified") {
		t.Fatalf("modified file: failed=%v errors=%v", m.Failed(), m.Nodes["plate"].Errors)
	}
}

func TestComponentOfAnImportedSTEPCarriesTheFile(t *testing.T) {
	path, want := plateSTEP(t)
	lib := t.TempDir()
	a := newInLibrary(t, "p", lib)
	defer a.Close()
	mustOK(t, a.ApplyTool("import_step", importArgs(t, map[string]any{"path": path}), PolicyAuto))
	mustOK(t, a.ApplyTool("create_component", []byte(`{"name":"Plate","features":["plate"]}`), PolicyCommit))
	if files, _ := filepath.Glob(filepath.Join(lib, "*", "assets", "steps", "*.step")); len(files) != 1 {
		t.Fatalf("the component should own its STEP file, found %v", files)
	}
	mustOK(t, a.ApplyTool("insert_component", []byte(`{"component":"Plate","dx":100}`), PolicyCommit))
	if !near(vol(t, a), 2*want) {
		t.Fatalf("two instances: volume %v, want %v", vol(t, a), 2*want)
	}
}

func mustRead(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
