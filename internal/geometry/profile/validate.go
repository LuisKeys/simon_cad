package profile

import (
	"fmt"
	"math"
)

// Issue is one validation finding.
type Issue struct {
	Severity string // "error" or "warning"
	Contour  int
	Message  string
}

// HasError reports whether any issue is an error.
func HasError(issues []Issue) bool { return firstError(issues) >= 0 }

func firstError(issues []Issue) int {
	for i, is := range issues {
		if is.Severity == "error" {
			return i
		}
	}
	return -1
}

const (
	minSegment = 1e-9 // coincident consecutive points
	minArea    = 1e-12
)

// Validate checks every contour for degenerate geometry, self-intersection, orientation and
// that holes sit inside an outer contour without crossing it.
func (p *Profile) Validate() []Issue {
	var out []Issue
	add := func(sev string, i int, f string, a ...any) {
		out = append(out, Issue{sev, i, fmt.Sprintf(f, a...)})
	}
	if len(p.Contours) == 0 {
		add("error", -1, "profile has no contours")
		return out
	}
	outers := 0
	for i, c := range p.Contours {
		if !c.Closed {
			add("error", i, "contour %d is open: profiles must be closed loops", i)
			continue
		}
		if !c.Hole {
			outers++
		}
		pts := c.Points
		if len(pts) < 3 {
			add("error", i, "contour %d has %d points, need at least 3", i, len(pts))
			continue
		}
		bad := false
		for _, v := range pts {
			if math.IsNaN(v[0]) || math.IsNaN(v[1]) || math.IsInf(v[0], 0) || math.IsInf(v[1], 0) {
				add("error", i, "contour %d has a non-finite coordinate", i)
				bad = true
				break
			}
		}
		if bad {
			continue
		}
		for j := range pts {
			if dist2(pts[j], pts[(j+1)%len(pts)]) < minSegment {
				add("error", i, "contour %d has coincident consecutive points at index %d", i, j)
				bad = true
				break
			}
		}
		if bad {
			continue
		}
		a := SignedArea(pts)
		if math.Abs(a) < minArea {
			add("error", i, "contour %d is degenerate (zero area)", i)
			continue
		}
		if (!c.Hole && a < 0) || (c.Hole && a > 0) {
			add("warning", i, "contour %d has the wrong orientation (outer must be counter-clockwise, holes clockwise); Normalize fixes it", i)
		}
		if j, k, ok := selfIntersect(pts); ok {
			add("error", i, "contour %d self-intersects (edges %d and %d cross)", i, j, k)
		}
	}
	if outers == 0 {
		add("error", -1, "profile has only hole contours")
	}
	// holes must lie inside an outer contour and not cross any contour
	for i, c := range p.Contours {
		if !c.Closed || len(c.Points) < 3 {
			continue
		}
		for j := i + 1; j < len(p.Contours); j++ {
			d := p.Contours[j]
			if !d.Closed || len(d.Points) < 3 {
				continue
			}
			if edgesCross(c.Points, d.Points) {
				add("error", j, "contours %d and %d cross each other", i, j)
			}
		}
		if c.Hole {
			inside := false
			for j, d := range p.Contours {
				if j != i && !d.Hole && d.Closed && len(d.Points) >= 3 && pointInPoly(c.Points[0], d.Points) {
					inside = true
					break
				}
			}
			if !inside {
				add("error", i, "hole contour %d is not inside any outer contour", i)
			}
		}
	}
	return out
}

func dist2(a, b Vec2) float64 { return math.Hypot(a[0]-b[0], a[1]-b[1]) }

func orient(a, b, c Vec2) float64 {
	return (b[0]-a[0])*(c[1]-a[1]) - (b[1]-a[1])*(c[0]-a[0])
}

func segCross(a, b, c, d Vec2) bool {
	o1, o2, o3, o4 := orient(a, b, c), orient(a, b, d), orient(c, d, a), orient(c, d, b)
	return o1*o2 < 0 && o3*o4 < 0
}

func selfIntersect(pts []Vec2) (int, int, bool) {
	n := len(pts)
	for i := 0; i < n; i++ {
		for j := i + 2; j < n; j++ {
			if i == 0 && j == n-1 {
				continue
			}
			if segCross(pts[i], pts[(i+1)%n], pts[j], pts[(j+1)%n]) {
				return i, j, true
			}
		}
	}
	return 0, 0, false
}

func edgesCross(a, b []Vec2) bool {
	for i := range a {
		for j := range b {
			if segCross(a[i], a[(i+1)%len(a)], b[j], b[(j+1)%len(b)]) {
				return true
			}
		}
	}
	return false
}

func pointInPoly(pt Vec2, poly []Vec2) bool {
	in := false
	for i, j := 0, len(poly)-1; i < len(poly); j, i = i, i+1 {
		if (poly[i][1] > pt[1]) != (poly[j][1] > pt[1]) &&
			pt[0] < (poly[j][0]-poly[i][0])*(pt[1]-poly[i][1])/(poly[j][1]-poly[i][1])+poly[i][0] {
			in = !in
		}
	}
	return in
}
