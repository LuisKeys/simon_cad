package vectorize

import (
	"context"
	"fmt"
	"image"
	"math"
	"sort"
	"time"
)

// Mode says what to extract. The three modes stay distinct: a filled logo keeps its solid
// regions, a handwritten drawing becomes strokes, and OUTLINE returns the boundaries alone.
type Mode string

const (
	Outline       Mode = "outline"        // closed boundary curves of the shapes (holes included)
	Centerline    Mode = "centerline"     // stroke centerlines with a width, to sweep a profile along
	FilledRegions Mode = "filled_regions" // closed regions with their holes, to extrude or emboss
)

// ValidMode reports whether m is a known mode.
func ValidMode(m Mode) bool { return m == Outline || m == Centerline || m == FilledRegions }

// Options configure a run. Zero values pick sensible defaults.
type Options struct {
	Mode   Mode
	Source string // auto, luminance, alpha
	Invert bool   // light shapes on a dark background

	Threshold      int // 0..255 on the ink field; 0 = automatic (Otsu)
	Adaptive       bool
	AdaptiveWindow int // px, default 31
	AdaptiveOffset int // default 8

	MinArea int     // drop foreground specks below this many px (default 12)
	MinHole int     // fill holes below this many px (default 12)
	Smooth  float64 // blur sigma in px before tracing, 0..1.5 (default 0.7)
	Tol     float64 // Bezier fit tolerance in px (default 0.8)
	Corner  float64 // turn in degrees that counts as a sharp corner (default 55)
	Spur    float64 // centerline branches shorter than this many px are dropped (default 6)
	Close   bool    // centerline: close one-pixel gaps in strokes first

	// PxPerMM scales the output; 0 leaves it in pixels. The origin is the image's bottom-left
	// corner and Y points up.
	PxPerMM  float64
	MaxLoops int // safety cap on contours (default 5000)
}

func (o *Options) normalize() error {
	if o.Mode == "" {
		o.Mode = FilledRegions
	}
	if !ValidMode(o.Mode) {
		return fmt.Errorf("mode must be outline, centerline or filled_regions (got %q)", o.Mode)
	}
	if o.Source == "" {
		o.Source = SourceAuto
	}
	if o.Source != SourceAuto && o.Source != SourceLuminance && o.Source != SourceAlpha {
		return fmt.Errorf("source must be auto, luminance or alpha (got %q)", o.Source)
	}
	if o.Threshold < 0 || o.Threshold > 255 {
		return fmt.Errorf("threshold must be 0..255")
	}
	if o.AdaptiveWindow <= 0 {
		o.AdaptiveWindow = 31
	}
	if o.AdaptiveOffset == 0 {
		o.AdaptiveOffset = 8
	}
	if o.MinArea == 0 {
		o.MinArea = 12
	}
	if o.MinHole == 0 {
		o.MinHole = 12
	}
	if o.Smooth < 0 || o.Smooth > 1.5 {
		return fmt.Errorf("smooth must be between 0 and 1.5 px")
	}
	if o.Smooth == 0 {
		o.Smooth = 0.7
	}
	if o.Tol == 0 {
		o.Tol = 0.8
	}
	if o.Tol < 0.05 {
		return fmt.Errorf("tolerance must be at least 0.05 px")
	}
	if o.Corner == 0 {
		o.Corner = 55
	}
	if o.Spur == 0 {
		o.Spur = 6
	}
	if o.PxPerMM < 0 || math.IsNaN(o.PxPerMM) {
		return fmt.Errorf("px_per_mm must be positive")
	}
	if o.MaxLoops == 0 {
		o.MaxLoops = 5000
	}
	return nil
}

// Region is a closed shape with its holes (counter-clockwise outer, clockwise holes, Y up).
type Region struct {
	Outer []Seg
	Holes [][]Seg
	Area  float64 // net area in output units²
}

