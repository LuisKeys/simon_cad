package vectorize

import (
	"context"
	"image"
	"image/color"
	"math"
	"math/rand"
	"reflect"
	"strings"
	"testing"
	"time"
)

// ---- synthetic images --------------------------------------------------------------------

type canvas struct{ ink *Ink }

func newCanvas(w, h int) *canvas { return &canvas{FromGray(w, h, make([]uint8, w*h))} }

// paint calls f(x, y) at every pixel centre and sets ink where it reports coverage (antialiased
// by 3x3 supersampling).
func (c *canvas) paint(f func(x, y float64) bool) {
	for y := 0; y < c.ink.H; y++ {
		for x := 0; x < c.ink.W; x++ {
			n := 0
			for sy := 0; sy < 3; sy++ {
				for sx := 0; sx < 3; sx++ {
					if f(float64(x)+(float64(sx)+0.5)/3, float64(y)+(float64(sy)+0.5)/3) {
						n++
					}
				}
			}
			if n > 0 {
				v := int(c.ink.Pix[y*c.ink.W+x]) + n*255/9
				c.ink.Pix[y*c.ink.W+x] = uint8(minInt(255, v))
			}
		}
	}
}

func (c *canvas) disc(cx, cy, r float64) {
	c.paint(func(x, y float64) bool { return math.Hypot(x-cx, y-cy) <= r })
}

func (c *canvas) erase(f func(x, y float64) bool) {
	for y := 0; y < c.ink.H; y++ {
		for x := 0; x < c.ink.W; x++ {
			if f(float64(x)+0.5, float64(y)+0.5) {
				c.ink.Pix[y*c.ink.W+x] = 0
			}
		}
	}
}

func (c *canvas) rect(x0, y0, x1, y1 float64) {
	c.paint(func(x, y float64) bool { return x >= x0 && x <= x1 && y >= y0 && y <= y1 })
}

func (c *canvas) poly(pts []Pt) {
	c.paint(func(x, y float64) bool { return pointInPoly(Pt{x, y}, pts) })
}

func (c *canvas) stroke(pts []Pt, width float64) {
	// a bitmask of covered 3x3 subsamples per pixel, filled segment by segment inside its box
	cov := make([]uint16, c.ink.W*c.ink.H)
	r := width/2 + 1
	for i := 0; i+1 < len(pts); i++ {
		a, b := pts[i], pts[i+1]
		x0, x1 := int(math.Floor(math.Min(a[0], b[0])-r)), int(math.Ceil(math.Max(a[0], b[0])+r))
		y0, y1 := int(math.Floor(math.Min(a[1], b[1])-r)), int(math.Ceil(math.Max(a[1], b[1])+r))
		for y := maxInt(0, y0); y < minInt(c.ink.H, y1); y++ {
			for x := maxInt(0, x0); x < minInt(c.ink.W, x1); x++ {
				for sy := 0; sy < 3; sy++ {
					for sx := 0; sx < 3; sx++ {
						p := Pt{float64(x) + (float64(sx)+0.5)/3, float64(y) + (float64(sy)+0.5)/3}
						if distSeg(p, a, b) <= width/2 {
							cov[y*c.ink.W+x] |= 1 << (sy*3 + sx)
						}
					}
				}
			}
		}
	}
	for i, m := range cov {
		n := 0
		for ; m != 0; m &= m - 1 {
			n++
		}
		if n > 0 {
			c.ink.Pix[i] = uint8(minInt(255, int(c.ink.Pix[i])+n*255/9))
		}
	}
}

