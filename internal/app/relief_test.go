package app

import (
	"fmt"
	"math"
	"path/filepath"
	"testing"
)

const plateBox = `{"id":"plate","dx":40,"dy":40,"dz":4,"x":-20,"y":-20}`

func faceCentroidX(t *testing.T, a *App, id string, z float64) (float64, bool) {
	t.Helper()
	m, err := a.Model()
	if err != nil {
		t.Fatal(err)
	}
	topo, ok := m.Topology(id)
	if !ok {
		t.Fatalf("no topology for %s", id)
	}
	for _, f := range topo.Faces {
		if f.Normal[2] > 0.99 && math.Abs(f.Centroid[2]-z) < 1e-3 && f.Area < 1000 {
			return f.Centroid[0], true
		}
	}
	return 0, false
}

func TestReliefEmbossAndDebossFromSketches(t *testing.T) {
	a := New("demo", clock)
	defer a.Close()
	sw(t, a, "create_box", plateBox)
	// a sketch on the top plane of the plate
	sw(t, a, "create_sketch", `{"id":"mark","plane":"XY","offset":4,"entities":[{"id":"r","kind":"rectangle","x":-5,"y":-5,"w":10,"h":6},{"id":"c","kind":"circle","cx":12,"cy":-10,"r":3}]}`)
	base := vol(t, a)
	sw(t, a, "create_relief", `{"id":"raised","source":"mark","target":"plate","op":"emboss","depth":1.5}`)
	within(t, "embossed volume", vol(t, a), base+(60+math.Pi*9)*1.5, 1e-4)
	assertWatertight(t, a, "raised")

	// deboss: cut the same shapes 2 mm into a fresh plate
	b := New("demo", clock)
	defer b.Close()
	sw(t, b, "create_box", plateBox)
	sw(t, b, "create_sketch", `{"id":"mark","plane":"XY","offset":4,"entities":[{"id":"r","kind":"rectangle","x":-5,"y":-5,"w":10,"h":6},{"id":"c","kind":"circle","cx":12,"cy":-10,"r":3}]}`)
	sw(t, b, "create_relief", `{"id":"cut","source":"mark","target":"plate","op":"deboss","depth":2}`)
	within(t, "debossed volume", vol(t, b), 40*40*4-(60+math.Pi*9)*2, 1e-4)
	assertWatertight(t, b, "cut")

	// editing the sketch regenerates the relief (the source stays editable)
	sw(t, a, "modify_parameter", `{"feature":"raised","parameter":"depth","value":3}`)
	within(t, "taller relief", vol(t, a), base+(60+math.Pi*9)*3, 1e-4)
	if _, err := a.Undo(); err != nil {
		t.Fatal(err)
	}
	within(t, "undo", vol(t, a), base+(60+math.Pi*9)*1.5, 1e-4)
}

// For a stamp the relief must read mirrored: the sketch is flipped about its vertical axis.
func TestReliefMirrorFlipsTheDesign(t *testing.T) {
	a := New("demo", clock)
	defer a.Close()
	sw(t, a, "create_box", plateBox)
	// an L far to the right of the origin
	sw(t, a, "create_sketch", `{"id":"ell","plane":"XY","offset":4,"entities":[{"id":"p","kind":"polygon","points":[[2,-6],[12,-6],[12,-3],[5,-3],[5,6],[2,6]]}]}`)
	sw(t, a, "create_relief", `{"id":"plain","source":"ell","target":"plate","op":"emboss","depth":1}`)
	x, ok := faceCentroidX(t, a, "plain", 5)
	if !ok || x <= 0 {
		t.Fatalf("unmirrored relief should sit at +x, got %v %v", x, ok)
	}
	b := New("demo", clock)
	defer b.Close()
	sw(t, b, "create_box", plateBox)
	sw(t, b, "create_sketch", `{"id":"ell","plane":"XY","offset":4,"entities":[{"id":"p","kind":"polygon","points":[[2,-6],[12,-6],[12,-3],[5,-3],[5,6],[2,6]]}]}`)
	sw(t, b, "create_relief", `{"id":"flipped","source":"ell","target":"plate","op":"emboss","depth":1,"mirror":true}`)
	xm, ok := faceCentroidX(t, b, "flipped", 5)
	if !ok || math.Abs(xm+x) > 1e-6 {
		t.Fatalf("mirrored centroid %v want %v", xm, -x)
	}
	within(t, "same volume", vol(t, b), vol(t, a), 1e-9)
}

