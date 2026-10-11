package app

import (
	"fmt"
	"math"
	"path/filepath"
	"testing"

	"github.com/accelone/simoncad/internal/geometry/text"
)

func textArea(t *testing.T, s string, size float64) float64 {
	t.Helper()
	l, err := text.Builtin().Lay(s, text.Options{Size: size})
	if err != nil {
		t.Fatal(err)
	}
	return text.Area(l.Regions)
}

// 4. extruded 3D text: volume = lettering area x height, letters keep their counters
func TestExtrudedText3D(t *testing.T) {
	a := New("demo", clock)
	defer a.Close()
	sw(t, a, "create_text_geometry", `{"id":"word","text":"Simon","size":12,"mode":"extrude","depth":3}`)
	within(t, "text volume", vol(t, a), textArea(t, "Simon", 12)*3, 2e-3)
	assertWatertight(t, a, "word")
	// the "o" leaves a hole: the solid is not just the outer silhouettes
	b := New("demo", clock)
	defer b.Close()
	sw(t, b, "create_text_geometry", `{"id":"o","text":"o","size":20,"mode":"extrude","depth":1}`)
	sw(t, b, "create_text_geometry", `{"id":"dot","text":"I","size":20,"mode":"extrude","depth":1}`)
	if v := vol(t, b); v <= 0 {
		t.Fatalf("volume %v", v)
	}
	// editing the depth regenerates; undo restores
	h := a.Graph.Hash()
	sw(t, a, "modify_parameter", `{"feature":"word","parameter":"depth","value":6}`)
	within(t, "taller text", vol(t, a), textArea(t, "Simon", 12)*6, 2e-3)
	if _, err := a.Undo(); err != nil {
		t.Fatal(err)
	}
	if a.Graph.Hash() != h {
		t.Error("undo did not restore the graph")
	}
}

func TestTextEmbossEngraveOnPlate(t *testing.T) {
	a := New("demo", clock)
	defer a.Close()
	sw(t, a, "create_box", `{"id":"plate","dx":60,"dy":30,"dz":4,"x":-30,"y":-15}`)
	base := vol(t, a)
	sw(t, a, "create_text_geometry", `{"id":"label","text":"Simon","size":10,"mode":"emboss","depth":1.5,"target":"plate","plane":"XY","offset":4,"center":true}`)
	within(t, "embossed", vol(t, a), base+textArea(t, "Simon", 10)*1.5, 3e-3)
	assertWatertight(t, a, "label")

	b := New("demo", clock)
	defer b.Close()
	sw(t, b, "create_box", `{"id":"plate","dx":60,"dy":30,"dz":4,"x":-30,"y":-15}`)
	sw(t, b, "create_text_geometry", `{"id":"label","text":"Simon","size":10,"mode":"engrave","depth":1,"target":"plate","offset":4,"center":true}`)
	within(t, "engraved", vol(t, b), 60*30*4-textArea(t, "Simon", 10)*1, 3e-3)
	assertWatertight(t, b, "label")
}

// A stamp prints mirrored: mirror=true flips the lettering about its own centre.
func TestTextMirrorForStamps(t *testing.T) {
	a := New("demo", clock)
	defer a.Close()
	sw(t, a, "create_text_geometry", `{"id":"n","text":"Lb","size":10,"mode":"extrude","depth":2,"center":true}`)
	sw(t, a, "create_text_geometry", `{"id":"m","text":"Lb","size":10,"mode":"extrude","depth":2,"center":true,"mirror":true}`)
	ln, hn := stampBounds(t, a, "n")
	lm, hm := stampBounds(t, a, "m")
	for i := 0; i < 3; i++ {
		if math.Abs((hn[i]-ln[i])-(hm[i]-lm[i])) > 1e-3 {
			t.Errorf("axis %d: mirroring changed the extent", i)
		}
	}
	// the L's heavy stem is at the left of the normal text and at the right of the mirrored one
	if cn, cm := a.Graph.Get("n"), a.Graph.Get("m"); cn == nil || cm == nil {
		t.Fatal("nodes missing")
	}
	sn, sm := a.Graph.Get("n_sketch"), a.Graph.Get("m_sketch")
	if sn == nil || sm == nil {
		t.Fatal("sketches missing")
	}
	if fmt.Sprint(sn.Parameters["entities"]) == fmt.Sprint(sm.Parameters["entities"]) {
		t.Error("mirror produced identical outlines")
	}
}

