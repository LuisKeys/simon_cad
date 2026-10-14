package vectorize

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
)

// SVG is a parsed vector drawing: subpaths of cubic Bezier segments in the document's user
// units (Y down, like an image).
type SVG struct {
	Width, Height float64 // user units (from viewBox, else width/height)
	Paths         []SVGPath
}

// SVGPath is one subpath with its painting.
type SVGPath struct {
	Segs        []Seg
	Closed      bool
	Fill        bool
	Stroke      bool
	StrokeWidth float64
}

type affine [6]float64 // a b c d e f: x' = a x + c y + e, y' = b x + d y + f

var identity = affine{1, 0, 0, 1, 0, 0}

func (m affine) mul(n affine) affine { // m applied after n
	return affine{
		m[0]*n[0] + m[2]*n[1], m[1]*n[0] + m[3]*n[1],
		m[0]*n[2] + m[2]*n[3], m[1]*n[2] + m[3]*n[3],
		m[0]*n[4] + m[2]*n[5] + m[4], m[1]*n[4] + m[3]*n[5] + m[5],
	}
}

func (m affine) apply(p Pt) Pt { return Pt{m[0]*p[0] + m[2]*p[1] + m[4], m[1]*p[0] + m[3]*p[1] + m[5]} }

func (m affine) scale() float64 { return math.Sqrt(math.Abs(m[0]*m[3] - m[1]*m[2])) }

func parseTransform(s string) (affine, error) {
	out := identity
	for s = strings.TrimSpace(s); s != ""; s = strings.TrimSpace(s) {
		open := strings.IndexByte(s, '(')
		cl := strings.IndexByte(s, ')')
		if open < 0 || cl < open {
			return out, fmt.Errorf("bad transform %q", s)
		}
		name := strings.TrimSpace(s[:open])
		args, err := numbers(s[open+1 : cl])
		if err != nil {
			return out, err
		}
		s = strings.TrimLeft(s[cl+1:], " ,")
		var m affine
		switch name {
		case "matrix":
			if len(args) != 6 {
				return out, fmt.Errorf("matrix needs 6 numbers")
			}
			copy(m[:], args)
		case "translate":
			ty := 0.0
			if len(args) > 1 {
				ty = args[1]
			}
			m = affine{1, 0, 0, 1, args[0], ty}
		case "scale":
			sy := args[0]
			if len(args) > 1 {
				sy = args[1]
			}
			m = affine{args[0], 0, 0, sy, 0, 0}
		case "rotate":
			a := args[0] * math.Pi / 180
			c, sn := math.Cos(a), math.Sin(a)
			m = affine{c, sn, -sn, c, 0, 0}
			if len(args) == 3 {
				m = affine{1, 0, 0, 1, args[1], args[2]}.mul(m).mul(affine{1, 0, 0, 1, -args[1], -args[2]})
			}
		case "skewX":
			m = affine{1, 0, math.Tan(args[0] * math.Pi / 180), 1, 0, 0}
		case "skewY":
			m = affine{1, math.Tan(args[0] * math.Pi / 180), 0, 1, 0, 0}
		default:
			return out, fmt.Errorf("unsupported transform %q", name)
		}
		out = out.mul(m)
	}
	return out, nil
}

func numbers(s string) ([]float64, error) {
	var out []float64
	for _, f := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' || r == '\n' || r == '\r' }) {
		v, err := strconv.ParseFloat(f, 64)
		if err != nil {
			return nil, fmt.Errorf("bad number %q", f)
		}
		out = append(out, v)
	}
	return out, nil
}

type style struct {
	fill, stroke string
	strokeWidth  float64
	tf           affine
}

