// Package builtin is a small preview slicer in pure Go. It exists so that print
// simulation works on a machine without PrusaSlicer. It is NOT a production slicer: it
// makes perimeters and rectilinear/grid infill with solid top and bottom layers, and no
// supports, bridges, ironing or seam control. Its output is labelled "preview".
package builtin

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"

	"github.com/accelone/simoncad/internal/printsim/meshprep"
	"github.com/accelone/simoncad/internal/printsim/profile"
	"github.com/accelone/simoncad/internal/printsim/slicing"
	"github.com/accelone/simoncad/internal/printsim/toolpath"
)

const (
	maxLayers = 20000
	// extrusion width as a multiple of the nozzle diameter
	widthFactor = 1.1
	// travels longer than this retract first
	retractMinTravel = 2.0
	retractLen       = 0.8
	retractSpeed     = 35.0
	skirtDistance    = 3.0
	brimWidth        = 5.0
)

// Slicer is the preview slicer.
type Slicer struct{}

func (Slicer) Name() string     { return slicing.ProviderBuiltin }
func (Slicer) Available() error { return nil }

type gen struct {
	p       profile.PrintProfile
	w, h    float64
	area    float64 // bead cross-section, mm²
	fil     float64 // filament cross-section, mm²
	res     *toolpath.SliceResult
	pos     pt
	z       float64
	retract bool
	maxSeg  int
}

func (g *gen) add(s toolpath.Segment) error {
	if len(g.res.Segments) >= g.maxSeg {
		return fmt.Errorf("the model needs more than %d toolpath segments; use a larger layer height or simplify it", g.maxSeg)
	}
	g.res.Segments = append(g.res.Segments, s)
	g.res.Layers[len(g.res.Layers)-1].Count++
	return nil
}

func seg3(a pt, za float64, b pt, zb float64, k toolpath.Kind, e, f float64) toolpath.Segment {
	return toolpath.Segment{X0: float32(a.x), Y0: float32(a.y), Z0: float32(za), X1: float32(b.x), Y1: float32(b.y), Z1: float32(zb),
		Kind: k, E: float32(e), F: float32(f)}
}

// travel moves the nozzle without extruding, retracting first on long moves.
func (g *gen) travel(to pt, z float64) error {
	d := math.Sqrt(math.Pow(dist(g.pos, to), 2) + math.Pow(z-g.z, 2))
	if d < 1e-6 {
		return nil
	}
	if d > retractMinTravel && !g.retract {
		if err := g.add(seg3(g.pos, g.z, g.pos, g.z, toolpath.Travel, -retractLen, retractSpeed)); err != nil {
			return err
		}
		g.retract = true
	}
	if err := g.add(seg3(g.pos, g.z, to, z, toolpath.Travel, 0, g.p.TravelSpeed)); err != nil {
		return err
	}
	g.pos, g.z = to, z
	return nil
}

// extrude deposits a bead from the current position to `to`.
func (g *gen) extrude(to pt, k toolpath.Kind, speed float64) error {
	l := dist(g.pos, to)
	if l < 1e-6 {
		return nil
	}
	if g.retract {
		if err := g.add(seg3(g.pos, g.z, g.pos, g.z, toolpath.Travel, retractLen, retractSpeed)); err != nil {
			return err
		}
		g.retract = false
	}
	if err := g.add(seg3(g.pos, g.z, to, g.z, k, g.area*l/g.fil, speed)); err != nil {
		return err
	}
	g.pos = to
	return nil
}

// loop prints a closed polygon starting from the vertex nearest the nozzle.
func (g *gen) loop(p poly, k toolpath.Kind, speed float64) error {
	best, bd := 0, math.Inf(1)
	for i, a := range p {
		if d := dist(a, g.pos); d < bd {
			best, bd = i, d
		}
	}
	if err := g.travel(p[best], g.z); err != nil {
		return err
	}
	for i := 1; i <= len(p); i++ {
		if err := g.extrude(p[(best+i)%len(p)], k, speed); err != nil {
			return err
		}
	}
	return nil
}

