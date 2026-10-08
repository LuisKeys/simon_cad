package api

import (
	"math"
	"runtime"
	"testing"
)

func TestBoxSubtractCylinderVolume(t *testing.T) {
	k := New()
	defer k.Close()

	box, err := k.Box(80, 60, 4)
	if err != nil {
		t.Fatal(err)
	}
	cyl, err := k.Cylinder(1.6, 10)
	if err != nil {
		t.Fatal(err)
	}
	cut, err := k.Subtract(box, cyl)
	if err != nil {
		t.Fatal(err)
	}
	ok, err := cut.IsValid()
	if err != nil || !ok {
		t.Fatalf("invalid solid: %v %v", ok, err)
	}
	p, err := cut.Props()
	if err != nil {
		t.Fatal(err)
	}
	want := 80.0*60*4 - 0.25*math.Pi*1.6*1.6*4 // quarter cylinder at the corner
	if math.Abs(p.Volume-want)/want > 1e-6 {
		t.Fatalf("volume %v want %v", p.Volume, want)
	}
	for _, s := range []*Shape{box, cyl, cut} {
		s.Release()
	}
	if n := k.LiveShapes(); n != 0 {
		t.Fatalf("leaked %d shapes", n)
	}
}

func TestErrorsDoNotCrash(t *testing.T) {
	k := New()
	defer k.Close()
	if _, err := k.Box(-1, 1, 1); err == nil {
		t.Fatal("expected error for negative box")
	}
	s, _ := k.Box(1, 1, 1)
	s.Release()
	s.Release() // double release is safe
	if _, err := s.Props(); err != ErrReleased {
		t.Fatalf("want ErrReleased, got %v", err)
	}
}

// TestChurnNoLeak runs 10k create/boolean/release cycles: handle count must return to
// zero and resident memory must stay flat.
func TestChurnNoLeak(t *testing.T) {
	if testing.Short() {
		t.Skip("short")
	}
	k := New()
	defer k.Close()

	cycle := func() {
		a, _ := k.Box(10, 10, 10)
		b, _ := k.Cylinder(2, 12)
		c, err := k.Subtract(a, b)
		if err != nil {
			t.Fatal(err)
		}
		a.Release()
		b.Release()
		c.Release()
	}
	for i := 0; i < 500; i++ { // warm-up
		cycle()
	}
	var m0, m1 runtime.MemStats
	runtime.ReadMemStats(&m0)
	rss0 := rssKB()
	for i := 0; i < 10000; i++ {
		cycle()
	}
	runtime.ReadMemStats(&m1)
	rss1 := rssKB()
	if n := k.LiveShapes(); n != 0 {
		t.Fatalf("leaked %d shapes", n)
	}
	// Allow 20 MB of slack for allocator noise; a real leak grows far faster over 10k cycles.
	if rss1-rss0 > 20*1024 {
		t.Fatalf("RSS grew from %d kB to %d kB", rss0, rss1)
	}
	t.Logf("RSS %d kB -> %d kB", rss0, rss1)
}

func TestRoundPrimitiveVolumes(t *testing.T) {
	k := New()
	defer k.Close()
	cases := []struct {
		name string
		mk   func() (*Shape, error)
		want float64
	}{
		{"sphere", func() (*Shape, error) { return k.Sphere(5) }, 4.0 / 3 * math.Pi * 125},
		{"cone", func() (*Shape, error) { return k.Cone(4, 0, 9) }, math.Pi * 16 * 9 / 3},
		{"torus", func() (*Shape, error) { return k.Torus(10, 2) }, 2 * math.Pi * math.Pi * 10 * 4},
	}
	for _, c := range cases {
		s, err := c.mk()
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		p, err := s.Props()
		if err != nil {
			t.Fatal(err)
		}
		if math.Abs(p.Volume-c.want)/c.want > 1e-6 {
			t.Errorf("%s volume %v want %v", c.name, p.Volume, c.want)
		}
		s.Release()
	}
	if _, err := k.Torus(2, 3); err == nil {
		t.Error("torus with minor >= major must fail")
	}
	if _, err := k.Sphere(0); err == nil {
		t.Error("sphere radius 0 must fail")
	}
}

func TestBoxIntersectBoxVolume(t *testing.T) {
	k := New()
	defer k.Close()

	a, err := k.Box(10, 10, 10)
	if err != nil {
		t.Fatal(err)
	}
	b0, err := k.Box(10, 10, 10)
	if err != nil {
		t.Fatal(err)
	}
	b, err := b0.Translate(5, 5, 5)
	if err != nil {
		t.Fatal(err)
	}
	common, err := k.Intersect(a, b)
	if err != nil {
		t.Fatal(err)
	}
	p, err := common.Props()
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(p.Volume-125) > 1e-6 {
		t.Fatalf("volume %v want 125", p.Volume)
	}
	for _, s := range []*Shape{a, b0, b, common} {
		s.Release()
	}
	if n := k.LiveShapes(); n != 0 {
		t.Fatalf("%d shapes leaked", n)
	}
}

func rectSegs(x, y, w, h float64) []Seg2D {
	return []Seg2D{
		{Kind: 0, A: [6]float64{x, y, x + w, y}},
		{Kind: 0, A: [6]float64{x + w, y, x + w, y + h}},
		{Kind: 0, A: [6]float64{x + w, y + h, x, y + h}},
		{Kind: 0, A: [6]float64{x, y + h, x, y}},
	}
}