func (st style) child(attrs []xml.Attr) (style, error) {
	n := st
	for _, a := range attrs {
		switch a.Name.Local {
		case "fill":
			n.fill = strings.TrimSpace(a.Value)
		case "stroke":
			n.stroke = strings.TrimSpace(a.Value)
		case "stroke-width":
			if v, err := strconv.ParseFloat(strings.TrimSuffix(strings.TrimSpace(a.Value), "px"), 64); err == nil {
				n.strokeWidth = v
			}
		case "style":
			for _, decl := range strings.Split(a.Value, ";") {
				kv := strings.SplitN(decl, ":", 2)
				if len(kv) != 2 {
					continue
				}
				k, v := strings.TrimSpace(kv[0]), strings.TrimSpace(kv[1])
				switch k {
				case "fill":
					n.fill = v
				case "stroke":
					n.stroke = v
				case "stroke-width":
					if f, err := strconv.ParseFloat(strings.TrimSuffix(v, "px"), 64); err == nil {
						n.strokeWidth = f
					}
				}
			}
		case "transform":
			t, err := parseTransform(a.Value)
			if err != nil {
				return n, err
			}
			n.tf = n.tf.mul(t)
		}
	}
	return n, nil
}

func (st style) painted() (fill, stroke bool) {
	return st.fill != "none" && st.fill != "transparent", st.stroke != "" && st.stroke != "none"
}

func attr(attrs []xml.Attr, name string) string {
	for _, a := range attrs {
		if a.Name.Local == name {
			return a.Value
		}
	}
	return ""
}

func num(attrs []xml.Attr, name string, def float64) float64 {
	v := strings.TrimSpace(attr(attrs, name))
	v = strings.TrimSuffix(strings.TrimSuffix(v, "px"), "mm")
	if v == "" {
		return def
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return def
	}
	return f
}

const kappa = 0.5522847498307936

func ellipseSegs(cx, cy, rx, ry float64) []Seg {
	p := func(x, y float64) Pt { return Pt{cx + x*rx, cy + y*ry} }
	return []Seg{
		{p(1, 0), p(1, kappa), p(kappa, 1), p(0, 1)}, {p(0, 1), p(-kappa, 1), p(-1, kappa), p(-1, 0)},
		{p(-1, 0), p(-1, -kappa), p(-kappa, -1), p(0, -1)}, {p(0, -1), p(kappa, -1), p(1, -kappa), p(1, 0)},
	}
}

func lineSegs(pts []Pt, closed bool) []Seg {
	var out []Seg
	n := len(pts)
	last := n - 1
	if closed {
		last = n
	}
	for i := 0; i < last; i++ {
		a, b := pts[i], pts[(i+1)%n]
		out = append(out, Seg{a, padd(a, pmul(psub(b, a), 1.0/3)), padd(a, pmul(psub(b, a), 2.0/3)), b})
	}
	return out
}

