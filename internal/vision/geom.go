package vision

import "math"

type pt = [2]float64

// arcCircle returns the center and radius of the circle through three points.
func arcCircle(a, m, b pt) (c pt, r float64, ok bool) {
	d := 2 * (a[0]*(m[1]-b[1]) + m[0]*(b[1]-a[1]) + b[0]*(a[1]-m[1]))
	if math.Abs(d) < 1e-12 {
		return c, 0, false
	}
	a2, m2, b2 := a[0]*a[0]+a[1]*a[1], m[0]*m[0]+m[1]*m[1], b[0]*b[0]+b[1]*b[1]
	c[0] = (a2*(m[1]-b[1]) + m2*(b[1]-a[1]) + b2*(a[1]-m[1])) / d
	c[1] = (a2*(b[0]-m[0]) + m2*(a[0]-b[0]) + b2*(m[0]-a[0])) / d
	return c, math.Hypot(a[0]-c[0], a[1]-c[1]), true
}

// arcPoints samples an arc a→m→b (m lies on it) into n+1 points.
func arcPoints(a, m, b pt, n int) []pt {
	c, r, ok := arcCircle(a, m, b)
	if !ok {
		return []pt{a, b}
	}
	ang := func(p pt) float64 { return math.Atan2(p[1]-c[1], p[0]-c[0]) }
	a0, am, a1 := ang(a), ang(m), ang(b)
	norm := func(x float64) float64 {
		for x < 0 {
			x += 2 * math.Pi
		}
		for x >= 2*math.Pi {
			x -= 2 * math.Pi
		}
		return x
	}
	// counter-clockwise sweep if m is reached before b going ccw from a
	ccw := norm(am-a0) < norm(a1-a0)
	sweep := norm(a1 - a0)
	if !ccw {
		sweep = -norm(a0 - a1)
	}
	out := make([]pt, 0, n+1)
	for i := 0; i <= n; i++ {
		t := a0 + sweep*float64(i)/float64(n)
		out = append(out, pt{c[0] + r*math.Cos(t), c[1] + r*math.Sin(t)})
	}
	return out
}

// polyArea is the signed shoelace area.
func polyArea(p []pt) float64 {
	var s float64
	for i := range p {
		j := (i + 1) % len(p)
		s += p[i][0]*p[j][1] - p[j][0]*p[i][1]
	}
	return s / 2
}

func pointInPoly(q pt, poly []pt) bool {
	in := false
	for i, j := 0, len(poly)-1; i < len(poly); j, i = i, i+1 {
		a, b := poly[i], poly[j]
		if (a[1] > q[1]) != (b[1] > q[1]) && q[0] < (b[0]-a[0])*(q[1]-a[1])/(b[1]-a[1])+a[0] {
			in = !in
		}
	}
	return in
}

func cross(o, a, b pt) float64 { return (a[0]-o[0])*(b[1]-o[1]) - (a[1]-o[1])*(b[0]-o[0]) }

// segsCross reports a proper intersection of segments ab and cd (touching endpoints do not count).
func segsCross(a, b, c, d pt) bool {
	d1, d2 := cross(a, b, c), cross(a, b, d)
	d3, d4 := cross(c, d, a), cross(c, d, b)
	return d1*d2 < -1e-12 && d3*d4 < -1e-12
}

// polysCross reports whether any edge of p properly crosses any edge of q.
func polysCross(p, q []pt) bool {
	for i := range p {
		a, b := p[i], p[(i+1)%len(p)]
		for j := range q {
			if segsCross(a, b, q[j], q[(j+1)%len(q)]) {
				return true
			}
		}
	}
	return false
}

// selfIntersects reports whether a closed polygon crosses itself.
func selfIntersects(p []pt) bool {
	n := len(p)
	for i := 0; i < n; i++ {
		a, b := p[i], p[(i+1)%n]
		for j := i + 2; j < n; j++ {
			if i == 0 && j == n-1 {
				continue
			}
			if segsCross(a, b, p[j], p[(j+1)%n]) {
				return true
			}
		}
	}
	return false
}

func bboxOf(pts []pt) (min, max pt) {
	min, max = pt{math.Inf(1), math.Inf(1)}, pt{math.Inf(-1), math.Inf(-1)}
	for _, p := range pts {
		min[0], min[1] = math.Min(min[0], p[0]), math.Min(min[1], p[1])
		max[0], max[1] = math.Max(max[0], p[0]), math.Max(max[1], p[1])
	}
	return
}

func circlePoly(c pt, r float64, n int) []pt {
	out := make([]pt, n)
	for i := range out {
		t := 2 * math.Pi * float64(i) / float64(n)
		out[i] = pt{c[0] + r*math.Cos(t), c[1] + r*math.Sin(t)}
	}
	return out
}
