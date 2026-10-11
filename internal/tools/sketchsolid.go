package tools

import (
	"context"
	"fmt"
	"math"
	"strings"

	"github.com/accelone/simoncad/internal/geometry/features"
	"github.com/accelone/simoncad/internal/vectorize"
)

func sketchSolidTools() []*Tool {
	return []*Tool{
		{
			Name: "sketch_to_solid",
			Description: "Turn a drawing or silhouette (PNG/JPEG/SVG) into an extruded solid in one step: trace → sketch → extrude, all editable and grouped. style=silhouette (default) extrudes the filled shapes with their holes " +
				"(a logo, a bracket outline, a cookie cutter); style=strokes thickens the line work of a drawing into ridges of `stroke_width` mm. The real size (width_mm, height_mm, px_per_mm or dpi) and `depth` are REQUIRED: " +
				"ask the user, never guess a dimension. The image only defines the outline: heights, holes that are not drawn and other 3D details are NOT inferred. " +
				"The answer lists the `assumed` defaults (threshold, placement). The change is proposed for the user's approval. Then check it: check_solid_vs_trace compares the solid with the trace, " +
				"and render_model (top / iso views) lets you compare the result with the source image and fix what differs.",
			Capability: capGeometry,
			Schema: object(withMeta(func() props {
				p := vectorizeProps(props{
					"style":        enum("silhouette (default): filled shapes. strokes: line work thickened to `stroke_width`", "silhouette", "strokes"),
					"depth":        positive("Extrusion height in mm (required)"),
					"stroke_width": positive("Ridge width in mm (style=strokes, required then)"),
					"symmetric":    boolean("Extrude half up and half down from the sketch plane"),
					"plane":        enum("Plane to place the drawing on (default XY)", "XY", "XZ", "YZ"),
					"offset":       number("Offset of the plane along its normal (mm)"),
					"x":            number("Lower-left corner u (mm, default 0)"),
					"y":            number("Lower-left corner v (mm, default 0)"),
					"center":       boolean("Centre the drawing on (x, y)"),
				})
				delete(p, "mode") // the style decides the trace mode
				return p
			}()), "depth"),
			Plan: planSketchToSolid,
		},
		{
			Name: "check_solid_vs_trace",
			Description: "Critic for sketch_to_solid: compares the finished solid with what the trace promised (volume = traced area × depth, bounding box of the drawing, one connected piece, no invented features) and " +
				"reports each check with the numbers. Run it after sketch_to_solid and after edits; a mismatch means the model was changed away from the drawing or the kernel altered it.",
			Capability: capGeometry,
			Schema:     object(props{"solid": text("Id of the extrude created by sketch_to_solid"), "tolerance": positive("Allowed relative volume error (default 0.01 = 1%)")}, "solid"),
			Query:      queryCheckSolidVsTrace,
		},
	}
}

