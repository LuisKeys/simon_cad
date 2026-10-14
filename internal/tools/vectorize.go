package tools

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/accelone/simoncad/internal/cadir"
	"github.com/accelone/simoncad/internal/geometry/features"
	"github.com/accelone/simoncad/internal/vectorize"
)

const (
	maxSourceBytes   = 20 << 20
	maxTracedShapes  = 300
	maxTracedLoops   = 2000
	vectorizeTimeout = 60 * time.Second
)

func vectorizeProps(p props) props {
	p["image_id"] = text("Id of an attached image (PNG or JPEG)")
	p["path"] = text("Path of a PNG, JPEG or SVG file on disk (alternative to image_id)")
	p["svg"] = text("SVG markup given inline (alternative to image_id / path)")
	p["mode"] = enum("outline: closed boundary curves. centerline: stroke centrelines with a width (for sweeping a profile along). filled_regions: solid regions with their holes (for extrusion / relief). Default filled_regions.",
		string(vectorize.Outline), string(vectorize.Centerline), string(vectorize.FilledRegions))
	p["threshold"] = integer("Ink level 1..255 that separates foreground (default: automatic, Otsu)", 1, 255)
	p["adaptive"] = boolean("Adaptive threshold: follows uneven lighting (photographed drawings)")
	p["invert"] = boolean("The subject is light on a dark background")
	p["source"] = enum("What is foreground: auto (alpha when the image is transparent, else darkness), luminance or alpha", "auto", "luminance", "alpha")
	p["min_area"] = integer("Ignore specks smaller than this many pixels (default 12)", 1, 100000)
	p["smooth"] = number("Blur in pixels before tracing, 0..1.5 (default 0.7): higher = smoother curves, fewer details")
	p["tolerance"] = number("Curve fitting tolerance in pixels (default 0.8): higher = fewer segments")
	p["corner_angle"] = number("Turn in degrees that counts as a sharp corner (default 55)")
	p["close_gaps"] = boolean("Centerline: close one-pixel gaps in strokes before thinning")
	p["px_per_mm"] = positive("Pixels per millimetre of the real object (the scale)")
	p["width_mm"] = positive("Real width of the whole image in mm (alternative to px_per_mm)")
	p["height_mm"] = positive("Real height of the whole image in mm (alternative to px_per_mm)")
	p["dpi"] = positive("Image resolution in dots per inch (alternative to px_per_mm)")
	return p
}

func vectorizeTools() []*Tool {
	return []*Tool{
		{
			Name: "vectorize_image",
			Description: "Trace a raster image (PNG/JPEG) or SVG into vector geometry WITHOUT changing the model, and report what was found (shapes, holes, strokes, lengths, stroke width, warnings). " +
				"Deterministic: no AI. Use it to pick settings (threshold, mode, min_area, smooth) before image_to_paths. Modes stay distinct: filled_regions keeps solid areas with holes (logos, silhouettes), " +
				"centerline gives stroke centrelines with width (handwriting, line drawings, a flower), outline gives boundary curves only. Give px_per_mm / width_mm / height_mm / dpi to get sizes in mm.",
			Capability: capGeometry,
			Schema: object(vectorizeProps(props{
				"preview": boolean("Also return the traced result as SVG markup (preview_svg)"),
			})),
			Query: queryVectorizeImage,
		},
		{
			Name: "image_to_paths",
			Description: "Trace an image or SVG and create editable geometry from it: filled_regions → one sketch with a region per shape (holes preserved; extrude it, emboss it, put it on a stamp); " +
				"outline → closed Bezier path nodes; centerline → open/closed Bezier path nodes whose per-point `w` carries the stroke width (sweep a profile along them). " +
				"The real size is required: pass width_mm, height_mm, px_per_mm or dpi (ask the user if it is unknown, never guess). Placed on `plane` (XY default) with its lower-left corner at (x, y) or centred (center). " +
				"Nothing is invented: only what the trace found is created; use vectorize_image first to tune settings.",
			Capability: capGeometry,
			Schema: object(withMeta(vectorizeProps(props{
				"plane":  enum("Plane to place the shapes on (default XY)", "XY", "XZ", "YZ"),
				"offset": number("Offset of the plane along its normal (mm)"),
				"x":      number("Placement of the image's lower-left corner along the plane's u axis (mm, default 0)"),
				"y":      number("Placement of the image's lower-left corner along the plane's v axis (mm, default 0)"),
				"center": boolean("Centre the image on (x, y) instead of placing its lower-left corner there"),
				"group":  text("Group name for the created nodes (default: the base id)"),
			}))),
			Plan: planImageToPaths,
		},
	}
}