// arcSegs converts an SVG elliptical arc to cubic segments (endpoint parametrization, SVG 1.1 F.6).
func arcSegs(p0 Pt, rx, ry, rotDeg float64, large, sweep bool, p1 Pt) []Seg {
	if pdist(p0, p1) < 1e-12 {
		return nil
	}
	rx, ry = math.Abs(rx), math.Abs(ry)
	if rx < 1e-12 || ry < 1e-12 {
		return lineSegs([]Pt{p0, p1}, false)
	}
	phi := rotDeg * math.Pi / 180
	cp, sp := math.Cos(phi), math.Sin(phi)
	dx, dy := (p0[0]-p1[0])/2, (p0[1]-p1[1])/2
	x1, y1 := cp*dx+sp*dy, -sp*dx+cp*dy
	if l := x1*x1/(rx*rx) + y1*y1/(ry*ry); l > 1 {
		s := math.Sqrt(l)
		rx, ry = rx*s, ry*s
	}
	num := rx*rx*ry*ry - rx*rx*y1*y1 - ry*ry*x1*x1
	den := rx*rx*y1*y1 + ry*ry*x1*x1
	co := 0.0
	if den > 0 && num > 0 {
		co = math.Sqrt(num / den)
	}
	if large == sweep {
		co = -co
	}
	cxp, cyp := co*rx*y1/ry, -co*ry*x1/rx
	cx, cy := cp*cxp-sp*cyp+(p0[0]+p1[0])/2, sp*cxp+cp*cyp+(p0[1]+p1[1])/2
	ang := func(ux, uy, vx, vy float64) float64 {
		a := math.Atan2(ux*vy-uy*vx, ux*vx+uy*vy)
		return a
	}
	th1 := ang(1, 0, (x1-cxp)/rx, (y1-cyp)/ry)
	dth := ang((x1-cxp)/rx, (y1-cyp)/ry, (-x1-cxp)/rx, (-y1-cyp)/ry)
	if !sweep && dth > 0 {
		dth -= 2 * math.Pi
	} else if sweep && dth < 0 {
		dth += 2 * math.Pi
	}
	n := int(math.Ceil(math.Abs(dth) / (math.Pi / 2)))
	step := dth / float64(n)
	var out []Seg
	pt := func(t float64) (Pt, Pt) { // point and derivative direction at angle t
		x, y := rx*math.Cos(t), ry*math.Sin(t)
		dx, dy := -rx*math.Sin(t), ry*math.Cos(t)
		return Pt{cp*x - sp*y + cx, sp*x + cp*y + cy}, Pt{cp*dx - sp*dy, sp*dx + cp*dy}
	}
	t := th1
	for i := 0; i < n; i++ {
		a, da := pt(t)
		b, db := pt(t + step)
		k := 4.0 / 3 * math.Tan(step/4)
		out = append(out, Seg{a, padd(a, pmul(da, k)), psub(b, pmul(db, k)), b})
		t += step
	}
	if len(out) > 0 {
		out[0].P0, out[len(out)-1].P1 = p0, p1
	}
	return out
}