func planSketchToSolid(env Env, a Args) (*Plan, error) {
	if !a.Has("depth") {
		return nil, fmt.Errorf("`depth` (the extrusion height in mm) is required: ask the user, a drawing does not say how thick the part is")
	}
	style := orDefault(a.Str("style"), "silhouette")
	if style == "strokes" && !a.Has("stroke_width") {
		return nil, fmt.Errorf("style=strokes needs `stroke_width` (the ridge width in mm): ask the user")
	}
	if style == "silhouette" && a.Has("stroke_width") {
		return nil, fmt.Errorf("stroke_width only applies to style=strokes")
	}
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
		return nil, features.Coded(features.CodeScaleRequired, "ask the user for the real size of the drawing (width_mm / height_mm / px_per_mm / dpi); do not invent one",
			"the real size is required: nothing is created at an invented scale")
	}
	a2 := Args{}
	for k, v := range a {
		a2[k] = v
	}
	a2["mode"] = map[string]string{"silhouette": string(vectorize.FilledRegions), "strokes": string(vectorize.Centerline)}[style]
	opt, err := vectorizeOptions(a2, px)
	if err != nil {
		return nil, err
	}
	res, err := runTrace(src, opt)
	if err != nil {
		return nil, err
	}
	regions := res.Regions
	if style == "strokes" {
		if len(res.Strokes) == 0 {
			return nil, features.Coded(features.CodeVectorizeEmpty, "check the threshold and invert, or use style=silhouette for solid shapes", "no strokes were found in %s", src.name)
		}
		rr, err := vectorize.StrokesToRegions(context.Background(), res.Strokes, a.Num("stroke_width"), vectorize.Options{})
		if err != nil {
			return nil, err
		}
		regions = rr.Regions
	}
	if len(regions) == 0 {
		return nil, features.Coded(features.CodeVectorizeEmpty, "check the threshold and invert settings with vectorize_image", "no shapes were found in %s", src.name)
	}
	if len(regions) > maxTracedLoops {
		return nil, fmt.Errorf("%d shapes found: too many for one sketch (raise min_area or tolerance)", len(regions))
	}

	plane, off := orDefault(a.Str("plane"), "XY"), a.Num("offset")
	x0, y0 := a.Num("x"), a.Num("y")
	if a.Bool("center") {
		x0 -= float64(res.WidthPx) * res.Scale / 2
		y0 -= float64(res.HeightPx) * res.Scale / 2
	}
	at := func(p vectorize.Pt) (float64, float64) { return x0 + p[0], y0 + p[1] }
	var ents, regs []any
	area, holes := 0.0, 0
	lo, hi := [2]float64{math.Inf(1), math.Inf(1)}, [2]float64{math.Inf(-1), math.Inf(-1)}
	grow := func(segs []vectorize.Seg) {
		for _, s := range segs {
			for _, p := range [4]vectorize.Pt{s.P0, s.C1, s.C2, s.P1} {
				u, v := at(p)
				lo[0], lo[1] = math.Min(lo[0], u), math.Min(lo[1], v)
				hi[0], hi[1] = math.Max(hi[0], u), math.Max(hi[1], v)
			}
		}
	}
	for i, rg := range regions {
		oid := fmt.Sprintf("r%d", i+1)
		ents = append(ents, curveEntity(oid, rg.Outer, at))
		grow(rg.Outer)
		area += rg.Area
		var hl []any
		for k, hole := range rg.Holes {
			hid := fmt.Sprintf("r%d_h%d", i+1, k+1)
			ents = append(ents, curveEntity(hid, hole, at))
			hl = append(hl, []any{hid})
			holes++
		}
		rgn := map[string]any{"id": oid, "outer": []any{oid}}
		if len(hl) > 0 {
			rgn["holes"] = hl
		}
		regs = append(regs, rgn)
	}
	sk := map[string]any{"plane": plane, "entities": ents, "regions": regs}
	if off != 0 {
		sk["offset"] = off
	}
	if err := features.ValidateSketch(features.Params(sk)); err != nil {
		return nil, features.Coded(features.CodeVectorizeEmpty, "raise tolerance or smooth, or crop the image", "the traced shapes do not form a valid sketch: %v", err)
	}
	depth := a.Num("depth")
	// what the trace promises: the critic compares the finished solid with this
	sk["trace"] = map[string]any{
		"source": src.name, "style": style, "area_mm2": round6(area), "regions": len(regions), "holes": holes,
		"bbox_mm": []any{round6(lo[0]), round6(lo[1]), round6(hi[0]), round6(hi[1])}, "px_per_mm": round6(px),
	}

	base := a.Str("id")
	if base == "" {
		base = strings.Trim(nonID.ReplaceAllString(strings.ToLower(a.Str("name")), "_"), "_")
	}
	if base == "" || base[0] < 'a' || base[0] > 'z' {
		base = "traced"
	}
	taken := map[string]bool{}
	solidID := derivedID(env, base, "", taken)
	skID := derivedID(env, base, "_sketch", taken)
	sub := Args{"intent": a.Str("intent")}
	skNode := newNode(env, sub, skID, "sketch", sk)
	skNode.Group = base
	ep := map[string]any{"sketch": skID, "mode": "add", "depth": depth}
	if a.Bool("symmetric") {
		ep["symmetric"] = true
	}
	exNode := newNode(env, a, solidID, "extrude", ep, skID)
	exNode.Group = base

	assumed := []string{}
	if !a.Has("threshold") && !a.Bool("adaptive") {
		assumed = append(assumed, fmt.Sprintf("ink threshold %d (automatic)", res.Stats.Threshold))
	}
	if !a.Has("x") && !a.Has("y") && !a.Bool("center") {
		assumed = append(assumed, "the drawing's lower-left corner is at the origin")
	}
	if !a.Has("plane") {
		assumed = append(assumed, "drawn on the XY plane, extruded +Z")
	}
	if !a.Has("min_area") {
		assumed = append(assumed, "specks under 12 px ignored")
	}
	assumedTxt := "none"
	if len(assumed) > 0 {
		assumedTxt = strings.Join(assumed, "; ")
	}
	exp := area * depth
	out := addPlan(intentOr(a, "Sketch to solid from "+src.name),
		fmt.Sprintf("Traces %s (%s × %s mm; %d shape(s), %d hole(s)) and extrudes it %s mm: expected volume ≈ %s mm³. Assumed: %s. Not inferred from the image: anything but the outline. Next: check_solid_vs_trace, then render_model to compare with the source.",
			src.name, fmtMM(hi[0]-lo[0]), fmtMM(hi[1]-lo[1]), len(regions), holes, fmtMM(depth), fmtMM(exp), assumedTxt), skNode, exNode)
	out.RequireApproval = true // the interpretation of a drawing is shown before it is committed
	for _, wmsg := range res.Warnings {
		out.Warnings = append(out.Warnings, wmsg)
	}
	return out, nil
}

