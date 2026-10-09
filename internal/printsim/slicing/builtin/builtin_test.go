package builtin

import (
	"bytes"
	"context"
	"errors"
	"math"
	"path/filepath"
	"testing"

	"github.com/accelone/simoncad/internal/printsim/profile"
	"github.com/accelone/simoncad/internal/printsim/slicing"
	"github.com/accelone/simoncad/internal/printsim/testmesh"
	"github.com/accelone/simoncad/internal/printsim/toolpath"
)

func slice(t *testing.T, tris []testmesh.Tri, mut func(*profile.PrintProfile)) *toolpath.SliceResult {
	t.Helper()
	p := profile.GenericFDM()
	p.Adhesion = profile.AdhesionNone
	if mut != nil {
		mut(&p)
	}
	path := filepath.Join(t.TempDir(), "m.stl")
	if err := testmesh.WriteSTL(path, tris); err != nil {
		t.Fatal(err)
	}
	r, err := Slicer{}.Slice(context.Background(), slicing.Input{STLPath: path}, p)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Validate(); err != nil {
		t.Fatal(err)
	}
	r.Finalize(toolpath.FinalizeParams{FilamentDiameter: p.FilamentDiameter, FilamentDensity: p.FilamentDensity, LayerHeight: p.LayerHeight, Nozzle: p.NozzleDiameter})
	return r
}

// kindLen sums the extruded length of one kind in one layer.
func kindLen(r *toolpath.SliceResult, layer int, k toolpath.Kind) float64 {
	l := r.Layers[layer]
	var s float64
	for _, sg := range r.Segments[l.First : l.First+l.Count] {
		if sg.Kind == k {
			s += sg.Length()
		}
	}
	return s
}

func TestCubeLayersWallsAndInfill(t *testing.T) {
	// 20×20×4 mm cube at the bed centre
	r := slice(t, testmesh.Box(100, 100, 0, 120, 120, 4), nil)
	if len(r.Layers) != 20 {
		t.Fatalf("layers: %d, want 20", len(r.Layers))
	}
	for i, l := range r.Layers {
		if want := float32(i+1) * 0.2; math.Abs(float64(l.Z-want)) > 1e-4 {
			t.Fatalf("layer %d z=%v want %v", i, l.Z, want)
		}
	}
	w := 0.4 * widthFactor
	// middle layer (sparse): outer wall is a square of side 20 - w, two walls total
	mid := 10
	ext := kindLen(r, mid, toolpath.ExternalPerimeter)
	if want := 4 * (20 - w); math.Abs(ext-want) > 0.5 {
		t.Fatalf("external perimeter length %v, want ≈%v", ext, want)
	}
	if per := kindLen(r, mid, toolpath.Perimeter); per <= 0 || per >= ext {
		t.Fatalf("inner wall length %v must be positive and shorter than the outer %v", per, ext)
	}
	// 20% infill over the area inside the walls
	inner := 20 - 2*2*w
	wantInfill := inner * inner / (w * 100 / 20)
	if got := kindLen(r, mid, toolpath.Infill); math.Abs(got-wantInfill)/wantInfill > 0.2 {
		t.Fatalf("infill length %v, want ≈%v", got, wantInfill)
	}
	// bottom and top layers are solid, the very last one is the top surface
	if kindLen(r, 0, toolpath.SolidInfill) < 3*kindLen(r, mid, toolpath.Infill) {
		t.Fatal("bottom layers must be solid infill")
	}
	if kindLen(r, len(r.Layers)-1, toolpath.TopBottom) <= 0 {
		t.Fatal("the last layer must be a top surface")
	}
	if kindLen(r, mid, toolpath.SolidInfill) != 0 {
		t.Fatal("middle layers must not be solid")
	}
	if !r.Preview || r.Provider != "builtin" || r.FilamentMM <= 0 || r.Duration <= 0 {
		t.Fatalf("result summary: %+v", r)
	}
	// every deposition stays inside the part
	for _, s := range r.Segments {
		if s.Kind == toolpath.Travel || s.Kind == toolpath.Skirt {
			continue
		}
		for _, c := range [][2]float32{{s.X0, s.Y0}, {s.X1, s.Y1}} {
			if c[0] < 99.9 || c[0] > 120.1 || c[1] < 99.9 || c[1] > 120.1 {
				t.Fatalf("segment outside the part: %+v", s)
			}
		}
	}
	var warned bool
	for _, d := range r.Diagnostics {
		if d.Code == "preview_slicer" {
			warned = true
		}
	}
	if !warned {
		t.Fatal("the preview slicer must label its output")
	}
}