// Stroke is a centerline: open, or closed when it loops back on itself. Widths holds the stroke
// width at every knot (len(Segs)+1 knots when open, len(Segs) when closed).
type Stroke struct {
	Segs   []Seg
	Closed bool
	Widths []float64
	Length float64
}

// ClosedLoop is one boundary curve (OUTLINE mode).
type ClosedLoop struct {
	Segs []Seg
	Hole bool
	Area float64
}

// Stats describes the run.
type Stats struct {
	Threshold    int
	ForegroundPx int
	Specks       int
	HolesFilled  int
	Loops        int
	Segments     int
	Elapsed      time.Duration
}

// Result is the vector geometry, in millimetres when PxPerMM was given (pixels otherwise).
type Result struct {
	Mode     Mode
	WidthPx  int
	HeightPx int
	Scale    float64 // output units per pixel
	Regions  []Region
	Outlines []ClosedLoop
	Strokes  []Stroke
	Warnings []string
	Stats    Stats
}

// Run vectorizes a decoded image. It stops early when ctx is cancelled.
func Run(ctx context.Context, img image.Image, opt Options) (*Result, error) {
	if err := opt.normalize(); err != nil {
		return nil, err
	}
	t0 := time.Now()
	ink, err := Extract(ctx, img, opt.Source, opt.Invert)
	if err != nil {
		return nil, err
	}
	return runInk(ctx, ink, opt, t0)
}

// RunInk vectorizes a prepared ink field (tests, or callers that built their own).
func RunInk(ctx context.Context, ink *Ink, opt Options) (*Result, error) {
	if err := opt.normalize(); err != nil {
		return nil, err
	}
	return runInk(ctx, ink, opt, time.Now())
}

func runInk(ctx context.Context, ink *Ink, opt Options, t0 time.Time) (*Result, error) {
	scale := 1.0
	if opt.PxPerMM > 0 {
		scale = 1 / opt.PxPerMM
	}
	res := &Result{Mode: opt.Mode, WidthPx: ink.W, HeightPx: ink.H, Scale: scale}
	var mask *Mask
	if opt.Adaptive {
		mask = ThresholdAdaptive(ink, opt.AdaptiveWindow, opt.AdaptiveOffset)
		res.Stats.Threshold = -1
	} else {
		t := opt.Threshold
		if t == 0 {
			t = Otsu(ink)
			// Otsu returns the last background level: foreground is strictly above it
			t++
		}
		res.Stats.Threshold = t
		mask = ThresholdFixed(ink, t)
	}
	var err error
	if res.Stats.Specks, res.Stats.HolesFilled, err = Clean(ctx, mask, opt.MinArea, opt.MinHole); err != nil {
		return nil, err
	}
	fg := mask.Count()
	res.Stats.ForegroundPx = fg
	total := ink.W * ink.H
	switch {
	case fg == 0:
		res.Warnings = append(res.Warnings, "VECTORIZE_EMPTY: nothing was detected as foreground (try a different threshold, adaptive thresholding or invert)")
		res.Stats.Elapsed = time.Since(t0)
		return res, nil
	case float64(fg) > 0.6*float64(total):
		res.Warnings = append(res.Warnings, "FOREGROUND_MAJORITY: most of the image is foreground; the subject may be light on dark (try invert)")
	}
	switch opt.Mode {
	case Centerline:
		err = runCenterline(ctx, mask, opt, res)
	default:
		err = runBoundaries(ctx, mask, opt, res)
	}
	if err != nil {
		return nil, err
	}
	res.Stats.Elapsed = time.Since(t0)
	if res.Stats.Loops == 0 && len(res.Strokes) == 0 {
		res.Warnings = append(res.Warnings, "VECTORIZE_EMPTY: no shapes survived the noise filter (lower min_area or the threshold)")
	}
	return res, nil
}

// toOut maps image pixel coordinates (y down) to output units (y up, origin bottom-left).
func (r *Result) toOut(p Pt) Pt { return Pt{p[0] * r.Scale, (float64(r.HeightPx) - p[1]) * r.Scale} }

