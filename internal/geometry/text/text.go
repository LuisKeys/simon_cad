// Package text turns a string into filled regions made of cubic Béziers, ready to become a
// sketch: glyph outlines come from an OpenType/TrueType font (golang.org/x/image/font/sfnt),
// with kerning, letter spacing, alignment, mirroring and line breaks. Counters (the hole of an
// "o") are recognised by winding and nesting, not by the font's contour order.
package text

import (
	"fmt"
	"math"
	"os"
	"strings"

	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/sfnt"
	"golang.org/x/image/math/fixed"
)

// Pt is a point in mm, Y up.
type Pt = [2]float64

// Seg is a cubic Bézier.
type Seg struct{ P0, C1, C2, P1 Pt }

// Region is a filled shape: a counter-clockwise outer loop and clockwise holes.
type Region struct {
	Outer []Seg
	Holes [][]Seg
}

// Font is a parsed font.
type Font struct {
	f    *sfnt.Font
	Name string
}

// Builtin returns the embedded default font (Go Regular, BSD-licensed).
func Builtin() *Font {
	f, err := sfnt.Parse(goregular.TTF)
	if err != nil {
		panic(err) // embedded data: cannot fail
	}
	return &Font{f: f, Name: "builtin"}
}

// Parse reads a TTF/OTF font.
func Parse(data []byte, name string) (*Font, error) {
	f, err := sfnt.Parse(data)
	if err != nil {
		return nil, fmt.Errorf("not a TrueType/OpenType font: %v", err)
	}
	return &Font{f: f, Name: name}, nil
}

// Load reads a font file.
func Load(path string) (*Font, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(b, path)
}

// Options control the layout. Size is the em height in mm.
type Options struct {
	Size        float64
	Spacing     float64 // extra advance per glyph, in mm
	LineSpacing float64 // line height as a multiple of Size (default 1.2)
	Align       string  // left (default), center, right
	Mirror      bool    // flip left-right (a stamp prints mirrored)
	NoKerning   bool
}

// Layout is the laid-out text.
type Layout struct {
	Regions []Region
	Width   float64 // advance width of the widest line, mm
	Height  float64 // total height of the lines, mm
	Missing []rune  // characters the font has no glyph for
}

const ppem = 1000 // glyphs are loaded at 1000 px/em and scaled down: precision without hinting

func fx(v fixed.Int26_6) float64 { return float64(v) / 64 }

// Lay lays out s with the options.
func (f *Font) Lay(s string, o Options) (*Layout, error) {
	if !(o.Size > 0) || math.IsInf(o.Size, 0) {
		return nil, fmt.Errorf("size must be > 0")
	}
	if o.LineSpacing == 0 {
		o.LineSpacing = 1.2
	}
	switch o.Align {
	case "", "left", "center", "right":
	default:
		return nil, fmt.Errorf("align must be left, center or right (got %q)", o.Align)
	}
	if strings.TrimSpace(s) == "" {
		return nil, fmt.Errorf("text is empty")
	}
	k := o.Size / ppem
	var buf sfnt.Buffer
	lay := &Layout{}
	lines := strings.Split(strings.ReplaceAll(s, "\r", ""), "\n")
	type placed struct {
		regs  []Region
		width float64
	}
	rows := make([]placed, len(lines))
	missing := map[rune]bool{}
	for li, line := range lines {
		x := 0.0
		var prev sfnt.GlyphIndex
		var loops []loop
		for _, r := range line {
			gi, err := f.f.GlyphIndex(&buf, r)
			if err != nil {
				return nil, err
			}
			if gi == 0 && r != ' ' {
				if !missing[r] {
					missing[r] = true
					lay.Missing = append(lay.Missing, r)
				}
			}
			if prev != 0 && !o.NoKerning {
				if kern, err := f.f.Kern(&buf, prev, gi, fixed.I(ppem), font.HintingNone); err == nil {
					x += fx(kern) * k
				}
			}
			segs, err := f.f.LoadGlyph(&buf, gi, fixed.I(ppem), nil)
			if err != nil {
				return nil, err
			}
			loops = append(loops, glyphLoops(segs, x, k)...)
			adv, err := f.f.GlyphAdvance(&buf, gi, fixed.I(ppem), font.HintingNone)
			if err != nil {
				return nil, err
			}
			x += fx(adv)*k + o.Spacing
			prev = gi
		}
		rows[li] = placed{regs: group(loops), width: math.Max(0, x-o.Spacing)}
		lay.Width = math.Max(lay.Width, rows[li].width)
	}
	met, err := f.f.Metrics(&buf, fixed.I(ppem), font.HintingNone)
	if err != nil {
		return nil, err
	}
	asc := fx(met.Ascent) * k
	step := o.Size * o.LineSpacing
	lay.Height = asc + step*float64(len(lines)-1)
	for li, row := range rows {
		dx := 0.0
		switch o.Align {
		case "center":
			dx = (lay.Width - row.width) / 2
		case "right":
			dx = lay.Width - row.width
		}
		dy := -step * float64(li)
		for _, rg := range row.regs {
			lay.Regions = append(lay.Regions, shift(rg, dx, dy))
		}
	}
	if o.Mirror {
		for i := range lay.Regions {
			lay.Regions[i] = mirrorX(lay.Regions[i], lay.Width)
		}
	}
	if len(lay.Regions) == 0 {
		return nil, fmt.Errorf("the text has no visible glyphs")
	}
	return lay, nil
}