func TestExtrusionMatchesVolume(t *testing.T) {
	r := slice(t, testmesh.Box(100, 100, 0, 120, 120, 4), func(p *profile.PrintProfile) { p.InfillPct = 100 })
	// printed volume ≈ model volume (1600 mm³): solid infill + walls fill the part
	vol := r.FilamentMM * math.Pi * 1.75 * 1.75 / 4
	if math.Abs(vol-1600)/1600 > 0.15 {
		t.Fatalf("deposited volume %.0f mm³, model is 1600 mm³", vol)
	}
}

func TestHollowCylinderHasInnerWall(t *testing.T) {
	outer := testmesh.Circle(110, 110, 10, 64)
	hole := testmesh.Circle(110, 110, 5, 64)
	r := slice(t, testmesh.Prism(outer, hole, 0, 2), nil)
	rmin, rmax := math.Inf(1), math.Inf(-1)
	for _, s := range r.Segments {
		if s.Kind != toolpath.ExternalPerimeter {
			continue
		}
		d := math.Hypot(float64(s.X1)-110, float64(s.Y1)-110)
		rmin, rmax = math.Min(rmin, d), math.Max(rmax, d)
	}
	w := 0.4 * widthFactor
	if math.Abs(rmax-(10-w/2)) > 0.1 {
		t.Fatalf("outer wall radius %v, want ≈%v", rmax, 10-w/2)
	}
	if math.Abs(rmin-(5+w/2)) > 0.1 {
		t.Fatalf("hole wall radius %v, want ≈%v (the hole must be preserved)", rmin, 5+w/2)
	}
	// nothing is printed inside the hole
	for _, s := range r.Segments {
		if s.Kind.Extrudes() && math.Hypot(float64(s.X1)-110, float64(s.Y1)-110) < 5+w/2-0.05 {
			t.Fatalf("material inside the hole: %+v", s)
		}
	}
}

func TestTwoIslands(t *testing.T) {
	tris := append(testmesh.Box(50, 50, 0, 60, 60, 2), testmesh.Box(150, 150, 0, 160, 160, 2)...)
	r := slice(t, tris, nil)
	var a, b bool
	for _, s := range r.Segments {
		if s.Kind == toolpath.ExternalPerimeter {
			a = a || s.X1 < 100
			b = b || s.X1 > 100
		}
	}
	if !a || !b {
		t.Fatal("both islands must be printed")
	}
}

func TestSkirtAndBrim(t *testing.T) {
	cube := testmesh.Box(100, 100, 0, 110, 110, 2)
	sk := slice(t, cube, func(p *profile.PrintProfile) { p.Adhesion = profile.AdhesionSkirt })
	br := slice(t, cube, func(p *profile.PrintProfile) { p.Adhesion = profile.AdhesionBrim })
	none := slice(t, cube, nil)
	if kindLen(none, 0, toolpath.Skirt) != 0 {
		t.Fatal("no adhesion, no skirt")
	}
	if s := kindLen(sk, 0, toolpath.Skirt); s < 40 {
		t.Fatalf("skirt too short: %v", s)
	}
	if kindLen(br, 0, toolpath.Skirt) < 3*kindLen(sk, 0, toolpath.Skirt)/2 {
		t.Fatal("a 5 mm brim must be longer than one skirt loop")
	}
	if kindLen(sk, 1, toolpath.Skirt) != 0 {
		t.Fatal("the skirt is first-layer only")
	}
}

