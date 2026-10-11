// Package sweep turns a path and a profile into the sections a kernel sweep lofts through. It
// is pure Go: sampling, parallel-transport frames, per-point width/height/twist, mitered
// corners for polylines, end caps and the validity checks (curvature, self-intersection).
package sweep

import (
	"errors"
	"fmt"
	"math"
	"sort"

	"github.com/accelone/simoncad/internal/geometry/curve"
	"github.com/accelone/simoncad/internal/geometry/profile"
)

type Vec = [3]float64

// Error codes (the features package re-exports them as its machine-readable codes).
const (
	CodePathInvalid        = "PATH_INVALID"
	CodePathSelfIntersect  = "PATH_SELF_INTERSECTION"
	CodeProfileInvalid     = "PROFILE_INVALID"
	CodeSweepSelfIntersect = "SWEEP_SELF_INTERSECTION"
	CodeExcessiveCurvature = "EXCESSIVE_CURVATURE"
	CodeSweepFailed        = "SWEEP_FAILED"
)

// Error is a planning failure with a stable code and an actionable hint.
type Error struct{ Code, Msg, Hint string }

func (e *Error) Error() string {
	if e.Hint == "" {
		return e.Msg
	}
	return e.Msg + " (" + e.Hint + ")"
}

func fail(code, hint, f string, a ...any) error { return &Error{code, fmt.Sprintf(f, a...), hint} }

// AsError returns the *Error inside err.
func AsError(err error) (*Error, bool) {
	var e *Error
	ok := errors.As(err, &e)
	return e, ok
}

// Warning is a non-fatal finding.
type Warning struct{ Code, Message string }

// Section places the profile: origin, in-plane axes (X, Y) and the scale applied to the profile.
type Section struct {
	Origin, X, Y Vec
	SX, SY       float64
}

// Contour is one profile loop: an analytic ellipse, a polygon shared by all sections, or one
// polygon per section.
type Contour struct {
	Ellipse    bool
	Hole       bool
	RX, RY     float64
	Points     [][2]float64
	PerSection [][][2]float64
}

// Job is one kernel sweep call.
type Job struct {
	Sections           []Section
	Contours           []Contour
	Closed, Ruled      bool
	ApexStart, ApexEnd *Vec
	// Cap rings at the start / end of Sections (they run to the apex) and whether the cap is a
	// cone (ruled) rather than a smooth dome.
	CapStartN, CapEndN         int
	CapStartRuled, CapEndRuled bool
}

// Options tune the plan. Zero values pick the defaults.
type Options struct {
	Tolerance   float64 // chord deviation in mm (default 0.05)
	MaxSections int     // default 600
	CapStart    string  // flat (default), round, point
	CapEnd      string
	CapLength   float64 // height of a pointed cap; default the profile radius
	Up          Vec     // seeds the first frame's normal (the profile's local X)
	TotalTwist  float64 // extra degrees of twist spread evenly along the path
}

// Plan is the result: the body sweep, optional end-cap sweeps to union with it, and findings.
type Plan struct {
	Body      Job
	Warnings  []Warning
	Length    float64
	MinRadius float64 // +Inf for straight paths
	Sections  int
	// ExpectedVolume is the analytic volume of the sweep (the integral of the section area along
	// the path; exact for a centred profile). The feature compares the kernel's result with it
	// to catch a loft that went unstable without being reported invalid.
	ExpectedVolume float64
}

const (
	// A bend whose radius is below minBendRatio x the profile reach makes the tube's inner surface
	// (almost) fold over itself; below tightBendRatio x it is valid but strongly curved.
	minBendRatio   = 1.15
	tightBendRatio = 1.6

	defaultTol      = 0.05
	defaultMaxSecs  = 600
	twistStepDeg    = 5.0
	roundCapRings   = 8
	maxMiterDegrees = 160.0
)

func (o *Options) defaults() {
	if o.Tolerance <= 0 {
		o.Tolerance = defaultTol
	}
	if o.MaxSections <= 0 {
		o.MaxSections = defaultMaxSecs
	}
	if o.CapStart == "" {
		o.CapStart = "flat"
	}
	if o.CapEnd == "" {
		o.CapEnd = "flat"
	}
}

func validCap(c string) bool { return c == "flat" || c == "round" || c == "point" }

// ---- vector helpers ---------------------------------------------------------------------

