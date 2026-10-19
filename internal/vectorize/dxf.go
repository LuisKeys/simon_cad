package vectorize

import (
	"bufio"
	"bytes"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

const (
	maxDXFSegs   = 400000 // curve segments in one drawing, after expanding block inserts
	maxDXFDepth  = 8      // nested INSERTs
	dxfUnitScale = 1000.0 // a drawing with known units is returned in µm so pixel rounding stays far below 1 µm
)

// dxfUnitMM is the length of one drawing unit in mm for $INSUNITS (0 and unknown codes: not stated).
var dxfUnitMM = map[int]float64{
	1: 25.4, 2: 304.8, 3: 1609344, 4: 1, 5: 10, 6: 1000, 7: 1e6, 8: 25.4e-6, 9: 0.0254, 10: 914.4,
	11: 1e-7, 12: 1e-6, 13: 1e-3, 14: 100, 15: 1e4, 16: 1e5, 17: 1e12, 18: 1.495978707e17,
	19: 9.4607304725808e18, 20: 3.0856775814914e19,
}

// IsDXF reports whether data looks like an ASCII DXF file (a group code 0 and the SECTION
// keyword, or a leading 999 comment, in the first lines).
func IsDXF(data []byte) bool {
	head := data
	if len(head) > 4096 {
		head = head[:4096]
	}
	head = bytes.TrimPrefix(head, []byte("\xef\xbb\xbf"))
	if bytes.HasPrefix(head, []byte("AutoCAD Binary DXF")) {
		return true
	}
	var codes []string
	for _, ln := range strings.Split(string(head), "\n") {
		codes = append(codes, strings.TrimSpace(ln))
		if len(codes) == 4 {
			break
		}
	}
	for i := 0; i+1 < len(codes); i += 2 {
		if codes[i] == "999" {
			continue
		}
		return codes[i] == "0" && strings.EqualFold(codes[i+1], "SECTION")
	}
	return false
}

type dxfPair struct {
	code int
	val  string
}

type dxfEnt struct {
	typ   string
	pairs []dxfPair
}

func (e *dxfEnt) str(code int) string {
	for _, p := range e.pairs {
		if p.code == code {
			return p.val
		}
	}
	return ""
}

func (e *dxfEnt) num(code int, def float64) float64 {
	for _, p := range e.pairs {
		if p.code == code {
			if v, err := strconv.ParseFloat(strings.TrimSpace(p.val), 64); err == nil && !math.IsNaN(v) && !math.IsInf(v, 0) {
				return v
			}
			return def
		}
	}
	return def
}

func (e *dxfEnt) has(code int) bool {
	for _, p := range e.pairs {
		if p.code == code {
			return true
		}
	}
	return false
}

// all returns every value of a repeated group code, in file order.
func (e *dxfEnt) all(code int) []float64 {
	var out []float64
	for _, p := range e.pairs {
		if p.code == code {
			if v, err := strconv.ParseFloat(strings.TrimSpace(p.val), 64); err == nil && !math.IsNaN(v) && !math.IsInf(v, 0) {
				out = append(out, v)
			}
		}
	}
	return out
}

type dxfBlock struct {
	base Pt
	ents []*dxfEnt
}

type dxfDoc struct {
	insUnits int
	hidden   map[string]bool // layers that are frozen or switched off
	blocks   map[string]*dxfBlock
	model    []*dxfEnt
}

func readDXFPairs(data []byte) ([]dxfPair, error) {
	sc := bufio.NewScanner(bytes.NewReader(bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))))
	sc.Buffer(make([]byte, 1<<16), 1<<24)
	var out []dxfPair
	for sc.Scan() {
		cs := strings.TrimSpace(sc.Text())
		if cs == "" {
			continue
		}
		code, err := strconv.Atoi(cs)
		if err != nil {
			return nil, fmt.Errorf("invalid DXF: expected a group code, found %q (line %d of pairs)", trunc(cs, 20), len(out)+1)
		}
		if !sc.Scan() {
			return nil, fmt.Errorf("invalid DXF: the file ends after group code %d", code)
		}
		out = append(out, dxfPair{code, strings.TrimRight(sc.Text(), "\r")})
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("invalid DXF: %v", err)
	}
	return out, nil
}

