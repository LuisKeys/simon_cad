package curve

import "math"

// Frame is a local coordinate frame on a sampled path: T is the tangent, N and B span the
// plane of the cross-section (N is the profile's local X, B its local Y).
type Frame struct {
	P, T, N, B Vec
}

// Frames builds rotation-minimizing (parallel transport) frames along a sampled polyline
// using the double-reflection method. twist[i] (degrees, optional) is applied on top. When
// the path is closed, the residual holonomy angle is spread linearly over the samples so the
// last frame meets the first. upHint, if non-zero, seeds the first normal.
//
// Coincident points are skipped by the caller; here a zero-length step keeps the last tangent.
func Frames(pts []Vec, closed bool, twist []float64, upHint Vec) []Frame {
	n := len(pts)
	if n < 2 {
		return nil
	}
	tan := make([]Vec, n)
	for i := range pts {
		var a, b Vec
		switch {
		case closed:
			a, b = pts[(i+n-1)%n], pts[(i+1)%n]
		case i == 0:
			a, b = pts[0], pts[1]
		case i == n-1:
			a, b = pts[n-2], pts[n-1]
		default:
			a, b = pts[i-1], pts[i+1]
		}
		tan[i] = unit(sub(b, a))
	}
	return FramesWithTangents(pts, tan, closed, twist, upHint)
}

// FramesWithTangents is Frames with caller-supplied unit tangents (one per point), used when
// the path's own tangents are known exactly (curves) or differ from the polyline's (mitered
// corners). Zero tangents borrow the nearest valid one.
func FramesWithTangents(pts, tan []Vec, closed bool, twist []float64, upHint Vec) []Frame {
	n := len(pts)
	if n < 2 || len(tan) != n {
		return nil
	}
	tan = append([]Vec(nil), tan...)
	// Coincident points give zero tangents: borrow the nearest valid one.
	first := -1
	for i := range tan {
		if tan[i] != (Vec{}) {
			first = i
			break
		}
	}
	if first < 0 {
		return nil
	}
	for i := 0; i < first; i++ {
		tan[i] = tan[first]
	}
	for i := first + 1; i < n; i++ {
		if tan[i] == (Vec{}) {
			tan[i] = tan[i-1]
		}
	}
	fr := make([]Frame, n)
	n0 := Perp(tan[0])
	if u := unit(sub(upHint, scale(tan[0], dot(upHint, tan[0])))); u != (Vec{}) {
		n0 = u
	}
	fr[0] = Frame{P: pts[0], T: tan[0], N: n0, B: cross(tan[0], n0)}
	for i := 1; i < n; i++ {
		v1 := sub(pts[i], pts[i-1])
		c1 := dot(v1, v1)
		if c1 < 1e-24 {
			fr[i] = fr[i-1]
			fr[i].P = pts[i]
			continue
		}
		rL := sub(fr[i-1].N, scale(v1, 2*dot(v1, fr[i-1].N)/c1))
		tL := sub(fr[i-1].T, scale(v1, 2*dot(v1, fr[i-1].T)/c1))
		v2 := sub(tan[i], tL)
		c2 := dot(v2, v2)
		nn := rL
		if c2 > 1e-24 {
			nn = sub(rL, scale(v2, 2*dot(v2, rL)/c2))
		}
		nn = unit(sub(nn, scale(tan[i], dot(nn, tan[i])))) // re-orthogonalize against drift
		fr[i] = Frame{P: pts[i], T: tan[i], N: nn, B: cross(tan[i], nn)}
	}
	if closed {
		// Carry the last frame across the closing step and measure the angle to frame 0.
		last := fr[n-1]
		v1 := sub(pts[0], pts[n-1])
		if c1 := dot(v1, v1); c1 > 1e-24 {
			rL := sub(last.N, scale(v1, 2*dot(v1, last.N)/c1))
			tL := sub(last.T, scale(v1, 2*dot(v1, last.T)/c1))
			v2 := sub(tan[0], tL)
			if c2 := dot(v2, v2); c2 > 1e-24 {
				rL = sub(rL, scale(v2, 2*dot(v2, rL)/c2))
			}
			ang := math.Atan2(dot(cross(fr[0].N, rL), fr[0].T), dot(fr[0].N, rL))
			for i := 1; i < n; i++ {
				a := -ang * float64(i) / float64(n)
				fr[i].N = rotateAbout(fr[i].N, fr[i].T, a)
				fr[i].B = cross(fr[i].T, fr[i].N)
			}
		}
	}
	if len(twist) > 0 {
		for i := range fr {
			if i < len(twist) && twist[i] != 0 {
				a := twist[i] * math.Pi / 180
				fr[i].N = rotateAbout(fr[i].N, fr[i].T, a)
				fr[i].B = cross(fr[i].T, fr[i].N)
			}
		}
	}
	return fr
}

