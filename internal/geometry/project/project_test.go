package project

import (
	"errors"
	"math"
	"testing"
)

// cylinder builds a closed cylinder mesh (axis Z, radius r, z in [0, h]) with radial vertex normals
// on the side and outward face winding.
func cylinder(r, h float64, seg int) (pos, nrm []float32, idx []uint32) {
	add := func(x, y, z, nx, ny, nz float64) uint32 {
		pos = append(pos, float32(x), float32(y), float32(z))
		nrm = append(nrm, float32(nx), float32(ny), float32(nz))
		return uint32(len(pos)/3 - 1)
	}
	for i := 0; i < seg; i++ {
		a0, a1 := 2*math.Pi*float64(i)/float64(seg), 2*math.Pi*float64(i+1)/float64(seg)
		b0 := add(r*math.Cos(a0), r*math.Sin(a0), 0, math.Cos(a0), math.Sin(a0), 0)
		b1 := add(r*math.Cos(a1), r*math.Sin(a1), 0, math.Cos(a1), math.Sin(a1), 0)
		t0 := add(r*math.Cos(a0), r*math.Sin(a0), h, math.Cos(a0), math.Sin(a0), 0)
		t1 := add(r*math.Cos(a1), r*math.Sin(a1), h, math.Cos(a1), math.Sin(a1), 0)
		idx = append(idx, b0, b1, t1, b0, t1, t0)
		c := add(0, 0, h, 0, 0, 1)
		u0 := add(r*math.Cos(a0), r*math.Sin(a0), h, 0, 0, 1)
		u1 := add(r*math.Cos(a1), r*math.Sin(a1), h, 0, 0, 1)
		idx = append(idx, c, u0, u1)
	}
	return
}

func surf(t *testing.T) *Surface {
	t.Helper()
	p, n, i := cylinder(10, 40, 96)
	s, err := New(p, n, i)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func radius(p Vec) float64 { return math.Hypot(p[0], p[1]) }

func TestPlanarWrapsALineOntoACylinder(t *testing.T) {
	s := surf(t)
	var line []Vec
	for i := 0; i <= 10; i++ { // a straight line across the front at x = 30, y from -6 to 6
		line = append(line, Vec{30, -6 + 1.2*float64(i), 20})
	}
	r, err := s.Project(line, Options{Mode: ModePlanar, Dir: Vec{-1, 0, 0}})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Points) != len(line) {
		t.Fatalf("planar changed the point count: %d", len(r.Points))
	}
	for i, p := range r.Points {
		if math.Abs(radius(p)-10) > 0.02 { // tessellation deflection of a 96-gon: r(1-cos(π/96)) = 0.005
			t.Errorf("point %d radius %v", i, radius(p))
		}
		if p[0] <= 0 {
			t.Errorf("point %d on the far side", i)
		}
		if math.Abs(p[1]-line[i][1]) > 1e-9 || math.Abs(p[2]-20) > 1e-9 {
			t.Errorf("point %d slid sideways: %v", i, p)
		}
		if n := r.Normals[i]; math.Abs(n[0]*p[0]/10+n[1]*p[1]/10-1) > 1e-3 {
			t.Errorf("normal %d is not radial: %v", i, n)
		}
	}
}

func TestOffsetLiftsAlongTheNormal(t *testing.T) {
	s := surf(t)
	r, err := s.Project([]Vec{{30, 3, 5}}, Options{Mode: ModePlanar, Dir: Vec{-1, 0, 0}, Offset: 2})
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(radius(r.Points[0])-12) > 0.02 {
		t.Errorf("radius %v, want 12", radius(r.Points[0]))
	}
}

func TestNormalModeFindsTheNearestPoint(t *testing.T) {
	s := surf(t)
	r, err := s.Project([]Vec{{15, 0, 10}, {0, -25, 30}}, Options{Mode: ModeNormal})
	if err != nil {
		t.Fatal(err)
	}
	for i, p := range r.Points {
		if math.Abs(radius(p)-10) > 0.02 {
			t.Errorf("point %d radius %v", i, radius(p))
		}
	}
	if math.Abs(r.Points[0][2]-10) > 1e-6 {
		t.Errorf("nearest point slid along the axis: %v", r.Points[0])
	}
}

func TestConformAddsPointsWhereTheSurfaceBends(t *testing.T) {
	s := surf(t)
	// two far-apart points: planar would join them with a chord through the cylinder
	line := []Vec{{30, -9, 20}, {30, 9, 20}}
	r, err := s.Project(line, Options{Mode: ModeConform, Dir: Vec{-1, 0, 0}, Tol: 0.05})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Points) <= 2 {
		t.Fatalf("no refinement: %d points", len(r.Points))
	}
	for i := 1; i < len(r.Points); i++ {
		a, b := r.Points[i-1], r.Points[i]
		mid := Vec{(a[0] + b[0]) / 2, (a[1] + b[1]) / 2, (a[2] + b[2]) / 2}
		if gap := 10 - radius(mid); gap > 0.06 {
			t.Errorf("chord %d is %v mm inside the surface", i, gap)
		}
	}
	if r.Source[0] != 0 || r.Source[len(r.Source)-1] != 1 {
		t.Errorf("source indices %v", r.Source)
	}
	// y is monotonic: the order is preserved
	for i := 1; i < len(r.Points); i++ {
		if r.Points[i][1] <= r.Points[i-1][1] {
			t.Fatalf("order broken at %d", i)
		}
	}
	// a flat top face needs no refinement
	flat, err := s.Project([]Vec{{-5, -5, 60}, {5, 5, 60}}, Options{Mode: ModeConform, Dir: Vec{0, 0, -1}})
	if err != nil || len(flat.Points) != 2 {
		t.Errorf("flat: %v points, %v", len(flat.Points), err)
	}
}

func TestMissesAreReportedByIndex(t *testing.T) {
	s := surf(t)
	_, err := s.Project([]Vec{{30, 0, 20}, {30, 50, 20}, {30, 0, 100}, {30, 4, 20}}, Options{Mode: ModePlanar, Dir: Vec{-1, 0, 0}})
	var fe *FailError
	if !errors.As(err, &fe) {
		t.Fatalf("err %v", err)
	}
	if len(fe.Indices) != 2 || fe.Indices[0] != 1 || fe.Indices[1] != 2 {
		t.Errorf("failed indices %v, want [1 2]", fe.Indices)
	}
}

func TestBadOptions(t *testing.T) {
	s := surf(t)
	if _, err := s.Project([]Vec{{1, 1, 1}}, Options{Mode: "spherical"}); err == nil {
		t.Error("unknown mode accepted")
	}
	if _, err := s.Project([]Vec{{1, 1, 1}}, Options{Mode: ModePlanar}); err == nil {
		t.Error("planar without a direction accepted")
	}
	if _, err := New(nil, nil, nil); err == nil {
		t.Error("empty mesh accepted")
	}
}