func trunc(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

// splitEntities cuts a section's pairs into entities: each group code 0 starts a new one.
func splitEntities(pairs []dxfPair) []*dxfEnt {
	var out []*dxfEnt
	var cur *dxfEnt
	for _, p := range pairs {
		if p.code == 0 {
			cur = &dxfEnt{typ: strings.ToUpper(strings.TrimSpace(p.val))}
			out = append(out, cur)
			continue
		}
		if cur != nil {
			cur.pairs = append(cur.pairs, p)
		}
	}
	return out
}

func parseDXFDoc(data []byte) (*dxfDoc, error) {
	if bytes.HasPrefix(bytes.TrimPrefix(data, []byte("\xef\xbb\xbf")), []byte("AutoCAD Binary DXF")) {
		return nil, fmt.Errorf("this is a binary DXF: save it as an ASCII DXF (or export it as SVG) and import it again")
	}
	pairs, err := readDXFPairs(data)
	if err != nil {
		return nil, err
	}
	doc := &dxfDoc{hidden: map[string]bool{}, blocks: map[string]*dxfBlock{}}
	sawSection := false
	for i := 0; i < len(pairs); i++ {
		if pairs[i].code != 0 || !strings.EqualFold(strings.TrimSpace(pairs[i].val), "SECTION") {
			continue
		}
		if i+1 >= len(pairs) || pairs[i+1].code != 2 {
			continue
		}
		name := strings.ToUpper(strings.TrimSpace(pairs[i+1].val))
		j := i + 2
		for j < len(pairs) && !(pairs[j].code == 0 && strings.EqualFold(strings.TrimSpace(pairs[j].val), "ENDSEC")) {
			j++
		}
		body := pairs[i+2 : j]
		sawSection = true
		switch name {
		case "HEADER":
			for k := 0; k+1 < len(body); k++ {
				if body[k].code == 9 && body[k].val == "$INSUNITS" && body[k+1].code == 70 {
					doc.insUnits, _ = strconv.Atoi(strings.TrimSpace(body[k+1].val))
				}
			}
		case "TABLES":
			for _, e := range splitEntities(body) {
				if e.typ == "LAYER" {
					if int(e.num(70, 0))&1 != 0 || e.num(62, 1) < 0 {
						doc.hidden[e.str(2)] = true
					}
				}
			}
		case "BLOCKS":
			var cur *dxfBlock
			for _, e := range splitEntities(body) {
				switch e.typ {
				case "BLOCK":
					cur = &dxfBlock{base: Pt{e.num(10, 0), e.num(20, 0)}}
					doc.blocks[e.str(2)] = cur
				case "ENDBLK":
					cur = nil
				default:
					if cur != nil {
						cur.ents = append(cur.ents, e)
					}
				}
			}
		case "ENTITIES":
			doc.model = splitEntities(body)
		}
		i = j
	}
	if !sawSection {
		return nil, fmt.Errorf("not a DXF file (no SECTION found)")
	}
	return doc, nil
}

// ---- geometry ------------------------------------------------------------------------------

type dxfChain struct {
	segs   []Seg
	closed bool
}

func reverseSegs(segs []Seg) []Seg {
	out := make([]Seg, len(segs))
	for i, s := range segs {
		out[len(segs)-1-i] = Seg{s.P1, s.C2, s.C1, s.P0}
	}
	return out
}

// paramArc approximates the arc P(t) = c + a·cos t + b·sin t, t from t0 over sweep (signed),
// with cubic Beziers of at most a quarter turn each.
func paramArc(c, a, b Pt, t0, sweep float64) []Seg {
	n := int(math.Ceil(math.Abs(sweep)/(math.Pi/2) - 1e-9))
	if n < 1 {
		n = 1
	}
	dt := sweep / float64(n)
	k := 4.0 / 3.0 * math.Tan(dt/4)
	pt := func(t float64) Pt {
		return Pt{c[0] + a[0]*math.Cos(t) + b[0]*math.Sin(t), c[1] + a[1]*math.Cos(t) + b[1]*math.Sin(t)}
	}
	d := func(t float64) Pt {
		return Pt{-a[0]*math.Sin(t) + b[0]*math.Cos(t), -a[1]*math.Sin(t) + b[1]*math.Cos(t)}
	}
	out := make([]Seg, n)
	for i := 0; i < n; i++ {
		ta, tb := t0+float64(i)*dt, t0+float64(i+1)*dt
		p0, p1 := pt(ta), pt(tb)
		out[i] = Seg{p0, padd(p0, pmul(d(ta), k)), psub(p1, pmul(d(tb), k)), p1}
	}
	return out
}

// bulgeSegs is the arc between two polyline vertices: bulge = tan(sweep/4), positive = counter-clockwise.
func bulgeSegs(p0, p1 Pt, bulge float64) []Seg {
	if math.Abs(bulge) < 1e-12 {
		return lineSegs([]Pt{p0, p1}, false)
	}
	chord := pdist(p0, p1)
	if chord < 1e-12 {
		return nil
	}
	theta := 4 * math.Atan(bulge)
	mid := pmul(padd(p0, p1), 0.5)
	left := Pt{-(p1[1] - p0[1]) / chord, (p1[0] - p0[0]) / chord}
	ctr := padd(mid, pmul(left, chord/2/math.Tan(theta/2)))
	r := pdist(ctr, p0)
	segs := paramArc(ctr, Pt{r, 0}, Pt{0, r}, math.Atan2(p0[1]-ctr[1], p0[0]-ctr[0]), theta)
	segs[0].P0, segs[len(segs)-1].P1 = p0, p1 // exact end points so chains connect
	return segs
}

type dxfVertex struct {
	p     Pt
	bulge float64
}

func polySegs(vs []dxfVertex, closed bool) []Seg {
	var out []Seg
	n := len(vs)
	last := n - 1
	if closed {
		last = n
	}
	for i := 0; i < last; i++ {
		out = append(out, bulgeSegs(vs[i].p, vs[(i+1)%n].p, vs[i].bulge)...)
	}
	return out
}

// lwVertices reads the repeated 10/20/42 groups of an LWPOLYLINE in order.
func lwVertices(e *dxfEnt) []dxfVertex {
	var out []dxfVertex
	for _, p := range e.pairs {
		v, err := strconv.ParseFloat(strings.TrimSpace(p.val), 64)
		if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
			continue
		}
		switch p.code {
		case 10:
			out = append(out, dxfVertex{p: Pt{v, 0}})
		case 20:
			if len(out) > 0 {
				out[len(out)-1].p[1] = v
			}
		case 42:
			if len(out) > 0 {
				out[len(out)-1].bulge = v
			}
		}
	}
	return out
}