// parsePathData converts a path "d" attribute into subpaths.
func parsePathData(d string) ([]SVGPath, error) {
	// The scanner reads numbers on demand: arc flags are single digits that may be glued to the
	// next number ("a1 1 0 01.5.5"), which only the parser knows.
	sc := &pathScanner{s: d}
	var out []SVGPath
	var cur *SVGPath
	var pos, start, lastC Pt
	var lastCmd byte
	flush := func() {
		if cur != nil && len(cur.Segs) > 0 {
			out = append(out, *cur)
		}
		cur = nil
	}
	nextN := sc.number
	flag := sc.flag
	var cmd byte
	for sc.skip(); !sc.done(); sc.skip() {
		if sc.atCommand() {
			cmd = sc.command()
			if cmd == 'Z' || cmd == 'z' {
				if cur != nil {
					cur.Closed = true
					if pdist(pos, start) > 1e-9 {
						cur.Segs = append(cur.Segs, lineSegs([]Pt{pos, start}, false)...)
					}
					flush()
				}
				pos = start
				lastCmd = 'z'
				continue
			}
		} else if cmd == 0 {
			return nil, fmt.Errorf("path data must start with a command")
		}
		rel := cmd >= 'a'
		c := cmd
		if rel {
			c = cmd - 32
		}
		get := func(x, y float64) Pt {
			if rel {
				return Pt{pos[0] + x, pos[1] + y}
			}
			return Pt{x, y}
		}
		switch c {
		case 'M':
			x, e1 := nextN()
			y, e2 := nextN()
			if e1 != nil || e2 != nil {
				return nil, fmt.Errorf("moveto needs x y")
			}
			flush()
			pos = get(x, y)
			start = pos
			cur = &SVGPath{}
			if rel {
				cmd = 'l'
			} else {
				cmd = 'L'
			}
		case 'L', 'H', 'V':
			var p Pt
			switch c {
			case 'L':
				x, e1 := nextN()
				y, e2 := nextN()
				if e1 != nil || e2 != nil {
					return nil, fmt.Errorf("lineto needs x y")
				}
				p = get(x, y)
			case 'H':
				x, err := nextN()
				if err != nil {
					return nil, err
				}
				p = Pt{x, pos[1]}
				if rel {
					p = Pt{pos[0] + x, pos[1]}
				}
			default:
				y, err := nextN()
				if err != nil {
					return nil, err
				}
				p = Pt{pos[0], y}
				if rel {
					p = Pt{pos[0], pos[1] + y}
				}
			}
			if cur == nil {
				cur = &SVGPath{}
			}
			cur.Segs = append(cur.Segs, lineSegs([]Pt{pos, p}, false)...)
			pos = p
		case 'C', 'S', 'Q', 'T':
			if cur == nil {
				cur = &SVGPath{}
			}
			var c1, c2, p Pt
			switch c {
			case 'C':
				v := make([]float64, 6)
				for k := range v {
					var err error
					if v[k], err = nextN(); err != nil {
						return nil, err
					}
				}
				c1, c2, p = get(v[0], v[1]), get(v[2], v[3]), get(v[4], v[5])
			case 'S':
				v := make([]float64, 4)
				for k := range v {
					var err error
					if v[k], err = nextN(); err != nil {
						return nil, err
					}
				}
				c1 = pos
				if lastCmd == 'C' || lastCmd == 'S' {
					c1 = psub(pmul(pos, 2), lastC)
				}
				c2, p = get(v[0], v[1]), get(v[2], v[3])
			case 'Q', 'T':
				var q Pt
				if c == 'Q' {
					v := make([]float64, 4)
					for k := range v {
						var err error
						if v[k], err = nextN(); err != nil {
							return nil, err
						}
					}
					q, p = get(v[0], v[1]), get(v[2], v[3])
				} else {
					v := make([]float64, 2)
					for k := range v {
						var err error
						if v[k], err = nextN(); err != nil {
							return nil, err
						}
					}
					q = pos
					if lastCmd == 'Q' || lastCmd == 'T' {
						q = psub(pmul(pos, 2), lastC)
					}
					p = get(v[0], v[1])
				}
				lastC = q
				c1, c2 = padd(pos, pmul(psub(q, pos), 2.0/3)), padd(p, pmul(psub(q, p), 2.0/3))
			}
			if c == 'C' || c == 'S' {
				lastC = c2
			}
			cur.Segs = append(cur.Segs, Seg{pos, c1, c2, p})
			pos = p
		case 'A':
			v := make([]float64, 3)
			for k := range v {
				var err error
				if v[k], err = nextN(); err != nil {
					return nil, err
				}
			}
			large, e1 := flag()
			sweep, e2 := flag()
			x, e3 := nextN()
			y, e4 := nextN()
			if e1 != nil || e2 != nil || e3 != nil || e4 != nil {
				return nil, fmt.Errorf("arc needs rx ry rotation large sweep x y")
			}
			p := get(x, y)
			if cur == nil {
				cur = &SVGPath{}
			}
			cur.Segs = append(cur.Segs, arcSegs(pos, v[0], v[1], v[2], large, sweep, p)...)
			pos = p
		default:
			return nil, fmt.Errorf("unsupported path command %q", cmd)
		}
		lastCmd = c
	}
	flush()
	return out, nil
}