func TestRetractionsAccompanyLongTravels(t *testing.T) {
	tris := append(testmesh.Box(50, 50, 0, 60, 60, 2), testmesh.Box(150, 150, 0, 160, 160, 2)...)
	r := slice(t, tris, nil)
	var retract, unretract float64
	for _, s := range r.Segments {
		if s.Kind == toolpath.Travel && s.E < 0 {
			retract += float64(s.E)
		}
		if s.Kind == toolpath.Travel && s.E > 0 {
			unretract += float64(s.E)
		}
	}
	if retract == 0 || math.Abs(retract+unretract) > retractLen*1.01 {
		t.Fatalf("retract %v unretract %v: they must balance", retract, unretract)
	}
}

func TestInfillPercentScales(t *testing.T) {
	cube := testmesh.Box(100, 100, 0, 120, 120, 4)
	lo := slice(t, cube, func(p *profile.PrintProfile) { p.InfillPct = 10 })
	hi := slice(t, cube, func(p *profile.PrintProfile) { p.InfillPct = 40 })
	a, b := kindLen(lo, 10, toolpath.Infill), kindLen(hi, 10, toolpath.Infill)
	if r := b / a; r < 3 || r > 5 {
		t.Fatalf("4× the density must give ≈4× the infill, got %.2f×", r)
	}
	none := slice(t, cube, func(p *profile.PrintProfile) { p.InfillPct = 0 })
	if kindLen(none, 10, toolpath.Infill) != 0 {
		t.Fatal("0% infill")
	}
}

func TestGridPatternCrossesLines(t *testing.T) {
	cube := testmesh.Box(100, 100, 0, 120, 120, 4)
	rect := slice(t, cube, nil)
	grid := slice(t, cube, func(p *profile.PrintProfile) { p.InfillPat = profile.InfillGrid })
	a, b := kindLen(rect, 10, toolpath.Infill), kindLen(grid, 10, toolpath.Infill)
	if math.Abs(a-b)/a > 0.25 { // two passes at double spacing ≈ the same density
		t.Fatalf("grid %v vs rectilinear %v", b, a)
	}
}

func TestDeterministic(t *testing.T) {
	cube := testmesh.Box(100, 100, 0, 120, 120, 4)
	a, _ := toolpath.Encode(slice(t, cube, nil))
	b, _ := toolpath.Encode(slice(t, cube, nil))
	if !bytes.Equal(a, b) {
		t.Fatal("slicing must be deterministic")
	}
}

func TestErrors(t *testing.T) {
	dir := t.TempDir()
	p := profile.GenericFDM()
	if _, err := (Slicer{}).Slice(context.Background(), slicing.Input{STLPath: filepath.Join(dir, "missing.stl")}, p); err == nil {
		t.Fatal("missing file")
	}
	empty := filepath.Join(dir, "empty.stl")
	testmesh.WriteSTL(empty, nil)
	if _, err := (Slicer{}).Slice(context.Background(), slicing.Input{STLPath: empty}, p); err == nil {
		t.Fatal("empty mesh")
	}
	bad := p
	bad.LayerHeight = 5
	box := filepath.Join(dir, "b.stl")
	testmesh.WriteSTL(box, testmesh.Box(0, 0, 0, 10, 10, 10))
	if _, err := (Slicer{}).Slice(context.Background(), slicing.Input{STLPath: box}, bad); err == nil {
		t.Fatal("invalid profile")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (Slicer{}).Slice(ctx, slicing.Input{STLPath: box}, p); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled slice: %v", err)
	}
}

