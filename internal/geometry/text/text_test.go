package text

import (
	"math"
	"testing"
)

func lay(t *testing.T, s string, o Options) *Layout {
	t.Helper()
	if o.Size == 0 {
		o.Size = 10
	}
	l, err := Builtin().Lay(s, o)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func TestCountersAreHoles(t *testing.T) {
	for s, want := range map[string]int{"o": 1, "A": 1, "B": 2, "I": 0, "8": 2} {
		l := lay(t, s, Options{})
		if len(l.Regions) != 1 {
			t.Fatalf("%q: %d regions, want 1", s, len(l.Regions))
		}
		if got := len(l.Regions[0].Holes); got != want {
			t.Errorf("%q: %d holes, want %d", s, got, want)
		}
	}
	if l := lay(t, "i", Options{}); len(l.Regions) != 2 {
		t.Errorf("i: %d regions (stem and dot), want 2", len(l.Regions))
	}
}

func TestSizeScalesLinearly(t *testing.T) {
	a, b := lay(t, "Simon", Options{Size: 10}), lay(t, "Simon", Options{Size: 20})
	if r := b.Width / a.Width; math.Abs(r-2) > 1e-9 {
		t.Errorf("width ratio %v, want 2", r)
	}
	if r := Area(b.Regions) / Area(a.Regions); math.Abs(r-4) > 1e-6 {
		t.Errorf("area ratio %v, want 4", r)
	}
	bb := Bounds(a.Regions)
	if h := bb[3] - bb[1]; h < 6 || h > 10 {
		t.Errorf("cap/ascender height %v mm for a 10 mm em", h)
	}
}

func TestSpacingAndKerning(t *testing.T) {
	a, b := lay(t, "AV", Options{}), lay(t, "AV", Options{Spacing: 1})
	if d := b.Width - a.Width; math.Abs(d-1) > 1e-9 {
		t.Errorf("spacing 1 mm widened by %v", d)
	}
	k, nk := lay(t, "AV", Options{}), lay(t, "AV", Options{NoKerning: true})
	if k.Width > nk.Width+1e-9 {
		t.Errorf("kerning widened AV: %v vs %v", k.Width, nk.Width)
	}
}

func TestMirror(t *testing.T) {
	n, m := lay(t, "Lb", Options{}), lay(t, "Lb", Options{Mirror: true})
	bn, bm := Bounds(n.Regions), Bounds(m.Regions)
	if math.Abs((bn[2]-bn[0])-(bm[2]-bm[0])) > 1e-9 {
		t.Error("mirroring changed the width")
	}
	// the L has its stem on the left: mirrored, the leftmost glyph is the mirrored b
	if math.Abs(Area(n.Regions)-Area(m.Regions)) > 1e-6 {
		t.Error("mirroring changed the area")
	}
	for _, r := range m.Regions {
		if a := makeLoop(r.Outer).area; a <= 0 {
			t.Fatalf("mirrored outer loop is clockwise (area %v)", a)
		}
		for _, h := range r.Holes {
			if makeLoop(h).area >= 0 {
				t.Fatal("mirrored hole is counter-clockwise")
			}
		}
	}
	if m.Regions[0].Outer[0].P0 == n.Regions[0].Outer[0].P0 {
		t.Error("mirror left the outline unchanged")
	}
}

func TestLinesAndAlign(t *testing.T) {
	l := lay(t, "WWWW\nii", Options{Align: "center"})
	if l.Height < 10*1.2 {
		t.Errorf("two lines are only %v mm tall", l.Height)
	}
	bb := Bounds(l.Regions)
	lowest := bb[1]
	if lowest > -10 {
		t.Errorf("second line not below the first: min y %v", lowest)
	}
	left := lay(t, "WWWW\nii", Options{})
	if Bounds(left.Regions)[0] >= Bounds(l.Regions)[0]-1 && Area(left.Regions) != Area(l.Regions) {
		t.Error("alignment changed the glyphs")
	}
}

func TestErrors(t *testing.T) {
	f := Builtin()
	for _, o := range []Options{{Size: 0}, {Size: -1}, {Size: 5, Align: "middle"}} {
		if _, err := f.Lay("x", o); err == nil {
			t.Errorf("%+v accepted", o)
		}
	}
	if _, err := f.Lay("  \n ", Options{Size: 5}); err == nil {
		t.Error("blank text accepted")
	}
	l, err := f.Lay("a日", Options{Size: 5})
	if err != nil || len(l.Missing) != 1 {
		t.Errorf("missing glyph not reported: %v %v", l, err)
	}
	if _, err := Parse([]byte("nope"), "x"); err == nil {
		t.Error("garbage parsed as a font")
	}
}

func TestDeterministic(t *testing.T) {
	a, b := lay(t, "Simon", Options{}), lay(t, "Simon", Options{})
	if len(a.Regions) != len(b.Regions) || Area(a.Regions) != Area(b.Regions) {
		t.Error("layout is not deterministic")
	}
}

func TestSubdividePreservesShape(t *testing.T) {
	l := lay(t, "Sg", Options{})
	sub := Subdivide(l.Regions, 8)
	if len(sub) != len(l.Regions) {
		t.Fatal("regions changed")
	}
	if math.Abs(Area(sub)-Area(l.Regions)) > 1e-6*Area(l.Regions) {
		t.Errorf("area %v vs %v", Area(sub), Area(l.Regions))
	}
	for i, r := range sub {
		if len(r.Outer) != 8*len(l.Regions[i].Outer) || len(r.Holes) != len(l.Regions[i].Holes) {
			t.Fatal("segment counts wrong")
		}
		for k := 1; k < len(r.Outer); k++ {
			if dist(r.Outer[k-1].P1, r.Outer[k].P0) > 1e-9 {
				t.Fatal("loop is not continuous")
			}
		}
	}
	if len(Subdivide(l.Regions, 1)) != len(l.Regions) {
		t.Error("n=1 must be a no-op")
	}
}
