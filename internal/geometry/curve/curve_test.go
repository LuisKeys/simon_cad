package curve

import (
	"math"
	"testing"
)

func near(a, b, tol float64) bool { return math.Abs(a-b) <= tol }

func nearV(a, b Vec, tol float64) bool { return dist(a, b) <= tol }

// circlePath is a closed Bezier circle of radius r (4 arcs, kappa handles).
func circlePath(r float64) *Path {
	k := 0.5522847498307936 * r
	pts := []Point{
		{P: Vec{r, 0, 0}, In: &Vec{r, -k, 0}, Out: &Vec{r, k, 0}},
		{P: Vec{0, r, 0}, In: &Vec{k, r, 0}, Out: &Vec{-k, r, 0}},
		{P: Vec{-r, 0, 0}, In: &Vec{-r, k, 0}, Out: &Vec{-r, -k, 0}},
		{P: Vec{0, -r, 0}, In: &Vec{-k, -r, 0}, Out: &Vec{k, -r, 0}},
	}
	return &Path{Type: Bezier, Points: pts, Closed: true}
}

func helixPoints(r, pitch float64, turns, perTurn int) []Point {
	n := turns*perTurn + 1
	out := make([]Point, n)
	for i := range out {
		a := 2 * math.Pi * float64(i) / float64(perTurn)
		out[i].P = Vec{r * math.Cos(a), r * math.Sin(a), pitch * a / (2 * math.Pi)}
	}
	return out
}

func TestBezierCircleLength(t *testing.T) {
	c, err := New(circlePath(10))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := c.Length(), 2*math.Pi*10; math.Abs(got-want)/want > 5e-4 {
		t.Fatalf("length %v want %v", got, want)
	}
}

func TestArcLengthParameterIsUniform(t *testing.T) {
	c, _ := New(circlePath(10))
	// quarter points of a circle land on the axes (the 4 arcs are symmetric)
	for i, want := range []Vec{{10, 0, 0}, {0, 10, 0}, {-10, 0, 0}, {0, -10, 0}} {
		if got := c.At(float64(i) / 4).P; !nearV(got, want, 2e-2) {
			t.Fatalf("t=%d/4: %v want %v", i, got, want)
		}
	}
	// tangent at t=0 is +Y
	if tg := c.At(0).T; !nearV(tg, Vec{0, 1, 0}, 1e-6) {
		t.Fatalf("tangent %v", tg)
	}
	// equal arc-length steps give equal chords (within curvature error)
	pts := c.ResampleUniform(64)
	for i := range pts {
		d := dist(pts[i], pts[(i+1)%len(pts)])
		if !near(d, 2*math.Pi*10/64, 5e-2) {
			t.Fatalf("chord %d = %v", i, d)
		}
	}
}

func TestHelixLength(t *testing.T) {
	for _, typ := range []Type{CatmullRom, BSpline} {
		p := &Path{Type: typ, Points: helixPoints(10, 5, 3, 24)}
		c, err := New(p)
		if err != nil {
			t.Fatal(err)
		}
		want := 3 * math.Hypot(2*math.Pi*10, 5)
		tol := 2e-3
		if typ == BSpline {
			tol = 3e-2 // B-spline approximates its control polygon, shrinking the radius a bit
		}
		if got := c.Length(); math.Abs(got-want)/want > tol {
			t.Fatalf("%s helix length %v want %v", typ, got, want)
		}
	}
}

func TestInterpolatingTypesPassThroughPoints(t *testing.T) {
	pts := helixPoints(5, 2, 1, 8)
	for _, typ := range []Type{Linear, CatmullRom} {
		p := &Path{Type: typ, Points: pts}
		segs := p.Segments()
		for i, s := range segs {
			if !nearV(s.At(0), pts[i].P, 1e-12) || !nearV(s.At(1), pts[i+1].P, 1e-12) {
				t.Fatalf("%s seg %d misses its end points", typ, i)
			}
		}
	}
	// a clamped open B-spline starts and ends at its end points
	bs := &Path{Type: BSpline, Points: pts}
	segs := bs.Segments()
	if !nearV(segs[0].At(0), pts[0].P, 1e-12) || !nearV(segs[len(segs)-1].At(1), pts[len(pts)-1].P, 1e-12) {
		t.Fatal("bspline does not interpolate its ends")
	}
}

