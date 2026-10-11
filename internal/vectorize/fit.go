package vectorize

import "math"

// Seg is one cubic Bezier segment.
type Seg struct{ P0, C1, C2, P1 Pt }

func padd(a, b Pt) Pt         { return Pt{a[0] + b[0], a[1] + b[1]} }
func psub(a, b Pt) Pt         { return Pt{a[0] - b[0], a[1] - b[1]} }
func pmul(a Pt, s float64) Pt { return Pt{a[0] * s, a[1] * s} }
func pdot(a, b Pt) float64    { return a[0]*b[0] + a[1]*b[1] }
func plen(a Pt) float64       { return math.Hypot(a[0], a[1]) }
func pdist(a, b Pt) float64   { return plen(psub(a, b)) }
func punit(a Pt) Pt {
	l := plen(a)
	if l < 1e-12 {
		return Pt{}
	}
	return pmul(a, 1/l)
}

// At evaluates the segment at t.
func (s Seg) At(t float64) Pt {
	u := 1 - t
	b0, b1, b2, b3 := u*u*u, 3*u*u*t, 3*u*t*t, t*t*t
	return Pt{b0*s.P0[0] + b1*s.C1[0] + b2*s.C2[0] + b3*s.P1[0], b0*s.P0[1] + b1*s.C1[1] + b2*s.C2[1] + b3*s.P1[1]}
}

func (s Seg) deriv(t float64) Pt {
	u := 1 - t
	a, b, c := 3*u*u, 6*u*t, 3*t*t
	d1, d2, d3 := psub(s.C1, s.P0), psub(s.C2, s.C1), psub(s.P1, s.C2)
	return Pt{a*d1[0] + b*d2[0] + c*d3[0], a*d1[1] + b*d2[1] + c*d3[1]}
}

func (s Seg) deriv2(t float64) Pt {
	u := 1 - t
	a := psub(padd(s.C2, s.P0), pmul(s.C1, 2))
	b := psub(padd(s.P1, s.C1), pmul(s.C2, 2))
	return Pt{6 * (u*a[0] + t*b[0]), 6 * (u*a[1] + t*b[1])}
}

// ---- simplification and corners -------------------------------------------------------------

func distSeg(p, a, b Pt) float64 {
	ab := psub(b, a)
	l2 := pdot(ab, ab)
	if l2 < 1e-24 {
		return pdist(p, a)
	}
	t := math.Max(0, math.Min(1, pdot(psub(p, a), ab)/l2))
	return pdist(p, padd(a, pmul(ab, t)))
}

func dp(pts []Pt, a, b int, tol float64, keep []bool) {
	if b <= a+1 {
		return
	}
	idx, md := -1, 0.0
	for i := a + 1; i < b; i++ {
		if d := distSeg(pts[i], pts[a], pts[b]); d > md {
			idx, md = i, d
		}
	}
	if idx >= 0 && md > tol {
		keep[idx] = true
		dp(pts, a, idx, tol, keep)
		dp(pts, idx, b, tol, keep)
	}
}

// SimplifyClosed is Douglas-Peucker for a closed polygon.
func SimplifyClosed(pts []Pt, tol float64) []Pt {
	n := len(pts)
	if n <= 4 || tol <= 0 {
		return pts
	}
	far, fd := 0, -1.0
	for i := 1; i < n; i++ {
		if d := pdist(pts[0], pts[i]); d > fd {
			far, fd = i, d
		}
	}
	keep := make([]bool, n)
	keep[0], keep[far] = true, true
	dp(pts, 0, far, tol, keep)
	wrap := append(append([]Pt{}, pts[far:]...), pts[0])
	wk := make([]bool, len(wrap))
	wk[0], wk[len(wrap)-1] = true, true
	dp(wrap, 0, len(wrap)-1, tol, wk)
	for i := 0; i < len(wrap)-1; i++ {
		if wk[i] {
			keep[far+i] = true
		}
	}
	var out []Pt
	for i, k := range keep {
		if k {
			out = append(out, pts[i])
		}
	}
	if len(out) < 3 {
		return pts
	}
	return out
}

