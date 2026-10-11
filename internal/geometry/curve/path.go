// Package curve is the pure-Go path engine: interpolation, arc length, resampling,
// simplification, smoothing and rotation-minimizing frames. It has no kernel dependency, so
// it is shared by the sweep builder, the projection engine and the vectorizer.
package curve

import (
	"fmt"
	"math"
	"sort"
)

// Type is the interpolation mode of a path.
type Type string

const (
	Linear     Type = "linear"
	Bezier     Type = "bezier"
	CatmullRom Type = "catmull_rom"
	BSpline    Type = "bspline"
)

// Valid reports whether t is a known interpolation type.
func (t Type) Valid() bool {
	switch t {
	case Linear, Bezier, CatmullRom, BSpline:
		return true
	}
	return false
}

// Point is one control point. HandleIn/HandleOut are absolute positions (Bezier only); a nil
// handle collapses onto the point. Width and Height are scale factors of the swept profile
// (0 is read as 1), Twist is degrees of extra rotation about the tangent.
type Point struct {
	P     Vec
	In    *Vec
	Out   *Vec
	W, H  float64
	Twist float64
}

// Scale returns the width and height factors, defaulting an unset (0) factor to 1.
func (p Point) Scale() (w, h float64) {
	w, h = p.W, p.H
	if w == 0 {
		w = 1
	}
	if h == 0 {
		h = 1
	}
	return
}

// Path is an open or closed sequence of control points.
type Path struct {
	Type   Type
	Points []Point
	Closed bool
}

// Validate checks the type and the point count for the interpolation mode.
func (p *Path) Validate() error {
	if !p.Type.Valid() {
		return fmt.Errorf("unknown path type %q (use linear, bezier, catmull_rom or bspline)", p.Type)
	}
	min := 2
	if p.Closed {
		min = 3
	}
	if len(p.Points) < min {
		return fmt.Errorf("a %s %s path needs at least %d points, got %d", openWord(p.Closed), p.Type, min, len(p.Points))
	}
	for i, pt := range p.Points {
		for _, v := range pt.P {
			if math.IsNaN(v) || math.IsInf(v, 0) {
				return fmt.Errorf("point %d has a non-finite coordinate", i)
			}
		}
	}
	return nil
}

func openWord(closed bool) string {
	if closed {
		return "closed"
	}
	return "open"
}

// Seg is one cubic Bezier segment. Every interpolation type is converted to these.
type Seg struct{ P [4]Vec }

// At evaluates the segment at u in [0,1].
func (s Seg) At(u float64) Vec {
	v := 1 - u
	a, b, c, d := v*v*v, 3*v*v*u, 3*v*u*u, u*u*u
	var r Vec
	for i := range r {
		r[i] = a*s.P[0][i] + b*s.P[1][i] + c*s.P[2][i] + d*s.P[3][i]
	}
	return r
}

// Deriv is dB/du.
func (s Seg) Deriv(u float64) Vec {
	v := 1 - u
	a, b, c := 3*v*v, 6*v*u, 3*u*u
	var r Vec
	for i := range r {
		r[i] = a*(s.P[1][i]-s.P[0][i]) + b*(s.P[2][i]-s.P[1][i]) + c*(s.P[3][i]-s.P[2][i])
	}
	return r
}

// split divides the segment at u = 0.5 (de Casteljau).
func (s Seg) split() (Seg, Seg) {
	p := s.P
	m01, m12, m23 := lerp(p[0], p[1], .5), lerp(p[1], p[2], .5), lerp(p[2], p[3], .5)
	m012, m123 := lerp(m01, m12, .5), lerp(m12, m23, .5)
	m := lerp(m012, m123, .5)
	return Seg{[4]Vec{p[0], m01, m012, m}}, Seg{[4]Vec{m, m123, m23, p[3]}}
}

// flat is the max distance of the inner control points from the chord.
func (s Seg) flat() float64 {
	return math.Max(distToLine(s.P[1], s.P[0], s.P[3]), distToLine(s.P[2], s.P[0], s.P[3]))
}