func (r *Result) mapSegs(segs []Seg) []Seg {
	out := make([]Seg, len(segs))
	for i, s := range segs {
		out[i] = Seg{r.toOut(s.P0), r.toOut(s.C1), r.toOut(s.C2), r.toOut(s.P1)}
	}
	return out
}

// segArea is the signed area enclosed by a closed chain of segments (shoelace on a fine sampling).
func segArea(segs []Seg) float64 {
	var pts []Pt
	for _, s := range segs {
		for i := 0; i < 12; i++ {
			pts = append(pts, s.At(float64(i)/12))
		}
	}
	return signedArea(pts)
}

// orient flips a closed chain so its signed area has the wanted sign.
func orient(segs []Seg, wantPositive bool) []Seg {
	if (segArea(segs) > 0) == wantPositive {
		return segs
	}
	out := make([]Seg, len(segs))
	for i, s := range segs {
		out[len(segs)-1-i] = Seg{s.P1, s.C2, s.C1, s.P0}
	}
	return out
}

func runBoundaries(ctx context.Context, mask *Mask, opt Options, res *Result) error {
	f, err := blurMask(ctx, mask, opt.Smooth)
	if err != nil {
		return err
	}
	loops, err := traceLoops(ctx, f)
	if err != nil {
		return err
	}
	if len(loops) > opt.MaxLoops {
		return fmt.Errorf("%d contours found: too many to vectorize (the image is noisy: raise min_area or the threshold)", len(loops))
	}
	parent := Nest(loops)
	minArea := float64(opt.MinArea) / 4
	type fitted struct {
		segs []Seg
		area float64
		ok   bool
	}
	fits := make([]fitted, len(loops))
	for i, l := range loops {
		if err := ctx.Err(); err != nil {
			return err
		}
		if math.Abs(signedArea(l.Pts)) < minArea {
			continue
		}
		pts := SimplifyClosed(l.Pts, opt.Tol*0.2)
		segs := FitClosed(pts, opt.Tol, opt.Corner)
		if len(segs) == 0 {
			continue
		}
		segs = orient(res.mapSegs(segs), !l.Hole)
		fits[i] = fitted{segs, math.Abs(segArea(segs)), true}
		res.Stats.Segments += len(segs)
	}
	if opt.Mode == Outline {
		for i, l := range loops {
			if fits[i].ok {
				res.Outlines = append(res.Outlines, ClosedLoop{Segs: fits[i].segs, Hole: l.Hole, Area: fits[i].area})
			}
		}
		res.Stats.Loops = len(res.Outlines)
		return nil
	}
	// regions: every outer loop collects the hole loops directly inside it
	regionOf := map[int]int{}
	for i, l := range loops {
		if !fits[i].ok || l.Hole {
			continue
		}
		regionOf[i] = len(res.Regions)
		res.Regions = append(res.Regions, Region{Outer: fits[i].segs, Area: fits[i].area})
	}
	for i, l := range loops {
		if !fits[i].ok || !l.Hole {
			continue
		}
		if ri, ok := regionOf[parent[i]]; ok {
			r := &res.Regions[ri]
			r.Holes = append(r.Holes, fits[i].segs)
			r.Area -= fits[i].area
		}
	}
	res.Stats.Loops = len(res.Regions)
	return nil
}

func smoothChain(pts []Pt, closed bool, window int) []Pt {
	n := len(pts)
	if n < 5 || window < 3 {
		return pts
	}
	r := window / 2
	out := make([]Pt, n)
	for i := range pts {
		if !closed && (i < 1 || i >= n-1) {
			out[i] = pts[i]
			continue
		}
		var sx, sy, c float64
		for k := -r; k <= r; k++ {
			j := i + k
			if closed {
				j = ((j % n) + n) % n
			} else if j < 0 || j >= n {
				continue
			}
			sx += pts[j][0]
			sy += pts[j][1]
			c++
		}
		out[i] = Pt{sx / c, sy / c}
	}
	return out
}