func TestReliefErrorsAreCodedAndRecoverable(t *testing.T) {
	a := New("demo", clock)
	defer a.Close()
	sw(t, a, "create_box", plateBox)
	// floating: the sketch is 10 mm above the plate
	sw(t, a, "create_sketch", `{"id":"high","plane":"XY","offset":14,"entities":[{"id":"r","kind":"rectangle","x":-5,"y":-5,"w":10,"h":6}]}`)
	if c := failCode(t, a, "create_relief", `{"source":"high","target":"plate","op":"emboss","depth":1}`); c != "RELIEF_DISCONNECTED" {
		t.Fatalf("code %s", c)
	}
	// recovery: lower the sketch plane to the surface
	sw(t, a, "modify_parameter", `{"feature":"high","parameter":"offset","value":4}`)
	sw(t, a, "create_relief", `{"id":"ok","source":"high","target":"plate","op":"emboss","depth":1}`)

	b := New("demo", clock)
	defer b.Close()
	sw(t, b, "create_box", plateBox)
	sw(t, b, "create_sketch", `{"id":"away","plane":"XY","offset":30,"entities":[{"id":"r","kind":"rectangle","x":-5,"y":-5,"w":10,"h":6}]}`)
	if c := failCode(t, b, "create_relief", `{"source":"away","target":"plate","op":"deboss","depth":1}`); c != "RELIEF_NO_EFFECT" {
		t.Fatalf("code %s", c)
	}
	// engraving a whole plate through is not a relief
	sw(t, b, "create_sketch", `{"id":"big","plane":"XY","offset":4,"entities":[{"id":"r","kind":"rectangle","x":-30,"y":-30,"w":60,"h":60}]}`)
	failCode(t, b, "create_relief", `{"source":"big","target":"plate","op":"deboss","depth":10}`)
	h := b.Graph.Hash()
	for _, bad := range []string{
		`{"source":"plate","target":"plate","op":"emboss","depth":1}`,
		`{"source":"away","target":"nope","op":"emboss","depth":1}`,
		`{"source":"away","target":"plate","op":"emboss"}`,
		`{"source":"away","target":"plate","op":"smear","depth":1}`,
	} {
		if o := b.ApplyTool("create_relief", []byte(bad), PolicyCommit); o.OK {
			t.Errorf("%s accepted", bad)
		}
	}
	sw(t, b, "create_path", `{"id":"line","points":[[0,0,4],[10,0,4]]}`)
	if o := b.ApplyTool("create_relief", []byte(`{"source":"line","target":"plate","op":"engrave","depth":1}`), PolicyCommit); o.OK {
		t.Error("a path has no volume to cut with")
	}
	_ = h
}

// 8. engrave a path into an existing solid: groove volume = profile area x path length
func TestEngraveSurfaceAlongPaths(t *testing.T) {
	cases := []struct {
		style string
		area  float64 // groove cross-section area for width 2, depth 1 (round: half circle)
	}{{"round", math.Pi / 2}, {"v", 1.0}, {"flat", 2.0}}
	for _, c := range cases {
		a := New("demo", clock)
		sw(t, a, "create_box", plateBox)
		base := vol(t, a)
		sw(t, a, "engrave_surface", fmt.Sprintf(`{"id":"groove","target":"plate","points":[[-15,0,4],[15,0,4]],"path_type":"linear","width":2,"depth":1,"style":%q}`, c.style))
		within(t, c.style+" groove volume", base-vol(t, a), c.area*30, 0.01)
		assertWatertight(t, a, "groove")
		a.Close()
	}
	// a curved groove: volume = area x length of the curve
	a := New("demo", clock)
	defer a.Close()
	sw(t, a, "create_box", plateBox)
	base := vol(t, a)
	sw(t, a, "engrave_surface", `{"id":"wave","target":"plate","points":[[-16,-8,4],[-6,8,4],[6,-8,4],[16,8,4]],"path_type":"catmull_rom","width":1.6,"depth":0.8,"style":"flat"}`)
	L := pathLength(t, a, "wave_path")
	within(t, "curved groove volume", base-vol(t, a), 1.6*0.8*L, 0.02)
	assertWatertight(t, a, "wave")
	// a raised bead on the same surface
	b := New("demo", clock)
	defer b.Close()
	sw(t, b, "create_box", plateBox)
	vb := vol(t, b)
	sw(t, b, "engrave_surface", `{"id":"bead","op":"emboss","target":"plate","points":[[-15,0,4],[15,0,4]],"path_type":"linear","width":2,"style":"round"}`)
	within(t, "bead volume", vol(t, b)-vb, math.Pi/2*30, 0.01)
	assertWatertight(t, b, "bead")
}

// 10. the path of an engraving can be edited and the groove follows; it survives save/load
func TestEngravingFollowsItsPathAndReloads(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "p.scadproj")
	a := New("demo", clock)
	sw(t, a, "create_box", plateBox)
	sw(t, a, "engrave_surface", `{"id":"g","target":"plate","points":[[-10,0,4],[10,0,4]],"path_type":"linear","width":2,"depth":1,"style":"flat"}`)
	v1 := vol(t, a)
	sw(t, a, "update_path", `{"path":"g_path","ops":[{"op":"move","index":1,"position":[18,0,4]}]}`)
	v2 := vol(t, a)
	if !(v2 < v1) {
		t.Fatalf("a longer groove removes more material: %v -> %v", v1, v2)
	}
	within(t, "longer groove", 40*40*4-v2, 2*1*28, 0.01)
	if _, err := a.Undo(); err != nil || !near(vol(t, a), v1) {
		t.Fatalf("undo: %v", err)
	}
	sw(t, a, "update_path", `{"path":"g_path","ops":[{"op":"move","index":1,"position":[18,0,4]}]}`)
	if err := a.Save(dir); err != nil {
		t.Fatal(err)
	}
	a.Close()
	b, err := Open(dir, clock)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if !near(vol(t, b), v2) {
		t.Fatal("reloaded engraving differs")
	}
	// the profile is reusable: widen the groove through the profile
	sw(t, b, "update_profile", `{"profile":"g_profile","shape":"custom","contours":[{"points":[[-2,-0.3],[-2,1],[2,1],[2,-0.3]]}]}`)
	within(t, "wider groove", 40*40*4-vol(t, b), 4*1*28, 0.01)
}