// extrusionFlip: entities in an object coordinate system whose normal is -Z are mirrored in X.
// ok is false for a normal that is not ±Z (the entity lies in a tilted plane).
func extrusionFlip(e *dxfEnt) (flip, ok bool) {
	nx, ny, nz := e.num(210, 0), e.num(220, 0), e.num(230, 1)
	if math.Abs(nx) > 1e-6 || math.Abs(ny) > 1e-6 {
		return false, false
	}
	return nz < 0, true
}

// deBoor evaluates a (possibly rational) B-spline of the given degree at u.
func splinePoint(deg int, knots []float64, ctrl []Pt, w []float64, u float64) Pt {
	n := len(ctrl) - 1
	k := deg
	for k < n && u >= knots[k+1] {
		k++
	}
	hx := make([][3]float64, deg+1)
	for j := 0; j <= deg; j++ {
		i := j + k - deg
		wt := 1.0
		if w != nil {
			wt = w[i]
		}
		hx[j] = [3]float64{ctrl[i][0] * wt, ctrl[i][1] * wt, wt}
	}
	for r := 1; r <= deg; r++ {
		for j := deg; j >= r; j-- {
			i := j + k - deg
			den := knots[i+deg-r+1] - knots[i]
			al := 0.0
			if den != 0 {
				al = (u - knots[i]) / den
			}
			for c := 0; c < 3; c++ {
				hx[j][c] = (1-al)*hx[j-1][c] + al*hx[j][c]
			}
		}
	}
	h := hx[deg]
	if h[2] == 0 {
		return Pt{h[0], h[1]}
	}
	return Pt{h[0] / h[2], h[1] / h[2]}
}