func sub(a, b Vec) Vec         { return Vec{a[0] - b[0], a[1] - b[1], a[2] - b[2]} }
func add(a, b Vec) Vec         { return Vec{a[0] + b[0], a[1] + b[1], a[2] + b[2]} }
func mul(a Vec, s float64) Vec { return Vec{a[0] * s, a[1] * s, a[2] * s} }
func dot(a, b Vec) float64     { return a[0]*b[0] + a[1]*b[1] + a[2]*b[2] }
func norm(a Vec) float64       { return math.Sqrt(dot(a, a)) }
func unit(a Vec) Vec {
	n := norm(a)
	if n < 1e-300 {
		return Vec{}
	}
	return mul(a, 1/n)
}

// ---- profile contours -------------------------------------------------------------------

type shape struct {
	contours []Contour // shared definition (ellipses or polygons)
	polys    [][][2]float64
	holes    []bool
	radius   float64 // max distance of the profile from its origin
	area     float64 // net area of the profile
}

func shapeOf(prof *profile.Profile) (*shape, error) {
	if is := prof.Validate(); profile.HasError(is) {
		for _, i := range is {
			if i.Severity == "error" {
				return nil, fail(CodeProfileInvalid, "fix the profile before sweeping it", "%s", i.Message)
			}
		}
	}
	sh := &shape{radius: prof.MaxRadius(), area: prof.Area()}
	outers := 0
	for _, c := range prof.Contours {
		if !c.Hole {
			outers++
		}
	}
	if outers != 1 {
		return nil, fail(CodeProfileInvalid, "split it into one sweep per outer contour", "a sweep profile needs exactly one outer contour (it has %d)", outers)
	}
	// outer first, then holes
	order := make([]profile.Contour, 0, len(prof.Contours))
	for _, c := range prof.Contours {
		if !c.Hole {
			order = append(order, c)
		}
	}
	for _, c := range prof.Contours {
		if c.Hole {
			order = append(order, c)
		}
	}
	for _, c := range order {
		poly := make([][2]float64, len(c.Points))
		copy(poly, c.Points)
		sh.polys = append(sh.polys, poly)
		sh.holes = append(sh.holes, c.Hole)
	}
	// analytic sections for round presets keep the surface exact
	if p := prof.Params; p != nil {
		switch prof.Type {
		case "circle":
			sh.contours = []Contour{{Ellipse: true, RX: p["radius"], RY: p["radius"]}}
		case "ellipse":
			sh.contours = []Contour{{Ellipse: true, RX: p["radius_x"], RY: p["radius_y"]}}
		case "hollow_circle":
			sh.contours = []Contour{
				{Ellipse: true, RX: p["outer_radius"], RY: p["outer_radius"]},
				{Ellipse: true, Hole: true, RX: p["inner_radius"], RY: p["inner_radius"]},
			}
		}
	}
	if sh.contours != nil { // analytic sections: exact ellipse areas, not the polygon's
		sh.area = 0
		for _, c := range sh.contours {
			a := math.Pi * c.RX * c.RY
			if c.Hole {
				a = -a
			}
			sh.area += a
		}
	}
	if sh.contours == nil {
		for i, poly := range sh.polys {
			sh.contours = append(sh.contours, Contour{Hole: sh.holes[i], Points: poly})
		}
	}
	return sh, nil
}

// support is how far the outer contour extends from the origin in the local direction (dx, dy)
// (a unit vector) after scaling by (w, h): the support function of the profile. The inside of a
// bend folds over itself when the bend radius is smaller than this reach toward the bend's centre.
func (s *shape) support(dx, dy, w, h float64) float64 {
	best := 0.0
	if len(s.contours) > 0 && s.contours[0].Ellipse {
		c := s.contours[0]
		best = math.Hypot(c.RX*w*dx, c.RY*h*dy)
	} else if len(s.polys) > 0 {
		for _, p := range s.polys[0] {
			best = math.Max(best, p[0]*w*dx+p[1]*h*dy)
		}
	}
	return math.Max(best, 0)
}

func (s *shape) hollow() bool {
	for _, h := range s.holes {
		if h {
			return true
		}
	}
	return false
}

// ---- keyed per-point properties ----------------------------------------------------------

type keyed struct {
	s           []float64 // arc length of each key (ascending)
	w, h, twist []float64
}

