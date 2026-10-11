package sweep

import (
	"fmt"
	"math"

	"github.com/accelone/simoncad/internal/geometry/curve"
)

type vertex struct {
	p     Vec
	w, h  float64
	twist float64
}

func positions(vs []vertex) []Vec {
	out := make([]Vec, len(vs))
	for i, v := range vs {
		out[i] = v.p
	}
	return out
}

// buildPolyline plans a linear path: straight runs joined by exact mitered corners. The
// sections sit at the vertices (the corner plane bisects the turn and the profile is stretched
// along the turn so both runs keep their width) and are joined with ruled bands, so every run
// is a true prism. Round profiles use their polygon here because the corner stretch is not a
// scale along the profile axes.
func buildPolyline(path *curve.Path, sh *shape, opt Options) (*Plan, error) {
	plan := &Plan{MinRadius: math.Inf(1)}
	var vs []vertex
	for i, pt := range path.Points {
		w, h := pt.Scale()
		if len(vs) > 0 && norm(sub(pt.P, vs[len(vs)-1].p)) < 1e-9 {
			plan.Warnings = append(plan.Warnings, Warning{"ZERO_LENGTH_SEGMENT", fmt.Sprintf("point %d coincides with the previous point and was skipped", i)})
			continue
		}
		vs = append(vs, vertex{pt.P, w, h, pt.Twist})
	}
	if path.Closed && len(vs) > 1 && norm(sub(vs[0].p, vs[len(vs)-1].p)) < 1e-9 {
		vs = vs[:len(vs)-1]
	}
	n := len(vs)
	minPts := 2
	if path.Closed {
		minPts = 3
	}
	if n < minPts {
		return nil, fail(CodePathInvalid, "add points that are not coincident", "the path has only %d distinct points", n)
	}
	segs := n - 1
	if path.Closed {
		segs = n
	}
	d := make([]Vec, segs)
	segLen := make([]float64, segs)
	total := 0.0
	for i := 0; i < segs; i++ {
		v := sub(vs[(i+1)%n].p, vs[i].p)
		segLen[i] = norm(v)
		d[i] = unit(v)
		total += segLen[i]
	}
	plan.Length = total
	if opt.TotalTwist != 0 { // spread the extra twist over the vertices by distance
		acc := 0.0
		for i := range vs {
			vs[i].twist += opt.TotalTwist * acc / total
			if i < segs {
				acc += segLen[i]
			}
		}
	}
	maxScale := 0.0
	for _, v := range vs {
		maxScale = math.Max(maxScale, math.Max(v.w, v.h))
	}
	rEff := sh.radius * maxScale

	// ---- corners
	tangent := make([]Vec, n)
	miter := make([]float64, n)
	turn := make([]float64, n)
	mdir := make([]Vec, n)
	for v := 0; v < n; v++ {
		miter[v] = 1
		if !path.Closed && (v == 0 || v == n-1) {
			if v == 0 {
				tangent[v] = d[0]
			} else {
				tangent[v] = d[segs-1]
			}
			continue
		}
		prev, cur := d[(v-1+segs)%segs], d[v%segs]
		sum := add(prev, cur)
		if norm(sum) < 1e-6 {
			return nil, fail(CodeExcessiveCurvature, "insert intermediate points or use a smooth path type",
				"the path doubles back on itself at point %d", v)
		}
		th := math.Acos(math.Max(-1, math.Min(1, dot(prev, cur))))
		if th*180/math.Pi > maxMiterDegrees {
			return nil, fail(CodeExcessiveCurvature, "round the corner with a smooth path type or add intermediate points",
				"the corner at point %d turns %.0f°, too sharp to sweep (max %.0f°)", v, th*180/math.Pi, maxMiterDegrees)
		}
		tangent[v] = unit(sum)
		turn[v] = th
		if th > 1e-6 {
			miter[v] = 1 / math.Cos(th/2)
			mdir[v] = unit(sub(cur, prev))
		}
	}
	// every run must be long enough for the corner extensions at both its ends
	for i := 0; i < segs; i++ {
		a, b := i, (i+1)%n
		r := sh.radius * math.Max(math.Max(vs[a].w, vs[a].h), math.Max(vs[b].w, vs[b].h))
		need := r * (math.Tan(turn[a]/2) + math.Tan(turn[b]/2))
		if need > segLen[i]*0.999 {
			return nil, fail(CodeExcessiveCurvature,
				fmt.Sprintf("make the run at least %.3g mm long or use a smaller profile", need),
				"the run between points %d and %d is %.3g mm, too short for the profile at its corners", a, b, segLen[i])
		}
	}
	if err := checkPolylineIntersection(positions(vs), path.Closed, segLen, rEff); err != nil {
		return nil, err
	}

	// ---- sections: vertices, plus extra sections where the profile twists along a run
	type sec struct {
		p     Vec
		tan   Vec
		w, h  float64
		twist float64
		k     float64
		m     Vec
		vtx   int // vertex index, or -1 for an extra section inside a leg
		leg   int
		frac  float64 // position within the leg for extra sections
	}
	var ss []sec
	for i := 0; i < segs; i++ {
		a, b := i, (i+1)%n
		ss = append(ss, sec{p: vs[a].p, tan: tangent[a], w: vs[a].w, h: vs[a].h, twist: vs[a].twist, k: miter[a], m: mdir[a], vtx: a, leg: i})
		kk := int(math.Ceil(math.Abs(vs[b].twist-vs[a].twist) / twistStepDeg))
		for j := 1; j < kk; j++ {
			t := float64(j) / float64(kk)
			ss = append(ss, sec{
				p: add(mul(vs[a].p, 1-t), mul(vs[b].p, t)), tan: d[i],
				w: vs[a].w + t*(vs[b].w-vs[a].w), h: vs[a].h + t*(vs[b].h-vs[a].h),
				twist: vs[a].twist + t*(vs[b].twist-vs[a].twist), k: 1, vtx: -1, leg: i, frac: t,
			})
		}
	}
	if !path.Closed {
		l := vs[n-1]
		ss = append(ss, sec{p: l.p, tan: tangent[n-1], w: l.w, h: l.h, twist: l.twist, k: 1, vtx: n - 1, leg: segs - 1})
	}
	// ---- frames. Each leg (straight run) has one frame, transported between legs by the minimal
	// rotation taking one direction to the next. A corner section is that frame turned by half
	// the corner, so it is consistent with both legs it joins; frames from a generic
	// transport between corner planes would twist legs when the corners turn about different axes.
	legN := make([]Vec, segs)
	seed := Perp(d[0])
	if u := unit(sub(opt.Up, mul(d[0], dot(opt.Up, d[0])))); u != (Vec{}) {
		seed = u
	}
	legN[0] = seed
	for i := 1; i < segs; i++ {
		legN[i] = turnFrame(legN[i-1], d[i-1], d[i], 1)
	}
	vtxN := make([]Vec, n)
	for v := 0; v < n; v++ {
		switch {
		case !path.Closed && v == 0:
			vtxN[v] = legN[0]
		case !path.Closed && v == n-1:
			vtxN[v] = legN[segs-1]
		default:
			prev := (v - 1 + segs) % segs
			vtxN[v] = turnFrame(legN[prev], d[prev], d[v%segs], 0.5)
		}
	}
	// A closed ring accumulates holonomy: the frame carried around the loop misses the first one.
	// Spread the missing angle as extra twist over the vertices.
	vtxExtra := make([]float64, n)
	if path.Closed {
		carried := turnFrame(legN[segs-1], d[segs-1], d[0], 1) // leg frame after the last corner
		phi := signedAngle(carried, legN[0], d[0]) * 180 / math.Pi
		acc := 0.0
		for v := 0; v < n; v++ {
			vtxExtra[v] = phi * acc / total
			acc += segLen[v]
		}
	}
	pos := make([]curve.Vec, len(ss))
	frames := make([]curve.Frame, len(ss))
	for i, s := range ss {
		pos[i] = s.p
		tw := s.twist
		var nrm Vec
		if s.vtx >= 0 {
			nrm = vtxN[s.vtx]
			tw += vtxExtra[s.vtx]
		} else { // an extra section inside leg s.leg
			nrm = legN[s.leg]
			a, b := s.leg, (s.leg+1)%n
			t := s.frac
			tw += vtxExtra[a] + t*(vtxExtra[b]-vtxExtra[a])
			if b == 0 && path.Closed { // wrapped: the ring's end carries the full angle
				tw = s.twist + vtxExtra[a] + t*(carriedPhi(path.Closed, legN, d, segs)-vtxExtra[a])
			}
		}
		tan := s.tan
		if tw != 0 {
			nrm = rotateAbout(nrm, tan, tw*math.Pi/180)
		}
		frames[i] = curve.Frame{P: s.p, T: tan, N: nrm, B: cross(tan, nrm)}
	}

	body := Job{Closed: path.Closed, Ruled: true}
	for _, f := range frames {
		body.Sections = append(body.Sections, Section{Origin: f.P, X: f.N, Y: f.B, SX: 1, SY: 1})
	}
	for ci, poly := range sh.polys {
		per := make([][][2]float64, len(ss))
		for i, s := range ss {
			f := frames[i]
			var ml [2]float64
			if s.k > 1 {
				mx, my := dot(s.m, f.N), dot(s.m, f.B)
				if l := math.Hypot(mx, my); l > 1e-12 {
					ml = [2]float64{mx / l, my / l}
				}
			}
			out := make([][2]float64, len(poly))
			for j, p := range poly {
				x, y := p[0]*s.w, p[1]*s.h
				if s.k > 1 {
					dm := x*ml[0] + y*ml[1]
					x += (s.k - 1) * dm * ml[0]
					y += (s.k - 1) * dm * ml[1]
				}
				out[j] = [2]float64{x, y}
			}
			per[i] = out
		}
		body.Contours = append(body.Contours, Contour{Hole: sh.holes[ci], PerSection: per})
	}
	// analytic volume: each leg integrates the (linearly varying) section area; mitered
	// corners add and remove equal wedges for a centred profile
	for i := 0; i < segs; i++ {
		a, b := vs[i], vs[(i+1)%n]
		// ∫ (w0 + (w1-w0)t)(h0 + (h1-h0)t) dt over [0,1]
		wh := a.w*a.h + (a.w*(b.h-a.h)+(b.w-a.w)*a.h)/2 + (b.w-a.w)*(b.h-a.h)/3
		plan.ExpectedVolume += sh.area * wh * segLen[i]
	}
	if !path.Closed {
		plan.ExpectedVolume += capVolume(sh, vs[0].w, vs[0].h, opt.CapStart, opt.CapLength)
		plan.ExpectedVolume += capVolume(sh, vs[n-1].w, vs[n-1].h, opt.CapEnd, opt.CapLength)
	}
	if !path.Closed {
		appendCap(&body, sh, false, opt.CapStart, opt.CapLength)
		appendCap(&body, sh, true, opt.CapEnd, opt.CapLength)
	} else if opt.CapStart != "flat" || opt.CapEnd != "flat" {
		plan.Warnings = append(plan.Warnings, Warning{"CAPS_IGNORED", "a closed path has no ends; end caps were ignored"})
	}
	plan.Body = body
	plan.Sections = len(body.Sections)
	return plan, nil
}