func splineChain(e *dxfEnt) (dxfChain, error) {
	flags := int(e.num(70, 0))
	deg := int(e.num(71, 3))
	xs, ys := e.all(10), e.all(20)
	fx, fy := e.all(11), e.all(21)
	closed := flags&1 != 0
	if len(xs) >= 2 && len(xs) == len(ys) && deg >= 1 && deg < len(xs) {
		knots := e.all(40)
		ctrl := make([]Pt, len(xs))
		for i := range xs {
			ctrl[i] = Pt{xs[i], ys[i]}
		}
		var w []float64
		if ws := e.all(41); len(ws) == len(ctrl) {
			w = ws
		}
		if len(knots) != len(ctrl)+deg+1 {
			return dxfChain{}, fmt.Errorf("spline knot vector does not match its control points")
		}
		u0, u1 := knots[deg], knots[len(ctrl)]
		if !(u1 > u0) {
			return dxfChain{}, fmt.Errorf("spline has an empty parameter range")
		}
		steps := 24 * len(ctrl)
		pts := make([]Pt, 0, steps+1)
		for i := 0; i <= steps; i++ {
			u := u0 + (u1-u0)*float64(i)/float64(steps)
			if i == steps {
				u = math.Nextafter(u1, u0)
			}
			pts = append(pts, splinePoint(deg, knots, ctrl, w, u))
		}
		return fitSampled(pts, closed), nil
	}
	if len(fx) >= 2 && len(fx) == len(fy) { // fit points only: a smooth curve through them
		pts := make([]Pt, len(fx))
		for i := range fx {
			pts[i] = Pt{fx[i], fy[i]}
		}
		return catmullRom(pts, closed), nil
	}
	return dxfChain{}, fmt.Errorf("spline has no usable control or fit points")
}

// fitSampled turns a dense polyline into Bezier segments within a tolerance that scales with its size.
func fitSampled(pts []Pt, closed bool) dxfChain {
	lo, hi := Pt{math.Inf(1), math.Inf(1)}, Pt{math.Inf(-1), math.Inf(-1)}
	for _, p := range pts {
		lo = Pt{math.Min(lo[0], p[0]), math.Min(lo[1], p[1])}
		hi = Pt{math.Max(hi[0], p[0]), math.Max(hi[1], p[1])}
	}
	tol := 1e-4 * math.Max(pdist(lo, hi), 1e-9)
	if closed && pdist(pts[0], pts[len(pts)-1]) > tol {
		closed = false
	}
	var segs []Seg
	if closed {
		segs = FitClosed(pts, tol, 60)
	} else {
		segs = FitOpen(pts, tol, 60)
	}
	return dxfChain{segs: segs, closed: closed && len(segs) > 0}
}

