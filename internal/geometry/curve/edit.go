package curve

import (
	"fmt"
	"math"
)

// Clone deep-copies a path.
func (p *Path) Clone() *Path {
	q := &Path{Type: p.Type, Closed: p.Closed, Points: make([]Point, len(p.Points))}
	for i, pt := range p.Points {
		q.Points[i] = pt
		if pt.In != nil {
			v := *pt.In
			q.Points[i].In = &v
		}
		if pt.Out != nil {
			v := *pt.Out
			q.Points[i].Out = &v
		}
	}
	return q
}

func (p *Path) checkIndex(i int, upper int) error {
	if i < 0 || i >= upper {
		return fmt.Errorf("point index %d out of range (path has %d points)", i, len(p.Points))
	}
	return nil
}

// Add appends a point.
func (p *Path) Add(pt Point) { p.Points = append(p.Points, pt) }

// Insert places pt before index i (i == len appends).
func (p *Path) Insert(i int, pt Point) error {
	if err := p.checkIndex(i, len(p.Points)+1); err != nil {
		return err
	}
	p.Points = append(p.Points, Point{})
	copy(p.Points[i+1:], p.Points[i:])
	p.Points[i] = pt
	return nil
}

// Remove deletes point i, refusing to go below the minimum for the path.
func (p *Path) Remove(i int) error {
	if err := p.checkIndex(i, len(p.Points)); err != nil {
		return err
	}
	min := 2
	if p.Closed {
		min = 3
	}
	if len(p.Points) <= min {
		return fmt.Errorf("cannot remove point %d: a %s path needs at least %d points", i, openWord(p.Closed), min)
	}
	p.Points = append(p.Points[:i], p.Points[i+1:]...)
	return nil
}

// Move relocates point i, carrying its Bezier handles with it.
func (p *Path) Move(i int, pos Vec) error {
	if err := p.checkIndex(i, len(p.Points)); err != nil {
		return err
	}
	d := sub(pos, p.Points[i].P)
	p.Points[i].P = pos
	if h := p.Points[i].In; h != nil {
		v := add(*h, d)
		p.Points[i].In = &v
	}
	if h := p.Points[i].Out; h != nil {
		v := add(*h, d)
		p.Points[i].Out = &v
	}
	return nil
}

// SetHandle sets the in or out handle (absolute position) of point i.
func (p *Path) SetHandle(i int, out bool, pos Vec) error {
	if err := p.checkIndex(i, len(p.Points)); err != nil {
		return err
	}
	if out {
		p.Points[i].Out = &pos
	} else {
		p.Points[i].In = &pos
	}
	return nil
}

// Reverse flips the direction of travel.
func (p *Path) Reverse() {
	n := len(p.Points)
	for i, j := 0, n-1; i < j; i, j = i+1, j-1 {
		p.Points[i], p.Points[j] = p.Points[j], p.Points[i]
	}
	for i := range p.Points {
		p.Points[i].In, p.Points[i].Out = p.Points[i].Out, p.Points[i].In
	}
}

// SetClosed closes or reopens the path.
func (p *Path) SetClosed(closed bool) error {
	if closed && len(p.Points) < 3 {
		return fmt.Errorf("cannot close a path with %d points (need 3)", len(p.Points))
	}
	p.Closed = closed
	return nil
}

// Convert switches the interpolation type. Converting to Bezier keeps the exact current
// shape by materializing handles; the other targets keep the control points and drop handles.
func (p *Path) Convert(t Type) error {
	if !t.Valid() {
		return fmt.Errorf("unknown path type %q", t)
	}
	if t == p.Type {
		return nil
	}
	if t == Bezier {
		segs := p.Segments()
		n := len(p.Points)
		for i := range p.Points {
			p.Points[i].In, p.Points[i].Out = nil, nil
		}
		for i, s := range segs {
			out, in := s.P[1], s.P[2]
			// approximating types (B-spline) start a segment away from the control point:
			// the Bezier points become the curve's own segment ends
			p.Points[i].P = s.P[0]
			p.Points[i].Out = &out
			p.Points[(i+1)%n].In = &in
		}
		if !p.Closed {
			p.Points[n-1].P = segs[len(segs)-1].P[3]
		}
		if !p.Closed { // an open path has no incoming/outgoing handle at its ends
			p.Points[0].In = nil
			p.Points[n-1].Out = nil
		}
	} else {
		for i := range p.Points {
			p.Points[i].In, p.Points[i].Out = nil, nil
		}
	}
	p.Type = t
	return nil
}