func runCenterline(ctx context.Context, mask *Mask, opt Options, res *Result) error {
	m := mask
	if opt.Close {
		m = Close(mask)
	}
	skel, err := Thin(ctx, m)
	if err != nil {
		return err
	}
	dist := EDT(m)
	chains, err := SkeletonChains(ctx, skel, m.W, m.H, dist, opt.Spur)
	if err != nil {
		return err
	}
	if len(chains) > opt.MaxLoops {
		return fmt.Errorf("%d strokes found: too many (the image is noisy: raise min_area or the threshold)", len(chains))
	}
	for _, c := range chains {
		if err := ctx.Err(); err != nil {
			return err
		}
		// A skeleton follows the pen to the pixel: its wobble is far below the pen's width, so
		// smooth and fit in proportion to it (a thick stroke tolerates a rounder centreline).
		wid := make([]float64, len(c.Width))
		copy(wid, c.Width)
		sort.Float64s(wid)
		pen := 1.0
		if len(wid) > 0 {
			pen = math.Max(1, wid[len(wid)/2])
		}
		win := int(math.Round(pen))*2 + 1
		if win < 5 {
			win = 5
		}
		pts := c.Pts
		for pass := 0; pass < 2; pass++ {
			pts = smoothChain(pts, c.Closed, win)
		}
		tol := math.Max(opt.Tol, 0.12*pen)
		var segs []Seg
		if c.Closed {
			pts = SimplifyClosed(pts, tol*0.2)
			segs = FitClosed(pts, tol, opt.Corner)
		} else {
			pts = SimplifyOpen(pts, tol*0.2)
			segs = FitOpen(pts, tol, opt.Corner)
		}
		if len(segs) == 0 {
			continue
		}
		// stroke width at every knot: from the nearest skeleton pixel
		knots := make([]Pt, 0, len(segs)+1)
		for _, s := range segs {
			knots = append(knots, s.P0)
		}
		if !c.Closed {
			knots = append(knots, segs[len(segs)-1].P1)
		}
		widths := make([]float64, len(knots))
		for k, p := range knots {
			best, bd := 0, math.Inf(1)
			for i, q := range c.Pts {
				if d := pdist(p, q); d < bd {
					best, bd = i, d
				}
			}
			// the median over a short stretch around it: one skeleton pixel is a noisy ruler
			span := maxInt(2, int(pen))
			var near []float64
			for i := maxInt(0, best-span); i <= minInt(len(c.Width)-1, best+span); i++ {
				near = append(near, c.Width[i])
			}
			sort.Float64s(near)
			widths[k] = near[len(near)/2] * res.Scale
		}
		mapped := res.mapSegs(segs)
		res.Strokes = append(res.Strokes, Stroke{Segs: mapped, Closed: c.Closed, Widths: widths, Length: ChainLength(mapped)})
		res.Stats.Segments += len(segs)
	}
	res.Stats.Loops = len(res.Strokes)
	return nil
}

// maxStrokeCanvas bounds the raster used to merge strokes (pixels per side).
const maxStrokeCanvas = 3000