func TestLinearLengthAndTangent(t *testing.T) {
	p := &Path{Type: Linear, Points: []Point{{P: Vec{0, 0, 0}}, {P: Vec{3, 4, 0}}, {P: Vec{3, 4, 12}}}}
	c, _ := New(p)
	if !near(c.Length(), 17, 1e-9) {
		t.Fatalf("length %v", c.Length())
	}
	e := c.At(5.0 / 17) // end of first leg
	if !nearV(e.P, Vec{3, 4, 0}, 1e-6) {
		t.Fatalf("corner %v", e.P)
	}
	if mid := c.At(2.5 / 17); !nearV(mid.T, Vec{0.6, 0.8, 0}, 1e-9) {
		t.Fatalf("tangent %v", mid.T)
	}
}

func TestResampleTolerance(t *testing.T) {
	c, _ := New(circlePath(10))
	for _, tol := range []float64{0.5, 0.05, 0.005} {
		pts := c.Resample(tol)
		maxDev := 0.0
		for i := range pts {
			a, b := pts[i], pts[(i+1)%len(pts)]
			mid := lerp(a, b, .5)
			if dev := 10 - norm(mid); dev > maxDev {
				maxDev = dev
			}
		}
		if maxDev > tol*1.5 {
			t.Fatalf("tol %v: deviation %v", tol, maxDev)
		}
		if len(pts) < 8 {
			t.Fatalf("tol %v: only %d points", tol, len(pts))
		}
	}
	if len(c.Resample(0.005)) <= len(c.Resample(0.5)) {
		t.Fatal("finer tolerance must give more points")
	}
}

func TestEditOps(t *testing.T) {
	p := &Path{Type: Linear, Points: []Point{{P: Vec{0, 0, 0}}, {P: Vec{1, 0, 0}}, {P: Vec{2, 0, 0}}}}
	if err := p.Insert(1, Point{P: Vec{.5, 1, 0}}); err != nil || len(p.Points) != 4 || p.Points[1].P[1] != 1 {
		t.Fatalf("insert: %v %v", err, p.Points)
	}
	if err := p.Insert(9, Point{}); err == nil {
		t.Fatal("expected out-of-range error")
	}
	if err := p.Remove(1); err != nil || len(p.Points) != 3 {
		t.Fatal(err)
	}
	if err := p.Remove(0); err != nil {
		t.Fatal(err)
	}
	if err := p.Remove(0); err == nil {
		t.Fatal("removing below 2 points must fail")
	}
	p.Reverse()
	if p.Points[0].P[0] != 2 {
		t.Fatalf("reverse: %v", p.Points)
	}
	if err := p.SetClosed(true); err == nil {
		t.Fatal("cannot close a 2-point path")
	}
}

func TestMoveCarriesHandles(t *testing.T) {
	p := circlePath(10)
	oldOut := *p.Points[0].Out
	if err := p.Move(0, Vec{11, 1, 0}); err != nil {
		t.Fatal(err)
	}
	if got := *p.Points[0].Out; !nearV(got, add(oldOut, Vec{1, 1, 0}), 1e-12) {
		t.Fatalf("handle %v", got)
	}
}

func TestConvertToBezierKeepsShape(t *testing.T) {
	for _, typ := range []Type{CatmullRom, BSpline, Linear} {
		p := &Path{Type: typ, Points: helixPoints(5, 2, 1, 8), Closed: false}
		before, _ := New(p)
		q := p.Clone()
		if err := q.Convert(Bezier); err != nil {
			t.Fatal(err)
		}
		after, _ := New(q)
		for _, u := range []float64{0, .13, .5, .77, 1} {
			if d := dist(before.At(u).P, after.At(u).P); d > 1e-6 {
				t.Fatalf("%s→bezier shape drift %v at %v", typ, d, u)
			}
		}
	}
}