func TestOffsetCollapsesThinShapes(t *testing.T) {
	thin := poly{{0, 0}, {10, 0}, {10, 0.5}, {0, 0.5}}
	if offsetLeft(thin, 0.4) != nil {
		t.Fatal("a 0.5 mm strip cannot hold a 0.4 mm inset")
	}
	sq := poly{{0, 0}, {10, 0}, {10, 10}, {0, 10}}
	o := offsetLeft(sq, 1)
	if o == nil || math.Abs(o.area()-64) > 1e-6 {
		t.Fatalf("inset square: %v", o)
	}
	if g := offsetLeft(sq, -1); g == nil || math.Abs(g.area()-144) > 1e-6 {
		t.Fatalf("outset square: %v", g)
	}
}

// Non-convex outlines stress the perimeter offset: reflex corners, narrow arms and concave notches.
func TestNonConvexShapes(t *testing.T) {
	ell := [][2]float32{{100, 100}, {140, 100}, {140, 120}, {120, 120}, {120, 140}, {100, 140}}
	var star [][2]float32
	for i := 0; i < 10; i++ {
		r := float32(20)
		if i%2 == 1 {
			r = 9
		}
		a := float64(i) * math.Pi / 5
		star = append(star, [2]float32{110 + r*float32(math.Cos(a)), 110 + r*float32(math.Sin(a))})
	}
	areaOf := func(p [][2]float32) float64 {
		var s float64
		for i, a := range p {
			b := p[(i+1)%len(p)]
			s += float64(a[0])*float64(b[1]) - float64(b[0])*float64(a[1])
		}
		return s / 2
	}
	for name, outline := range map[string][][2]float32{"L": ell, "star": star} {
		t.Run(name, func(t *testing.T) {
			r := slice(t, testmesh.Prism(outline, nil, 0, 3), func(p *profile.PrintProfile) { p.InfillPct = 100 })
			want := areaOf(outline) * 3 // mm³
			got := r.FilamentMM * math.Pi * 1.75 * 1.75 / 4
			// the preview slicer is approximate, but the part must be substantially filled and never wildly over-extruded
			if got < 0.6*want || got > 1.15*want {
				t.Fatalf("deposited %.0f mm³ for a %.0f mm³ part", got, want)
			}
			if kindLen(r, 7, toolpath.ExternalPerimeter) < 50 {
				t.Fatal("the outline must be walled")
			}
			lo, hi := polyBounds(outline)
			for _, s := range r.Segments {
				if !s.Kind.Extrudes() || s.Kind == toolpath.Skirt {
					continue
				}
				for _, c := range [][2]float32{{s.X0, s.Y0}, {s.X1, s.Y1}} {
					if c[0] < lo[0]-0.1 || c[0] > hi[0]+0.1 || c[1] < lo[1]-0.1 || c[1] > hi[1]+0.1 {
						t.Fatalf("material outside the part's bounding box: %+v", s)
					}
				}
			}
		})
	}
}

func polyBounds(p [][2]float32) (lo, hi [2]float32) {
	lo, hi = p[0], p[0]
	for _, q := range p {
		for a := 0; a < 2; a++ {
			if q[a] < lo[a] {
				lo[a] = q[a]
			}
			if q[a] > hi[a] {
				hi[a] = q[a]
			}
		}
	}
	return
}

// A wall thinner than the walls need must not push beads or infill outside the part.
func TestThinWallsStayInsideThePart(t *testing.T) {
	outer := testmesh.Rect(100, 100, 120, 120)
	hole := testmesh.Rect(100.5, 100.5, 119.5, 119.5) // 0.5 mm walls all round
	// the prism helper needs matching point counts; a rectangle ring works
	r := slice(t, testmesh.Prism(outer, hole, 0, 3), func(p *profile.PrintProfile) { p.Adhesion = profile.AdhesionNone })
	for _, s := range r.Segments {
		if !s.Kind.Extrudes() {
			continue
		}
		for _, c := range [][2]float32{{s.X0, s.Y0}, {s.X1, s.Y1}} {
			if c[0] < 99.99 || c[0] > 120.01 || c[1] < 99.99 || c[1] > 120.01 {
				t.Fatalf("material outside the part: %+v", s)
			}
		}
	}
}
