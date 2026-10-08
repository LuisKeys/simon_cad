package transform

import (
	"math"
	"testing"

	"github.com/accelone/simoncad/internal/geometry/features"
)

func near3(a, b [3]float64) bool {
	for i := range a {
		if math.Abs(a[i]-b[i]) > 1e-9 {
			return false
		}
	}
	return true
}

func TestRotationKeepsThePivotFixed(t *testing.T) {
	pivot := [3]float64{10, -5, 3}
	for _, axis := range [][3]float64{{1, 0, 0}, {0, 1, 0}, {0, 0, 1}} {
		m := Rotation(axis, 1.234, pivot)
		if got := m.Apply(pivot); !near3(got, pivot) {
			t.Fatalf("pivot moved about %v: %v", axis, got)
		}
	}
	// 90° about Z through (10, 0, 0): (20, 0, 0) → (10, 10, 0).
	if got := Rotation([3]float64{0, 0, 1}, math.Pi/2, [3]float64{10, 0, 0}).Apply([3]float64{20, 0, 0}); !near3(got, [3]float64{10, 10, 0}) {
		t.Fatalf("got %v", got)
	}
}

func TestComposeAndInverse(t *testing.T) {
	a := Rotation([3]float64{1, 2, 3}, 0.7, [3]float64{4, 5, 6}).Then(Translation([3]float64{-1, 2, 9}))
	b := Rotation([3]float64{0, 1, 0}, -2.1, [3]float64{-3, 0, 1})
	p := [3]float64{7, -8, 2.5}
	if got, want := a.Then(b).Apply(p), b.Apply(a.Apply(p)); !near3(got, want) {
		t.Fatalf("Then is not composition: %v vs %v", got, want)
	}
	if !a.Then(a.Inverse()).IsIdentity(1e-12) {
		t.Fatal("m then its inverse must be the identity")
	}
}

// Any pivot describes the same motion; Params → FromParams round-trips it.
func TestPlacementRoundTrips(t *testing.T) {
	m := Rotation([3]float64{0, 0, 1}, -math.Pi/6, [3]float64{3, 4, 0}).Then(Translation([3]float64{12.5, 0, -2}))
	for _, pivot := range [][3]float64{{3, 4, 0}, {0, 0, 0}, {-50, 20, 7}} {
		pl := PlacementOf(m, pivot)
		prm := features.Params{}
		for k, v := range pl.Params() {
			prm[k] = v
		}
		back := FromParams(prm).Rigid()
		for _, p := range [][3]float64{{0, 0, 0}, {10, -3, 8}} {
			if a, b := back.Apply(p), m.Apply(p); math.Abs(a[0]-b[0])+math.Abs(a[1]-b[1])+math.Abs(a[2]-b[2]) > 3e-6 { // stored to 1 nm
				t.Fatalf("pivot %v: %v vs %v", pivot, back.Apply(p), m.Apply(p))
			}
		}
	}
	// A rotation about -Z is written as a negative angle about +Z (the default axis).
	pl := PlacementOf(Rotation([3]float64{0, 0, 1}, -math.Pi/6, [3]float64{}), [3]float64{})
	prm := pl.Params()
	if prm["angle_deg"] != -30 || len(prm) != 1 {
		t.Fatalf("want only angle_deg=-30, got %v", prm)
	}
}

func TestPureMoveStoresOnlyTheOffset(t *testing.T) {
	prm := PlacementOf(Translation([3]float64{0.1 + 0.2, 0, -4}), [3]float64{9, 9, 9}).Params()
	if len(prm) != 2 || prm["dx"] != 0.3 || prm["dz"] != -4 {
		t.Fatalf("got %v", prm)
	}
}

// A component's placement composes with a world motion: the instance keeps one placement,
// whatever its old rotation, and its own (nonuniform) scale stays first.
func TestComposeIntoAnInstancePlacement(t *testing.T) {
	old := Placement{AngleDeg: 30, Axis: [3]float64{1, 0, 0}, Pivot: [3]float64{1, 2, 3}, D: [3]float64{5, 0, 0}}
	move := Rotation([3]float64{0, 0, 1}, math.Pi/4, [3]float64{-2, 0, 0}).Then(Translation([3]float64{0, 7, 0}))
	got := Compose(old, move, [3]float64{-2, 0, 0})
	if got.Pivot != old.Pivot {
		t.Fatalf("an instance that already rotates keeps its pivot: %v", got.Pivot)
	}
	scale := [3]float64{2, 0.5, 1} // applied first, about the component origin
	for _, x := range [][3]float64{{0, 0, 0}, {1, 1, 1}, {-4, 3, 2}} {
		sx := [3]float64{x[0] * scale[0], x[1] * scale[1], x[2] * scale[2]}
		if a, b := got.Rigid().Apply(sx), move.Apply(old.Rigid().Apply(sx)); !near3(a, b) {
			t.Fatalf("composed placement %v != motion after old %v", a, b)
		}
	}
	// No previous rotation: the new pivot is used.
	if p := Compose(Placement{D: [3]float64{1, 0, 0}}, move, [3]float64{-2, 0, 0}); p.Pivot != [3]float64{-2, 0, 0} {
		t.Fatalf("pivot %v", p.Pivot)
	}
}

// Rotations crossing ±180° stay exact: 170° + 20° is 190° (= -170°).
func TestRotationAcrossHalfTurn(t *testing.T) {
	z := [3]float64{0, 0, 1}
	m := Rotation(z, 170*math.Pi/180, [3]float64{}).Then(Rotation(z, 20*math.Pi/180, [3]float64{}))
	want := Rotation(z, -170*math.Pi/180, [3]float64{})
	if !m.Then(want.Inverse()).IsIdentity(1e-12) {
		t.Fatal("170° then 20° must equal -170°")
	}
	if pl := PlacementOf(m, [3]float64{}); math.Abs(pl.AngleDeg+170) > 1e-9 {
		t.Fatalf("stored as %g°", pl.AngleDeg)
	}
}

func TestRoundIsDecimal(t *testing.T) {
	if Round(0.1+0.2, 6) != 0.3 || Round(-1e-12, 6) != 0 || math.Signbit(Round(-1e-12, 6)) {
		t.Fatal("rounding must be decimal and never -0")
	}
}