func queryCheckSolidVsTrace(env Env, a Args) (any, error) {
	n, err := requireFeature(env, a.Str("solid"))
	if err != nil {
		return nil, err
	}
	if n.Type != "extrude" {
		return nil, fmt.Errorf("%q is a %s: pass the extrude created by sketch_to_solid", n.ID, n.Type)
	}
	sk := env.Graph().Get(fmt.Sprint(n.Parameters["sketch"]))
	if sk == nil {
		return nil, fmt.Errorf("%q has no sketch", n.ID)
	}
	tr, ok := sk.Parameters["trace"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%q was not made by sketch_to_solid (its sketch %q carries no trace record)", n.ID, sk.ID)
	}
	m, err := env.Model()
	if err != nil {
		return nil, err
	}
	r := m.Nodes[n.ID]
	if r == nil || r.Body == nil {
		reason := "it failed to build"
		if r != nil && len(r.Errors) > 0 {
			reason = r.Errors[0]
		}
		return nil, fmt.Errorf("%q has no geometry: %s", n.ID, reason)
	}
	tol := a.NumOr("tolerance", 0.01)
	area, _ := tr["area_mm2"].(float64)
	depth, _ := n.Parameters["depth"].(float64)
	want := area * depth
	p := r.Body.Props
	var checks []map[string]any
	ok = true
	add := func(name string, pass bool, f string, args ...any) {
		ok = ok && pass
		checks = append(checks, map[string]any{"check": name, "ok": pass, "detail": fmt.Sprintf(f, args...)})
	}
	rel := math.Abs(p.Volume-want) / math.Max(want, 1e-9)
	add("volume", rel <= tol, "solid %.2f mm³, traced area %.2f mm² × depth %.2f = %.2f mm³ (%.2f%% off, limit %.2f%%)", p.Volume, area, depth, want, rel*100, tol*100)
	if bb, ok2 := tr["bbox_mm"].([]any); ok2 && len(bb) == 4 && sk.Parameters["plane"] == nil || sk.Parameters["plane"] == "XY" {
		// the drawing's footprint on the XY plane (the common case) must match the solid's
		w, h := bb[2].(float64)-bb[0].(float64), bb[3].(float64)-bb[1].(float64)
		sw, sh := p.MaxX-p.MinX, p.MaxY-p.MinY
		okb := math.Abs(sw-w) <= math.Max(0.05, 0.005*w) && math.Abs(sh-h) <= math.Max(0.05, 0.005*h)
		add("footprint", okb, "solid %.2f × %.2f mm, drawing %.2f × %.2f mm", sw, sh, w, h)
	}
	add("single_valid_body", r.Body.Props.Solids >= 1, "%d solid(s)", r.Body.Props.Solids)
	return map[string]any{"solid": n.ID, "consistent": ok, "checks": checks, "trace": tr,
		"next": "if a check fails, the solid no longer matches the drawing: re-run sketch_to_solid or undo the edit. For shape fidelity compare render_model views with the source image."}, nil
}