type traceSource struct {
	img  image.Image
	svg  *vectorize.SVG
	name string
}

func loadTraceSource(env Env, a Args) (*traceSource, error) {
	n := 0
	for _, k := range []string{"image_id", "path", "svg"} {
		if a.Has(k) {
			n++
		}
	}
	if n != 1 {
		return nil, fmt.Errorf("give exactly one source: `image_id` (an attached image), `path` (a PNG/JPEG/SVG file) or `svg` (inline markup)")
	}
	var data []byte
	name := ""
	switch {
	case a.Has("image_id"):
		im, ok := env.Images().Get(a.Str("image_id"))
		if !ok {
			return nil, fmt.Errorf("image %q is not attached to this session; ask the user to attach it", a.Str("image_id"))
		}
		data, name = im.Data, a.Str("image_id")
	case a.Has("path"):
		p := a.Str("path")
		switch strings.ToLower(filepath.Ext(p)) {
		case ".png", ".jpg", ".jpeg", ".svg":
		default:
			return nil, fmt.Errorf("path must be a .png, .jpg or .svg file (got %q)", filepath.Ext(p))
		}
		p, err := env.ResolveIOPath(p, false)
		if err != nil {
			return nil, err
		}
		st, err := os.Stat(p)
		if err != nil {
			return nil, fmt.Errorf("cannot read %s: %v", p, err)
		}
		if st.Size() > maxSourceBytes {
			return nil, fmt.Errorf("%s is larger than %d MB", p, maxSourceBytes>>20)
		}
		if data, err = os.ReadFile(p); err != nil {
			return nil, fmt.Errorf("cannot read %s: %v", p, err)
		}
		name = filepath.Base(p)
	default:
		data, name = []byte(a.Str("svg")), "inline.svg"
		if len(data) > maxSourceBytes {
			return nil, fmt.Errorf("svg is larger than %d MB", maxSourceBytes>>20)
		}
	}
	if t := bytes.TrimSpace(data); len(t) > 0 && t[0] == '<' {
		s, err := vectorize.ParseSVG(data)
		if err != nil {
			return nil, err
		}
		return &traceSource{svg: s, name: name}, nil
	}
	img, err := vectorize.Decode(data)
	if err != nil {
		return nil, err
	}
	return &traceSource{img: img, name: name}, nil
}

func (s *traceSource) size() (w, h int) {
	if s.svg != nil {
		lo, hi := s.svg.Width, s.svg.Height
		if lo <= 0 || hi <= 0 {
			_, hb := s.svg.Width, s.svg.Height
			_ = hb
			return 0, 0
		}
		return int(math.Ceil(lo)), int(math.Ceil(hi))
	}
	b := s.img.Bounds()
	return b.Dx(), b.Dy()
}

// traceScale turns the scale arguments into pixels per mm (0 = none given).
func traceScale(a Args, wPx, hPx int) (float64, error) {
	n := 0
	var v float64
	for _, k := range []string{"px_per_mm", "width_mm", "height_mm", "dpi"} {
		if a.Has(k) {
			n++
		}
	}
	if n > 1 {
		return 0, fmt.Errorf("give only one of px_per_mm, width_mm, height_mm, dpi")
	}
	switch {
	case a.Has("px_per_mm"):
		v = a.Num("px_per_mm")
	case a.Has("dpi"):
		v = a.Num("dpi") / 25.4
	case a.Has("width_mm"):
		if wPx <= 0 {
			return 0, fmt.Errorf("the image width is unknown: use px_per_mm")
		}
		v = float64(wPx) / a.Num("width_mm")
	case a.Has("height_mm"):
		if hPx <= 0 {
			return 0, fmt.Errorf("the image height is unknown: use px_per_mm")
		}
		v = float64(hPx) / a.Num("height_mm")
	}
	return v, nil
}