// Radii returns the radius of curvature at every point of the polyline (Menger curvature of
// consecutive triples): +Inf on straight stretches and at the ends of an open polyline.
func Radii(pts []Vec, closed bool) []float64 {
	n := len(pts)
	out := make([]float64, n)
	for i := range out {
		out[i] = math.Inf(1)
		if !closed && (i == 0 || i == n-1) {
			continue
		}
		a, b, c := pts[(i+n-1)%n], pts[i], pts[(i+1)%n]
		ab, bc, ca := dist(a, b), dist(b, c), dist(c, a)
		area2 := norm(cross(sub(b, a), sub(c, a)))
		if area2 < 1e-15 || ab*bc*ca == 0 {
			continue
		}
		out[i] = ab * bc * ca / (2 * area2)
	}
	return out
}

// MinRadius returns the smallest radius of curvature over the polyline and the index where it
// occurs. A straight polyline returns +Inf and -1.
func MinRadius(pts []Vec, closed bool) (float64, int) {
	best, at := math.Inf(1), -1
	for i, r := range Radii(pts, closed) {
		if r < best {
			best, at = r, i
		}
	}
	return best, at
}

// Intersection reports two non-adjacent polyline segments that come closer than eps.
type Intersection struct {
	A, B int     // segment indices (segment i joins pts[i] and pts[i+1])
	Dist float64 // minimum distance between them
}

// SelfIntersections finds pairs of non-adjacent segments closer than eps. It is O(n log n)
// on typical paths thanks to a uniform grid.
func SelfIntersections(pts []Vec, closed bool, eps float64) []Intersection {
	return SelfIntersectionsGap(pts, closed, eps, 2)
}

// SelfIntersectionsGap is SelfIntersections ignoring segment pairs closer than minGap indices
// apart (cyclically when closed): a sweep only cares about parts of the path that are far
// apart along it yet close in space.
func SelfIntersectionsGap(pts []Vec, closed bool, eps float64, minGap int) []Intersection {
	if minGap < 2 {
		minGap = 2
	}
	n := len(pts)
	segs := n - 1
	if closed {
		segs = n
	}
	if segs < 3 {
		return nil
	}
	// Grid cell size: the mean segment length (at least 4*eps), so each segment touches few cells.
	mean := PolyLength(pts, closed) / float64(segs)
	cell := math.Max(mean, 4*eps)
	if cell <= 0 {
		return nil
	}
	type key [3]int64
	grid := map[key][]int{}
	cellOf := func(v Vec, pad float64) (lo, hi key) {
		for k := 0; k < 3; k++ {
			lo[k] = int64(math.Floor((v[k] - pad) / cell))
			hi[k] = int64(math.Floor((v[k] + pad) / cell))
		}
		return
	}
	for i := 0; i < segs; i++ {
		a, b := pts[i], pts[(i+1)%n]
		var mn, mx Vec
		for k := 0; k < 3; k++ {
			mn[k], mx[k] = math.Min(a[k], b[k])-eps, math.Max(a[k], b[k])+eps
		}
		lo, _ := cellOf(mn, 0)
		_, hi := cellOf(mx, 0)
		for x := lo[0]; x <= hi[0]; x++ {
			for y := lo[1]; y <= hi[1]; y++ {
				for z := lo[2]; z <= hi[2]; z++ {
					grid[key{x, y, z}] = append(grid[key{x, y, z}], i)
				}
			}
		}
	}
	seen := map[[2]int]bool{}
	var out []Intersection
	for _, list := range grid {
		for ai := 0; ai < len(list); ai++ {
			for bi := ai + 1; bi < len(list); bi++ {
				i, j := list[ai], list[bi]
				if i > j {
					i, j = j, i
				}
				gap := j - i
				if closed && segs-gap < gap {
					gap = segs - gap
				}
				if gap < minGap || seen[[2]int{i, j}] {
					continue
				}
				seen[[2]int{i, j}] = true
				if d := segSegDist(pts[i], pts[(i+1)%n], pts[j], pts[(j+1)%n]); d < eps {
					out = append(out, Intersection{i, j, d})
				}
			}
		}
	}
	return out
}

// segSegDist is the minimum distance between segments p1q1 and p2q2 (Ericson, RTCD 5.1.9).
func segSegDist(p1, q1, p2, q2 Vec) float64 {
	d1, d2, r := sub(q1, p1), sub(q2, p2), sub(p1, p2)
	a, e, f := dot(d1, d1), dot(d2, d2), dot(d2, r)
	clamp := func(x float64) float64 { return math.Min(1, math.Max(0, x)) }
	var s, t float64
	switch {
	case a <= 1e-24 && e <= 1e-24:
		return dist(p1, p2)
	case a <= 1e-24:
		t = clamp(f / e)
	default:
		c := dot(d1, r)
		if e <= 1e-24 {
			s = clamp(-c / a)
		} else {
			b := dot(d1, d2)
			den := a*e - b*b
			if den > 1e-24 {
				s = clamp((b*f - c*e) / den)
			}
			t = (b*s + f) / e
			if t < 0 {
				t, s = 0, clamp(-c/a)
			} else if t > 1 {
				t, s = 1, clamp((b-c)/a)
			}
		}
	}
	return dist(add(p1, scale(d1, s)), add(p2, scale(d2, t)))
}