// catmullRom: a C1 curve through the points (uniform Catmull-Rom as cubic Beziers).
func catmullRom(pts []Pt, closed bool) dxfChain {
	n := len(pts)
	at := func(i int) Pt {
		if closed {
			return pts[((i%n)+n)%n]
		}
		if i < 0 {
			return psub(pmul(pts[0], 2), pts[1])
		}
		if i >= n {
			return psub(pmul(pts[n-1], 2), pts[n-2])
		}
		return pts[i]
	}
	last := n - 1
	if closed {
		last = n
	}
	var segs []Seg
	for i := 0; i < last; i++ {
		p0, p1, p2, p3 := at(i-1), at(i), at(i+1), at(i+2)
		segs = append(segs, Seg{p1, padd(p1, pmul(psub(p2, p0), 1.0/6)), psub(p2, pmul(psub(p3, p1), 1.0/6)), p2})
	}
	return dxfChain{segs: segs, closed: closed}
}

// ---- walking the drawing -------------------------------------------------------------------

type dxfWalker struct {
	doc     *dxfDoc
	chains  []dxfChain
	skipped map[string]int
	bad     map[string]int
	hidden  int
	segs    int
	err     error
}

func (w *dxfWalker) add(c dxfChain, tf affine, flipX bool) {
	if len(c.segs) == 0 {
		return
	}
	if flipX {
		tf = tf.mul(affine{-1, 0, 0, 1, 0, 0})
	}
	out := make([]Seg, len(c.segs))
	for i, s := range c.segs {
		out[i] = Seg{tf.apply(s.P0), tf.apply(s.C1), tf.apply(s.C2), tf.apply(s.P1)}
	}
	w.segs += len(out)
	if w.segs > maxDXFSegs && w.err == nil {
		w.err = fmt.Errorf("the drawing has more than %d curve segments: simplify it or split it", maxDXFSegs)
	}
	w.chains = append(w.chains, dxfChain{segs: out, closed: c.closed})
}

