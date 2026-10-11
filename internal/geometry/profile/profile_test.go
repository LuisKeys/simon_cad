package profile

import (
	"math"
	"testing"
)

func mk(t *testing.T, typ string, n map[string]float64) *Profile {
	t.Helper()
	p, err := New(typ, Params{Num: n})
	if err != nil {
		t.Fatalf("%s: %v", typ, err)
	}
	if HasError(p.Validate()) {
		t.Fatalf("%s invalid: %v", typ, p.Validate())
	}
	return p
}

func TestPresetAreas(t *testing.T) {
	tol := func(want float64) float64 { return want * 2e-3 }
	cases := []struct {
		typ  string
		n    map[string]float64
		area float64
	}{
		{"circle", map[string]float64{"radius": 5, "segments": 360}, math.Pi * 25},
		{"ellipse", map[string]float64{"radius_x": 4, "radius_y": 2, "segments": 360}, math.Pi * 8},
		{"square", map[string]float64{"side": 3}, 9},
		{"rectangle", map[string]float64{"width": 4, "height": 2.5}, 10},
		{"triangle", map[string]float64{"width": 6, "height": 4}, 12},
		{"semicircle", map[string]float64{"radius": 5, "segments": 360}, math.Pi * 25 / 2},
		{"hollow_circle", map[string]float64{"outer_radius": 5, "inner_radius": 3, "segments": 360}, math.Pi * 16},
	}
	for _, c := range cases {
		p := mk(t, c.typ, c.n)
		if got := p.Area(); math.Abs(got-c.area) > tol(c.area) {
			t.Errorf("%s area %v want %v", c.typ, got, c.area)
		}
	}
}

func TestOrientationNormalized(t *testing.T) {
	p := mk(t, "hollow_circle", map[string]float64{"outer_radius": 5, "inner_radius": 2})
	if SignedArea(p.Contours[0].Points) <= 0 || SignedArea(p.Contours[1].Points) >= 0 {
		t.Fatal("outer must be CCW and hole CW")
	}
	cw, err := New("polygon", Params{Vertices: []Vec2{{0, 0}, {0, 1}, {1, 1}, {1, 0}}})
	if err != nil || SignedArea(cw.Contours[0].Points) <= 0 {
		t.Fatalf("clockwise polygon not normalized: %v", err)
	}
}

func TestInvalidParameters(t *testing.T) {
	bad := []struct {
		typ string
		n   map[string]float64
	}{
		{"circle", map[string]float64{"radius": 0}},
		{"circle", map[string]float64{"radius": 1, "segments": 2}},
		{"rectangle", map[string]float64{"width": 3}},
		{"hollow_circle", map[string]float64{"outer_radius": 2, "inner_radius": 2}},
		{"star", nil},
	}
	for _, b := range bad {
		if _, err := New(b.typ, Params{Num: b.n}); err == nil {
			t.Errorf("%s %v accepted", b.typ, b.n)
		}
	}
	if _, err := New("polygon", Params{Vertices: []Vec2{{0, 0}, {1, 0}}}); err == nil {
		t.Error("2-vertex polygon accepted")
	}
}

func TestValidateDetectsProblems(t *testing.T) {
	bow := &Profile{Contours: []Contour{{Closed: true, Points: []Vec2{{0, 0}, {2, 2}, {2, 0}, {0, 2}}}}}
	if !HasError(bow.Validate()) {
		t.Error("bow tie not flagged")
	}
	line := &Profile{Contours: []Contour{{Closed: true, Points: []Vec2{{0, 0}, {1, 1}, {2, 2}}}}}
	if !HasError(line.Validate()) {
		t.Error("zero-area contour not flagged")
	}
	dup := &Profile{Contours: []Contour{{Closed: true, Points: []Vec2{{0, 0}, {0, 0}, {1, 0}, {1, 1}}}}}
	if !HasError(dup.Validate()) {
		t.Error("coincident points not flagged")
	}
	open := &Profile{Contours: []Contour{{Closed: false, Points: []Vec2{{0, 0}, {1, 0}, {1, 1}}}}}
	if !HasError(open.Validate()) {
		t.Error("open contour not flagged")
	}
	outside := &Profile{Contours: []Contour{
		{Closed: true, Points: []Vec2{{0, 0}, {4, 0}, {4, 4}, {0, 4}}},
		{Closed: true, Hole: true, Points: []Vec2{{10, 10}, {10, 11}, {11, 11}, {11, 10}}},
	}}
	if !HasError(outside.Validate()) {
		t.Error("hole outside outer not flagged")
	}
	crossing := &Profile{Contours: []Contour{
		{Closed: true, Points: []Vec2{{0, 0}, {4, 0}, {4, 4}, {0, 4}}},
		{Closed: true, Hole: true, Points: []Vec2{{3, 1}, {3, 3}, {6, 3}, {6, 1}}},
	}}
	if !HasError(crossing.Validate()) {
		t.Error("hole crossing outer not flagged")
	}
	wrong := &Profile{Contours: []Contour{{Closed: true, Points: []Vec2{{0, 0}, {0, 1}, {1, 1}, {1, 0}}}}}
	if is := wrong.Validate(); HasError(is) || len(is) == 0 {
		t.Errorf("wrong orientation must warn, not fail: %v", is)
	}
}

func TestCustomMultipleContours(t *testing.T) {
	p, err := Custom([]Contour{
		{Closed: true, Points: []Vec2{{0, 0}, {10, 0}, {10, 10}, {0, 10}}},
		{Closed: true, Hole: true, Points: []Vec2{{2, 2}, {8, 2}, {8, 8}, {2, 8}}},
		{Closed: true, Points: []Vec2{{20, 0}, {22, 0}, {22, 2}, {20, 2}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := p.Area(); math.Abs(got-(100-36+4)) > 1e-9 {
		t.Fatalf("area %v", got)
	}
	if _, err := Custom([]Contour{{Closed: true, Points: []Vec2{{0, 0}, {2, 2}, {2, 0}, {0, 2}}}}); err == nil {
		t.Fatal("self-intersecting custom profile accepted")
	}
}

func TestScaleRotate(t *testing.T) {
	p := mk(t, "rectangle", map[string]float64{"width": 4, "height": 2})
	s, err := p.Scaled(2, 3)
	if err != nil || math.Abs(s.Area()-48) > 1e-9 {
		t.Fatalf("scaled area %v %v", s.Area(), err)
	}
	if p.Area() != 8 {
		t.Fatal("Scaled mutated the original")
	}
	if _, err := p.Scaled(-1, 1); err == nil {
		t.Fatal("negative scale accepted")
	}
	r := p.Rotated(90)
	lo, hi := r.Bounds()
	if math.Abs((hi[0]-lo[0])-2) > 1e-9 || math.Abs((hi[1]-lo[1])-4) > 1e-9 {
		t.Fatalf("rotated bounds %v %v", lo, hi)
	}
	if math.Abs(r.Area()-8) > 1e-9 {
		t.Fatal("rotation changed area")
	}
}

func TestSemicircleOrientation(t *testing.T) {
	up := mk(t, "semicircle", map[string]float64{"radius": 5})
	_, hi := up.Bounds()
	if hi[1] < 4.9 {
		t.Fatalf("default semicircle should bulge to +Y: %v", hi)
	}
	left := mk(t, "semicircle", map[string]float64{"radius": 5, "orientation": 90})
	lo, _ := left.Bounds()
	if lo[0] > -4.9 {
		t.Fatalf("90° semicircle should bulge to -X: %v", lo)
	}
}