// SimplifyOpen is Douglas-Peucker for an open polyline.
func SimplifyOpen(pts []Pt, tol float64) []Pt {
	n := len(pts)
	if n <= 2 || tol <= 0 {
		return pts
	}
	keep := make([]bool, n)
	keep[0], keep[n-1] = true, true
	dp(pts, 0, n-1, tol, keep)
	var out []Pt
	for i, k := range keep {
		if k {
			out = append(out, pts[i])
		}
	}
	return out
}

// corners returns the indices of sharp corners: where the direction changes by more than
// `deg` between the chords of length ~`span` on either side. Non-maximum suppressed.
func corners(pts []Pt, closed bool, span float64, deg float64) []int {
	n := len(pts)
	// arc-length position of every vertex
	cum := make([]float64, n+1)
	for i := 0; i < n; i++ {
		cum[i+1] = cum[i] + pdist(pts[i], pts[(i+1)%n])
	}
	total := cum[n]
	at := func(i int, ds float64) Pt { // point at arc distance ds (may be negative) from vertex i
		s := cum[i] + ds
		if closed {
			s = math.Mod(math.Mod(s, total)+total, total)
		} else {
			s = math.Max(0, math.Min(cum[n-1], s))
		}
		// binary search the segment
		lo, hi := 0, n-1
		for lo < hi {
			mid := (lo + hi + 1) / 2
			if cum[mid] <= s {
				lo = mid
			} else {
				hi = mid - 1
			}
		}
		j := lo
		seg := pdist(pts[j], pts[(j+1)%n])
		if seg < 1e-12 {
			return pts[j]
		}
		t := (s - cum[j]) / seg
		return padd(pts[j], pmul(psub(pts[(j+1)%n], pts[j]), t))
	}
	angle := make([]float64, n)
	for i := 0; i < n; i++ {
		if !closed && (i == 0 || i == n-1) {
			continue
		}
		a, b, c := at(i, -span), pts[i], at(i, span)
		u, v := psub(b, a), psub(c, b)
		if plen(u) < 1e-9 || plen(v) < 1e-9 {
			continue
		}
		cos := pdot(u, v) / (plen(u) * plen(v))
		angle[i] = math.Acos(math.Max(-1, math.Min(1, cos))) * 180 / math.Pi
	}
	// non-maximum suppression by arc length: of the candidates closer than ~2 spans along the
	// curve, the sharpest one is the corner (vertex indices say nothing about distance)
	arc := func(i, j int) float64 {
		d := math.Abs(cum[i] - cum[j])
		if closed && total-d < d {
			d = total - d
		}
		return d
	}
	var out []int
	for i := 0; i < n; i++ {
		if angle[i] < deg {
			continue
		}
		isMax := true
		for j := 0; j < n && isMax; j++ {
			if j == i || angle[j] < deg || arc(i, j) > 2*span {
				continue
			}
			if angle[j] > angle[i] || (angle[j] == angle[i] && j < i) {
				isMax = false
			}
		}
		if isMax {
			out = append(out, i)
		}
	}
	return out
}

// ---- Bezier fitting (Schneider, "An Algorithm for Automatically Fitting Digitized Curves") ----

func chordParams(pts []Pt) []float64 {
	u := make([]float64, len(pts))
	for i := 1; i < len(pts); i++ {
		u[i] = u[i-1] + pdist(pts[i], pts[i-1])
	}
	if u[len(u)-1] > 0 {
		for i := range u {
			u[i] /= u[len(u)-1]
		}
	}
	return u
}

