package app

import (
	"math"
	"path/filepath"
	"testing"
)

const plateSketch = `{"id":"sk","plane":"XY","entities":[
 {"id":"e1","kind":"rectangle","x":0,"y":0,"w":80,"h":50},
 {"id":"e2","kind":"circle","cx":20,"cy":25,"r":3},
 {"id":"e3","kind":"circle","cx":60,"cy":25,"r":3}],
 "regions":[{"id":"r1","outer":["e1"],"holes":[["e2"],["e3"]]}]}`

func TestSketchToolsPlateWithHoles(t *testing.T) {
	a := New("demo", clock)
	defer a.Close()
	mustOK(t, a.ApplyTool("create_sketch", []byte(plateSketch), PolicyCommit))
	if v := vol(t, a); v != 0 {
		t.Fatalf("a sketch alone has no volume, got %v", v)
	}
	mustOK(t, a.ApplyTool("extrude_sketch", []byte(`{"id":"plate","sketch":"sk","depth":5}`), PolicyCommit))
	want := (80*50 - 2*math.Pi*9) * 5
	if v := vol(t, a); !near(v, want) {
		t.Fatalf("volume %v want %v", v, want)
	}

	// Argument errors are normal tool errors and never touch the model.
	h := a.Graph.Hash()
	for _, bad := range []struct{ tool, args string }{
		{"extrude_sketch", `{"sketch":"nope","depth":5}`},
		{"extrude_sketch", `{"sketch":"sk"}`},
		{"extrude_sketch", `{"sketch":"sk","depth":5,"region":"zz"}`},
		{"extrude_sketch", `{"sketch":"plate","depth":5}`},
		{"create_sketch", `{"entities":[{"id":"l","kind":"line","p1":[0,0],"p2":[1,0]}],"regions":[{"id":"r","outer":["l"]}]}`},
		{"create_sketch", `{"entities":[{"id":"c","kind":"circle","cx":0,"cy":0,"r":-1}]}`},
	} {
		if o := a.ApplyTool(bad.tool, []byte(bad.args), PolicyCommit); o.OK {
			t.Errorf("%s %s should fail", bad.tool, bad.args)
		}
	}
	if a.Graph.Hash() != h {
		t.Fatal("rejected calls mutated the model")
	}
}

func TestSketchUndoRedoSaveLoadWithoutVision(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "p.scadproj")
	a := New("demo", clock)
	a.SetActor(Actor{CreatedBy: "agent", Capability: "vision", Model: "recorded"})
	mustOK(t, a.ApplyTool("create_sketch", []byte(plateSketch), PolicyCommit))
	h0 := a.Graph.Hash()
	mustOK(t, a.ApplyTool("extrude_sketch", []byte(`{"id":"plate","sketch":"sk","depth":5}`), PolicyCommit))
	h1 := a.Graph.Hash()
	v1 := vol(t, a)

	if _, err := a.Undo(); err != nil {
		t.Fatal(err)
	}
	if a.Graph.Hash() != h0 || vol(t, a) != 0 {
		t.Fatal("undo did not restore the sketch-only state")
	}
	if _, err := a.Redo(); err != nil {
		t.Fatal(err)
	}
	if a.Graph.Hash() != h1 || !near(vol(t, a), v1) {
		t.Fatal("redo did not restore the extruded state")
	}
	if err := a.Save(dir); err != nil {
		t.Fatal(err)
	}
	a.Close()

	// Reopen: geometry rebuilds from the stored CAD-IR alone (no provider exists in this package).
	b, err := Open(dir, clock)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if !near(vol(t, b), v1) {
		t.Fatalf("reloaded volume %v want %v", vol(t, b), v1)
	}
	_, match, n, err := b.Replay()
	if err != nil || !match || n != 2 {
		t.Fatalf("replay: match=%v n=%d err=%v", match, n, err)
	}
	if p := b.Graph.Get("sk").Provenance; p.CreatedBy != "agent" || p.Capability != "vision" {
		t.Fatalf("provenance lost: %+v", p)
	}
}