func (k *keyed) at(s float64) (w, h, tw float64) {
	n := len(k.s)
	if n == 1 || s <= k.s[0] {
		return k.w[0], k.h[0], k.twist[0]
	}
	if s >= k.s[n-1] {
		return k.w[n-1], k.h[n-1], k.twist[n-1]
	}
	i := sort.SearchFloat64s(k.s, s)
	if k.s[i] == s {
		return k.w[i], k.h[i], k.twist[i]
	}
	t := (s - k.s[i-1]) / (k.s[i] - k.s[i-1])
	return k.w[i-1] + t*(k.w[i]-k.w[i-1]), k.h[i-1] + t*(k.h[i]-k.h[i-1]), k.twist[i-1] + t*(k.twist[i]-k.twist[i-1])
}

func (k *keyed) varies() bool {
	for i := 1; i < len(k.s); i++ {
		if k.w[i] != k.w[0] || k.h[i] != k.h[0] || k.twist[i] != k.twist[0] {
			return true
		}
	}
	return false
}

func (k *keyed) maxScale() float64 {
	m := 0.0
	for i := range k.w {
		m = math.Max(m, math.Max(k.w[i], k.h[i]))
	}
	return m
}

// ---- planning ---------------------------------------------------------------------------

// Build plans the sweep of prof along path.
func Build(path *curve.Path, prof *profile.Profile, opt Options) (*Plan, error) {
	opt.defaults()
	if !validCap(opt.CapStart) || !validCap(opt.CapEnd) {
		return nil, fail(CodeSweepFailed, "use flat, round or point", "unknown end cap %q/%q", opt.CapStart, opt.CapEnd)
	}
	if err := path.Validate(); err != nil {
		return nil, fail(CodePathInvalid, "", "%v", err)
	}
	sh, err := shapeOf(prof)
	if err != nil {
		return nil, err
	}
	if sh.hollow() && (opt.CapStart != "flat" || opt.CapEnd != "flat") && !path.Closed {
		return nil, fail(CodeSweepFailed, "use cap_start/cap_end \"flat\" for hollow profiles", "hollow profiles support flat end caps only")
	}
	if path.Closed && math.Mod(opt.TotalTwist, 360) != 0 {
		return nil, fail(CodeSweepFailed, "use a multiple of 360° (e.g. 360 or 720) for a closed path", "a closed path can only twist by whole turns, got %g°", opt.TotalTwist)
	}
	if path.Type == curve.Linear {
		return buildPolyline(path, sh, opt)
	}
	return buildSmooth(path, sh, opt)
}

func keysOf(path *curve.Path, starts []float64, total float64) *keyed {
	k := &keyed{}
	n := len(path.Points)
	for i := 0; i < n; i++ {
		s := total
		if i < len(starts) {
			s = starts[i]
		}
		w, h := path.Points[i].Scale()
		k.s = append(k.s, s)
		k.w, k.h, k.twist = append(k.w, w), append(k.h, h), append(k.twist, path.Points[i].Twist)
	}
	if path.Closed { // wrap back to the first point
		w, h := path.Points[0].Scale()
		k.s = append(k.s, total)
		k.w, k.h, k.twist = append(k.w, w), append(k.h, h), append(k.twist, path.Points[0].Twist)
	}
	// the last key of an open path must sit at the end
	if !path.Closed {
		k.s[n-1] = total
	}
	return k
}