// ParseSVG reads the subset of SVG that matters for vectorizing: path, rect, circle, ellipse,
// line, polyline and polygon, with transforms, fill and stroke. Text, images, gradients and
// clipping are ignored (they carry no editable outline).
func ParseSVG(data []byte) (*SVG, error) {
	dec := xml.NewDecoder(bytes.NewReader(data))
	dec.Strict = false
	out := &SVG{}
	stack := []style{{fill: "#000", tf: identity}}
	skipDepth := 0
	depth := 0
	sawSVG := false
	for {
		tk, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("invalid SVG: %w", err)
		}
		switch t := tk.(type) {
		case xml.StartElement:
			depth++
			name := t.Name.Local
			if skipDepth > 0 || name == "defs" || name == "clipPath" || name == "mask" || name == "symbol" || name == "style" || name == "text" || name == "image" {
				if skipDepth == 0 {
					skipDepth = depth
				}
				stack = append(stack, stack[len(stack)-1])
				continue
			}
			parent := stack[len(stack)-1]
			st, err := parent.child(t.Attr)
			if err != nil {
				return nil, err
			}
			stack = append(stack, st)
			var subs []SVGPath
			switch name {
			case "svg":
				if !sawSVG {
					sawSVG = true
					if vb := strings.Fields(strings.ReplaceAll(attr(t.Attr, "viewBox"), ",", " ")); len(vb) == 4 {
						var v [4]float64
						for i := range v {
							f, perr := strconv.ParseFloat(vb[i], 64)
							if perr != nil || math.IsNaN(f) || math.IsInf(f, 0) {
								return nil, fmt.Errorf("svg: invalid viewBox %q", attr(t.Attr, "viewBox"))
							}
							v[i] = f
						}
						x0, y0, w, h := v[0], v[1], v[2], v[3]
						out.Width, out.Height = w, h
						stack[len(stack)-1].tf = stack[len(stack)-1].tf.mul(affine{1, 0, 0, 1, -x0, -y0})
					} else {
						out.Width, out.Height = num(t.Attr, "width", 0), num(t.Attr, "height", 0)
					}
				}
			case "path":
				subs, err = parsePathData(attr(t.Attr, "d"))
				if err != nil {
					return nil, err
				}
			case "rect":
				x, y, w, h := num(t.Attr, "x", 0), num(t.Attr, "y", 0), num(t.Attr, "width", 0), num(t.Attr, "height", 0)
				if w > 0 && h > 0 {
					rx, ry := num(t.Attr, "rx", -1), num(t.Attr, "ry", -1)
					if rx < 0 && ry >= 0 {
						rx = ry
					}
					if ry < 0 && rx >= 0 {
						ry = rx
					}
					if rx > 0 {
						rx, ry = math.Min(rx, w/2), math.Min(ry, h/2)
						d := fmt.Sprintf("M%g %gH%gA%g %g 0 0 1 %g %gV%gA%g %g 0 0 1 %g %gH%gA%g %g 0 0 1 %g %gV%gA%g %g 0 0 1 %g %gZ",
							x+rx, y, x+w-rx, rx, ry, x+w, y+ry, y+h-ry, rx, ry, x+w-rx, y+h, x+rx, rx, ry, x, y+h-ry, y+ry, rx, ry, x+rx, y)
						subs, _ = parsePathData(d)
					} else {
						subs = []SVGPath{{Segs: lineSegs([]Pt{{x, y}, {x + w, y}, {x + w, y + h}, {x, y + h}}, true), Closed: true}}
					}
				}
			case "circle":
				if r := num(t.Attr, "r", 0); r > 0 {
					subs = []SVGPath{{Segs: ellipseSegs(num(t.Attr, "cx", 0), num(t.Attr, "cy", 0), r, r), Closed: true}}
				}
			case "ellipse":
				if rx, ry := num(t.Attr, "rx", 0), num(t.Attr, "ry", 0); rx > 0 && ry > 0 {
					subs = []SVGPath{{Segs: ellipseSegs(num(t.Attr, "cx", 0), num(t.Attr, "cy", 0), rx, ry), Closed: true}}
				}
			case "line":
				subs = []SVGPath{{Segs: lineSegs([]Pt{{num(t.Attr, "x1", 0), num(t.Attr, "y1", 0)}, {num(t.Attr, "x2", 0), num(t.Attr, "y2", 0)}}, false)}}
			case "polyline", "polygon":
				nums, err := numbers(attr(t.Attr, "points"))
				if err != nil || len(nums) < 4 {
					break
				}
				var pts []Pt
				for i := 0; i+1 < len(nums); i += 2 {
					pts = append(pts, Pt{nums[i], nums[i+1]})
				}
				subs = []SVGPath{{Segs: lineSegs(pts, name == "polygon"), Closed: name == "polygon"}}
			}
			fill, stroke := st.painted()
			for _, sp := range subs {
				if len(sp.Segs) == 0 {
					continue
				}
				// shapes drawn from a path whose subpath is open can still be filled (implicitly closed)
				sp.Fill, sp.Stroke = fill, stroke
				sp.StrokeWidth = st.strokeWidth * st.tf.scale()
				if sp.StrokeWidth == 0 && stroke {
					sp.StrokeWidth = 1 * st.tf.scale()
				}
				for i, s := range sp.Segs {
					sp.Segs[i] = Seg{st.tf.apply(s.P0), st.tf.apply(s.C1), st.tf.apply(s.C2), st.tf.apply(s.P1)}
				}
				out.Paths = append(out.Paths, sp)
			}
		case xml.EndElement:
			if skipDepth > 0 && depth == skipDepth {
				skipDepth = 0
			}
			depth--
			if len(stack) > 1 {
				stack = stack[:len(stack)-1]
			}
		}
	}
	if !sawSVG {
		return nil, fmt.Errorf("not an SVG document (no <svg> element)")
	}
	return out, nil
}