func TestSketchOnSelectedFaceCut(t *testing.T) {
	a := New("demo", clock)
	defer a.Close()
	mustOK(t, a.ApplyTool("create_box", []byte(`{"id":"plate","dx":100,"dy":60,"dz":10}`), PolicyCommit))
	mustOK(t, a.ApplyTool("create_sketch", []byte(`{"id":"sk","face":"face(feature=plate,side=+Z)",
	  "entities":[{"id":"c","kind":"circle","cx":0,"cy":0,"r":5}]}`), PolicyCommit))
	mustOK(t, a.ApplyTool("extrude_sketch", []byte(`{"id":"pocket","sketch":"sk","target":"plate","mode":"cut","depth":4}`), PolicyCommit))
	want := 100.0*60*10 - math.Pi*25*4
	if v := vol(t, a); !near(v, want) {
		t.Fatalf("pocket volume %v want %v", v, want)
	}
	// through cut from the face
	mustOK(t, a.ApplyTool("modify_parameter", []byte(`{"feature":"pocket","parameter":"depth","value":10}`), PolicyCommit))
	if v := vol(t, a); !near(v, 100.0*60*10-math.Pi*25*10) {
		t.Fatalf("deeper pocket volume %v", v)
	}
	// non-planar / unknown faces are refused
	if o := a.ApplyTool("create_sketch", []byte(`{"face":"face(feature=ghost,side=+Z)","entities":[{"id":"c","kind":"circle","cx":0,"cy":0,"r":5}]}`), PolicyCommit); o.OK {
		t.Fatal("unknown feature face must fail")
	}
}

func TestSketchBooleanChainUndoRedoSaveLoad(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "p.scadproj")
	a := New("demo", clock)
	steps := []struct{ tool, args string }{
		{"create_sketch", `{"id":"ska","entities":[{"id":"a","kind":"rectangle","x":0,"y":0,"w":40,"h":20}]}`},
		{"extrude_sketch", `{"id":"base","sketch":"ska","depth":5}`},
		{"create_sketch", `{"id":"skb","entities":[{"id":"b","kind":"rectangle","x":30,"y":0,"w":20,"h":20}]}`},
		{"extrude_sketch", `{"id":"tab","sketch":"skb","depth":5}`},
		{"boolean", `{"op":"union","target":"base","tool":"tab","id":"joined"}`},
		{"create_sketch", `{"id":"skc","entities":[{"id":"c","kind":"circle","cx":10,"cy":10,"r":3},{"id":"d","kind":"slot","x1":30,"y1":10,"x2":40,"y2":10,"width":4}]}`},
		{"extrude_sketch", `{"id":"cutter","sketch":"skc","depth":5,"through":false}`},
		{"boolean", `{"op":"subtract","target":"joined","tool":"cutter","id":"final"}`},
	}
	var hashes []string
	for _, s := range steps {
		mustOK(t, a.ApplyTool(s.tool, []byte(s.args), PolicyCommit))
		hashes = append(hashes, a.Graph.Hash())
	}
	want := (40.0*20+20*20-10.0*20/2*0-0)*5 - (math.Pi*9+10*4+math.Pi*4)*5
	// base 40x20 and tab 20x20 overlap on x∈[30,40] (10x20): union area 40*20+20*20-10*20
	want = (40.0*20+20*20-10*20)*5 - (math.Pi*9+10*4+math.Pi*4)*5
	if v := vol(t, a); !near(v, want) {
		t.Fatalf("final volume %v want %v", v, want)
	}
	final := vol(t, a)
	for i := len(steps) - 2; i >= 0; i-- {
		if _, err := a.Undo(); err != nil {
			t.Fatal(err)
		}
		if a.Graph.Hash() != hashes[i] {
			t.Fatalf("undo step %d: hash mismatch", i)
		}
	}
	for i := 1; i < len(steps); i++ {
		if _, err := a.Redo(); err != nil {
			t.Fatal(err)
		}
		if a.Graph.Hash() != hashes[i] {
			t.Fatalf("redo step %d: hash mismatch", i)
		}
	}
	if !near(vol(t, a), final) {
		t.Fatal("volume after redo chain")
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
	if !near(vol(t, b), final) {
		t.Fatalf("reloaded %v want %v", vol(t, b), final)
	}
	if _, match, n, err := b.Replay(); err != nil || !match || n != len(steps) {
		t.Fatalf("replay match=%v n=%d err=%v", match, n, err)
	}
}