func buildSmooth(path *curve.Path, sh *shape, opt Options) (*Plan, error) {
	c, err := curve.New(path)
	if err != nil {
		return nil, fail(CodePathInvalid, "", "%v", err)
	}
	L := c.Length()
	if L < 1e-6 {
		return nil, fail(CodePathInvalid, "move the points apart", "path has zero length")
	}
	keys := keysOf(path, c.SegmentStarts(), L)
	rEff := sh.radius * keys.maxScale()
	plan := &Plan{Length: L, MinRadius: math.Inf(1)}

	// ---- validity: curvature and self-intersection on a dense polyline
	dn := int(math.Min(2000, math.Max(96, L/0.25)))
	dense := c.ResampleUniform(dn)
	minR, at := curve.MinRadius(dense, path.Closed)
	if at >= 0 {
		plan.MinRadius = minR
	}
	// The profile may be thicker in some places (w/h per point) and not round: a bend folds the
	// inside of the tube only as far as the profile reaches toward the bend's centre, so measure
	// the profile's extent in that direction (in the local frame) at every dense sample.
	den0 := float64(len(dense) - 1)
	if path.Closed {
		den0 = float64(len(dense))
	}
	dn0 := len(dense)
	denseTan := make([]curve.Vec, dn0)
	denseTw := make([]float64, dn0)
	denseW, denseH := make([]float64, dn0), make([]float64, dn0)
	for i := range dense {
		sArc := L * float64(i) / den0
		denseTan[i] = c.At(sArc / L).T
		denseW[i], denseH[i], denseTw[i] = keys.at(sArc)
		denseTw[i] += opt.TotalTwist * sArc / L
	}
	denseFr := curve.FramesWithTangents(dense, denseTan, path.Closed, denseTw, opt.Up)
	radiusAt := func(i int) float64 { return sh.radius * math.Max(denseW[i], denseH[i]) }
	reachAt := func(i int) float64 {
		if denseFr == nil {
			return radiusAt(i)
		}
		prev, next := dense[(i+dn0-1)%dn0], dense[(i+1)%dn0]
		ctr := curve.Vec{prev[0] - 2*dense[i][0] + next[0], prev[1] - 2*dense[i][1] + next[1], prev[2] - 2*dense[i][2] + next[2]}
		l := math.Sqrt(ctr[0]*ctr[0] + ctr[1]*ctr[1] + ctr[2]*ctr[2])
		if l < 1e-12 {
			return 0 // straight here: nothing folds
		}
		f := denseFr[i]
		cx := (ctr[0]*f.N[0] + ctr[1]*f.N[1] + ctr[2]*f.N[2]) / l
		cy := (ctr[0]*f.B[0] + ctr[1]*f.B[1] + ctr[2]*f.B[2]) / l
		return sh.support(cx, cy, denseW[i], denseH[i])
	}
	tight := false
	for i, r := range curve.Radii(dense, path.Closed) {
		reach := reachAt(i)
		if r < minBendRatio*reach {
			// the inner side of the tube folds over itself (or nearly: a cusp the kernel cannot mesh)
			need := minBendRatio * reach
			return nil, fail(CodeExcessiveCurvature,
				fmt.Sprintf("increase the bend radius to at least %.3g mm or shrink the profile to a radius below %.3g mm there", need, r/minBendRatio),
				"the path bends with a radius of %.3g mm where the profile reaches %.3g mm from the path (the bend must be at least %.3g times the reach)", r, reach, minBendRatio)
		}
		if !tight && r < tightBendRatio*reach {
			tight = true
			plan.Warnings = append(plan.Warnings, Warning{"TIGHT_BEND", fmt.Sprintf("bend radius %.3g mm is close to the profile reach %.3g mm: the inside of the bend is very curved", r, reach)})
		}
	}
	if err := checkSelfIntersection(dense, path.Closed, L, rEff, radiusAt); err != nil {
		return nil, err
	}
	plan.Warnings = append(plan.Warnings, cornerWarnings(path, c)...)

	// ---- sample positions: runs of evenly spaced sections, each spaced by its own tightest bend
	ss, coarsened := runSamples(curve.Radii(dense, path.Closed), path.Closed, L, opt.Tolerance, opt.MaxSections)
	if coarsened {
		plan.Warnings = append(plan.Warnings, Warning{"SECTION_LIMIT", fmt.Sprintf("capped at %d sections; raise max_sections for a finer sweep", opt.MaxSections)})
	}
	if keys.varies() { // put a section exactly where w/h/twist change direction
		ss = snapToKeys(ss, keys.s, L, path.Closed)
	}
	pts := make([]curve.Vec, len(ss))
	tans := make([]curve.Vec, len(ss))
	twist := make([]float64, len(ss))
	ws, hs := make([]float64, len(ss)), make([]float64, len(ss))
	for i, s := range ss {
		e := c.At(s / L)
		pts[i], tans[i] = e.P, e.T
		ws[i], hs[i], twist[i] = keys.at(s)
		twist[i] += opt.TotalTwist * s / L
	}
	frames := curve.FramesWithTangents(pts, tans, path.Closed, twist, opt.Up)
	if frames == nil {
		return nil, fail(CodeSweepFailed, "", "could not build frames along the path")
	}
	body := Job{Closed: path.Closed, Contours: sh.contours}
	for i, f := range frames {
		body.Sections = append(body.Sections, Section{Origin: f.P, X: f.N, Y: f.B, SX: ws[i], SY: hs[i]})
	}
	if !path.Closed {
		appendCap(&body, sh, false, opt.CapStart, opt.CapLength)
		appendCap(&body, sh, true, opt.CapEnd, opt.CapLength)
	} else if opt.CapStart != "flat" || opt.CapEnd != "flat" {
		plan.Warnings = append(plan.Warnings, Warning{"CAPS_IGNORED", "a closed path has no ends; end caps were ignored"})
	}
	// analytic volume: trapezoid of the scaled section area along the arc length
	for i := 0; i+1 < len(ss); i++ {
		a0, a1 := sh.area*ws[i]*hs[i], sh.area*ws[i+1]*hs[i+1]
		plan.ExpectedVolume += (a0 + a1) / 2 * (ss[i+1] - ss[i])
	}
	if path.Closed {
		last := len(ss) - 1
		a0, a1 := sh.area*ws[last]*hs[last], sh.area*ws[0]*hs[0]
		plan.ExpectedVolume += (a0 + a1) / 2 * (L - ss[last])
	} else {
		plan.ExpectedVolume += capVolume(sh, ws[0], hs[0], opt.CapStart, opt.CapLength)
		plan.ExpectedVolume += capVolume(sh, ws[len(ws)-1], hs[len(hs)-1], opt.CapEnd, opt.CapLength)
	}
	plan.Body = body
	plan.Sections = len(body.Sections)
	return plan, nil
}