func (w *dxfWalker) walk(ents []*dxfEnt, tf affine, depth int, topLevel bool) {
	for i := 0; i < len(ents) && w.err == nil; i++ {
		e := ents[i]
		if topLevel && e.str(67) == "1" { // paper space
			continue
		}
		if e.num(60, 0) == 1 || w.doc.hidden[e.str(8)] {
			w.hidden++
			continue
		}
		flip, planar := extrusionFlip(e)
		switch e.typ {
		case "LINE":
			p0, p1 := Pt{e.num(10, 0), e.num(20, 0)}, Pt{e.num(11, 0), e.num(21, 0)}
			w.add(dxfChain{segs: lineSegs([]Pt{p0, p1}, false)}, tf, false)
		case "CIRCLE", "ARC":
			if !planar {
				w.bad[e.typ]++
				continue
			}
			r := e.num(40, 0)
			if !(r > 0) {
				continue
			}
			c := Pt{e.num(10, 0), e.num(20, 0)}
			if e.typ == "CIRCLE" {
				w.add(dxfChain{segs: ellipseSegs(c[0], c[1], r, r), closed: true}, tf, flip)
				continue
			}
			a0, a1 := e.num(50, 0)*math.Pi/180, e.num(51, 360)*math.Pi/180
			sweep := math.Mod(a1-a0, 2*math.Pi)
			if sweep <= 1e-12 {
				sweep += 2 * math.Pi
			}
			w.add(dxfChain{segs: paramArc(c, Pt{r, 0}, Pt{0, r}, a0, sweep)}, tf, flip)
		case "ELLIPSE":
			major := Pt{e.num(11, 0), e.num(21, 0)}
			ratio := e.num(40, 1)
			if !planar {
				w.bad["ELLIPSE"]++
				continue
			}
			if !(ratio > 0) || plen(major) < 1e-12 {
				continue
			}
			minor := Pt{-major[1] * ratio, major[0] * ratio}
			if flip {
				minor = pmul(minor, -1)
			}
			t0, t1 := e.num(41, 0), e.num(42, 2*math.Pi)
			sweep := t1 - t0
			full := math.Abs(math.Mod(sweep, 2*math.Pi)) < 1e-9
			if sweep <= 0 {
				sweep += 2 * math.Pi
			}
			if full || sweep > 2*math.Pi-1e-9 {
				sweep, full = 2*math.Pi, true
			}
			w.add(dxfChain{segs: paramArc(Pt{e.num(10, 0), e.num(20, 0)}, major, minor, t0, sweep), closed: full}, tf, false)
		case "LWPOLYLINE":
			if !planar {
				w.bad[e.typ]++
				continue
			}
			vs := lwVertices(e)
			closed := int(e.num(70, 0))&1 != 0
			if len(vs) >= 2 {
				w.add(dxfChain{segs: polySegs(vs, closed), closed: closed && len(vs) >= 3}, tf, flip)
			}
		case "POLYLINE":
			flags := int(e.num(70, 0))
			j := i + 1
			var vs []dxfVertex
			for ; j < len(ents) && ents[j].typ != "SEQEND"; j++ {
				if ents[j].typ != "VERTEX" {
					break
				}
				if f := int(ents[j].num(70, 0)); f&16 != 0 { // spline frame points, polyface data
					continue
				}
				vs = append(vs, dxfVertex{Pt{ents[j].num(10, 0), ents[j].num(20, 0)}, ents[j].num(42, 0)})
			}
			i = j // the vertices (and SEQEND) belong to this polyline
			if flags&(16|64) != 0 {
				w.bad["POLYLINE (mesh)"]++
				continue
			}
			if !planar {
				w.bad["POLYLINE"]++
				continue
			}
			closed := flags&1 != 0
			if len(vs) >= 2 {
				w.add(dxfChain{segs: polySegs(vs, closed), closed: closed && len(vs) >= 3}, tf, flip)
			}
		case "SPLINE":
			c, err := splineChain(e)
			if err != nil {
				w.bad["SPLINE"]++
				continue
			}
			w.add(c, tf, false)
		case "INSERT":
			w.insert(e, tf, depth)
		case "SEQEND", "VERTEX", "ATTRIB", "ATTDEF":
		default:
			w.skipped[e.typ]++
		}
	}
}

func (w *dxfWalker) insert(e *dxfEnt, tf affine, depth int) {
	name := e.str(2)
	blk := w.doc.blocks[name]
	if blk == nil {
		w.bad["INSERT (unknown block)"]++
		return
	}
	if depth >= maxDXFDepth {
		w.bad["INSERT (nested too deep)"]++
		return
	}
	sx, sy := e.num(41, 1), e.num(42, 1)
	rot := e.num(50, 0) * math.Pi / 180
	cols, rows := int(e.num(70, 1)), int(e.num(71, 1))
	if cols < 1 {
		cols = 1
	}
	if rows < 1 {
		rows = 1
	}
	if cols*rows > 10000 {
		w.bad["INSERT (array too large)"]++
		return
	}
	dc, dr := e.num(44, 0), e.num(45, 0)
	flip, planar := extrusionFlip(e)
	if !planar {
		w.bad["INSERT"]++
		return
	}
	if flip { // the insert is placed in a mirrored (object) coordinate system
		tf = tf.mul(affine{-1, 0, 0, 1, 0, 0})
	}
	cs, sn := math.Cos(rot), math.Sin(rot)
	ins := Pt{e.num(10, 0), e.num(20, 0)}
	for r := 0; r < rows && w.err == nil; r++ {
		for c := 0; c < cols && w.err == nil; c++ {
			// p' = ins + offset(rot(c·dc, r·dr)) + R(rot)·S·(p − base)
			off := Pt{float64(c) * dc, float64(r) * dr}
			offR := Pt{off[0]*cs - off[1]*sn, off[0]*sn + off[1]*cs}
			m := affine{cs * sx, sn * sx, -sn * sy, cs * sy, 0, 0}
			m[4] = ins[0] + offR[0] - (m[0]*blk.base[0] + m[2]*blk.base[1])
			m[5] = ins[1] + offR[1] - (m[1]*blk.base[0] + m[3]*blk.base[1])
			w.walk(blk.ents, tf.mul(m), depth+1, false)
		}
	}
}