// bounds of all path control points.
func (s *SVG) bounds() (lo, hi Pt) {
	lo, hi = Pt{math.Inf(1), math.Inf(1)}, Pt{math.Inf(-1), math.Inf(-1)}
	for _, p := range s.Paths {
		for _, g := range p.Segs {
			for _, q := range []Pt{g.P0, g.C1, g.C2, g.P1} {
				lo = Pt{math.Min(lo[0], q[0]), math.Min(lo[1], q[1])}
				hi = Pt{math.Max(hi[0], q[0]), math.Max(hi[1], q[1])}
			}
		}
	}
	return
}

// RunSVG vectorizes a parsed SVG in the given mode. Fills become regions (holes by nesting),
// outlines are the same loops without grouping, and every path becomes a centerline stroke
// (with its stroke width when it has one).
func RunSVG(ctx context.Context, s *SVG, opt Options) (*Result, error) {
	if err := opt.normalize(); err != nil {
		return nil, err
	}
	lo, hi := s.bounds()
	if math.IsInf(lo[0], 1) {
		return nil, fmt.Errorf("VECTORIZE_EMPTY: the SVG has no drawable shapes")
	}
	w, h := s.Width, s.Height
	if !(w > 0) || !(h > 0) {
		w, h = hi[0], hi[1]
	}
	scale := 1.0
	if opt.PxPerMM > 0 {
		scale = 1 / opt.PxPerMM
	}
	res := &Result{Mode: opt.Mode, WidthPx: int(math.Ceil(w)), HeightPx: int(math.Ceil(h)), Scale: scale}
	res.HeightPx = int(math.Ceil(h))
	flipH := h
	conv := func(segs []Seg) []Seg {
		out := make([]Seg, len(segs))
		f := func(p Pt) Pt { return Pt{p[0] * scale, (flipH - p[1]) * scale} }
		for i, g := range segs {
			out[i] = Seg{f(g.P0), f(g.C1), f(g.C2), f(g.P1)}
		}
		return out
	}
	if opt.Mode == Centerline {
		for _, p := range s.Paths {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			segs := conv(p.Segs)
			n := len(segs) + 1
			if p.Closed {
				n = len(segs)
			}
			ws := make([]float64, n)
			for i := range ws {
				ws[i] = p.StrokeWidth * scale
			}
			res.Strokes = append(res.Strokes, Stroke{Segs: segs, Closed: p.Closed, Widths: ws, Length: ChainLength(segs)})
			res.Stats.Segments += len(segs)
		}
		res.Stats.Loops = len(res.Strokes)
		return res, nil
	}
	// filled closed shapes → loops → nesting
	var loops []Loop
	var segsOf [][]Seg
	for _, p := range s.Paths {
		if !p.Fill {
			continue
		}
		segs := p.Segs
		if !p.Closed { // an open filled path is implicitly closed
			if len(segs) == 0 || pdist(segs[len(segs)-1].P1, segs[0].P0) > 1e-9 {
				segs = append(append([]Seg{}, segs...), lineSegs([]Pt{segs[len(segs)-1].P1, segs[0].P0}, false)...)
			}
		}
		var poly []Pt
		for _, g := range segs {
			for i := 0; i < 8; i++ {
				poly = append(poly, g.At(float64(i)/8))
			}
		}
		if len(poly) >= 3 {
			loops = append(loops, Loop{Pts: poly})
			segsOf = append(segsOf, segs)
		}
	}
	parent := Nest(loops)
	fits := make([][]Seg, len(loops))
	for i := range loops {
		fits[i] = orient(conv(segsOf[i]), !loops[i].Hole)
		res.Stats.Segments += len(fits[i])
	}
	if opt.Mode == Outline {
		for i := range loops {
			res.Outlines = append(res.Outlines, ClosedLoop{Segs: fits[i], Hole: loops[i].Hole, Area: math.Abs(segArea(fits[i]))})
		}
		res.Stats.Loops = len(res.Outlines)
		return res, nil
	}
	regionOf := map[int]int{}
	for i := range loops {
		if !loops[i].Hole {
			regionOf[i] = len(res.Regions)
			res.Regions = append(res.Regions, Region{Outer: fits[i], Area: math.Abs(segArea(fits[i]))})
		}
	}
	for i := range loops {
		if loops[i].Hole {
			if ri, ok := regionOf[parent[i]]; ok {
				res.Regions[ri].Holes = append(res.Regions[ri].Holes, fits[i])
				res.Regions[ri].Area -= math.Abs(segArea(fits[i]))
			}
		}
	}
	res.Stats.Loops = len(res.Regions)
	if res.Stats.Loops == 0 {
		res.Warnings = append(res.Warnings, "VECTORIZE_EMPTY: the SVG has no filled shapes (try centerline mode for stroked drawings)")
	}
	return res, nil
}