// Slice implements slicing.Slicer.
func (Slicer) Slice(ctx context.Context, in slicing.Input, p profile.PrintProfile) (*toolpath.SliceResult, error) {
	if err := p.Validate(); err != nil {
		return nil, fmt.Errorf("invalid print profile: %w", err)
	}
	tris, err := meshprep.ReadTriangles(in.STLPath)
	if err != nil {
		return nil, err
	}
	n := len(tris) / 9
	if n == 0 {
		return nil, errors.New("the mesh has no triangles")
	}
	h := p.LayerHeight
	zmax := 0.0
	for i := 0; i < n; i++ {
		for j := 0; j < 3; j++ {
			zmax = math.Max(zmax, float64(tris[9*i+3*j+2]))
		}
	}
	nl := int(math.Ceil(zmax/h - 1e-6))
	if nl < 1 {
		return nil, errors.New("the model is too flat to slice at this layer height")
	}
	if nl > maxLayers {
		return nil, fmt.Errorf("%d layers is too many; increase the layer height", nl)
	}
	nudge := h * 1.2345e-4 // keeps slicing planes off exact vertex heights
	plane := func(k int) float64 { return (float64(k)+0.5)*h + nudge }

	// bucket triangles by the layers whose plane crosses them
	buckets := make([][]int32, nl)
	for i := 0; i < n; i++ {
		lo, hi := math.Inf(1), math.Inf(-1)
		for j := 0; j < 3; j++ {
			z := float64(tris[9*i+3*j+2])
			lo, hi = math.Min(lo, z), math.Max(hi, z)
		}
		k0 := int(math.Floor((lo-nudge)/h - 0.5))
		k1 := int(math.Ceil((hi-nudge)/h - 0.5))
		for k := max(k0, 0); k <= min(k1, nl-1); k++ {
			if z := plane(k); lo < z && z < hi {
				buckets[k] = append(buckets[k], int32(i))
			}
		}
	}

	w := p.NozzleDiameter * widthFactor
	g := &gen{
		p: p, w: w, h: h,
		area:   (w-h)*h + math.Pi*h*h/4,
		fil:    math.Pi * p.FilamentDiameter * p.FilamentDiameter / 4,
		res:    &toolpath.SliceResult{Provider: slicing.ProviderBuiltin, Version: "preview", Preview: true},
		maxSeg: toolpath.MaxSegments,
	}
	var openChains int
	for k := 0; k < nl; k++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		segs := make([]seg, 0, len(buckets[k]))
		for _, ti := range buckets[k] {
			if s, ok := sliceTriangle(tris[9*int(ti):9*int(ti)+9], plane(k)); ok {
				segs = append(segs, s)
			}
		}
		loops, open := chain(segs)
		openChains += open
		z := float64(k+1) * h
		g.res.Layers = append(g.res.Layers, toolpath.Layer{First: len(g.res.Segments), Z: float32(z)})
		if err := g.layer(k, nl, z, islands(loops)); err != nil {
			return nil, err
		}
	}
	// drop layers the model did not reach (a hollow-only shell can leave empty planes)
	kept := g.res.Layers[:0]
	for _, l := range g.res.Layers {
		if l.Count > 0 {
			kept = append(kept, l)
		}
	}
	g.res.Layers = kept
	if len(g.res.Segments) == 0 {
		return nil, errors.New("slicing produced no toolpath: the mesh has no closed cross-sections")
	}
	d := &g.res.Diagnostics
	*d = append(*d, toolpath.Diagnostic{Level: toolpath.LevelWarning, Code: "preview_slicer",
		Message: "Preview slicer: perimeters and simple infill only, no supports or bridges. Install PrusaSlicer for production-grade toolpaths."})
	if openChains > 0 {
		*d = append(*d, toolpath.Diagnostic{Level: toolpath.LevelWarning, Code: "open_contours", Count: openChains,
			Message: "some cross-section outlines were not closed and were skipped"})
	}
	if p.Supports {
		*d = append(*d, toolpath.Diagnostic{Level: toolpath.LevelWarning, Code: "no_supports",
			Message: "the preview slicer does not generate supports"})
	}
	if p.InfillPat != profile.InfillRectilinear && p.InfillPat != profile.InfillGrid {
		*d = append(*d, toolpath.Diagnostic{Level: toolpath.LevelInfo, Code: "infill_pattern",
			Message: "the preview slicer supports rectilinear and grid infill; " + string(p.InfillPat) + " was drawn as rectilinear"})
	}
	return g.res, nil
}

func (g *gen) speed(k toolpath.Kind, layer int) float64 {
	v := g.p.PrintSpeed
	switch k {
	case toolpath.ExternalPerimeter:
		v *= 0.7
	case toolpath.SolidInfill:
		v *= 0.8
	case toolpath.TopBottom:
		v *= 0.6
	case toolpath.Skirt:
		v *= 0.7
	}
	if layer == 0 {
		v *= 0.5
	}
	return v
}

// layer emits all toolpaths for one layer.
func (g *gen) layer(k, nl int, z float64, isl []island) error {
	if len(isl) == 0 {
		return nil
	}
	// lift to the layer height (a vertical hop, retracting first)
	if err := g.travel(g.pos, z); err != nil {
		return err
	}
	if k == 0 {
		if err := g.adhesion(isl); err != nil {
			return err
		}
	}
	// visit islands nearest-first
	remaining := append([]island(nil), isl...)
	for len(remaining) > 0 {
		best, bd := 0, math.Inf(1)
		for i, is := range remaining {
			for _, a := range is.outer {
				if d := dist(a, g.pos); d < bd {
					best, bd = i, d
				}
			}
		}
		is := remaining[best]
		remaining = append(remaining[:best], remaining[best+1:]...)
		if err := g.island(k, nl, z, is); err != nil {
			return err
		}
	}
	return nil
}