// Simplify drops control points (Douglas-Peucker on the control polygon) while keeping every
// remaining point within tol of the original polyline. End points of an open path stay.
func (p *Path) Simplify(tol float64) {
	n := len(p.Points)
	if n <= 2 || tol <= 0 {
		return
	}
	pts := make([]Vec, n)
	for i, pt := range p.Points {
		pts[i] = pt.P
	}
	keep := make([]bool, n)
	if p.Closed {
		// Anchor the point farthest from point 0 so the ring splits into two chains.
		far, fd := 0, -1.0
		for i := 1; i < n; i++ {
			if d := dist(pts[0], pts[i]); d > fd {
				far, fd = i, d
			}
		}
		keep[0], keep[far] = true, true
		douglasPeucker(pts, 0, far, tol, keep)
		// second chain wraps: unroll it to reuse the same routine
		wrap := append(append([]Vec{}, pts[far:]...), pts[0])
		wk := make([]bool, len(wrap))
		wk[0], wk[len(wrap)-1] = true, true
		douglasPeucker(wrap, 0, len(wrap)-1, tol, wk)
		for i := 0; i < len(wrap)-1; i++ {
			if wk[i] {
				keep[far+i] = true
			}
		}
		if cnt := countTrue(keep); cnt < 3 {
			return // simplifying would collapse the ring
		}
	} else {
		keep[0], keep[n-1] = true, true
		douglasPeucker(pts, 0, n-1, tol, keep)
	}
	out := p.Points[:0:0]
	for i, k := range keep {
		if k {
			out = append(out, p.Points[i])
		}
	}
	p.Points = out
}

func countTrue(b []bool) int {
	c := 0
	for _, v := range b {
		if v {
			c++
		}
	}
	return c
}

func douglasPeucker(pts []Vec, a, b int, tol float64, keep []bool) {
	if b <= a+1 {
		return
	}
	idx, md := -1, 0.0
	for i := a + 1; i < b; i++ {
		if d := distToSegment(pts[i], pts[a], pts[b]); d > md {
			idx, md = i, d
		}
	}
	if idx >= 0 && md > tol {
		keep[idx] = true
		douglasPeucker(pts, a, idx, tol, keep)
		douglasPeucker(pts, idx, b, tol, keep)
	}
}

func distToSegment(p, a, b Vec) float64 {
	ab := sub(b, a)
	l2 := dot(ab, ab)
	if l2 < 1e-24 {
		return dist(p, a)
	}
	t := math.Min(1, math.Max(0, dot(sub(p, a), ab)/l2))
	return dist(p, add(a, scale(ab, t)))
}

// SimplifyPolyline is Douglas-Peucker on a raw polyline (open).
func SimplifyPolyline(pts []Vec, tol float64) []Vec {
	n := len(pts)
	if n <= 2 || tol <= 0 {
		return pts
	}
	keep := make([]bool, n)
	keep[0], keep[n-1] = true, true
	douglasPeucker(pts, 0, n-1, tol, keep)
	out := make([]Vec, 0, n)
	for i, k := range keep {
		if k {
			out = append(out, pts[i])
		}
	}
	return out
}

// Smooth relaxes the control points toward their neighbours (Laplacian, lambda 0.5) for the
// given number of iterations. Ends of an open path stay fixed; handles move with their point.
func (p *Path) Smooth(iterations int) {
	n := len(p.Points)
	if n < 3 {
		return
	}
	for it := 0; it < iterations; it++ {
		next := make([]Vec, n)
		for i := range p.Points {
			if !p.Closed && (i == 0 || i == n-1) {
				next[i] = p.Points[i].P
				continue
			}
			a, b := p.Points[(i+n-1)%n].P, p.Points[(i+1)%n].P
			mid := scale(add(a, b), .5)
			next[i] = lerp(p.Points[i].P, mid, .5)
		}
		for i := range p.Points {
			_ = p.Move(i, next[i])
		}
	}
}