func generateBezier(pts []Pt, u []float64, t1, t2 Pt) Seg {
	n := len(pts)
	p0, p3 := pts[0], pts[n-1]
	var c00, c01, c11, x0, x1 float64
	for i := 0; i < n; i++ {
		t := u[i]
		mt := 1 - t
		b0, b1, b2, b3 := mt*mt*mt, 3*mt*mt*t, 3*mt*t*t, t*t*t
		a1, a2 := pmul(t1, b1), pmul(t2, b2)
		c00 += pdot(a1, a1)
		c01 += pdot(a1, a2)
		c11 += pdot(a2, a2)
		tmp := psub(pts[i], padd(pmul(p0, b0+b1), pmul(p3, b2+b3)))
		x0 += pdot(a1, tmp)
		x1 += pdot(a2, tmp)
	}
	det := c00*c11 - c01*c01
	var al, ar float64
	if math.Abs(det) > 1e-12 {
		al, ar = (x0*c11-x1*c01)/det, (c00*x1-c01*x0)/det
	}
	seg := pdist(p0, p3)
	eps := 1e-6 * seg
	if al < eps || ar < eps { // degenerate: fall back to the Wu/Barsky heuristic
		al, ar = seg/3, seg/3
	}
	// arms much longer than the chord make the curve loop or overshoot; the least-squares
	// solution can ask for them when the end tangents are poor
	maxArm := 0.7 * math.Max(seg, chordSpan(pts))
	al, ar = math.Min(al, maxArm), math.Min(ar, maxArm)
	return Seg{p0, padd(p0, pmul(t1, al)), padd(p3, pmul(t2, ar)), p3}
}

// chordSpan is the length of the polyline through pts: an upper bound for a sensible arm length
// when the end points are close together (a nearly closed piece).
func chordSpan(pts []Pt) float64 {
	l := 0.0
	for i := 1; i < len(pts); i++ {
		l += pdist(pts[i], pts[i-1])
	}
	return l / 2
}

func reparam(s Seg, pts []Pt, u []float64) []float64 {
	out := make([]float64, len(u))
	for i := range pts {
		t := u[i]
		d := psub(s.At(t), pts[i])
		d1, d2 := s.deriv(t), s.deriv2(t)
		num := pdot(d, d1)
		den := pdot(d1, d1) + pdot(d, d2)
		if math.Abs(den) < 1e-12 {
			out[i] = t
			continue
		}
		out[i] = math.Max(0, math.Min(1, t-num/den))
	}
	return out
}

func maxError(s Seg, pts []Pt, u []float64) (float64, int) {
	worst, at := 0.0, len(pts)/2
	for i := 1; i < len(pts)-1; i++ {
		if d := pdist(s.At(u[i]), pts[i]); d > worst {
			worst, at = d, i
		}
	}
	return worst, at
}

func fitCubic(pts []Pt, t1, t2 Pt, tol float64, out *[]Seg, depth int) {
	n := len(pts)
	if n == 2 {
		d := pdist(pts[0], pts[1]) / 3
		*out = append(*out, Seg{pts[0], padd(pts[0], pmul(t1, d)), padd(pts[1], pmul(t2, d)), pts[1]})
		return
	}
	u := chordParams(pts)
	s := generateBezier(pts, u, t1, t2)
	err, split := maxError(s, pts, u)
	if err < tol {
		*out = append(*out, s)
		return
	}
	if err < tol*4 { // close: refine the parametrization a few times
		for it := 0; it < 6; it++ {
			u = reparam(s, pts, u)
			s = generateBezier(pts, u, t1, t2)
			if err, split = maxError(s, pts, u); err < tol {
				*out = append(*out, s)
				return
			}
		}
	}
	if depth > 24 || split < 1 || split > n-2 {
		*out = append(*out, s)
		return
	}
	center := punit(psub(pts[split-1], pts[split+1]))
	fitCubic(pts[:split+1], t1, center, tol, out, depth+1)
	fitCubic(pts[split:], pmul(center, -1), t2, tol, out, depth+1)
}

// tangentWindow is how many points an end tangent is estimated over: single-pixel wobble at the
// end of a traced curve must not decide where the curve leaves its end point.
const tangentWindow = 8

// tangentAt is the unit direction of the polyline at index i looking `forward` (toward higher
// indices) or backward, estimated over up to tangentWindow points (k is a lower bound).
func tangentAt(pts []Pt, i, k int, forward bool) Pt {
	n := len(pts)
	w := maxInt(k, minInt(tangentWindow, n/4))
	j := i + w
	if !forward {
		j = i - w
	}
	j = maxInt(0, minInt(n-1, j))
	if j == i {
		return Pt{}
	}
	return punit(psub(pts[j], pts[i]))
}