func (g *gen) adhesion(isl []island) error {
	var rings []poly
	switch g.p.Adhesion {
	case profile.AdhesionSkirt:
		for _, is := range isl {
			if r := offsetLeft(is.outer, -skirtDistance); r != nil {
				rings = append(rings, r)
			}
		}
	case profile.AdhesionBrim:
		nb := int(math.Ceil(brimWidth / g.w))
		for i := nb; i >= 1; i-- { // outermost first, so the brim grows toward the part
			for _, is := range isl {
				if r := offsetLeft(is.outer, -float64(i)*g.w+g.w/2); r != nil {
					rings = append(rings, r)
				}
			}
		}
	}
	for _, r := range rings {
		if err := g.loop(r, toolpath.Skirt, g.speed(toolpath.Skirt, 0)); err != nil {
			return err
		}
	}
	return nil
}

func (g *gen) island(k, nl int, z float64, is island) error {
	loops := append([]poly{is.outer}, is.holes...)
	// walls, innermost first so the outer wall is printed last
	walls := g.p.Walls
	for i := walls - 1; i >= 0; i-- {
		d := g.w/2 + float64(i)*g.w
		kind := toolpath.Perimeter
		if i == 0 {
			kind = toolpath.ExternalPerimeter
		}
		ow := offsetLeft(is.outer, d)
		if ow == nil {
			continue // too thin for this wall
		}
		for li, l := range loops {
			if o := offsetLeft(l, d); o != nil && (li == 0 || insidePoly(ow, o)) {
				if err := g.loop(o, kind, g.speed(kind, k)); err != nil {
					return err
				}
			}
		}
	}
	// infill region: every loop moved in by the full wall thickness
	inner := offsetLeft(is.outer, float64(walls)*g.w)
	if inner == nil {
		return nil
	}
	region := []poly{inner}
	for _, hole := range is.holes {
		o := offsetLeft(hole, float64(walls)*g.w)
		if o == nil {
			continue
		}
		if !insidePoly(inner, o) { // the hole's walls reach the outer ones: no room for infill
			return nil
		}
		region = append(region, o)
	}
	t := g.p.TopBottomLay
	solid := k < t || k >= nl-t
	pct := g.p.InfillPct
	kind := toolpath.Infill
	if solid {
		pct, kind = 100, toolpath.SolidInfill
		if k == nl-1 {
			kind = toolpath.TopBottom
		}
	}
	if pct <= 0 {
		return nil
	}
	spacing := g.w * 100 / pct
	angles := []float64{math.Pi / 4}
	if k%2 == 1 {
		angles = []float64{3 * math.Pi / 4}
	}
	if g.p.InfillPat == profile.InfillGrid && !solid {
		angles = []float64{math.Pi / 4, 3 * math.Pi / 4}
		spacing *= 2
	}
	for _, a := range angles {
		if err := g.fill(region, a, spacing, kind, g.speed(kind, k), k); err != nil {
			return err
		}
	}
	return nil
}

type interval struct{ a, b pt }

// fill covers the region with parallel lines at the given angle, joined boustrophedon.
// Lines sit on a lattice anchored at the origin so they stack from layer to layer.
func (g *gen) fill(region []poly, angle, spacing float64, kind toolpath.Kind, speed float64, layer int) error {
	cos, sin := math.Cos(angle), math.Sin(angle)
	rot := func(p pt) pt { return pt{p.x*cos + p.y*sin, -p.x*sin + p.y*cos} }  // into line space
	unrot := func(p pt) pt { return pt{p.x*cos - p.y*sin, p.x*sin + p.y*cos} } // back
	var edges [][2]pt
	minY, maxY := math.Inf(1), math.Inf(-1)
	for _, pl := range region {
		for i, a := range pl {
			ra, rb := rot(a), rot(pl[(i+1)%len(pl)])
			edges = append(edges, [2]pt{ra, rb})
			minY, maxY = math.Min(minY, ra.y), math.Max(maxY, ra.y)
		}
	}
	var lines [][]interval
	for j := int(math.Ceil(minY / spacing)); float64(j)*spacing <= maxY; j++ {
		y := float64(j) * spacing
		var xs []float64
		for _, e := range edges {
			a, b := e[0], e[1]
			if (a.y <= y) != (b.y <= y) {
				xs = append(xs, a.x+(y-a.y)/(b.y-a.y)*(b.x-a.x))
			}
		}
		sort.Float64s(xs)
		var row []interval
		for i := 0; i+1 < len(xs); i += 2 {
			if xs[i+1]-xs[i] < g.w*0.5 {
				continue
			}
			row = append(row, interval{unrot(pt{xs[i], y}), unrot(pt{xs[i+1], y})})
		}
		if len(row) > 0 {
			lines = append(lines, row)
		}
	}
	for r, row := range lines {
		if r%2 == 1 { // alternate direction
			for i, j := 0, len(row)-1; i < j; i, j = i+1, j-1 {
				row[i], row[j] = row[j], row[i]
			}
			for i := range row {
				row[i].a, row[i].b = row[i].b, row[i].a
			}
		}
		for _, iv := range row {
			if err := g.travel(iv.a, g.z); err != nil {
				return err
			}
			if err := g.extrude(iv.b, kind, speed); err != nil {
				return err
			}
		}
	}
	return nil
}

// insidePoly reports whether every vertex of q lies inside p.
func insidePoly(p, q poly) bool {
	for _, v := range q {
		if !p.contains(v) {
			return false
		}
	}
	return true
}
