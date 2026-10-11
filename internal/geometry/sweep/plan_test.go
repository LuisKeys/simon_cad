package sweep

import (
	"math"
	"testing"

	"github.com/accelone/simoncad/internal/geometry/curve"
	"github.com/accelone/simoncad/internal/geometry/profile"
)

func circle(t *testing.T, r float64) *profile.Profile {
	t.Helper()
	p, err := profile.New("circle", profile.Params{Num: map[string]float64{"radius": r}})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func rect(t *testing.T, w, h float64) *profile.Profile {
	t.Helper()
	p, err := profile.New("rectangle", profile.Params{Num: map[string]float64{"width": w, "height": h}})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func lin(pts ...Vec) *curve.Path {
	p := &curve.Path{Type: curve.Linear}
	for _, v := range pts {
		p.Add(curve.Point{P: v})
	}
	return p
}

func code(err error) string {
	if e, ok := AsError(err); ok {
		return e.Code
	}
	return ""
}

// smoothLine is a straight catmull_rom path: smooth paths keep exact ellipse sections.
func smoothLine(a, b Vec) *curve.Path {
	return &curve.Path{Type: curve.CatmullRom, Points: []curve.Point{{P: a}, {P: b}}}
}

func TestStraightPathSections(t *testing.T) {
	plan, err := Build(smoothLine(Vec{0, 0, 0}, Vec{0, 0, 20}), circle(t, 2), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Body.Sections) != 2 || plan.Body.Closed {
		t.Fatalf("sections %d", len(plan.Body.Sections))
	}
	for _, s := range plan.Body.Sections {
		z := cross(s.X, s.Y)
		if math.Abs(z[2]-1) > 1e-9 {
			t.Fatalf("section normal %v is not the path tangent", z)
		}
	}
	if !plan.Body.Contours[0].Ellipse {
		t.Fatal("a circle profile must stay an exact ellipse")
	}
	if math.Abs(plan.ExpectedVolume-math.Pi*4*20) > 0.2 { // 48-gon area vs the circle
		t.Fatalf("expected volume %v", plan.ExpectedVolume)
	}
}

func TestCurvedPathAddsSectionsWithTolerance(t *testing.T) {
	arc := &curve.Path{Type: curve.CatmullRom, Points: []curve.Point{{P: Vec{0, 0, 0}}, {P: Vec{10, 10, 0}}, {P: Vec{20, 0, 0}}, {P: Vec{30, 10, 0}}}}
	coarse, err := Build(arc, circle(t, 0.5), Options{Tolerance: 1})
	if err != nil {
		t.Fatal(err)
	}
	fine, err := Build(arc, circle(t, 0.5), Options{Tolerance: 0.001})
	if err != nil {
		t.Fatal(err)
	}
	if len(fine.Body.Sections) <= len(coarse.Body.Sections) {
		t.Fatalf("finer tolerance must add sections: %d vs %d", len(fine.Body.Sections), len(coarse.Body.Sections))
	}
	capped, err := Build(arc, circle(t, 0.5), Options{Tolerance: 0.0001, MaxSections: 12})
	if err != nil {
		t.Fatal(err)
	}
	if len(capped.Body.Sections) > 12 {
		t.Fatalf("max_sections ignored: %d", len(capped.Body.Sections))
	}
	if len(capped.Warnings) == 0 {
		t.Fatal("hitting the section limit must warn")
	}
}

func TestPerPointScaleAndTwistInterpolate(t *testing.T) {
	p := &curve.Path{Type: curve.Linear, Points: []curve.Point{{P: Vec{0, 0, 0}, W: 1, H: 1}, {P: Vec{0, 0, 30}, W: 3, H: 2, Twist: 90}}}
	plan, err := Build(p, rect(t, 2, 2), Options{Up: Vec{1, 0, 0}})
	if err != nil {
		t.Fatal(err)
	}
	// 90° of twist needs several extra sections (ruled surfaces only twist in small steps)
	if len(plan.Body.Sections) < 90/int(twistStepDeg) {
		t.Fatalf("too few sections for the twist: %d", len(plan.Body.Sections))
	}
	last := plan.Body.Sections[len(plan.Body.Sections)-1]
	if math.Abs(last.X[1]-1) > 1e-6 && math.Abs(last.X[1]+1) > 1e-6 { // X turned a quarter turn from +X
		t.Fatalf("last X axis %v", last.X)
	}
	// polygons carry the w/h scale: last section is 3 wide
	pts := plan.Body.Contours[0].PerSection[len(plan.Body.Sections)-1]
	maxX := 0.0
	for _, q := range pts {
		maxX = math.Max(maxX, math.Abs(q[0]))
	}
	if math.Abs(maxX-3) > 1e-9 {
		t.Fatalf("scaled half-width %v", maxX)
	}
	// the analytic volume uses the linear taper: ∫ (1+2t)(1+t) dt = 1 + 1.5 + 2/3
	want := 4 * (1 + 1.5 + 2.0/3) * 30
	if math.Abs(plan.ExpectedVolume-want) > 1e-6 {
		t.Fatalf("expected volume %v want %v", plan.ExpectedVolume, want)
	}
}

func TestMiterStretchesCorner(t *testing.T) {
	plan, err := Build(lin(Vec{0, 0, 0}, Vec{20, 0, 0}, Vec{20, 20, 0}), rect(t, 2, 2), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Body.Ruled || len(plan.Body.Sections) != 3 {
		t.Fatalf("polyline plan: ruled=%v sections=%d", plan.Body.Ruled, len(plan.Body.Sections))
	}
	mid := plan.Body.Contours[0].PerSection[1]
	maxR := 0.0
	for _, q := range mid {
		maxR = math.Max(maxR, math.Hypot(q[0], q[1]))
	}
	// the corner section is stretched by 1/cos(45°) along the turn
	if maxR <= math.Sqrt2*1.01 {
		t.Fatalf("corner section not stretched: %v", maxR)
	}
	if n := plan.Body.Sections[1]; math.Abs(cross(n.X, n.Y)[0]-1/math.Sqrt2) > 1e-9 {
		t.Fatalf("corner plane is not the bisector: %v", cross(n.X, n.Y))
	}
}

func TestErrors(t *testing.T) {
	// zero-length path
	if _, err := Build(lin(Vec{1, 1, 1}, Vec{1, 1, 1}), circle(t, 1), Options{}); code(err) != CodePathInvalid {
		t.Fatalf("zero length: %v", err)
	}
	// profile wider than the bend radius
	bend := &curve.Path{Type: curve.CatmullRom, Closed: true, Points: []curve.Point{{P: Vec{5, 0, 0}}, {P: Vec{0, 5, 0}}, {P: Vec{-5, 0, 0}}, {P: Vec{0, -5, 0}}}}
	if _, err := Build(bend, circle(t, 6), Options{}); code(err) != CodeExcessiveCurvature {
		t.Fatalf("curvature: %v", err)
	}
	if _, err := Build(bend, circle(t, 1), Options{}); err != nil {
		t.Fatalf("a thin profile must pass: %v", err)
	}
	// self-crossing
	if _, err := Build(lin(Vec{0, 0, 0}, Vec{10, 10, 0}, Vec{10, 0, 0}, Vec{0, 10, 0}), circle(t, 0.5), Options{}); code(err) != CodePathSelfIntersect {
		t.Fatalf("crossing: %v", err)
	}
	// parallel legs closer than the thickness
	u := lin(Vec{0, 0, 0}, Vec{30, 0, 0}, Vec{30, 3, 0}, Vec{0, 3, 0}, Vec{0, 20, 0}, Vec{30, 20, 0})
	if _, err := Build(u, circle(t, 2), Options{}); err == nil {
		t.Fatal("overlapping legs accepted")
	}
	// unknown cap, hollow profile with a pointed cap
	if _, err := Build(lin(Vec{0, 0, 0}, Vec{9, 0, 0}), circle(t, 1), Options{CapEnd: "square"}); code(err) != CodeSweepFailed {
		t.Fatalf("cap: %v", err)
	}
	hol, _ := profile.New("hollow_circle", profile.Params{Num: map[string]float64{"outer_radius": 3, "inner_radius": 2}})
	if _, err := Build(lin(Vec{0, 0, 0}, Vec{9, 0, 0}), hol, Options{CapEnd: "point"}); code(err) != CodeSweepFailed {
		t.Fatalf("hollow cap: %v", err)
	}
	// a closed path takes whole turns of twist only
	if _, err := Build(bend, circle(t, 1), Options{TotalTwist: 45}); code(err) != CodeSweepFailed {
		t.Fatalf("closed twist: %v", err)
	}
	// a profile with two outer loops cannot be swept as one
	two, _ := profile.Custom([]profile.Contour{
		{Closed: true, Points: []profile.Vec2{{0, 0}, {1, 0}, {1, 1}, {0, 1}}},
		{Closed: true, Points: []profile.Vec2{{5, 0}, {6, 0}, {6, 1}, {5, 1}}},
	})
	if _, err := Build(lin(Vec{0, 0, 0}, Vec{9, 0, 0}), two, Options{}); code(err) != CodeProfileInvalid {
		t.Fatalf("two outers: %v", err)
	}
}

func TestCapsExtendTheLoft(t *testing.T) {
	p := smoothLine(Vec{0, 0, 0}, Vec{0, 0, 20})
	plan, err := Build(p, circle(t, 2), Options{CapStart: "round", CapEnd: "point"})
	if err != nil {
		t.Fatal(err)
	}
	b := plan.Body
	if b.ApexStart == nil || b.ApexEnd == nil || b.CapStartN == 0 || b.CapEndN == 0 {
		t.Fatalf("caps missing: %+v", b)
	}
	if b.CapStartRuled || !b.CapEndRuled {
		t.Fatal("round caps are smooth, pointed caps are cones")
	}
	// the round start cap bulges backwards by the radius, the point cap adds its height forwards
	if math.Abs(b.ApexStart[2]+2) > 1e-9 || math.Abs(b.ApexEnd[2]-22) > 1e-9 {
		t.Fatalf("apex %v %v", *b.ApexStart, *b.ApexEnd)
	}
	// rings shrink toward the apex
	first := b.Sections[0]
	if first.SX >= b.Sections[b.CapStartN].SX {
		t.Fatalf("start cap rings must shrink toward the apex: %v vs %v", first.SX, b.Sections[b.CapStartN].SX)
	}
	// every section travels along +Z so the whole loft is one oriented sweep
	for i, s := range b.Sections {
		if z := cross(s.X, s.Y); z[2] < 0.999 {
			t.Fatalf("section %d runs backwards: %v", i, z)
		}
	}
	want := math.Pi*4*20 + 2.0/3*math.Pi*8 + math.Pi*4*2.0/3
	if math.Abs(plan.ExpectedVolume-want)/want > 0.01 {
		t.Fatalf("expected volume %v want %v", plan.ExpectedVolume, want)
	}
}

func TestTwoCornersAxesStayConsistent(t *testing.T) {
	// corners turning about different axes: each corner section must be the leg frame turned by
	// half its corner, so the two sections of a leg describe the same prism
	plan, err := Build(lin(Vec{0, 0, 0}, Vec{30, 0, 0}, Vec{30, 40, 0}, Vec{30, 40, 25}), rect(t, 6, 4), Options{})
	if err != nil {
		t.Fatal(err)
	}
	s := plan.Body.Sections
	if len(s) != 4 {
		t.Fatalf("sections %d", len(s))
	}
	// on the middle leg (along +Y) the profile's normal at both ends, un-mitered, is the leg
	// frame: compare the leg frame recovered from each end by undoing the half turn
	back := func(sec Section, from, to Vec, sign float64) Vec {
		return turnFrame(sec.X, from, to, sign*0.5)
	}
	legA := back(s[1], Vec{1, 0, 0}, Vec{0, 1, 0}, -1) // vertex 1 sits between leg 0 (+X) and leg 1 (+Y)
	_ = legA
	d1, d2 := Vec{0, 1, 0}, Vec{0, 0, 1}
	endFrame := back(s[2], d1, d2, -1)
	startFrame := turnFrame(s[1].X, Vec{1, 0, 0}, Vec{0, 1, 0}, 0.5)
	for k := 0; k < 3; k++ {
		if math.Abs(endFrame[k]-startFrame[k]) > 1e-9 {
			t.Fatalf("leg frame differs between its two ends: %v vs %v", startFrame, endFrame)
		}
	}
}
