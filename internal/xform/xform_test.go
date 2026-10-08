package xform

import (
	"math"
	"testing"
)

func near(a, b [3]float64) bool {
	for i := range a {
		if math.Abs(a[i]-b[i]) > 1e-9 {
			return false
		}
	}
	return true
}

func TestAxisAngleRotatesByTheRightHandRule(t *testing.T) {
	r := AxisAngle([3]float64{0, 0, 2}, 90)
	if got := r.Apply([3]float64{1, 0, 0}); !near(got, [3]float64{0, 1, 0}) {
		t.Fatalf("X about +Z by 90° = %v, want +Y", got)
	}
	if !r.Mul(r.T()).IsIdentity(1e-12) {
		t.Fatal("R·Rᵀ must be the identity")
	}
	if !AxisAngle([3]float64{}, 30).IsIdentity(0) || !AxisAngle([3]float64{1, 0, 0}, 0).IsIdentity(0) {
		t.Fatal("a zero axis or angle is the identity")
	}
}

func TestToAxisAngleRoundTrips(t *testing.T) {
	cases := []struct {
		axis [3]float64
		deg  float64
	}{
		{[3]float64{0, 0, 1}, 30}, {[3]float64{1, 1, 0}, 75}, {[3]float64{0, 1, 0}, 180},
		{[3]float64{1, -2, 3}, 179.99}, {[3]float64{-1, 0, 0}, 120}, {[3]float64{0, -1, 1}, 180},
	}
	for _, c := range cases {
		r := AxisAngle(c.axis, c.deg)
		ax, deg := r.ToAxisAngle()
		back := AxisAngle(ax, deg)
		for i := 0; i < 3; i++ {
			if !near(back.Axis(i), r.Axis(i)) {
				t.Fatalf("%v %g°: decomposed to %v %g°, which is a different rotation", c.axis, c.deg, ax, deg)
			}
		}
	}
	if ax, deg := Identity.ToAxisAngle(); deg != 0 || ax != [3]float64{0, 0, 1} {
		t.Fatalf("identity = %v %g", ax, deg)
	}
}

func TestMulAppliesTheRightOperandFirst(t *testing.T) {
	rz, rx := AxisAngle([3]float64{0, 0, 1}, 90), AxisAngle([3]float64{1, 0, 0}, 90)
	// rx then rz: X stays X under rx, then becomes Y under rz.
	if got := rz.Mul(rx).Apply([3]float64{1, 0, 0}); !near(got, [3]float64{0, 1, 0}) {
		t.Fatalf("got %v", got)
	}
}