func vectorizeOptions(a Args, pxPerMM float64) (vectorize.Options, error) {
	o := vectorize.Options{
		Mode: vectorize.Mode(orDefault(a.Str("mode"), string(vectorize.FilledRegions))), Source: a.Str("source"),
		Invert: a.Bool("invert"), Adaptive: a.Bool("adaptive"), Close: a.Bool("close_gaps"),
		Threshold: int(a.Num("threshold")), MinArea: int(a.Num("min_area")), Smooth: a.Num("smooth"),
		Tol: a.Num("tolerance"), Corner: a.Num("corner_angle"), PxPerMM: pxPerMM,
	}
	return o, nil
}

func runTrace(src *traceSource, opt vectorize.Options) (*vectorize.Result, error) {
	ctx, cancel := context.WithTimeout(context.Background(), vectorizeTimeout)
	defer cancel()
	var res *vectorize.Result
	var err error
	if src.svg != nil {
		res, err = vectorize.RunSVG(ctx, src.svg, opt)
	} else {
		res, err = vectorize.Run(ctx, src.img, opt)
	}
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("vectorizing took longer than %s: use a smaller image or raise min_area", vectorizeTimeout)
		}
		if strings.Contains(err.Error(), "VECTORIZE_EMPTY") {
			return nil, features.Coded(features.CodeVectorizeEmpty, "check the source, threshold and invert", "%v", err)
		}
		return nil, err
	}
	return res, nil
}

func summarizeTrace(res *vectorize.Result, scaled bool) map[string]any {
	unit := "px"
	if scaled {
		unit = "mm"
	}
	out := map[string]any{
		"mode": res.Mode, "image_px": []int{res.WidthPx, res.HeightPx}, "units": unit,
		"regions": len(res.Regions), "outlines": len(res.Outlines), "strokes": len(res.Strokes),
		"segments": res.Stats.Segments, "warnings": res.Warnings,
		"stats": map[string]any{"threshold": res.Stats.Threshold, "foreground_px": res.Stats.ForegroundPx,
			"specks_removed": res.Stats.Specks, "holes_filled": res.Stats.HolesFilled, "elapsed_ms": res.Stats.Elapsed.Milliseconds()},
	}
	if scaled {
		out["size_mm"] = []float64{round6(float64(res.WidthPx) * res.Scale), round6(float64(res.HeightPx) * res.Scale)}
	}
	holes, area := 0, 0.0
	for _, r := range res.Regions {
		holes += len(r.Holes)
		area += r.Area
	}
	if len(res.Regions) > 0 {
		out["holes"] = holes
		out["total_area"] = round6(area)
	}
	if len(res.Strokes) > 0 {
		var length float64
		var widths []float64
		for _, s := range res.Strokes {
			length += s.Length
			widths = append(widths, s.Widths...)
		}
		sort.Float64s(widths)
		out["total_stroke_length"] = round6(length)
		if len(widths) > 0 {
			out["median_stroke_width"] = round6(widths[len(widths)/2])
		}
	}
	return out
}

func queryVectorizeImage(env Env, a Args) (any, error) {
	src, err := loadTraceSource(env, a)
	if err != nil {
		return nil, err
	}
	w, h := src.size()
	px, err := traceScale(a, w, h)
	if err != nil {
		return nil, err
	}
	opt, err := vectorizeOptions(a, px)
	if err != nil {
		return nil, err
	}
	res, err := runTrace(src, opt)
	if err != nil {
		return nil, err
	}
	out := summarizeTrace(res, px > 0)
	out["source"] = src.name
	if a.Bool("preview") {
		out["preview_svg"] = res.SVG()
	}
	return out, nil
}

// ---- image_to_paths ------------------------------------------------------------------------