// loop is one closed glyph contour.
type loop struct {
	segs []Seg
	area float64 // signed (positive = counter-clockwise)
	bbox [4]float64
}

func glyphLoops(segs sfnt.Segments, x0, k float64) []loop {
	var out []loop
	var cur []Seg
	var start, pen Pt
	tr := func(p fixed.Point26_6) Pt { return Pt{x0 + fx(p.X)*k, -fx(p.Y) * k} } // sfnt Y grows downward
	closeLoop := func() {
		if len(cur) == 0 {
			return
		}
		if d := dist(pen, start); d > 1e-9 {
			cur = append(cur, line(pen, start))
		}
		out = append(out, makeLoop(cur))
		cur = nil
	}
	for _, s := range segs {
		switch s.Op {
		case sfnt.SegmentOpMoveTo:
			closeLoop()
			start = tr(s.Args[0])
			pen = start
		case sfnt.SegmentOpLineTo:
			p := tr(s.Args[0])
			if dist(p, pen) > 1e-9 {
				cur = append(cur, line(pen, p))
			}
			pen = p
		case sfnt.SegmentOpQuadTo:
			c, p := tr(s.Args[0]), tr(s.Args[1])
			cur = append(cur, Seg{pen,
				Pt{pen[0] + 2.0/3*(c[0]-pen[0]), pen[1] + 2.0/3*(c[1]-pen[1])},
				Pt{p[0] + 2.0/3*(c[0]-p[0]), p[1] + 2.0/3*(c[1]-p[1])}, p})
			pen = p
		case sfnt.SegmentOpCubeTo:
			c1, c2, p := tr(s.Args[0]), tr(s.Args[1]), tr(s.Args[2])
			cur = append(cur, Seg{pen, c1, c2, p})
			pen = p
		}
	}
	closeLoop()
	return out
}

func line(a, b Pt) Seg {
	return Seg{a, Pt{a[0] + (b[0]-a[0])/3, a[1] + (b[1]-a[1])/3}, Pt{a[0] + 2*(b[0]-a[0])/3, a[1] + 2*(b[1]-a[1])/3}, b}
}

func dist(a, b Pt) float64 { return math.Hypot(a[0]-b[0], a[1]-b[1]) }

func makeLoop(segs []Seg) loop {
	l := loop{segs: segs, bbox: [4]float64{math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)}}
	for _, s := range segs {
		p := [4]Pt{s.P0, s.C1, s.C2, s.P1}
		l.area += segArea(s)
		for _, q := range p {
			l.bbox[0], l.bbox[1] = math.Min(l.bbox[0], q[0]), math.Min(l.bbox[1], q[1])
			l.bbox[2], l.bbox[3] = math.Max(l.bbox[2], q[0]), math.Max(l.bbox[3], q[1])
		}
	}
	return l
}

func reverse(segs []Seg) []Seg {
	out := make([]Seg, len(segs))
	for i, s := range segs {
		out[len(segs)-1-i] = Seg{s.P1, s.C2, s.C1, s.P0}
	}
	return out
}

// group nests loops into regions: a loop inside an odd number of others is a hole of its
// innermost container. Orientation is normalised (outer CCW, holes CW) so fonts with either
// winding convention behave the same.
func group(loops []loop) []Region {
	n := len(loops)
	parent := make([]int, n)
	depth := make([]int, n)
	for i := range loops {
		parent[i] = -1
		probe := loops[i].segs[0].P0
		best := -1
		for j := range loops {
			if i == j || math.Abs(loops[j].area) <= math.Abs(loops[i].area) || !contains(loops[j], probe) {
				continue
			}
			if best < 0 || math.Abs(loops[j].area) < math.Abs(loops[best].area) {
				best = j
			}
			depth[i]++
		}
		parent[i] = best
	}
	regionOf := map[int]int{}
	var out []Region
	for i, l := range loops {
		if depth[i]%2 == 0 {
			segs := l.segs
			if l.area < 0 {
				segs = reverse(segs)
			}
			regionOf[i] = len(out)
			out = append(out, Region{Outer: segs})
		}
	}
	for i, l := range loops {
		if depth[i]%2 == 1 {
			if ri, ok := regionOf[parent[i]]; ok {
				segs := l.segs
				if l.area > 0 {
					segs = reverse(segs)
				}
				out[ri].Holes = append(out[ri].Holes, segs)
			}
		}
	}
	return out
}