// FitOpen fits a chain of cubic Beziers through an open polyline within tol, breaking at sharp
// corners (turning more than cornerDeg) so they stay sharp.
func FitOpen(pts []Pt, tol, cornerDeg float64) []Seg {
	pts = dedupePts(pts)
	if len(pts) < 2 {
		return nil
	}
	cs := corners(pts, false, math.Max(2*tol, 3), cornerDeg)
	cuts := append(append([]int{0}, cs...), len(pts)-1)
	var out []Seg
	for k := 0; k+1 < len(cuts); k++ {
		a, b := cuts[k], cuts[k+1]
		if b <= a {
			continue
		}
		piece := pts[a : b+1]
		t1 := tangentAt(piece, 0, minInt(3, len(piece)-1), true)
		t2 := tangentAt(piece, len(piece)-1, minInt(3, len(piece)-1), false)
		fitCubic(piece, t1, t2, tol, &out, 0)
	}
	return out
}

// FitClosed fits a closed polyline. Where there is no corner the curve is smooth all around.
func FitClosed(pts []Pt, tol, cornerDeg float64) []Seg {
	pts = dedupePts(pts)
	if len(pts) > 1 && pdist(pts[0], pts[len(pts)-1]) < 1e-9 {
		pts = pts[:len(pts)-1]
	}
	n := len(pts)
	if n < 3 {
		return nil
	}
	cs := corners(pts, true, math.Max(2*tol, 3), cornerDeg)
	if len(cs) == 0 {
		// smooth closed curve: cut at two opposite points, keeping a shared tangent at the seam
		cs = []int{0}
	}
	var out []Seg
	for k := 0; k < len(cs); k++ {
		a, b := cs[k], cs[(k+1)%len(cs)]
		var piece []Pt
		if len(cs) == 1 {
			// the loop is cut at one point: half and half so tangents stay continuous
			mid := (a + n/2) % n
			for _, pair := range [][2]int{{a, mid}, {mid, a}} {
				piece = ringSlice(pts, pair[0], pair[1])
				seam := seamTangent(pts, pair[0])
				seam2 := seamTangent(pts, pair[1])
				fitCubic(piece, seam, pmul(seam2, -1), tol, &out, 0)
			}
			return closeUp(out)
		}
		piece = ringSlice(pts, a, b)
		t1 := tangentAt(piece, 0, minInt(3, len(piece)-1), true)
		t2 := tangentAt(piece, len(piece)-1, minInt(3, len(piece)-1), false)
		fitCubic(piece, t1, t2, tol, &out, 0)
	}
	return closeUp(out)
}

// seamTangent is the direction of travel at vertex i of a closed ring.
func seamTangent(pts []Pt, i int) Pt {
	n := len(pts)
	return punit(psub(pts[(i+2)%n], pts[((i-2)%n+n)%n]))
}

func ringSlice(pts []Pt, a, b int) []Pt {
	n := len(pts)
	out := []Pt{pts[a]}
	for i := (a + 1) % n; ; i = (i + 1) % n {
		out = append(out, pts[i])
		if i == b {
			break
		}
		if len(out) > n+1 {
			break
		}
	}
	return out
}

// closeUp snaps the last segment's end to the first segment's start (they are the same vertex).
func closeUp(segs []Seg) []Seg {
	if len(segs) > 0 {
		segs[len(segs)-1].P1 = segs[0].P0
	}
	return segs
}

func dedupePts(pts []Pt) []Pt {
	out := make([]Pt, 0, len(pts))
	for _, p := range pts {
		if len(out) == 0 || pdist(out[len(out)-1], p) > 1e-9 {
			out = append(out, p)
		}
	}
	return out
}

// Length of a segment chain (sampled).
func ChainLength(segs []Seg) float64 {
	l := 0.0
	for _, s := range segs {
		prev := s.P0
		for i := 1; i <= 16; i++ {
			q := s.At(float64(i) / 16)
			l += pdist(prev, q)
			prev = q
		}
	}
	return l
}