func TestConvertClosedBezierRoundTrip(t *testing.T) {
	p := &Path{Type: CatmullRom, Closed: true, Points: []Point{{P: Vec{0, 0, 0}}, {P: Vec{4, 0, 0}}, {P: Vec{4, 4, 0}}, {P: Vec{0, 4, 0}}}}
	before, _ := New(p)
	if err := p.Convert(Bezier); err != nil {
		t.Fatal(err)
	}
	after, _ := New(p)
	if !near(before.Length(), after.Length(), 1e-6) {
		t.Fatalf("length %v vs %v", before.Length(), after.Length())
	}
}

func TestSimplify(t *testing.T) {
	var pts []Point
	for i := 0; i <= 100; i++ {
		pts = append(pts, Point{P: Vec{float64(i), 0.001 * math.Sin(float64(i)), 0}})
	}
	p := &Path{Type: Linear, Points: pts}
	p.Simplify(0.01)
	if len(p.Points) != 2 {
		t.Fatalf("nearly straight line kept %d points", len(p.Points))
	}
	// a square ring with collinear midpoints keeps its 4 corners
	ring := &Path{Type: Linear, Closed: true}
	for _, v := range []Vec{{0, 0, 0}, {5, 0, 0}, {10, 0, 0}, {10, 5, 0}, {10, 10, 0}, {5, 10, 0}, {0, 10, 0}, {0, 5, 0}} {
		ring.Add(Point{P: v})
	}
	ring.Simplify(0.01)
	if len(ring.Points) != 4 {
		t.Fatalf("ring kept %d points, want 4", len(ring.Points))
	}
}

func TestSmoothKeepsEndsAndReducesZigzag(t *testing.T) {
	p := &Path{Type: Linear}
	for i := 0; i < 20; i++ {
		y := 0.0
		if i%2 == 1 {
			y = 1
		}
		p.Add(Point{P: Vec{float64(i), y, 0}})
	}
	first, last := p.Points[0].P, p.Points[19].P
	p.Smooth(5)
	if p.Points[0].P != first || p.Points[19].P != last {
		t.Fatal("ends moved")
	}
	if p.Points[10].P[1] < 0.3 || p.Points[10].P[1] > 0.7 {
		t.Fatalf("zigzag not flattened: %v", p.Points[10].P)
	}
}

func TestFramesOrthonormalAndParallel(t *testing.T) {
	c, _ := New(&Path{Type: CatmullRom, Points: helixPoints(10, 5, 2, 16)})
	pts := c.ResampleUniform(200)
	fr := Frames(pts, false, nil, Vec{})
	maxTwist := 0.0
	for i, f := range fr {
		if !near(norm(f.N), 1, 1e-9) || !near(norm(f.B), 1, 1e-9) || !near(dot(f.N, f.T), 0, 1e-9) {
			t.Fatalf("frame %d not orthonormal", i)
		}
		if i > 0 {
			// the frame must not spin about the tangent: compare N with N parallel-moved
			a := math.Acos(math.Min(1, math.Max(-1, dot(fr[i-1].N, f.N))))
			if a > maxTwist {
				maxTwist = a
			}
		}
	}
	if maxTwist > 0.1 { // ~ curvature * step, far below any spin
		t.Fatalf("max step rotation %v rad", maxTwist)
	}
}

func TestFramesTwistApplied(t *testing.T) {
	pts := []Vec{{0, 0, 0}, {0, 0, 1}, {0, 0, 2}}
	fr := Frames(pts, false, []float64{0, 90, 0}, Vec{1, 0, 0})
	if d := dot(fr[0].N, fr[1].N); !near(d, 0, 1e-9) {
		t.Fatalf("90° twist: dot %v", d)
	}
}

