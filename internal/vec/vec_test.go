package vec

import (
	"math"
	"testing"
)

func TestBasicArithmetic(t *testing.T) {
	a, b := V{1, 2, 3}, V{4, -5, 6}
	if Add(a, b) != (V{5, -3, 9}) || Sub(a, b) != (V{-3, 7, -3}) || Scale(a, 2) != (V{2, 4, 6}) {
		t.Fatal("add/sub/scale")
	}
	if Dot(a, b) != 12 {
		t.Fatalf("dot = %v", Dot(a, b))
	}
	if Lerp(a, b, 0) != a || Lerp(a, b, 1) != b || Lerp(a, b, 0.5) != (V{2.5, -1.5, 4.5}) {
		t.Fatal("lerp")
	}
}

func TestCrossIsOrthogonalAndRightHanded(t *testing.T) {
	x, y := V{1, 0, 0}, V{0, 1, 0}
	if Cross(x, y) != (V{0, 0, 1}) || Cross(y, x) != (V{0, 0, -1}) {
		t.Fatal("handedness")
	}
	a, b := V{1, 2, 3}, V{-2, 0.5, 4}
	c := Cross(a, b)
	if math.Abs(Dot(c, a)) > 1e-12 || math.Abs(Dot(c, b)) > 1e-12 {
		t.Fatalf("cross not orthogonal: %v", c)
	}
}

func TestLenDistUnit(t *testing.T) {
	if Len(V{3, 4, 0}) != 5 || Dist(V{1, 1, 1}, V{4, 5, 1}) != 5 {
		t.Fatal("length/distance")
	}
	u := Unit(V{0, 0, 7})
	if u != (V{0, 0, 1}) {
		t.Fatalf("unit = %v", u)
	}
	if math.Abs(Len(Unit(V{1, 2, 3}))-1) > 1e-15 {
		t.Fatal("unit length")
	}
	if Unit(V{}) != (V{}) || Unit(V{1e-310, 0, 0}) != (V{}) {
		t.Fatal("the zero (or denormal) vector has no direction and must give zero, not NaN")
	}
}
