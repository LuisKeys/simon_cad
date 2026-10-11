package tools

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"

	"github.com/accelone/simoncad/internal/cadir"
	"github.com/accelone/simoncad/internal/geometry/curve"
	"github.com/accelone/simoncad/internal/geometry/features"
	txt "github.com/accelone/simoncad/internal/geometry/text"
	"github.com/accelone/simoncad/internal/selectors"
)

const maxTextGlyphs = 400

func textTools() []*Tool {
	return []*Tool{{
		Name: "create_text_geometry",
		Description: "Create 3D lettering. Builds a sketch of the text (letters with their counters: the hole of an 'o', 'A', 'B' is kept) from a TrueType/OpenType font, then optionally extrudes it, " +
			"embosses it onto a body or engraves it into one. mode=sketch (default) only creates the sketch; extrude makes a free solid of height `depth`; emboss raises it on `target`; engrave cuts it into `target`. " +
			"`size` is the em height in mm (capitals are about 70% of it) and is required: do not guess a size, ask. `mirror` flips the lettering left-right (for stamps: the impression then reads correctly). " +
			"The text sits on `plane` (XY default, with `offset`) or on a planar `face` (e.g. \"body.face[top]\"), starting at (x, y) on the baseline, or centred there with center=true. " +
			"With `path` (an existing path feature lying in that plane) the baseline follows the path from `start` mm along it. `font` is a .ttf/.otf file path (default: the built-in sans). Reports characters the font lacks.",
		Capability: capGeometry,
		Schema: object(withMeta(props{
			"text":         text("The text; \\n starts a new line"),
			"size":         positive("Em height in mm"),
			"mode":         enum("sketch (default), extrude, emboss or engrave", "sketch", "extrude", "emboss", "engrave"),
			"depth":        positive("Height (extrude, emboss) or cut depth (engrave) in mm"),
			"target":       text("Body to emboss onto / engrave into"),
			"font":         text("Path to a .ttf/.otf file (default: built-in)"),
			"spacing":      number("Extra space between letters in mm"),
			"line_spacing": positive("Line height as a multiple of size (default 1.2)"),
			"align":        enum("Line alignment (default left)", "left", "center", "right"),
			"mirror":       boolean("Mirror left-right (lettering for stamps)"),
			"kerning":      boolean("Apply the font's kerning (default true)"),
			"plane":        enum("Sketch plane (default XY)", "XY", "XZ", "YZ"),
			"offset":       number("Plane offset along its normal (mm)"),
			"face":         text("Planar face selector to put the text on, instead of a plane"),
			"x":            number("Text origin u (mm, default 0)"),
			"y":            number("Text origin v (mm, default 0)"),
			"center":       boolean("Centre the text's bounding box on (x, y)"),
			"path":         text("Path feature the baseline follows"),
			"start":        number("Distance along the path where the text starts (mm, default 0)"),
		}), "text", "size"),
		Plan: planCreateText,
	}}
}

// loadFont resolves the `font` argument. A user font is a file the agent or user names.
func loadFont(name string) (*txt.Font, error) {
	if name == "" || name == "builtin" {
		return txt.Builtin(), nil
	}
	switch strings.ToLower(filepath.Ext(name)) {
	case ".ttf", ".otf":
	default:
		return nil, features.Coded(features.CodeFontNotFound, "use a .ttf/.otf file or omit `font` for the built-in one", "font %q is not a .ttf/.otf file", name)
	}
	if _, err := os.Stat(name); err != nil {
		return nil, features.Coded(features.CodeFontNotFound, "give the path of an installed .ttf/.otf file, or omit `font` for the built-in one", "font file %q not found", name)
	}
	f, err := txt.Load(name)
	if err != nil {
		return nil, features.Coded(features.CodeFontNotFound, "the file must be a valid TrueType/OpenType font", "%v", err)
	}
	return f, nil
}

// planeCoords is the inverse of planePoint: world → sketch-plane (u, v).
func planeCoords(plane string, p curve.Vec) (u, v float64) {
	switch plane {
	case "XZ":
		return p[2], p[0]
	case "YZ":
		return p[1], p[2]
	}
	return p[0], p[1]
}