func TestFramesClosedHolonomy(t *testing.T) {
	// a tilted closed loop accumulates holonomy; the correction must make the closing step smooth
	c, _ := New(&Path{Type: CatmullRom, Closed: true, Points: []Point{
		{P: Vec{10, 0, 0}}, {P: Vec{0, 10, 4}}, {P: Vec{-10, 0, 0}}, {P: Vec{0, -10, -4}}}})
	pts := c.ResampleUniform(240)
	fr := Frames(pts, true, nil, Vec{})
	last, first := fr[len(fr)-1], fr[0]
	a := math.Acos(math.Min(1, dot(last.N, first.N)))
	if a > 0.1 {
		t.Fatalf("seam angle %v rad", a)
	}
}

func TestFramesCoincidentPoints(t *testing.T) {
	pts := []Vec{{0, 0, 0}, {0, 0, 0}, {1, 0, 0}, {1, 0, 0}, {2, 0, 0}}
	fr := Frames(pts, false, nil, Vec{})
	for i, f := range fr {
		if math.IsNaN(f.N[0]) || !near(norm(f.N), 1, 1e-9) {
			t.Fatalf("frame %d invalid: %+v", i, f)
		}
	}
}

func TestFramesSharpCornerStable(t *testing.T) {
	pts := []Vec{{0, 0, 0}, {5, 0, 0}, {10, 0, 0}, {10, 5, 0}, {10, 10, 0}}
	for i, f := range Frames(pts, false, nil, Vec{}) {
		if math.IsNaN(f.N[0]+f.N[1]+f.N[2]) || !near(norm(f.N), 1, 1e-9) {
			t.Fatalf("frame %d invalid", i)
		}
	}
}

func TestMinRadius(t *testing.T) {
	c, _ := New(circlePath(10))
	r, _ := MinRadius(c.ResampleUniform(180), true)
	if !near(r, 10, 0.1) {
		t.Fatalf("radius %v", r)
	}
	if r, _ := MinRadius([]Vec{{0, 0, 0}, {1, 0, 0}, {2, 0, 0}}, false); !math.IsInf(r, 1) {
		t.Fatalf("straight line radius %v", r)
	}
}

func TestSelfIntersections(t *testing.T) {
	c, _ := New(circlePath(10))
	if got := SelfIntersections(c.ResampleUniform(120), true, 0.01); len(got) != 0 {
		t.Fatalf("circle flagged: %v", got)
	}
	bow := []Vec{{0, 0, 0}, {10, 10, 0}, {10, 0, 0}, {0, 10, 0}} // X shape: segments 0 and 2 cross
	if got := SelfIntersections(bow, false, 0.01); len(got) != 1 || got[0].A != 0 || got[0].B != 2 {
		t.Fatalf("bow tie: %v", got)
	}
	// a helix climbs, so its turns never touch
	h, _ := New(&Path{Type: CatmullRom, Points: helixPoints(10, 5, 3, 24)})
	if got := SelfIntersections(h.ResampleUniform(300), false, 0.5); len(got) != 0 {
		t.Fatalf("helix flagged: %v", got)
	}
}

func TestValidate(t *testing.T) {
	if (&Path{Type: "wavy", Points: []Point{{}, {}}}).Validate() == nil {
		t.Fatal("unknown type accepted")
	}
	if (&Path{Type: Linear, Points: []Point{{}}}).Validate() == nil {
		t.Fatal("1 point accepted")
	}
	if (&Path{Type: Linear, Closed: true, Points: []Point{{}, {P: Vec{1, 0, 0}}}}).Validate() == nil {
		t.Fatal("2-point closed accepted")
	}
	if (&Path{Type: Linear, Points: []Point{{}, {P: Vec{math.NaN(), 0, 0}}}}).Validate() == nil {
		t.Fatal("NaN accepted")
	}
}