// checkPolylineIntersection is checkSelfIntersection for a polyline whose vertices are the
// samples: pairs of runs that are far apart along the path yet closer than the thickness.
func checkPolylineIntersection(pts []Vec, closed bool, segLen []float64, rEff float64) error {
	eps := 2 * rEff * 0.999
	hits := curve.SelfIntersections(pts, closed, eps)
	if len(hits) == 0 {
		return nil
	}
	cum := make([]float64, len(segLen)+1)
	for i, l := range segLen {
		cum[i+1] = cum[i] + l
	}
	total := cum[len(segLen)]
	var worst *curve.Intersection
	for k := range hits {
		h := hits[k]
		sep := cum[h.B] - cum[h.A+1]
		if closed {
			sep = math.Min(sep, total-(cum[h.B+1]-cum[h.A]))
		}
		if sep <= 3*rEff {
			continue
		}
		if worst == nil || h.Dist < worst.Dist {
			worst = &hits[k]
		}
	}
	if worst == nil {
		return nil
	}
	at := pts[worst.A]
	if worst.Dist < 1e-3 {
		return fail(CodePathSelfIntersect, "reroute the path so it does not cross itself, or split it into separate paths",
			"the path crosses itself near (%.3g, %.3g, %.3g)", at[0], at[1], at[2])
	}
	return fail(CodeSweepSelfIntersect, fmt.Sprintf("keep parts of the path at least %.3g mm apart or use a thinner profile", 2*rEff),
		"the swept solid would overlap itself near (%.3g, %.3g, %.3g): two runs are only %.3g mm apart", at[0], at[1], at[2], worst.Dist)
}