func distToLine(p, a, b Vec) float64 {
	ab := sub(b, a)
	l2 := dot(ab, ab)
	if l2 < 1e-24 {
		return dist(p, a)
	}
	t := dot(sub(p, a), ab) / l2
	return dist(p, add(a, scale(ab, t)))
}

// Segments converts the path to cubic Bezier segments (n-1 open, n closed).
func (p *Path) Segments() []Seg {
	n := len(p.Points)
	if n < 2 {
		return nil
	}
	pos := func(i int) Vec { return p.Points[i].P }
	count := n - 1
	if p.Closed {
		count = n
	}
	// idx returns the control point at i, wrapping when closed and reflecting at the ends
	// of an open path so the curve passes through its first and last point.
	idx := func(i int) Vec {
		if p.Closed {
			return pos(((i % n) + n) % n)
		}
		if i < 0 {
			return sub(scale(pos(0), 2), pos(1))
		}
		if i > n-1 {
			return sub(scale(pos(n-1), 2), pos(n-2))
		}
		return pos(i)
	}
	out := make([]Seg, 0, count)
	for i := 0; i < count; i++ {
		a, b := pos(i), pos((i+1)%n)
		var s Seg
		switch p.Type {
		case Bezier:
			c1, c2 := a, b
			if h := p.Points[i].Out; h != nil {
				c1 = *h
			}
			if h := p.Points[(i+1)%n].In; h != nil {
				c2 = *h
			}
			s = Seg{[4]Vec{a, c1, c2, b}}
		case CatmullRom:
			p0, p3 := idx(i-1), idx(i+2)
			s = Seg{[4]Vec{a, add(a, scale(sub(b, p0), 1.0/6)), sub(b, scale(sub(p3, a), 1.0/6)), b}}
		case BSpline:
			p0, p1, p2, p3 := idx(i-1), idx(i), idx(i+1), idx(i+2)
			s = Seg{[4]Vec{
				scale(add(add(p0, scale(p1, 4)), p2), 1.0/6),
				scale(add(scale(p1, 2), p2), 1.0/3),
				scale(add(p1, scale(p2, 2)), 1.0/3),
				scale(add(add(p1, scale(p2, 4)), p3), 1.0/6),
			}}
		default: // Linear
			s = Seg{[4]Vec{a, lerp(a, b, 1.0/3), lerp(a, b, 2.0/3), b}}
		}
		out = append(out, s)
	}
	return out
}

// Gauss-Legendre 8-point nodes and weights on [-1,1].
var glX = [8]float64{-0.9602898564975363, -0.7966664774136267, -0.5255324099163290, -0.1834346424956498,
	0.1834346424956498, 0.5255324099163290, 0.7966664774136267, 0.9602898564975363}
var glW = [8]float64{0.1012285362903763, 0.2223810344533745, 0.3137066458778873, 0.3626837833783620,
	0.3626837833783620, 0.3137066458778873, 0.2223810344533745, 0.1012285362903763}

// segLen integrates |B'(u)| over [u0,u1] with 4 Gauss-Legendre panels.
func segLen(s Seg, u0, u1 float64) float64 {
	const panels = 4
	h := (u1 - u0) / panels
	total := 0.0
	for k := 0; k < panels; k++ {
		a := u0 + float64(k)*h
		mid, half := a+h/2, h/2
		for i := range glX {
			total += glW[i] * norm(s.Deriv(mid+half*glX[i]))
		}
		_ = a
	}
	return total * h / 2
}

// Eval is the result of evaluating a path: position and unit tangent.
type Eval struct {
	P, T Vec
}

// Curve is a path prepared for arc-length queries. It is immutable; build a new one after
// editing the path.
type Curve struct {
	Segs   []Seg
	cum    []float64 // cumulative length at the end of each segment
	Closed bool
}

// New prepares the arc-length table of a path.
func New(p *Path) (*Curve, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	segs := p.Segments()
	c := &Curve{Segs: segs, Closed: p.Closed, cum: make([]float64, len(segs))}
	acc := 0.0
	for i, s := range segs {
		acc += segLen(s, 0, 1)
		c.cum[i] = acc
	}
	return c, nil
}