// joinChains connects open chains end to end where their ends meet within tol, so loose LINEs
// and ARCs that outline a profile become closed shapes.
func joinChains(chains []dxfChain, tol float64) []dxfChain {
	var out []dxfChain
	var open []dxfChain
	for _, c := range chains {
		if c.closed {
			out = append(out, c)
		} else if len(c.segs) > 0 {
			open = append(open, c)
		}
	}
	type key [2]int64
	cell := func(p Pt) key { return key{int64(math.Floor(p[0] / tol)), int64(math.Floor(p[1] / tol))} }
	type end struct {
		chain int
		start bool
	}
	idx := map[key][]end{}
	for i, c := range open {
		s, e := c.segs[0].P0, c.segs[len(c.segs)-1].P1
		idx[cell(s)] = append(idx[cell(s)], end{i, true})
		idx[cell(e)] = append(idx[cell(e)], end{i, false})
	}
	used := make([]bool, len(open))
	find := func(p Pt) (end, bool) {
		k := cell(p)
		best, bd, ok := end{}, tol, false
		for dx := int64(-1); dx <= 1; dx++ {
			for dy := int64(-1); dy <= 1; dy++ {
				for _, c := range idx[key{k[0] + dx, k[1] + dy}] {
					if used[c.chain] {
						continue
					}
					q := open[c.chain].segs[0].P0
					if !c.start {
						q = open[c.chain].segs[len(open[c.chain].segs)-1].P1
					}
					d := pdist(p, q)
					if d <= bd && (!ok || d < bd || c.chain < best.chain) {
						best, bd, ok = c, d, true
					}
				}
			}
		}
		return best, ok
	}
	for i := range open {
		if used[i] {
			continue
		}
		used[i] = true
		cur := append([]Seg{}, open[i].segs...)
		for { // extend at the tail
			if pdist(cur[0].P0, cur[len(cur)-1].P1) <= tol && len(cur) > 1 {
				break
			}
			nx, ok := find(cur[len(cur)-1].P1)
			if !ok {
				break
			}
			used[nx.chain] = true
			segs := open[nx.chain].segs
			if !nx.start {
				segs = reverseSegs(segs)
			}
			segs = append([]Seg{}, segs...)
			segs[0].P0 = cur[len(cur)-1].P1
			cur = append(cur, segs...)
		}
		for { // extend at the head
			if pdist(cur[0].P0, cur[len(cur)-1].P1) <= tol && len(cur) > 1 {
				break
			}
			nx, ok := find(cur[0].P0)
			if !ok {
				break
			}
			used[nx.chain] = true
			segs := open[nx.chain].segs
			if nx.start {
				segs = reverseSegs(segs)
			}
			segs = append([]Seg{}, segs...)
			segs[len(segs)-1].P1 = cur[0].P0
			cur = append(segs, cur...)
		}
		closed := len(cur) > 1 && pdist(cur[0].P0, cur[len(cur)-1].P1) <= tol
		if closed {
			cur[len(cur)-1].P1 = cur[0].P0
		}
		out = append(out, dxfChain{segs: cur, closed: closed})
	}
	return out
}