// StrokesToRegions turns centerlines into the solid regions a pen of constant width would leave:
// every stroke is drawn with round ends and joins at widthMM into a raster, the union is traced
// back into regions with holes. The merging at junctions is exact in 2D, which is far more robust
// than a boolean of swept solids that touch almost tangentially. Coordinates stay in the strokes'
// own frame (millimetres).
func StrokesToRegions(ctx context.Context, strokes []Stroke, widthMM float64, opt Options) (*Result, error) {
	if !(widthMM > 0) {
		return nil, fmt.Errorf("stroke width must be positive")
	}
	if err := opt.normalize(); err != nil {
		return nil, err
	}
	// flatten every stroke into a polyline in mm
	var lines [][]Pt
	lo, hi := Pt{math.Inf(1), math.Inf(1)}, Pt{math.Inf(-1), math.Inf(-1)}
	for _, s := range strokes {
		var pl []Pt
		for _, g := range s.Segs {
			steps := int(math.Max(4, math.Ceil(pdist(g.P0, g.C1)+pdist(g.C1, g.C2)+pdist(g.C2, g.P1))/(widthMM*0.2)))
			for i := 0; i < steps; i++ {
				pl = append(pl, g.At(float64(i)/float64(steps)))
			}
		}
		if len(s.Segs) > 0 {
			pl = append(pl, s.Segs[len(s.Segs)-1].P1)
		}
		if s.Closed && len(pl) > 0 {
			pl = append(pl, pl[0])
		}
		if len(pl) < 2 {
			continue
		}
		for _, p := range pl {
			lo = Pt{math.Min(lo[0], p[0]), math.Min(lo[1], p[1])}
			hi = Pt{math.Max(hi[0], p[0]), math.Max(hi[1], p[1])}
		}
		lines = append(lines, pl)
	}
	if len(lines) == 0 {
		return nil, fmt.Errorf("VECTORIZE_EMPTY: no strokes to draw")
	}
	m := widthMM
	x0, y0 := lo[0]-m, lo[1]-m
	spanX, spanY := hi[0]-lo[0]+2*m, hi[1]-lo[1]+2*m
	pxmm := math.Max(8/widthMM, 6) // at least 8 pixels across the pen
	if f := maxStrokeCanvas / math.Max(spanX, spanY); pxmm > f {
		pxmm = f
	}
	if pxmm*widthMM < 3 {
		return nil, fmt.Errorf("the strokes are too long for their width (%.0f mm of drawing for a %.2f mm pen)", math.Max(spanX, spanY), widthMM)
	}
	W, H := int(math.Ceil(spanX*pxmm)), int(math.Ceil(spanY*pxmm))
	mask := newMask(W, H)
	r := widthMM / 2 * pxmm
	toPx := func(p Pt) Pt { return Pt{(p[0] - x0) * pxmm, (float64(H) - (p[1]-y0)*pxmm)} }
	for li, pl := range lines {
		if li&7 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		for i := 0; i+1 < len(pl); i++ {
			a, b := toPx(pl[i]), toPx(pl[i+1])
			xa, xb := int(math.Floor(math.Min(a[0], b[0])-r-1)), int(math.Ceil(math.Max(a[0], b[0])+r+1))
			ya, yb := int(math.Floor(math.Min(a[1], b[1])-r-1)), int(math.Ceil(math.Max(a[1], b[1])+r+1))
			for y := maxInt(0, ya); y < minInt(H, yb); y++ {
				for x := maxInt(0, xa); x < minInt(W, xb); x++ {
					if distSeg(Pt{float64(x) + 0.5, float64(y) + 0.5}, a, b) <= r {
						mask.Pix[y*W+x] = true
					}
				}
			}
		}
	}
	res := &Result{Mode: FilledRegions, WidthPx: W, HeightPx: H, Scale: 1 / pxmm}
	opt.Mode = FilledRegions
	if err := runBoundaries(ctx, mask, opt, res); err != nil {
		return nil, err
	}
	shift := func(segs []Seg) []Seg {
		out := make([]Seg, len(segs))
		f := func(p Pt) Pt { return Pt{p[0] + x0, p[1] + y0} }
		for i, s := range segs {
			out[i] = Seg{f(s.P0), f(s.C1), f(s.C2), f(s.P1)}
		}
		return out
	}
	for i := range res.Regions {
		rg := &res.Regions[i]
		rg.Outer = shift(rg.Outer)
		for k := range rg.Holes {
			rg.Holes[k] = shift(rg.Holes[k])
		}
	}
	return res, nil
}