// planePoint maps sketch-plane coordinates (u, v) to world space with the same convention as
// create_sketch: XY u=X v=Y; XZ u=Z v=X; YZ u=Y v=Z.
func planePoint(plane string, off, u, v float64) [3]float64 {
	switch plane {
	case "XZ":
		return [3]float64{v, off, u}
	case "YZ":
		return [3]float64{off, u, v}
	}
	return [3]float64{u, v, off}
}

func pt3(p [3]float64) []any { return []any{round6(p[0]), round6(p[1]), round6(p[2])} }

// bezierPathParams turns a chain of Bezier segments into the parameters of a `path` node. Stroke
// widths (mm) become the per-point `w` factor relative to `nominal`.
func bezierPathParams(segs []vectorize.Seg, closed bool, widths []float64, nominal float64, plane string, off float64, at func(p vectorize.Pt) (float64, float64)) map[string]any {
	n := len(segs)
	world := func(p vectorize.Pt) [3]float64 { u, v := at(p); return planePoint(plane, off, u, v) }
	count := n + 1
	if closed {
		count = n
	}
	pts := make([]any, count)
	for i := 0; i < count; i++ {
		var p, in, out vectorize.Pt
		hasIn, hasOut := false, false
		if i < n {
			p, out, hasOut = segs[i].P0, segs[i].C1, true
		} else {
			p = segs[n-1].P1
		}
		switch {
		case i > 0:
			in, hasIn = segs[i-1].C2, true
		case closed:
			in, hasIn = segs[n-1].C2, true
		}
		m := map[string]any{"p": pt3(world(p))}
		if hasIn {
			m["in"] = pt3(world(in))
		}
		if hasOut {
			m["out"] = pt3(world(out))
		}
		if nominal > 0 && i < len(widths) {
			if w := widths[i] / nominal; math.Abs(w-1) > 1e-6 {
				m["w"], m["h"] = round6(math.Max(w, 0.05)), round6(math.Max(w, 0.05))
			}
		}
		pts[i] = m
	}
	out := map[string]any{"type": "bezier", "points": pts}
	if closed {
		out["closed"] = true
	}
	return out
}

func curveEntity(id string, segs []vectorize.Seg, at func(p vectorize.Pt) (float64, float64)) map[string]any {
	q := func(p vectorize.Pt) []any { u, v := at(p); return []any{round6(u), round6(v)} }
	ss := make([]any, len(segs))
	for i, s := range segs {
		ss[i] = []any{q(s.P0), q(s.C1), q(s.C2), q(s.P1)}
	}
	return map[string]any{"id": id, "kind": "curve", "segments": ss}
}