// capVolume is the volume of an end cap: a hemisphere-like round cap (2/3 of area*radius) or a
// cone (1/3 of area*height).
func capVolume(sh *shape, w, h float64, kind string, capLen float64) float64 {
	area := sh.area * w * h
	r := sh.radius * math.Max(w, h)
	switch kind {
	case "round":
		return area * r * 2 / 3
	case "point":
		if capLen <= 0 {
			capLen = r
		}
		return area * capLen / 3
	}
	return 0
}

// checkSelfIntersection fails when two parts of the path that are far apart along it come
// closer than the swept thickness.
func checkSelfIntersection(dense []Vec, closed bool, L, rEff float64, reachAt func(int) float64) error {
	n := len(dense)
	step := L / float64(n-1)
	if closed {
		step = L / float64(n)
	}
	gap := int(math.Ceil(3*rEff/step)) + 2 // only parts separated by more than ~3 radii along the path
	eps := 2 * rEff * 0.999
	var worst *curve.Intersection
	hits := curve.SelfIntersectionsGap(dense, closed, eps, gap)
	for k := range hits {
		h := hits[k]
		// two parts clash when they come closer than the sum of their local reaches
		if h.Dist >= (reachAt(h.A)+reachAt(h.B))*0.999 {
			continue
		}
		if worst == nil || h.Dist < worst.Dist {
			worst = &hits[k]
		}
	}
	if worst == nil {
		return nil
	}
	at := dense[worst.A]
	if worst.Dist < 1e-3 {
		return fail(CodePathSelfIntersect,
			"reroute the path so it does not cross itself, or split it into separate paths",
			"the path crosses itself near (%.3g, %.3g, %.3g)", at[0], at[1], at[2])
	}
	return fail(CodeSweepSelfIntersect,
		fmt.Sprintf("keep parts of the path at least %.3g mm apart or use a thinner profile", 2*rEff),
		"the swept solid would overlap itself near (%.3g, %.3g, %.3g): two parts of the path are only %.3g mm apart", at[0], at[1], at[2], worst.Dist)
}

func cornerWarnings(path *curve.Path, c *curve.Curve) []Warning {
	var out []Warning
	n := len(c.Segs)
	for i := 0; i < n; i++ {
		j := i + 1
		if j == n {
			if !path.Closed {
				break
			}
			j = 0
		}
		a, b := unit(c.Segs[i].Deriv(1)), unit(c.Segs[j].Deriv(0))
		if a == (Vec{}) || b == (Vec{}) {
			continue
		}
		if ang := math.Acos(math.Max(-1, math.Min(1, dot(a, b)))) * 180 / math.Pi; ang > 10 {
			out = append(out, Warning{"SHARP_CORNER", fmt.Sprintf("the tangent turns %.0f° at point %d; a smooth sweep rounds it — use a linear path for a mitered corner", ang, j)})
		}
	}
	return out
}