// ParseDXF reads the 2D geometry of an ASCII DXF: LINE, ARC, CIRCLE, ELLIPSE, LWPOLYLINE,
// POLYLINE, SPLINE and INSERT (blocks, scaled, rotated, arrays). Loose lines and arcs whose ends
// meet are joined into closed profiles. Text, dimensions, hatches, 3D entities, hidden layers
// and paper space are left out and reported in Warnings. Y is flipped to point down like an image.
//
// When the file states its units ($INSUNITS) the drawing is returned in µm and
// NativePxPerMM is 1000; otherwise it stays in drawing units and NativePxPerMM is 0.
func ParseDXF(data []byte) (*SVG, error) {
	doc, err := parseDXFDoc(data)
	if err != nil {
		return nil, err
	}
	w := &dxfWalker{doc: doc, skipped: map[string]int{}, bad: map[string]int{}}
	w.walk(doc.model, identity, 0, true)
	if w.err != nil {
		return nil, w.err
	}
	if len(w.chains) == 0 {
		return nil, fmt.Errorf("VECTORIZE_EMPTY: the DXF has no 2D geometry I can read%s", w.describe())
	}
	lo, hi := Pt{math.Inf(1), math.Inf(1)}, Pt{math.Inf(-1), math.Inf(-1)}
	for _, c := range w.chains {
		for _, s := range c.segs {
			for i := 0; i <= 16; i++ {
				p := s.At(float64(i) / 16)
				lo = Pt{math.Min(lo[0], p[0]), math.Min(lo[1], p[1])}
				hi = Pt{math.Max(hi[0], p[0]), math.Max(hi[1], p[1])}
			}
		}
	}
	if math.IsInf(lo[0], 0) || math.IsNaN(lo[0]) || math.IsInf(hi[0], 0) || math.IsNaN(hi[0]) {
		return nil, fmt.Errorf("invalid DXF: coordinates are not finite numbers")
	}
	diag := pdist(lo, hi)
	chains := joinChains(w.chains, math.Max(1e-5*diag, 1e-9))

	out := &SVG{}
	k := 1.0
	if mm, ok := dxfUnitMM[doc.insUnits]; ok && doc.insUnits != 0 {
		k = mm * dxfUnitScale // drawing unit → µm
		out.NativePxPerMM = dxfUnitScale
	}
	// move the drawing to the origin, flip Y and scale
	f := func(p Pt) Pt { return Pt{(p[0] - lo[0]) * k, (hi[1] - p[1]) * k} }
	out.Width, out.Height = (hi[0]-lo[0])*k, (hi[1]-lo[1])*k
	openN, closedN := 0, 0
	for _, c := range chains {
		segs := make([]Seg, len(c.segs))
		for i, s := range c.segs {
			segs[i] = Seg{f(s.P0), f(s.C1), f(s.C2), f(s.P1)}
		}
		out.Paths = append(out.Paths, SVGPath{Segs: segs, Closed: c.closed, Fill: c.closed, Stroke: true})
		if c.closed {
			closedN++
		} else {
			openN++
		}
	}
	if openN > 0 {
		out.Warnings = append(out.Warnings, fmt.Sprintf("DXF_OPEN_PROFILES: %d open chain(s) do not close into a shape and are left out of filled regions (centerline mode keeps them); check for gaps between lines", openN))
	}
	if closedN == 0 {
		out.Warnings = append(out.Warnings, "DXF_NO_CLOSED_SHAPES: no closed profile was found (use centerline mode, or close the gaps in the drawing)")
	}
	if d := w.describe(); d != "" {
		out.Warnings = append(out.Warnings, "DXF_IGNORED"+d)
	}
	return out, nil
}

func (w *dxfWalker) describe() string {
	var parts []string
	list := func(m map[string]int, what string) {
		var keys []string
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			parts = append(parts, fmt.Sprintf("%d %s %s", m[k], k, what))
		}
	}
	list(w.skipped, "not imported")
	list(w.bad, "could not be read")
	if w.hidden > 0 {
		parts = append(parts, fmt.Sprintf("%d on hidden or frozen layers", w.hidden))
	}
	if len(parts) == 0 {
		return ""
	}
	return ": " + strings.Join(parts, ", ")
}