func planImageToPaths(env Env, a Args) (*Plan, error) {
	src, err := loadTraceSource(env, a)
	if err != nil {
		return nil, err
	}
	w, h := src.size()
	px, err := traceScale(a, w, h)
	if err != nil {
		return nil, err
	}
	if px <= 0 {
		return nil, features.Coded(features.CodeScaleRequired, "ask the user for the real size of the drawing (width_mm / height_mm / px_per_mm / dpi); if they want an assumption, state it and pass px_per_mm explicitly",
			"the real size is required: nothing is created at an invented scale")
	}
	opt, err := vectorizeOptions(a, px)
	if err != nil {
		return nil, err
	}
	res, err := runTrace(src, opt)
	if err != nil {
		return nil, err
	}
	plane, off := orDefault(a.Str("plane"), "XY"), a.Num("offset")
	x0, y0 := a.Num("x"), a.Num("y")
	if a.Bool("center") {
		x0 -= float64(res.WidthPx) * res.Scale / 2
		y0 -= float64(res.HeightPx) * res.Scale / 2
	}
	at := func(p vectorize.Pt) (float64, float64) { return x0 + p[0], y0 + p[1] }

	base := a.Str("id")
	if base == "" {
		base = strings.Trim(nonID.ReplaceAllString(strings.ToLower(a.Str("name")), "_"), "_")
	}
	if base == "" || base[0] < 'a' || base[0] > 'z' {
		base = "trace"
	}
	group := orDefault(a.Str("group"), base)
	taken := map[string]bool{}
	var nodes []*cadir.Node
	sub := Args{"intent": a.Str("intent")}
	add := func(typ string, params map[string]any, suffix string) {
		id := derivedID(env, base, suffix, taken)
		n := newNode(env, sub, id, typ, params)
		n.Group = group
		nodes = append(nodes, n)
	}
	desc := ""
	switch res.Mode {
	case vectorize.FilledRegions:
		if len(res.Regions) == 0 {
			return nil, features.Coded(features.CodeVectorizeEmpty, "check the threshold, invert and min_area (vectorize_image shows what is found)", "no filled regions were found")
		}
		if len(res.Regions) > maxTracedLoops {
			return nil, fmt.Errorf("%d regions found: too many for one sketch (raise min_area or tolerance)", len(res.Regions))
		}
		var ents, regions []any
		for i, rg := range res.Regions {
			oid := fmt.Sprintf("r%d", i+1)
			ents = append(ents, curveEntity(oid, rg.Outer, at))
			var holes []any
			for k, hole := range rg.Holes {
				hid := fmt.Sprintf("r%d_h%d", i+1, k+1)
				ents = append(ents, curveEntity(hid, hole, at))
				holes = append(holes, []any{hid})
			}
			rgn := map[string]any{"id": oid, "outer": []any{oid}}
			if len(holes) > 0 {
				rgn["holes"] = holes
			}
			regions = append(regions, rgn)
		}
		sk := map[string]any{"plane": plane, "entities": ents, "regions": regions}
		if off != 0 {
			sk["offset"] = off
		}
		if err := features.ValidateSketch(features.Params(sk)); err != nil {
			return nil, features.Coded(features.CodeVectorizeEmpty, "raise tolerance or smooth, or crop the image", "the traced regions do not form a valid sketch: %v", err)
		}
		add("sketch", sk, "")
		desc = fmt.Sprintf("a sketch with %d region(s) and %d hole(s)", len(res.Regions), countHoles(res))
	case vectorize.Outline:
		if len(res.Outlines) > maxTracedShapes {
			return nil, fmt.Errorf("%d outlines found: too many to create as separate paths (raise min_area or tolerance)", len(res.Outlines))
		}
		for _, l := range res.Outlines {
			add("path", bezierPathParams(l.Segs, true, nil, 0, plane, off, at), "_path")
		}
		desc = fmt.Sprintf("%d closed outline path(s)", len(res.Outlines))
	default:
		if len(res.Strokes) > maxTracedShapes {
			return nil, fmt.Errorf("%d strokes found: too many to create as separate paths (raise min_area or tolerance, or use close_gaps)", len(res.Strokes))
		}
		nominal := medianWidth(res)
		for _, s := range res.Strokes {
			add("path", bezierPathParams(s.Segs, s.Closed, s.Widths, nominal, plane, off, at), "_stroke")
		}
		desc = fmt.Sprintf("%d stroke path(s); stroke width ≈ %s mm (per-point w is relative to that: sweep a profile of that size)", len(res.Strokes), fmtMM(nominal))
	}
	if len(nodes) == 0 {
		return nil, features.Coded(features.CodeVectorizeEmpty, "check the threshold, invert and min_area", "nothing was found to create")
	}
	var warns []string
	for _, wmsg := range res.Warnings {
		warns = append(warns, wmsg)
	}
	out := addPlan(intentOr(a, "Trace "+src.name), "Creates "+desc+" from "+src.name+" ("+fmtMM(float64(res.WidthPx)*res.Scale)+" × "+fmtMM(float64(res.HeightPx)*res.Scale)+" mm).", nodes...)
	out.Warnings = warns
	out.LowRisk = len(warns) == 0
	return out, nil
}

func countHoles(r *vectorize.Result) int {
	n := 0
	for _, g := range r.Regions {
		n += len(g.Holes)
	}
	return n
}

func medianWidth(r *vectorize.Result) float64 {
	var ws []float64
	for _, s := range r.Strokes {
		ws = append(ws, s.Widths...)
	}
	if len(ws) == 0 {
		return 0
	}
	sort.Float64s(ws)
	return ws[len(ws)/2]
}