// turnFrame carries unit normal n (perpendicular to direction a) toward direction b by the
// minimal rotation, scaled by frac (1 = all the way, 0.5 = half the turn).
func turnFrame(n, a, b Vec, frac float64) Vec {
	axis := cross(a, b)
	sin := norm(axis)
	if sin < 1e-12 {
		return n
	}
	ang := math.Atan2(sin, dot(a, b)) * frac
	return unit(rotateAbout(n, mul(axis, 1/sin), ang))
}

func rotateAbout(v, k Vec, angle float64) Vec {
	c, s := math.Cos(angle), math.Sin(angle)
	return add(add(mul(v, c), mul(cross(k, v), s)), mul(k, dot(k, v)*(1-c)))
}

// signedAngle is the angle from a to b about the unit axis.
func signedAngle(a, b, axis Vec) float64 {
	return math.Atan2(dot(cross(a, b), axis), dot(a, b))
}

// Perp returns a unit vector perpendicular to the unit vector t.
func Perp(t Vec) Vec {
	a := Vec{1, 0, 0}
	if math.Abs(t[0]) > 0.9 {
		a = Vec{0, 1, 0}
	}
	return unit(cross(t, a))
}

// carriedPhi is the closing holonomy angle in degrees (see buildPolyline).
func carriedPhi(closed bool, legN []Vec, d []Vec, segs int) float64 {
	if !closed {
		return 0
	}
	carried := turnFrame(legN[segs-1], d[segs-1], d[0], 1)
	return signedAngle(carried, legN[0], d[0]) * 180 / math.Pi
}
