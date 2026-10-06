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
