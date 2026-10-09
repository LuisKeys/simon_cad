package builtin

import "math"

type pt struct{ x, y float64 }

func (a pt) sub(b pt) pt        { return pt{a.x - b.x, a.y - b.y} }
func (a pt) add(b pt) pt        { return pt{a.x + b.x, a.y + b.y} }
func (a pt) mul(s float64) pt   { return pt{a.x * s, a.y * s} }
func (a pt) dot(b pt) float64   { return a.x*b.x + a.y*b.y }
func (a pt) cross(b pt) float64 { return a.x*b.y - a.y*b.x }
func (a pt) len() float64       { return math.Hypot(a.x, a.y) }
func dist(a, b pt) float64      { return a.sub(b).len() }
func (a pt) unit() pt {
	l := a.len()
	if l < 1e-12 {
		return pt{}
	}
	return pt{a.x / l, a.y / l}
}

// poly is a closed polygon (the last point connects to the first). Material lies to the
// left of every edge: outer boundaries are counter-clockwise, holes clockwise.
type poly []pt

func (p poly) area() float64 {
	var s float64
	for i, a := range p {
		b := p[(i+1)%len(p)]
		s += a.cross(b)
	}
	return s / 2
}

func (p poly) bbox() (lo, hi pt) {
	lo, hi = pt{math.Inf(1), math.Inf(1)}, pt{math.Inf(-1), math.Inf(-1)}
	for _, a := range p {
		lo.x, lo.y = math.Min(lo.x, a.x), math.Min(lo.y, a.y)
		hi.x, hi.y = math.Max(hi.x, a.x), math.Max(hi.y, a.y)
	}
	return
}

// contains is the even-odd point-in-polygon test.
func (p poly) contains(q pt) bool {
	in := false
	for i, a := range p {
		b := p[(i+1)%len(p)]
		if (a.y > q.y) != (b.y > q.y) && q.x < (b.x-a.x)*(q.y-a.y)/(b.y-a.y)+a.x {
			in = !in
		}
	}
	return in
}

// clean removes repeated and collinear points.
func (p poly) clean() poly {
	out := make(poly, 0, len(p))
	for _, a := range p {
		if len(out) > 0 && dist(a, out[len(out)-1]) < 1e-5 {
			continue
		}
		out = append(out, a)
	}
	for len(out) > 1 && dist(out[0], out[len(out)-1]) < 1e-5 {
		out = out[:len(out)-1]
	}
	for changed := true; changed && len(out) >= 3; {
		changed = false
		res := make(poly, 0, len(out))
		for i, b := range out {
			a, c := out[(i+len(out)-1)%len(out)], out[(i+1)%len(out)]
			ac := c.sub(a)
			if l := ac.len(); l > 1e-9 && math.Abs(ac.cross(b.sub(a)))/l < 1e-4 && b.sub(a).dot(c.sub(b)) > 0 {
				changed = true
				continue
			}
			res = append(res, b)
		}
		out = res
	}
	return out
}

// selfIntersects reports whether two non-adjacent edges cross. It is quadratic, so very
// large polygons are not checked.
func (p poly) selfIntersects() bool {
	n := len(p)
	if n > 3000 {
		return false
	}
	for i := 0; i < n; i++ {
		a, b := p[i], p[(i+1)%n]
		for j := i + 2; j < n; j++ {
			if i == 0 && j == n-1 {
				continue
			}
			if segCross(a, b, p[j], p[(j+1)%n]) {
				return true
			}
		}
	}
	return false
}

func segCross(a, b, c, d pt) bool {
	d1, d2 := b.sub(a).cross(c.sub(a)), b.sub(a).cross(d.sub(a))
	d3, d4 := d.sub(c).cross(a.sub(c)), d.sub(c).cross(b.sub(c))
	return ((d1 > 1e-9 && d2 < -1e-9) || (d1 < -1e-9 && d2 > 1e-9)) && ((d3 > 1e-9 && d4 < -1e-9) || (d3 < -1e-9 && d4 > 1e-9))
}

// miterLimit caps how far a sharp corner's offset vertex may travel, in multiples of d.
const miterLimit = 3.0

// offsetLeft moves every edge d to its left (toward the material): outer boundaries shrink
// and holes grow for d > 0; a negative d grows outer boundaries. It returns nil when the
// polygon collapses or the result would cross itself, which is how thin features lose
// their inner walls in the preview.
func offsetLeft(p poly, d float64) poly {
	if len(p) < 3 {
		return nil
	}
	orig := p.area()
	cur := append(poly(nil), p...)
	for iter := 0; iter < 6; iter++ {
		n := len(cur)
		if n < 3 {
			return nil
		}
		nl := make([]pt, n)
		for i := range cur {
			u := cur[(i+1)%n].sub(cur[i]).unit()
			nl[i] = pt{-u.y, u.x}
		}
		out := make(poly, n)
		for i := range cur {
			n1, n2 := nl[(i+n-1)%n], nl[i]
			den := 1 + n1.dot(n2)
			m := n1.add(n2)
			if den < 1e-3 {
				m = n2.mul(2) // a reversal: fall back to the incoming edge's normal
				den = 2
			}
			v := m.mul(d / den)
			if l := v.len(); l > miterLimit*math.Abs(d) {
				v = v.mul(miterLimit * math.Abs(d) / l)
			}
			out[i] = cur[i].add(v)
		}
		// an edge that flipped direction has collapsed: drop its end vertex and retry
		bad := -1
		for i := range cur {
			j := (i + 1) % n
			if out[j].sub(out[i]).dot(cur[j].sub(cur[i])) <= 0 {
				bad = j
				break
			}
		}
		if bad >= 0 {
			cur = append(cur[:bad:bad], cur[bad+1:]...)
			continue
		}
		res := out.clean()
		if len(res) < 3 {
			return nil
		}
		a := res.area()
		if a*orig <= 0 || math.Abs(a) < 1e-6 {
			return nil
		}
		if res.selfIntersects() {
			return nil
		}
		return res
	}
	return nil
}