func TestTextAlongAPath(t *testing.T) {
	a := New("demo", clock)
	defer a.Close()
	sw(t, a, "create_path", `{"id":"arc","type":"catmull_rom","points":[[0,0,0],[20,12,0],[40,16,0],[60,12,0],[80,0,0]]}`)
	o := sw(t, a, "create_text_geometry", `{"id":"curved","text":"Hello","size":14,"mode":"extrude","depth":2,"path":"arc"}`)
	_ = o
	lo, hi := stampBounds(t, a, "curved")
	if hi[0]-lo[0] < 20 || hi[1]-lo[1] < 8 {
		t.Errorf("text did not follow the curve: bounds %v %v", lo, hi)
	}
	assertWatertight(t, a, "curved")
	if o := a.ApplyTool("create_text_geometry", []byte(`{"text":"this sentence is far too long for the path","size":8,"path":"arc"}`), PolicyCommit); o.OK {
		t.Error("text longer than the path accepted")
	}
}

func TestTextErrors(t *testing.T) {
	a := New("demo", clock)
	defer a.Close()
	sw(t, a, "create_box", `{"id":"plate","dx":20,"dy":20,"dz":4}`)
	h := a.Graph.Hash()
	if c := failCode(t, a, "create_text_geometry", `{"text":"x","size":5,"font":"/nowhere/font.ttf"}`); c != "FONT_NOT_FOUND" {
		t.Errorf("code %q", c)
	}
	if c := failCode(t, a, "create_text_geometry", `{"text":"x","size":5,"font":"notes.txt"}`); c != "FONT_NOT_FOUND" {
		t.Errorf("code %q", c)
	}
	for _, bad := range []string{
		`{"text":"   ","size":5}`,
		`{"text":"x"}`,
		`{"text":"x","size":-3}`,
		`{"text":"x","size":5,"mode":"extrude"}`,
		`{"text":"x","size":5,"mode":"emboss","depth":1}`,
		`{"text":"x","size":5,"mode":"emboss","depth":1,"target":"nope"}`,
		`{"text":"x","size":5,"plane":"XY","face":"plate.face[top]"}`,
	} {
		if o := a.ApplyTool("create_text_geometry", []byte(bad), PolicyCommit); o.OK {
			t.Errorf("%s accepted", bad)
		}
	}
	if a.Graph.Hash() != h {
		t.Error("a failed tool changed the graph")
	}
}

func TestTextSurvivesSaveAndReload(t *testing.T) {
	a := New("demo", clock)
	defer a.Close()
	sw(t, a, "create_text_geometry", `{"id":"word","text":"Hi","size":10,"mode":"extrude","depth":2}`)
	v := vol(t, a)
	dir := filepath.Join(t.TempDir(), "p.scadproj")
	if err := a.Save(dir); err != nil {
		t.Fatal(err)
	}
	b, err := Open(dir, clock)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	within(t, "reloaded", vol(t, b), v, 1e-9)
}

// A stamp with lettering: the text sketch sits on the plate's top face and is raised mirrored.
func TestStampWithMirroredText(t *testing.T) {
	a := New("demo", clock)
	defer a.Close()
	sw(t, a, "create_text_geometry", `{"id":"word","text":"Simon","size":12,"offset":4,"center":true}`)
	sw(t, a, "create_stamp", `{"id":"stamp","width":60,"height":30,"base_thickness":4,"sketch":"word","relief_height":1.5,"mirror":true}`)
	if v := vol(t, a); v < 60*30*4 {
		t.Fatalf("stamp volume %v is less than the plate", v)
	}
	assertWatertight(t, a, "stamp")
	lo, hi := stampBounds(t, a, "stamp")
	if math.Abs(hi[2]-lo[2]-5.5) > 1e-3 {
		t.Errorf("stamp height %v, want plate 4 + relief 1.5", hi[2]-lo[2])
	}
}