// SegmentStarts is the arc length at the start of each segment (so control point i of a
// non-approximating path sits at SegmentStarts()[i]).
func (c *Curve) SegmentStarts() []float64 {
	out := make([]float64, len(c.cum))
	for i := 1; i < len(c.cum); i++ {
		out[i] = c.cum[i-1]
	}
	return out
}

// Length is the total arc length.
func (c *Curve) Length() float64 {
	if len(c.cum) == 0 {
		return 0
	}
	return c.cum[len(c.cum)-1]
}

// locate maps an arc length s to (segment, u).
func (c *Curve) locate(s float64) (int, float64) {
	total := c.Length()
	if s <= 0 {
		return 0, 0
	}
	if s >= total {
		return len(c.Segs) - 1, 1
	}
	i := sort.SearchFloat64s(c.cum, s)
	start := 0.0
	if i > 0 {
		start = c.cum[i-1]
	}
	segL := c.cum[i] - start
	if segL < 1e-300 {
		return i, 0
	}
	target := s - start
	lo, hi := 0.0, 1.0
	u := target / segL
	for it := 0; it < 40; it++ {
		l := segLen(c.Segs[i], 0, u)
		d := l - target
		if math.Abs(d) < 1e-12*math.Max(1, segL) {
			break
		}
		if d > 0 {
			hi = u
		} else {
			lo = u
		}
		sp := norm(c.Segs[i].Deriv(u))
		next := u - d/math.Max(sp, 1e-12)
		if next <= lo || next >= hi {
			next = (lo + hi) / 2
		}
		u = next
	}
	return i, u
}

// At evaluates the curve at normalized arc-length parameter t in [0,1] (clamped).
func (c *Curve) At(t float64) Eval {
	t = math.Min(1, math.Max(0, t))
	i, u := c.locate(t * c.Length())
	s := c.Segs[i]
	tan := unit(s.Deriv(u))
	if tan == (Vec{}) { // coincident control points: fall back to the chord
		tan = unit(sub(s.P[3], s.P[0]))
	}
	return Eval{P: s.At(u), T: tan}
}

// Resample flattens the curve so no chord deviates more than tol from it. Closed curves
// repeat nothing: the last point is not the first.
func (c *Curve) Resample(tol float64) []Vec {
	if tol <= 0 {
		tol = 1e-3
	}
	var out []Vec
	var rec func(s Seg, depth int)
	rec = func(s Seg, depth int) {
		if depth >= 18 || s.flat() <= tol {
			out = append(out, s.P[3])
			return
		}
		a, b := s.split()
		rec(a, depth+1)
		rec(b, depth+1)
	}
	if len(c.Segs) == 0 {
		return nil
	}
	out = append(out, c.Segs[0].P[0])
	for _, s := range c.Segs {
		rec(s, 0)
	}
	if c.Closed && len(out) > 1 && dist(out[0], out[len(out)-1]) < 1e-12 {
		out = out[:len(out)-1]
	}
	return dedupe(out, 1e-12)
}

// ResampleUniform returns n points equally spaced by arc length (n >= 2). A closed curve
// returns n distinct points without repeating the start.
func (c *Curve) ResampleUniform(n int) []Vec {
	if n < 2 {
		n = 2
	}
	out := make([]Vec, n)
	den := float64(n - 1)
	if c.Closed {
		den = float64(n)
	}
	for i := range out {
		out[i] = c.At(float64(i) / den).P
	}
	return out
}

func dedupe(pts []Vec, eps float64) []Vec {
	out := pts[:0:0]
	for _, p := range pts {
		if len(out) == 0 || dist(out[len(out)-1], p) > eps {
			out = append(out, p)
		}
	}
	return out
}

// Length of a polyline.
func PolyLength(pts []Vec, closed bool) float64 {
	l := 0.0
	for i := 1; i < len(pts); i++ {
		l += dist(pts[i-1], pts[i])
	}
	if closed && len(pts) > 1 {
		l += dist(pts[len(pts)-1], pts[0])
	}
	return l
}