func run(t *testing.T, c *canvas, opt Options) *Result {
	t.Helper()
	r, err := RunInk(context.Background(), c.ink, opt)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func within(t *testing.T, what string, got, want, rel float64) {
	t.Helper()
	if math.Abs(got-want) > rel*math.Abs(want) {
		t.Fatalf("%s: got %.4f want %.4f (±%.1f%%)", what, got, want, rel*100)
	}
}

// ---- filled regions ----------------------------------------------------------------------

func TestFilledDiscArea(t *testing.T) {
	c := newCanvas(400, 400)
	c.disc(200, 200, 100)
	r := run(t, c, Options{Mode: FilledRegions, PxPerMM: 4})
	if len(r.Regions) != 1 || len(r.Regions[0].Holes) != 0 {
		t.Fatalf("regions %d", len(r.Regions))
	}
	within(t, "disc area (mm²)", r.Regions[0].Area, math.Pi*25*25, 0.01)
	if n := len(r.Regions[0].Outer); n > 20 {
		t.Fatalf("a circle should need few Bezier segments, got %d", n)
	}
	if segArea(r.Regions[0].Outer) <= 0 {
		t.Fatal("outer loops are counter-clockwise (Y up)")
	}
	// the region is placed in mm with Y up: the centre of the disc is at (50, 50) mm
	minX, minY, maxX, maxY := math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
	for _, s := range r.Regions[0].Outer {
		for u := 0.0; u <= 1; u += 0.05 {
			p := s.At(u)
			minX, maxX, minY, maxY = math.Min(minX, p[0]), math.Max(maxX, p[0]), math.Min(minY, p[1]), math.Max(maxY, p[1])
		}
	}
	if math.Abs((minX+maxX)/2-50) > 0.3 || math.Abs((minY+maxY)/2-50) > 0.3 {
		t.Fatalf("centre (%.2f, %.2f) want (50, 50)", (minX+maxX)/2, (minY+maxY)/2)
	}
	within(t, "diameter", maxX-minX, 50, 0.01)
}

func TestRingHasOneHole(t *testing.T) {
	c := newCanvas(400, 400)
	c.disc(200, 200, 100)
	c.erase(func(x, y float64) bool { return math.Hypot(x-200, y-200) < 60 })
	r := run(t, c, Options{Mode: FilledRegions, PxPerMM: 4})
	if len(r.Regions) != 1 || len(r.Regions[0].Holes) != 1 {
		t.Fatalf("regions %d holes %d", len(r.Regions), len(r.Regions[0].Holes))
	}
	within(t, "ring area", r.Regions[0].Area, math.Pi*(25*25-15*15), 0.015)
	if segArea(r.Regions[0].Holes[0]) >= 0 {
		t.Fatal("holes are clockwise")
	}
}

func TestRectangleKeepsItsCorners(t *testing.T) {
	c := newCanvas(300, 200)
	c.rect(50, 40, 250, 160)
	r := run(t, c, Options{PxPerMM: 1})
	if len(r.Regions) != 1 {
		t.Fatalf("regions %d", len(r.Regions))
	}
	within(t, "area", r.Regions[0].Area, 200*120, 0.01)
	// sharp corners survive: the extreme points of the outline reach the true corners
	minX, minY, maxX, maxY := math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
	for _, s := range r.Regions[0].Outer {
		for t := 0.0; t <= 1; t += 0.05 {
			p := s.At(t)
			minX, maxX, minY, maxY = math.Min(minX, p[0]), math.Max(maxX, p[0]), math.Min(minY, p[1]), math.Max(maxY, p[1])
		}
	}
	for _, c := range []struct{ got, want float64 }{{minX, 50}, {maxX, 250}, {minY, 40}, {maxY, 160}} {
		if math.Abs(c.got-c.want) > 1.2 {
			t.Fatalf("corner drift: %v vs %v", c.got, c.want)
		}
	}
	if len(r.Regions[0].Outer) > 8 {
		t.Fatalf("a rectangle needs ~4 segments, got %d", len(r.Regions[0].Outer))
	}
}

func TestLetterWithCounter(t *testing.T) {
	c := newCanvas(300, 300)
	c.poly([]Pt{{150, 30}, {260, 270}, {215, 270}, {190, 210}, {110, 210}, {85, 270}, {40, 270}})
	c.erase(func(x, y float64) bool { return pointInPoly(Pt{x, y}, []Pt{{150, 90}, {175, 170}, {125, 170}}) })
	r := run(t, c, Options{PxPerMM: 2})
	if len(r.Regions) != 1 || len(r.Regions[0].Holes) != 1 {
		t.Fatalf("an A has one counter: regions %d holes %d", len(r.Regions), len(r.Regions[0].Holes))
	}
}

func TestNestedIslandsAreSeparateRegions(t *testing.T) {
	c := newCanvas(500, 500)
	c.disc(250, 250, 200)
	c.erase(func(x, y float64) bool { return math.Hypot(x-250, y-250) < 150 })
	c.disc(250, 250, 70) // an island inside the hole
	c.disc(470, 30, 20)  // and a separate dot
	r := run(t, c, Options{PxPerMM: 5})
	if len(r.Regions) != 3 {
		t.Fatalf("ring + island + dot = 3 regions, got %d", len(r.Regions))
	}
	holes := 0
	for _, rg := range r.Regions {
		holes += len(rg.Holes)
	}
	if holes != 1 {
		t.Fatalf("holes %d", holes)
	}
}

func TestSpecklesAreRemoved(t *testing.T) {
	c := newCanvas(400, 400)
	c.disc(200, 200, 100)
	rng := rand.New(rand.NewSource(7))
	for i := 0; i < 40; i++ {
		x, y := rng.Float64()*380+10, rng.Float64()*380+10
		if math.Hypot(x-200, y-200) > 115 {
			c.disc(x, y, 1.2)
		}
	}
	// a pinhole inside the disc
	c.erase(func(x, y float64) bool { return math.Hypot(x-200, y-200) < 1.5 })
	r := run(t, c, Options{PxPerMM: 4})
	if len(r.Regions) != 1 || len(r.Regions[0].Holes) != 0 {
		t.Fatalf("noise must not survive: regions %d holes %d", len(r.Regions), len(r.Regions[0].Holes))
	}
	if r.Stats.Specks < 20 {
		t.Fatalf("specks removed %d", r.Stats.Specks)
	}
}

func TestAdaptiveThresholdFollowsUnevenLight(t *testing.T) {
	c := newCanvas(400, 300)
	// a bright-to-dark gradient behind a bar that is darker than its surroundings everywhere
	for y := 0; y < c.ink.H; y++ {
		for x := 0; x < c.ink.W; x++ {
			c.ink.Pix[y*c.ink.W+x] = uint8(40 + x*160/c.ink.W)
		}
	}
	for _, xs := range [][2]int{{60, 100}, {300, 340}} { // two bars: one on dim, one on bright background
		for y := 80; y < 220; y++ {
			for x := xs[0]; x < xs[1]; x++ {
				c.ink.Pix[y*c.ink.W+x] += 60
			}
		}
	}
	fixed := run(t, c, Options{})
	adaptive := run(t, c, Options{Adaptive: true, AdaptiveWindow: 61, AdaptiveOffset: 20})
	if len(adaptive.Regions) != 2 {
		t.Fatalf("adaptive found %d regions, want the 2 bars", len(adaptive.Regions))
	}
	if len(fixed.Regions) == 2 && fixed.Regions[0].Area > 0 && len(adaptive.Regions) == 2 {
		// both bars need to have the same size under adaptive thresholding
		within(t, "bar areas match", adaptive.Regions[0].Area, adaptive.Regions[1].Area, 0.15)
	}
}

func TestAlphaSourceAndInvert(t *testing.T) {
	// a light-coloured logo on a transparent background: luminance sees nothing, alpha sees the logo
	img := image.NewNRGBA(image.Rect(0, 0, 200, 200))
	for y := 60; y < 140; y++ {
		for x := 60; x < 140; x++ {
			img.Set(x, y, color.NRGBA{250, 250, 250, 255})
		}
	}
	r, err := Run(context.Background(), img, Options{Mode: FilledRegions, PxPerMM: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Regions) != 1 {
		t.Fatalf("alpha logo: regions %d", len(r.Regions))
	}
	within(t, "logo area", r.Regions[0].Area, 40*40, 0.02)
	lum, _ := Run(context.Background(), img, Options{Mode: FilledRegions, Source: SourceLuminance, Threshold: 128})
	if len(lum.Regions) != 0 {
		t.Fatalf("luminance of a white logo on white is empty, got %d regions", len(lum.Regions))
	}
	// white shape on black, opaque: needs invert; without it the warning says so
	dark := image.NewGray(image.Rect(0, 0, 200, 200))
	for y := 60; y < 140; y++ {
		for x := 60; x < 140; x++ {
			dark.SetGray(x, y, color.Gray{255})
		}
	}
	noInv, _ := Run(context.Background(), dark, Options{})
	if len(noInv.Warnings) == 0 {
		t.Fatalf("a dark image must warn about foreground majority: %+v", noInv.Stats)
	}
	inv, _ := Run(context.Background(), dark, Options{Invert: true, PxPerMM: 1})
	if len(inv.Regions) != 1 {
		t.Fatalf("inverted: regions %d", len(inv.Regions))
	}
}

func TestOutlineModeReturnsBoundariesOnly(t *testing.T) {
	c := newCanvas(400, 400)
	c.disc(200, 200, 100)
	c.erase(func(x, y float64) bool { return math.Hypot(x-200, y-200) < 60 })
	r := run(t, c, Options{Mode: Outline, PxPerMM: 4})
	if len(r.Regions) != 0 || len(r.Strokes) != 0 {
		t.Fatalf("OUTLINE must not mix modes: %d regions %d strokes", len(r.Regions), len(r.Strokes))
	}
	if len(r.Outlines) != 2 {
		t.Fatalf("outlines %d", len(r.Outlines))
	}
	holes := 0
	for _, l := range r.Outlines {
		if l.Hole {
			holes++
		}
	}
	if holes != 1 {
		t.Fatalf("holes %d", holes)
	}
}

// ---- centerlines -------------------------------------------------------------------------

func TestCenterlineOfAStrokeKeepsLengthAndWidth(t *testing.T) {
	c := newCanvas(400, 300)
	// a smooth arc drawn with a 9 px pen
	var pts []Pt
	for a := 0.2; a <= math.Pi-0.2; a += 0.02 {
		pts = append(pts, Pt{200 - 150*math.Cos(a), 230 - 140*math.Sin(a)})
	}
	c.stroke(pts, 9)
	r := run(t, c, Options{Mode: Centerline, PxPerMM: 2})
	if len(r.Regions) != 0 {
		t.Fatal("CENTERLINE must not return regions")
	}
	if len(r.Strokes) != 1 {
		t.Fatalf("one stroke expected, got %d", len(r.Strokes))
	}
	want := 0.0
	for i := 1; i < len(pts); i++ {
		want += pdist(pts[i], pts[i-1])
	}
	within(t, "stroke length (mm)", r.Strokes[0].Length, want/2, 0.03)
	w := r.Strokes[0].Widths
	mid := w[len(w)/2]
	within(t, "pen width (mm)", mid, 9.0/2, 0.15)
}

func TestCenterlineOfAClosedLoopIsClosed(t *testing.T) {
	c := newCanvas(300, 300)
	var pts []Pt
	for a := 0.0; a <= 2*math.Pi+0.05; a += 0.03 {
		pts = append(pts, Pt{150 + 100*math.Cos(a), 150 + 100*math.Sin(a)})
	}
	c.stroke(pts, 8)
	r := run(t, c, Options{Mode: Centerline, PxPerMM: 1})
	if len(r.Strokes) != 1 || !r.Strokes[0].Closed {
		t.Fatalf("a ring stroke is one closed centerline: %d strokes, closed=%v", len(r.Strokes), len(r.Strokes) > 0 && r.Strokes[0].Closed)
	}
	within(t, "circumference", r.Strokes[0].Length, 2*math.Pi*100, 0.03)
}

func TestCenterlineJunctionAndSpurs(t *testing.T) {
	c := newCanvas(300, 300)
	c.stroke([]Pt{{40, 150}, {260, 150}}, 7)
	c.stroke([]Pt{{150, 40}, {150, 260}}, 7)
	r := run(t, c, Options{Mode: Centerline, PxPerMM: 1})
	if len(r.Strokes) != 4 {
		t.Fatalf("a plus is four branches meeting at a junction, got %d strokes", len(r.Strokes))
	}
	total := 0.0
	for _, s := range r.Strokes {
		total += s.Length
	}
	within(t, "total length", total, 440, 0.05)
	// a flower-like drawing: petals as closed strokes around a stem
	f := newCanvas(400, 500)
	f.stroke([]Pt{{200, 480}, {200, 260}}, 6)
	for k := 0; k < 5; k++ {
		a := float64(k) * 2 * math.Pi / 5
		var petal []Pt
		for s := 0.0; s <= 2*math.Pi+0.05; s += 0.1 {
			petal = append(petal, Pt{200 + 70*math.Cos(a) + 35*math.Cos(s)*math.Cos(a) - 18*math.Sin(s)*math.Sin(a), 200 + 70*math.Sin(a) + 35*math.Cos(s)*math.Sin(a) + 18*math.Sin(s)*math.Cos(a)})
		}
		f.stroke(petal, 5)
	}
	fr := run(t, f, Options{Mode: Centerline, PxPerMM: 1})
	if len(fr.Strokes) < 5 || len(fr.Strokes) > 14 {
		t.Fatalf("flower strokes %d", len(fr.Strokes))
	}
}

// ---- robustness --------------------------------------------------------------------------

func TestCancellationStopsWork(t *testing.T) {
	c := newCanvas(600, 600)
	c.disc(300, 300, 250)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := RunInk(ctx, c.ink, Options{}); err == nil {
		t.Fatal("a cancelled context must stop the run")
	}
}

func TestInvalidOptionsAreExplained(t *testing.T) {
	c := newCanvas(50, 50)
	c.disc(25, 25, 10)
	for _, o := range []Options{{Mode: "blobs"}, {Source: "x"}, {Threshold: 300}, {Smooth: 5}, {Tol: 0.01}, {PxPerMM: -1}} {
		if _, err := RunInk(context.Background(), c.ink, o); err == nil {
			t.Errorf("%+v accepted", o)
		}
	}
	empty, err := RunInk(context.Background(), newCanvas(50, 50).ink, Options{})
	if err != nil || len(empty.Warnings) == 0 {
		t.Fatalf("a blank image warns: %v %v", err, empty)
	}
}

func TestEDTMatchesBruteForce(t *testing.T) {
	rng := rand.New(rand.NewSource(3))
	m := newMask(40, 30)
	for i := range m.Pix {
		m.Pix[i] = rng.Float64() < 0.8
	}
	d := EDT(m)
	for y := 0; y < m.H; y++ {
		for x := 0; x < m.W; x++ {
			if !m.Pix[y*m.W+x] {
				if d[y*m.W+x] != 0 {
					t.Fatalf("background distance %v", d[y*m.W+x])
				}
				continue
			}
			best := math.Inf(1)
			for yy := 0; yy < m.H; yy++ {
				for xx := 0; xx < m.W; xx++ {
					if !m.Pix[yy*m.W+xx] {
						best = math.Min(best, math.Hypot(float64(x-xx), float64(y-yy)))
					}
				}
			}
			if !math.IsInf(best, 1) && math.Abs(d[y*m.W+x]-best) > 1e-9 {
				t.Fatalf("edt(%d,%d)=%v want %v", x, y, d[y*m.W+x], best)
			}
		}
	}
}

func TestOtsuSeparatesTwoModes(t *testing.T) {
	ink := FromGray(100, 10, make([]uint8, 1000))
	for i := range ink.Pix {
		if i%100 < 40 {
			ink.Pix[i] = 200
		} else {
			ink.Pix[i] = 30
		}
	}
	if thr := Otsu(ink); thr < 30 || thr >= 200 {
		t.Fatalf("otsu %d", thr)
	}
}

func TestFitReproducesACircleWithinTolerance(t *testing.T) {
	var pts []Pt
	for a := 0.0; a < 2*math.Pi; a += 0.05 {
		pts = append(pts, Pt{100 + 40*math.Cos(a), 100 + 40*math.Sin(a)})
	}
	segs := FitClosed(pts, 0.3, 55)
	if len(segs) == 0 || len(segs) > 8 {
		t.Fatalf("segments %d", len(segs))
	}
	for _, p := range pts {
		best := math.Inf(1)
		for _, s := range segs {
			for k := 0.0; k <= 1; k += 0.002 {
				best = math.Min(best, pdist(s.At(k), p))
			}
		}
		if best > 0.7 {
			t.Fatalf("fit deviates %v", best)
		}
	}
	if pdist(segs[len(segs)-1].P1, segs[0].P0) > 1e-9 {
		t.Fatal("closed fit must close")
	}
}

func TestPerformance1024(t *testing.T) {
	c := newCanvas(1024, 1024)
	rng := rand.New(rand.NewSource(1))
	// a busy logo: many discs, rings and bars
	for i := 0; i < 40; i++ {
		x, y, r := rng.Float64()*900+60, rng.Float64()*900+60, rng.Float64()*40+15
		c.disc(x, y, r)
		if i%3 == 0 {
			c.erase(func(px, py float64) bool { return math.Hypot(px-x, py-y) < r*0.5 })
		}
	}
	start := time.Now()
	for _, mode := range []Mode{FilledRegions, Outline, Centerline} {
		if _, err := RunInk(context.Background(), c.ink, Options{Mode: mode, PxPerMM: 10}); err != nil {
			t.Fatal(err)
		}
	}
	per := time.Since(start) / 3
	t.Logf("1024px vectorization: %v per mode", per)
	if per > 2*time.Second {
		t.Fatalf("1024 px vectorization took %v per mode (target < 1 s on reference hardware)", per)
	}
}

func BenchmarkVectorize1024(b *testing.B) {
	c := newCanvas(1024, 1024)
	rng := rand.New(rand.NewSource(1))
	for i := 0; i < 40; i++ {
		c.disc(rng.Float64()*900+60, rng.Float64()*900+60, rng.Float64()*40+15)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := RunInk(context.Background(), c.ink, Options{Mode: FilledRegions, PxPerMM: 10}); err != nil {
			b.Fatal(err)
		}
	}
}

func TestResultSVGPreview(t *testing.T) {
	c := newCanvas(200, 200)
	c.disc(100, 100, 60)
	c.erase(func(x, y float64) bool { return math.Hypot(x-100, y-100) < 30 })
	r := run(t, c, Options{PxPerMM: 2})
	svg := r.SVG()
	for _, want := range []string{"<svg", "viewBox=\"0 0 100.000 100.000\"", "fill-rule=\"evenodd\"", "<path"} {
		if !strings.Contains(svg, want) {
			t.Fatalf("preview missing %q: %.200s", want, svg)
		}
	}
	// the ring is one path with two sub-paths (outer + hole)
	if strings.Count(svg, "M") < 2 {
		t.Fatal("hole missing from the preview")
	}
}

// The same input must give bit-identical output: recipes and undo/redo rely on it.
func TestTraceIsDeterministic(t *testing.T) {
	c := newCanvas(300, 300)
	c.disc(100, 100, 60)
	c.disc(210, 190, 50)
	c.erase(func(x, y float64) bool { return math.Hypot(x-210, y-190) < 20 })
	c.stroke([]Pt{{20, 280}, {150, 250}, {280, 280}}, 6)
	for _, mode := range []Mode{FilledRegions, Outline, Centerline} {
		first := run(t, c, Options{Mode: mode, PxPerMM: 3})
		for i := 0; i < 8; i++ {
			again := run(t, c, Options{Mode: mode, PxPerMM: 3})
			first.Stats.Elapsed, again.Stats.Elapsed = 0, 0
			if !reflect.DeepEqual(first, again) {
				t.Fatalf("%s: run %d differs from the first", mode, i+2)
			}
		}
	}
}

func TestStrokesToRegionsMergesJunctionsAndKeepsLoops(t *testing.T) {
	line := func(a, b Pt) Stroke {
		return Stroke{Segs: []Seg{{a, padd(a, pmul(psub(b, a), 1.0/3)), padd(a, pmul(psub(b, a), 2.0/3)), b}}}
	}
	// a plus: two crossing strokes merge into ONE region of area 2*L*w - w^2 (plus the round ends)
	res, err := StrokesToRegions(context.Background(), []Stroke{line(Pt{0, 20}, Pt{40, 20}), line(Pt{20, 0}, Pt{20, 40})}, 2, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Regions) != 1 || len(res.Regions[0].Holes) != 0 {
		t.Fatalf("a plus is one region: %d", len(res.Regions))
	}
	want := 2*40*2 - 2*2 + 4*(math.Pi*1*1/2) // minus the crossing, plus four half-disc caps
	within(t, "plus area", res.Regions[0].Area, want, 0.03)
	// coordinates stay in the strokes' frame: the plus is centred on (20, 20)
	minX, maxX := math.Inf(1), math.Inf(-1)
	for _, s := range res.Regions[0].Outer {
		for u := 0.0; u <= 1; u += 0.05 {
			p := s.At(u)
			minX, maxX = math.Min(minX, p[0]), math.Max(maxX, p[0])
		}
	}
	if math.Abs(minX+1-0) > 0.2 || math.Abs(maxX-41) > 0.2 {
		t.Fatalf("x extent %v..%v want -1..41", minX, maxX)
	}
	// a closed ring stroke is a region with a hole
	var segs []Seg
	for k := 0; k < 4; k++ {
		a0, a1 := float64(k)*math.Pi/2, float64(k+1)*math.Pi/2
		kk := 4.0 / 3 * math.Tan((a1-a0)/4) * 10
		p0, p1 := Pt{20 + 10*math.Cos(a0), 20 + 10*math.Sin(a0)}, Pt{20 + 10*math.Cos(a1), 20 + 10*math.Sin(a1)}
		segs = append(segs, Seg{p0, Pt{p0[0] - kk*math.Sin(a0), p0[1] + kk*math.Cos(a0)}, Pt{p1[0] + kk*math.Sin(a1), p1[1] - kk*math.Cos(a1)}, p1})
	}
	ring, err := StrokesToRegions(context.Background(), []Stroke{{Segs: segs, Closed: true}}, 2, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(ring.Regions) != 1 || len(ring.Regions[0].Holes) != 1 {
		t.Fatalf("ring: %d regions", len(ring.Regions))
	}
	within(t, "ring area", ring.Regions[0].Area, 2*math.Pi*10*2, 0.03)
	if _, err := StrokesToRegions(context.Background(), nil, 2, Options{}); err == nil {
		t.Fatal("no strokes")
	}
	if _, err := StrokesToRegions(context.Background(), []Stroke{line(Pt{0, 0}, Pt{1, 1})}, 0, Options{}); err == nil {
		t.Fatal("zero width")
	}
}