func circSeg(cx, cy, r float64) Seg2D { return Seg2D{Kind: 2, A: [6]float64{cx, cy, r}} }

func TestSketchFaceAndPrism(t *testing.T) {
	k := New()
	defer k.Close()

	// plain rectangle
	f, err := k.MakeFace(rectSegs(0, 0, 80, 50), []int{4})
	if err != nil {
		t.Fatal(err)
	}
	p, _ := f.Props()
	if math.Abs(p.Area-4000) > 1e-6 {
		t.Fatalf("face area %v want 4000", p.Area)
	}

	// rectangle with two circular holes -> area wh - 2*pi*r^2, prism volume * depth
	segs := append(rectSegs(0, 0, 80, 50), circSeg(20, 25, 3), circSeg(60, 25, 3))
	f, err = k.MakeFace(segs, []int{4, 1, 1})
	if err != nil {
		t.Fatal(err)
	}
	p, _ = f.Props()
	want := 4000 - 2*math.Pi*9
	if math.Abs(p.Area-want) > 1e-6 {
		t.Fatalf("holed face area %v want %v", p.Area, want)
	}
	solid, err := f.Prism(0, 0, 5)
	if err != nil {
		t.Fatal(err)
	}
	p, _ = solid.Props()
	if math.Abs(p.Volume-want*5) > 1e-5 {
		t.Fatalf("prism volume %v want %v", p.Volume, want*5)
	}
	if ok, _ := solid.IsValid(); !ok {
		t.Fatal("prism invalid")
	}

	// half-disc: line + arc through the top point, area pi r^2 / 2
	half := []Seg2D{
		{Kind: 0, A: [6]float64{-10, 0, 10, 0}},
		{Kind: 1, A: [6]float64{10, 0, 0, 10, -10, 0}},
	}
	f, err = k.MakeFace(half, []int{2})
	if err != nil {
		t.Fatal(err)
	}
	p, _ = f.Props()
	if math.Abs(p.Area-math.Pi*100/2) > 1e-4 {
		t.Fatalf("half disc area %v", p.Area)
	}

	// open loop and disconnected segments fail
	if _, err := k.MakeFace(rectSegs(0, 0, 10, 10)[:3], []int{3}); err == nil {
		t.Error("open loop must fail")
	}
	// hole outside the outer loop fails
	bad := append(rectSegs(0, 0, 10, 10), circSeg(50, 50, 2))
	if _, err := k.MakeFace(bad, []int{4, 1}); err == nil {
		t.Error("hole outside outer loop must fail")
	}
	// zero-length prism fails
	if _, err := f.Prism(0, 0, 0); err == nil {
		t.Error("zero prism must fail")
	}
}

func TestPlaceAndFaceFrame(t *testing.T) {
	k := New()
	defer k.Close()

	f, err := k.MakeFace(rectSegs(0, 0, 10, 4), []int{4})
	if err != nil {
		t.Fatal(err)
	}
	// local XY -> plane with normal +X at x=7; local x axis -> world +Y
	pl, err := f.Place([3]float64{7, 0, 0}, [3]float64{1, 0, 0}, [3]float64{0, 1, 0})
	if err != nil {
		t.Fatal(err)
	}
	p, _ := pl.Props()
	if math.Abs(p.MinX-7) > 1e-9 || math.Abs(p.MaxX-7) > 1e-9 ||
		math.Abs(p.MaxY-10) > 1e-9 || math.Abs(p.MaxZ-4) > 1e-9 {
		t.Fatalf("bad placement bounds %+v", p)
	}

	// frame of the +Z face of a 30x10x5 box: longest edge is along X
	box, _ := k.Box(30, 10, 5)
	mesh, err := box.Tessellate(0.1)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for i, fc := range mesh.Faces {
		if fc.Normal[2] > 0.9 {
			fr, err := box.FaceFrame(i)
			if err != nil {
				t.Fatal(err)
			}
			if math.Abs(fr.Normal[2]-1) > 1e-9 || math.Abs(fr.X[0]-1) > 1e-9 || math.Abs(fr.Origin[2]-5) > 1e-9 {
				t.Fatalf("bad frame %+v", fr)
			}
			found = true
		}
	}
	if !found {
		t.Fatal("no +Z face")
	}
	// rotated box: frame normal follows the rotation
	rot, _ := box.Rotate(0, 0, 0, 0, 1, 0, math.Pi/2)
	mesh, _ = rot.Tessellate(0.1)
	for i, fc := range mesh.Faces {
		if fc.Normal[0] > 0.9 {
			fr, err := rot.FaceFrame(i)
			if err != nil {
				t.Fatal(err)
			}
			if math.Abs(fr.Normal[0]-1) > 1e-9 {
				t.Fatalf("rotated frame %+v", fr)
			}
		}
	}
	if _, err := box.FaceFrame(99); err == nil {
		t.Error("out-of-range face must fail")
	}
	cyl, _ := k.Cylinder(3, 10)
	cm, _ := cyl.Tessellate(0.1)
	for i, fc := range cm.Faces {
		if fc.Surface == 2 { // cylinder side is not planar
			if _, err := cyl.FaceFrame(i); err == nil {
				t.Error("non-planar face must fail")
			}
		}
	}
}