// contains is a point-in-loop test on the flattened outline.
func contains(l loop, p Pt) bool {
	in := false
	var prev Pt
	test := func(a, b Pt) {
		if (a[1] > p[1]) != (b[1] > p[1]) && p[0] < (b[0]-a[0])*(p[1]-a[1])/(b[1]-a[1])+a[0] {
			in = !in
		}
	}
	for _, s := range l.segs {
		prev = s.P0
		for t := 1; t <= 8; t++ {
			u := float64(t) / 8
			q := bez(s, u)
			test(prev, q)
			prev = q
		}
	}
	return in
}

func bez(s Seg, t float64) Pt {
	u := 1 - t
	a, b, c, d := u*u*u, 3*u*u*t, 3*u*t*t, t*t*t
	return Pt{a*s.P0[0] + b*s.C1[0] + c*s.C2[0] + d*s.P1[0], a*s.P0[1] + b*s.C1[1] + c*s.C2[1] + d*s.P1[1]}
}

func mapSegs(segs []Seg, f func(Pt) Pt) []Seg {
	out := make([]Seg, len(segs))
	for i, s := range segs {
		out[i] = Seg{f(s.P0), f(s.C1), f(s.C2), f(s.P1)}
	}
	return out
}

func mapRegion(r Region, f func(Pt) Pt, flip bool) Region {
	fix := func(s []Seg) []Seg {
		s = mapSegs(s, f)
		if flip {
			s = reverse(s)
		}
		return s
	}
	out := Region{Outer: fix(r.Outer)}
	for _, h := range r.Holes {
		out.Holes = append(out.Holes, fix(h))
	}
	return out
}

func shift(r Region, dx, dy float64) Region {
	return mapRegion(r, func(p Pt) Pt { return Pt{p[0] + dx, p[1] + dy} }, false)
}

// mirrorX flips across the vertical axis at x = w/2 (reversing loops keeps CCW outers).
func mirrorX(r Region, w float64) Region {
	return mapRegion(r, func(p Pt) Pt { return Pt{w - p[0], p[1]} }, true)
}

// Bounds returns the bounding box [minX, minY, maxX, maxY] of the regions.
func Bounds(regs []Region) [4]float64 {
	b := [4]float64{math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)}
	for _, r := range regs {
		for _, segs := range append([][]Seg{r.Outer}, r.Holes...) {
			l := makeLoop(segs)
			b[0], b[1] = math.Min(b[0], l.bbox[0]), math.Min(b[1], l.bbox[1])
			b[2], b[3] = math.Max(b[2], l.bbox[2]), math.Max(b[3], l.bbox[3])
		}
	}
	return b
}

// Transform returns the regions mapped through f (used to place text on a plane or a path).
func Transform(regs []Region, f func(Pt) Pt) []Region {
	out := make([]Region, len(regs))
	for i, r := range regs {
		out[i] = mapRegion(r, f, false)
	}
	return out
}

// Area is the filled area of the regions.
func Area(regs []Region) float64 {
	a := 0.0
	for _, r := range regs {
		a += math.Abs(makeLoop(r.Outer).area)
		for _, h := range r.Holes {
			a -= math.Abs(makeLoop(h).area)
		}
	}
	return a
}

// segArea is the signed area swept by a Bézier, ½∫(x dy − y dx), by 3-point Gauss-Legendre
// (the integrand is a degree-4 polynomial, so the rule is exact).
func segArea(s Seg) float64 {
	d := func(a, b, c, e float64, t float64) float64 { // derivative of a cubic Bézier coordinate
		u := 1 - t
		return 3 * (u*u*(b-a) + 2*u*t*(c-b) + t*t*(e-c))
	}
	ts := [3]float64{0.5 - math.Sqrt(0.6)/2, 0.5, 0.5 + math.Sqrt(0.6)/2}
	ws := [3]float64{5.0 / 18, 8.0 / 18, 5.0 / 18}
	a := 0.0
	for i, t := range ts {
		p := bez(s, t)
		dx := d(s.P0[0], s.C1[0], s.C2[0], s.P1[0], t)
		dy := d(s.P0[1], s.C1[1], s.C2[1], s.P1[1], t)
		a += ws[i] * (p[0]*dy - p[1]*dx)
	}
	return a / 2
}