const (
	roundCapRingsSmooth = 10
	pointCapRings       = 6
)

// appendCap extends the sweep with a rounded or pointed end: rings that shrink along the
// tangent and finish at an apex vertex. They join the same loft as the body, so the cap is
// one continuous surface with no boolean (a union of coincident end faces is fragile).
func appendCap(job *Job, sh *shape, atEnd bool, kind string, capLen float64) {
	if kind == "flat" || len(job.Sections) == 0 {
		return
	}
	idx := 0
	if atEnd {
		idx = len(job.Sections) - 1
	}
	s := job.Sections[idx]
	tan := unit(cross(s.X, s.Y))
	dir := tan
	if !atEnd {
		dir = mul(tan, -1)
	}
	// reach of the profile at this end: polygons of a mitered body are pre-scaled
	r := sh.radius * math.Max(s.SX, s.SY)
	for _, c := range job.Contours {
		if c.PerSection != nil && !c.Hole {
			r = 0
			for _, p := range c.PerSection[idx] {
				r = math.Max(r, math.Hypot(p[0], p[1]))
			}
		}
	}
	rings, size := roundCapRingsSmooth, r
	if kind == "point" {
		rings, size = pointCapRings, capLen
		if size <= 0 {
			size = r
		}
	}
	var ext []Section
	perSection := job.Contours[0].PerSection != nil // polygons carry their own scale
	perRing := make([]float64, 0, rings)
	for j := 1; j < rings; j++ {
		var off, sc float64
		if kind == "point" {
			t := float64(j) / float64(rings)
			off, sc = size*t, 1-t
		} else {
			th := float64(j) * (math.Pi / 2) / float64(rings)
			off, sc = size*math.Sin(th), math.Cos(th)
		}
		perRing = append(perRing, sc)
		ns := Section{Origin: add(s.Origin, mul(dir, off)), X: s.X, Y: s.Y, SX: s.SX, SY: s.SY}
		if !perSection {
			ns.SX, ns.SY = s.SX*sc, s.SY*sc
		}
		ext = append(ext, ns)
	}
	apex := add(s.Origin, mul(dir, size))
	// per-section polygons for the new rings (shared / ellipse contours scale through SX, SY)
	for ci := range job.Contours {
		c := &job.Contours[ci]
		if c.PerSection == nil {
			continue
		}
		base := c.PerSection[idx]
		var rows [][][2]float64
		for _, sc := range perRing {
			row := make([][2]float64, len(base))
			for k, p := range base {
				row[k] = [2]float64{p[0] * sc, p[1] * sc}
			}
			rows = append(rows, row)
		}
		if atEnd {
			c.PerSection = append(c.PerSection, rows...)
		} else {
			rev := make([][][2]float64, len(rows))
			for k := range rows {
				rev[len(rows)-1-k] = rows[k]
			}
			c.PerSection = append(rev, c.PerSection...)
		}
	}
	if atEnd {
		job.Sections = append(job.Sections, ext...)
		job.ApexEnd = &apex
		job.CapEndN, job.CapEndRuled = len(ext), kind == "point"
	} else {
		rev := make([]Section, len(ext))
		for k := range ext {
			rev[len(ext)-1-k] = ext[k]
		}
		job.Sections = append(rev, job.Sections...)
		job.ApexStart = &apex
		job.CapStartN, job.CapStartRuled = len(ext), kind == "point"
	}
}

func cross(a, b Vec) Vec {
	return Vec{a[1]*b[2] - a[2]*b[1], a[2]*b[0] - a[0]*b[2], a[0]*b[1] - a[1]*b[0]}
}

// SectionsPerRun is how many consecutive sections the kernel lofts together
// (kChunk in native/occ/sweep.cpp). Sections inside one run are evenly spaced, so each loft is
// well conditioned; spacing may differ from one run to the next.
const SectionsPerRun = 10