// pathScanner reads path data on demand.
type pathScanner struct {
	s string
	i int
}

func (p *pathScanner) skip() {
	for p.i < len(p.s) && (p.s[p.i] == ' ' || p.s[p.i] == ',' || p.s[p.i] == '\t' || p.s[p.i] == '\n' || p.s[p.i] == '\r') {
		p.i++
	}
}

func (p *pathScanner) done() bool { return p.i >= len(p.s) }

func (p *pathScanner) atCommand() bool {
	c := p.s[p.i]
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func (p *pathScanner) command() byte {
	c := p.s[p.i]
	p.i++
	return c
}

// number reads one number (sign, digits, one dot, optional exponent); "1.5.5" is 1.5 then .5.
func (p *pathScanner) number() (float64, error) {
	p.skip()
	d := p.s
	i := p.i
	j := i
	if j < len(d) && (d[j] == '+' || d[j] == '-') {
		j++
	}
	seenDot, seenExp, digits := false, false, 0
	for j < len(d) {
		ch := d[j]
		switch {
		case ch >= '0' && ch <= '9':
			digits++
			j++
		case ch == '.' && !seenDot && !seenExp:
			seenDot = true
			j++
		case (ch == 'e' || ch == 'E') && !seenExp && digits > 0:
			seenExp = true
			j++
			if j < len(d) && (d[j] == '+' || d[j] == '-') {
				j++
			}
		default:
			goto done
		}
	}
done:
	if digits == 0 {
		return 0, fmt.Errorf("path data is missing a number near %q", d[i:minInt(len(d), i+12)])
	}
	v, err := strconv.ParseFloat(d[i:j], 64)
	if err != nil {
		return 0, fmt.Errorf("bad number %q in path data", d[i:j])
	}
	p.i = j
	return v, nil
}

// flag reads an arc flag: a single 0 or 1, which may be glued to what follows.
func (p *pathScanner) flag() (bool, error) {
	p.skip()
	if p.i < len(p.s) && (p.s[p.i] == '0' || p.s[p.i] == '1') {
		v := p.s[p.i] == '1'
		p.i++
		return v, nil
	}
	return false, fmt.Errorf("arc flag must be 0 or 1")
}