func planCreateText(env Env, a Args) (*Plan, error) {
	s := a.Str("text")
	if strings.TrimSpace(s) == "" {
		return nil, fmt.Errorf("text is empty")
	}
	if n := len([]rune(s)); n > maxTextGlyphs {
		return nil, fmt.Errorf("text has %d characters; the limit is %d", n, maxTextGlyphs)
	}
	mode := orDefault(a.Str("mode"), "sketch")
	if mode != "sketch" && !a.Has("depth") {
		return nil, fmt.Errorf("mode %s needs `depth`", mode)
	}
	if (mode == "emboss" || mode == "engrave") && a.Str("target") == "" {
		return nil, fmt.Errorf("mode %s needs a `target` body", mode)
	}
	if a.Has("target") {
		if _, err := requireBounds(env, a.Str("target")); err != nil {
			return nil, err
		}
	}
	if a.Has("face") && a.Has("plane") {
		return nil, fmt.Errorf("give either `plane` or `face`, not both")
	}
	f, err := loadFont(a.Str("font"))
	if err != nil {
		return nil, err
	}
	lay, err := f.Lay(s, txt.Options{
		Size: a.Num("size"), Spacing: a.Num("spacing"), LineSpacing: a.Num("line_spacing"),
		Align: a.Str("align"), Mirror: a.Bool("mirror"), NoKerning: a.Has("kerning") && !a.Bool("kerning"),
	})
	if err != nil {
		return nil, err
	}
	plane := orDefault(a.Str("plane"), "XY")
	regs := lay.Regions
	x0, y0 := a.Num("x"), a.Num("y")
	var warns []string
	if len(lay.Missing) > 0 {
		warns = append(warns, fmt.Sprintf("the font has no glyph for %q (drawn as nothing)", string(lay.Missing)))
	}
	if a.Has("path") {
		pn, err := requirePath(env, a.Str("path"))
		if err != nil {
			return nil, err
		}
		pa, err := features.PathFromParams(features.Params(pn.Parameters))
		if err != nil {
			return nil, err
		}
		cv, err := curve.New(pa)
		if err != nil {
			return nil, err
		}
		L := cv.Length()
		start := a.Num("start")
		if !cv.Closed && start+lay.Width > L+1e-6 {
			return nil, fmt.Errorf("the text is %s mm long but only %s mm of the path remain after start=%s: shorten it, lower size or lengthen the path",
				fmtMM(lay.Width), fmtMM(math.Max(0, L-start)), fmtMM(start))
		}
		var tooBent bool
		regs = txt.Transform(regs, func(p txt.Pt) txt.Pt {
			sArc := start + p[0]
			if cv.Closed {
				sArc = math.Mod(math.Mod(sArc, L)+L, L)
			}
			e := cv.At(sArc / L)
			pu, pv := planeCoords(plane, e.P)
			tu, tv := planeCoords(plane, e.T)
			tl := math.Hypot(tu, tv)
			if tl < 1e-9 {
				tooBent = true
				return txt.Pt{pu, pv}
			}
			tu, tv = tu/tl, tv/tl
			return txt.Pt{pu - tv*p[1], pv + tu*p[1]} // the left normal of the tangent is "up"
		})
		if tooBent {
			return nil, fmt.Errorf("the path runs along the plane normal: draw it in the %s plane", plane)
		}
		warns = append(warns, "text follows the path by moving the outline's control points: tight curves distort the letters (use a smaller size or a gentler path)")
	} else {
		dx, dy := x0, y0
		if a.Bool("center") {
			b := txt.Bounds(regs)
			dx -= (b[0] + b[2]) / 2
			dy -= (b[1] + b[3]) / 2
		}
		regs = txt.Transform(regs, func(p txt.Pt) txt.Pt { return txt.Pt{p[0] + dx, p[1] + dy} })
	}

	var ents, regions []any
	for i, rg := range regs {
		oid := fmt.Sprintf("g%d", i+1)
		ents = append(ents, textCurve(oid, rg.Outer))
		rgn := map[string]any{"id": oid, "outer": []any{oid}}
		var holes []any
		for k, h := range rg.Holes {
			hid := fmt.Sprintf("g%d_h%d", i+1, k+1)
			ents = append(ents, textCurve(hid, h))
			holes = append(holes, []any{hid})
		}
		if len(holes) > 0 {
			rgn["holes"] = holes
		}
		regions = append(regions, rgn)
	}
	if len(regions) > maxTracedLoops {
		return nil, fmt.Errorf("the text has %d shapes: too many for one sketch (shorten it)", len(regions))
	}
	sk := map[string]any{"entities": ents, "regions": regions}
	if !a.Has("face") {
		sk["plane"] = plane
		if a.Has("offset") {
			sk["offset"] = a.Num("offset")
		}
	}
	if err := features.ValidateSketch(features.Params(sk)); err != nil {
		return nil, fmt.Errorf("the lettering does not form a valid sketch: %v", err)
	}

	base := a.Str("id")
	if base == "" {
		base = strings.Trim(nonID.ReplaceAllString(strings.ToLower(s), "_"), "_")
		if len(base) > 24 {
			base = strings.Trim(base[:24], "_")
		}
		if base == "" || base[0] < 'a' || base[0] > 'z' {
			base = "text"
		} else {
			base = "text_" + base
		}
	}
	taken := map[string]bool{}
	skID := base
	if mode != "sketch" {
		skID = derivedID(env, base, "_sketch", taken)
	} else {
		skID = derivedID(env, base, "", taken)
	}
	sub := Args{"intent": a.Str("intent")}
	skNode := newNode(env, sub, skID, "sketch", sk)
	if f := a.Str("face"); f != "" {
		sel, err := selectors.Parse(f)
		if err != nil {
			return nil, err
		}
		if sel.Kind != "face" {
			return nil, fmt.Errorf("face must be a face selector, got %q", f)
		}
		d, err := resolveSelector(env, sel)
		if err != nil {
			return nil, err
		}
		if d.SurfaceType != "plane" {
			return nil, fmt.Errorf("%s is not a planar face (%s)", f, d.SurfaceType)
		}
		skNode.References = map[string]string{"face": sel.String()}
		skNode.Dependencies = []string{sel.Feature()}
	}
	nodes := []*cadir.Node{skNode}
	b := txt.Bounds(regs)
	desc := fmt.Sprintf("%s × %s mm of lettering, %d shape(s)", fmtMM(b[2]-b[0]), fmtMM(b[3]-b[1]), len(regions))
	switch mode {
	case "extrude":
		id, err := uniqueID(env.Graph(), a, "extrude")
		if err != nil {
			return nil, err
		}
		if a.Str("id") != "" {
			id = a.Str("id")
		}
		nodes = append(nodes, newNode(env, a, id, "extrude", map[string]any{"sketch": skID, "mode": "add", "depth": a.Num("depth")}, skID))
	case "emboss", "engrave":
		id := derivedID(env, base, "_"+mode, taken)
		if a.Str("id") != "" {
			id = a.Str("id")
			skNode.ID = derivedID(env, id, "_sketch", taken)
			skID = skNode.ID
		}
		rp := map[string]any{"source": skID, "target": a.Str("target"), "op": mode, "depth": a.Num("depth")}
		nodes = append(nodes, newNode(env, a, id, features.ReliefType, rp, skID, a.Str("target")))
	}
	out := addPlan(intentOr(a, fmt.Sprintf("Text %q", s)), fmt.Sprintf("Creates %s (%s).", desc, mode), nodes...)
	out.Warnings = warns
	out.LowRisk = len(warns) == 0
	return out, nil
}

func textCurve(id string, segs []txt.Seg) map[string]any {
	q := func(p txt.Pt) []any { return []any{round6(p[0]), round6(p[1])} }
	ss := make([]any, len(segs))
	for i, s := range segs {
		ss[i] = []any{q(s.P0), q(s.C1), q(s.C2), q(s.P1)}
	}
	return map[string]any{"id": id, "kind": "curve", "segments": ss}
}