// runSamples picks the arc lengths of the sections. The loft interpolates them with cubic
// splines, so the spacing is limited by the local bend radius R: a chord-deviation limit
// (3*sqrt(8*R*tol)) and an angle limit (about 17° of turn per step, 0.3*R). Each run of
// SectionsPerRun sections is spaced by the tightest bend inside it: bends get dense sections,
// straight stretches few. radii holds the radius of curvature at the uniformly spaced dense points.
func runSamples(radii []float64, closed bool, L, tol float64, maxN int) (samples []float64, coarsened bool) {
	n := len(radii)
	den := float64(n - 1)
	if closed {
		den = float64(n)
	}
	d := L / den
	limit := make([]float64, n)
	straight := true
	for i, r := range radii {
		limit[i] = math.Inf(1)
		if !math.IsInf(r, 1) {
			straight = false
			limit[i] = math.Min(3*math.Sqrt(8*r*tol), 0.3*r)
		}
	}
	if straight { // a straight path needs only its ends
		if closed {
			return ringSamples(L, 8), false
		}
		return []float64{0, L}, false
	}
	hCap := L / float64(SectionsPerRun-1)
	// tightest limit over the arc interval [a, b]
	over := func(a, b float64) float64 {
		lo, hi := int(math.Floor(a/d)), int(math.Ceil(b/d))
		m := math.Inf(1)
		for i := lo; i <= hi; i++ {
			j := i
			if closed {
				j = ((i % n) + n) % n
			} else if j >= n {
				j = n - 1
			}
			m = math.Min(m, limit[j])
		}
		return m
	}
	scale := 1.0
	var out []float64
	for attempt := 0; attempt < 14; attempt++ {
		out = []float64{0}
		for s := 0.0; s < L-1e-9*L; {
			// the largest h <= hCap with h <= the tightest bend limit over its own run [s, s+9h]
			// (the limit only falls as the run grows, so bisection finds it)
			fits := func(h float64) bool {
				return h <= over(s, math.Min(L, s+float64(SectionsPerRun-1)*h))*scale
			}
			h := hCap
			if !fits(h) {
				lo, hi := 0.0, hCap // lo always fits; at lo=0 the run is a point
				for it := 0; it < 30; it++ {
					mid := (lo + hi) / 2
					if fits(mid) {
						lo = mid
					} else {
						hi = mid
					}
				}
				h = math.Max(lo, 1e-6*L)
			}
			for k := 1; k < SectionsPerRun; k++ {
				if p := s + float64(k)*h; p < L-1e-9*L {
					out = append(out, p)
				}
			}
			s += float64(SectionsPerRun-1) * h
		}
		if !closed {
			if len(out) > 2 && L-out[len(out)-1] < 0.3*(out[len(out)-1]-out[len(out)-2]) {
				out = out[:len(out)-1]
			}
			out = append(out, L)
		}
		if len(out) <= maxN {
			break
		}
		scale *= 1.35 // too many sections: allow coarser spacing everywhere and walk again
	}
	if closed && len(out) < 8 {
		return ringSamples(L, 8), scale > 1
	}
	return out, scale > 1
}

func ringSamples(L float64, n int) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = L * float64(i) / float64(n)
	}
	return out
}

// snapToKeys moves the section nearest to each key (a control point where width, height or twist
// change slope) onto it, so the interpolated scale has no kink between sections. Moving a section
// keeps the spacing even; inserting one next to a neighbour would leave a sliver gap, which makes
// the loft overshoot. A key is inserted only when it is farther than half a spacing from every
// free section.
func snapToKeys(ss, keys []float64, L float64, closed bool) []float64 {
	out := append([]float64(nil), ss...)
	moved := make([]bool, len(out))
	for _, k := range keys {
		if k <= 1e-9*L || k >= L-1e-9*L { // the path's ends are sections already
			continue
		}
		best, bd := -1, math.Inf(1)
		for i, v := range out {
			if i == 0 || (!closed && i == len(out)-1) || moved[i] {
				continue
			}
			if d := math.Abs(v - k); d < bd {
				best, bd = i, d
			}
		}
		spacing := L / float64(len(out))
		if best >= 0 && bd <= 0.5*spacing {
			lo, hi := out[best-1], L
			if best+1 < len(out) {
				hi = out[best+1]
			}
			if k > lo && k < hi { // stays between its neighbours
				out[best] = k
				moved[best] = true
				continue
			}
		}
		// too far from every free section: add one
		out = append(out, k)
		moved = append(moved, true)
		sort.Float64s(out)
		// moved flags are positional; recompute conservatively after the sort
		moved = make([]bool, len(out))
		for i, v := range out {
			for _, kk := range keys {
				if math.Abs(v-kk) < 1e-9*L {
					moved[i] = true
				}
			}
		}
	}
	return out
}
