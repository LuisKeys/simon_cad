package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"sort"
	"strings"

	"github.com/accelone/simoncad/internal/cadir"
	"github.com/accelone/simoncad/internal/commands"
	"github.com/accelone/simoncad/internal/geometry/features"
	"github.com/accelone/simoncad/internal/recipe"
	"github.com/accelone/simoncad/internal/vectorize"
)

func stampTools() []*Tool {
	traceProps := func(p props) props {
		p["image_id"] = text("Design from an attached image (PNG/JPEG)")
		p["path"] = text("Design from a PNG/JPEG/SVG file on disk")
		p["svg"] = text("Design from inline SVG markup")
		p["sketch"] = text("Design from an existing sketch (its regions are raised as they are; it must lie on the stamp's top face)")
		p["mode"] = enum("filled_regions raises the drawn areas (logos, silhouettes, lettering); centerline raises ridges of stroke_width along the strokes (line drawings)", "filled_regions", "centerline")
		p["threshold"] = integer("Ink level 1..255 separating the design (default automatic)", 1, 255)
		p["adaptive"] = boolean("Adaptive threshold for uneven lighting")
		p["invert"] = boolean("The design is light on a dark background")
		p["min_area"] = integer("Ignore specks below this many pixels", 1, 100000)
		p["smooth"] = number("Smoothing in pixels, 0..1.5")
		p["tolerance"] = number("Curve fitting tolerance in pixels")
		return p
	}
	return []*Tool{
		{
			Name: "create_stamp",
			Description: "Make a printable stamp from an image, SVG or sketch: a plate (rectangle or circle) with the design raised on top, optional handle, rounded edges and shrinkage compensation, " +
				"all as editable nodes grouped under one id (the final body has that id). The size of the stamp is required (width+height, or diameter); the design is scaled to fit the plate minus `margin`. " +
				"`mirror` (default true) mirrors the relief so the IMPRESSION reads correctly: for lettering keep it on. `shrinkage_percent` is the expected shrinkage of the clay/porcelain (ask the user; none is assumed): the model is scaled up by 1/(1-s). " +
				"Check the result with stamp_impression (what the stamp prints) and validate_printability. Defaults that were assumed (thickness, relief height, margin) are listed in the answer.",
			Capability: capGeometry,
			Schema: object(withMeta(traceProps(props{
				"shape":             enum("Plate shape (default rectangle)", "rectangle", "circle"),
				"width":             positive("Plate width in mm (rectangle)"),
				"height":            positive("Plate height in mm (rectangle)"),
				"diameter":          positive("Plate diameter in mm (circle)"),
				"base_thickness":    positive("Plate thickness in mm (default 4)"),
				"relief_height":     positive("Height of the raised design in mm (default 1.5)"),
				"stroke_width":      positive("Ridge width in mm for centerline designs (default 1.5)"),
				"margin":            number("Free border between the design and the plate edge in mm (default 3)"),
				"design_width_mm":   positive("Real width of the design in mm (default: fit the plate)"),
				"mirror":            boolean("Mirror the relief so the impression reads correctly (default true)"),
				"rounded_edges":     boolean("Round the plate edges"),
				"edge_radius":       positive("Edge radius in mm (default ~1)"),
				"handle_enabled":    boolean("Add a cylindrical handle on the back"),
				"handle_height":     positive("Handle height in mm (default 12)"),
				"handle_diameter":   positive("Handle diameter in mm (default 40% of the plate)"),
				"shrinkage_percent": number("Expected material shrinkage in % (0-40). The stamp is scaled up to compensate. Ask the user: no value is assumed."),
			}))),
			Plan: planCreateStamp,
		},
		{
			Name: "apply_recipe",
			Description: "Create geometry from a declarative recipe (JSON). Supported type: stamp {base:{shape,width,height|diameter,thickness,rounded_edges,edge_radius}, " +
				"design:{source:{image_id|path|svg|sketch},mode,relief_height,stroke_width,margin,width_mm,trace}, mirror, handle:{enabled,height,diameter}, shrinkage_percent}. " +
				"Deterministic: the same recipe gives the same nodes. The recipe is stored as a node (get_recipe / update_recipe) so it can be edited and reused.",
			Capability: capGeometry,
			Schema: object(withMeta(props{
				"recipe": map[string]any{"type": "object", "description": "The recipe object"},
			}), "recipe"),
			Plan: planApplyRecipe,
		},
		{
			Name: "update_recipe",
			Description: "Edit a stored recipe with a JSON merge patch (`changes`: objects merge, null removes a field) and regenerate its geometry: nodes whose settings changed are edited in place, " +
				"structural changes (handle on/off, rounded edges, a new design) add or remove nodes. One undo step. Example: {\"design\":{\"relief_height\":2},\"mirror\":false}.",
			Capability: capGeometry,
			Schema: object(props{
				"recipe":  text("Recipe node id (<stamp id>_recipe) or the stamp id"),
				"changes": map[string]any{"type": "object", "description": "JSON merge patch applied to the recipe"},
				"intent":  text("Why"),
			}, "recipe", "changes"),
			Plan: planUpdateRecipe,
		},
		{
			Name:        "get_recipe",
			Description: "Read a stored recipe (canonical JSON) to reuse or edit it.",
			Capability:  capGeometry,
			Schema:      object(props{"recipe": text("Recipe node id (<stamp id>_recipe) or the stamp id")}, "recipe"),
			Query:       queryGetRecipe,
		},
	}
}

// ---- recipe nodes ----------------------------------------------------------------------------

func findRecipeNode(env Env, id string) (*cadir.Node, error) {
	g := env.Graph()
	if n := g.Get(id); n != nil && n.Type == features.RecipeType {
		return n, nil
	}
	if n := g.Get(id + "_recipe"); n != nil && n.Type == features.RecipeType {
		return n, nil
	}
	return nil, fmt.Errorf("no recipe %q (recipes: %v)", id, nodesOfType(env, features.RecipeType))
}

func recipeBaseID(n *cadir.Node) string { return strings.TrimSuffix(n.ID, "_recipe") }

func queryGetRecipe(env Env, a Args) (any, error) {
	n, err := findRecipeNode(env, a.Str("recipe"))
	if err != nil {
		return nil, err
	}
	return map[string]any{"recipe_id": n.ID, "stamp": recipeBaseID(n), "recipe": n.Parameters}, nil
}

// ---- tool plans --------------------------------------------------------------------------------

func planCreateStamp(env Env, a Args) (*Plan, error) {
	r := map[string]any{"type": recipe.TypeStamp}
	base := map[string]any{"shape": orDefault(a.Str("shape"), "rectangle")}
	for from, to := range map[string]string{"width": "width", "height": "height", "diameter": "diameter", "base_thickness": "thickness", "edge_radius": "edge_radius"} {
		if a.Has(from) {
			base[to] = a.Num(from)
		}
	}
	if a.Bool("rounded_edges") {
		base["rounded_edges"] = true
	}
	design := map[string]any{"mode": orDefault(a.Str("mode"), recipe.ModeFilled)}
	src := map[string]any{}
	for _, k := range []string{"image_id", "path", "svg", "sketch"} {
		if a.Has(k) {
			src[k] = a.Str(k)
		}
	}
	design["source"] = src
	for from, to := range map[string]string{"relief_height": "relief_height", "stroke_width": "stroke_width", "margin": "margin", "design_width_mm": "width_mm"} {
		if a.Has(from) {
			design[to] = a.Num(from)
		}
	}
	trace := map[string]any{}
	for _, k := range []string{"threshold", "adaptive", "invert", "min_area", "smooth", "tolerance"} {
		if a.Has(k) {
			trace[k] = a[k]
		}
	}
	if len(trace) > 0 {
		design["trace"] = trace
	}
	r["base"], r["design"] = base, design
	r["mirror"] = true
	if a.Has("mirror") {
		r["mirror"] = a.Bool("mirror")
	}
	if a.Bool("handle_enabled") {
		h := map[string]any{"enabled": true}
		if a.Has("handle_height") {
			h["height"] = a.Num("handle_height")
		}
		if a.Has("handle_diameter") {
			h["diameter"] = a.Num("handle_diameter")
		}
		r["handle"] = h
	}
	if a.Has("shrinkage_percent") {
		r["shrinkage_percent"] = a.Num("shrinkage_percent")
	}
	return planStampRecipe(env, a, r)
}

func planApplyRecipe(env Env, a Args) (*Plan, error) {
	r, ok := a["recipe"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("recipe must be an object")
	}
	return planStampRecipe(env, a, r)
}

func planStampRecipe(env Env, a Args, r map[string]any) (*Plan, error) {
	st, assumed, err := recipe.ParseStamp(r)
	if err != nil {
		return nil, features.Coded(features.CodeRecipeInvalid, "see the recipe schema in the tool description", "%v", err)
	}
	id, err := uniqueID(env.Graph(), a, "stamp")
	if err != nil {
		return nil, err
	}
	if env.Graph().Get(id+"_recipe") != nil {
		return nil, fmt.Errorf("a recipe %q_recipe already exists: use update_recipe to change it", id)
	}
	gen, err := generateStamp(env, st, id, nil)
	if err != nil {
		return nil, err
	}
	notes := append([]string{}, gen.notes...)
	if len(assumed) > 0 {
		notes = append(notes, "assumed: "+strings.Join(assumed, ", ")+" (change them with update_recipe)")
	}
	out := addPlan(intentOr(a, "Create stamp "+id), "Creates a stamp: "+gen.summary+"."+noteSuffix(notes), gen.nodes...)
	out.Warnings = gen.warnings
	out.LowRisk = len(gen.warnings) == 0
	return out, nil
}

func noteSuffix(notes []string) string {
	if len(notes) == 0 {
		return ""
	}
	return " " + strings.Join(notes, "; ") + "."
}

func planUpdateRecipe(env Env, a Args) (*Plan, error) {
	rn, err := findRecipeNode(env, a.Str("recipe"))
	if err != nil {
		return nil, err
	}
	patch, ok := a["changes"].(map[string]any)
	if !ok || len(patch) == 0 {
		return nil, fmt.Errorf("changes must be a non-empty object (a JSON merge patch)")
	}
	merged := recipe.Merge(rn.Parameters, patch)
	st, _, err := recipe.ParseStamp(merged)
	if err != nil {
		return nil, features.Coded(features.CodeRecipeInvalid, "the merged recipe must stay valid", "%v", err)
	}
	id := recipeBaseID(rn)
	old := groupNodes(env.Graph(), id)
	gen, err := generateStamp(env, st, id, old)
	if err != nil {
		return nil, err
	}
	cmds, err := diffNodes(env.Graph(), old, gen.nodes)
	if err != nil {
		return nil, err
	}
	if len(cmds) == 0 {
		return nil, fmt.Errorf("the changes leave the stamp unchanged")
	}
	out := &Plan{Intent: intentOr(a, "Update stamp "+id), Explanation: "Regenerates stamp " + id + ": " + gen.summary + "." + noteSuffix(gen.notes), Commands: cmds, Warnings: gen.warnings}
	return out, nil
}

func groupNodes(g *cadir.Graph, group string) []*cadir.Node {
	var out []*cadir.Node
	for _, n := range g.Nodes {
		if n.Group == group {
			out = append(out, n)
		}
	}
	return out
}

// diffNodes turns "these are the nodes the recipe now describes" into the smallest set of
// commands over the nodes it described before: parameters of unchanged nodes are edited in place;
// a node whose type or inputs changed (and everything built on it) is removed and added again.
func diffNodes(g *cadir.Graph, old, fresh []*cadir.Node) ([]commands.Command, error) {
	oldBy := map[string]*cadir.Node{}
	for _, n := range old {
		oldBy[n.ID] = n
	}
	freshBy := map[string]*cadir.Node{}
	for _, n := range fresh {
		freshBy[n.ID] = n
	}
	stale := map[string]bool{} // old nodes to remove
	redo := map[string]bool{}  // fresh nodes to add
	for _, n := range old {
		if freshBy[n.ID] == nil {
			stale[n.ID] = true
		}
	}
	for _, n := range fresh { // fresh is in dependency order
		o := oldBy[n.ID]
		if o == nil {
			redo[n.ID] = true
			continue
		}
		changed := o.Type != n.Type || !reflect.DeepEqual(o.Dependencies, n.Dependencies)
		for _, d := range n.Dependencies {
			changed = changed || redo[d]
		}
		if changed {
			stale[n.ID], redo[n.ID] = true, true
		}
	}
	var cmds []commands.Command
	// remove stale nodes, dependents first
	var order []string
	for i := len(old) - 1; i >= 0; i-- {
		if stale[old[i].ID] {
			order = append(order, old[i].ID)
		}
	}
	inGroup := map[string]bool{}
	for _, n := range old {
		inGroup[n.ID] = true
	}
	for _, id := range order {
		for _, d := range g.Dependents(id) {
			if !inGroup[d] {
				return nil, fmt.Errorf("%s cannot be replaced: %s depends on it (remove or re-point that first)", id, d)
			}
		}
		cmds = append(cmds, &commands.RemoveFeature{ID: id})
	}
	for _, n := range fresh {
		if redo[n.ID] {
			if o := oldBy[n.ID]; o != nil { // keep the look of a node that is rebuilt
				n.Appearance = o.Appearance.Clone()
			}
			cmds = append(cmds, &commands.AddFeature{Node: n, Index: -1})
			continue
		}
		o := oldBy[n.ID]
		keys := map[string]bool{}
		for k := range o.Parameters {
			keys[k] = true
		}
		for k := range n.Parameters {
			keys[k] = true
		}
		var names []string
		for k := range keys {
			names = append(names, k)
		}
		sort.Strings(names)
		for _, k := range names {
			ov, ook := o.Parameters[k]
			nv, nok := n.Parameters[k]
			switch {
			case !nok:
				cmds = append(cmds, &commands.SetParameter{ID: n.ID, Name: k, Delete: true})
			case !ook || !reflect.DeepEqual(normalizeJSON(ov), normalizeJSON(nv)):
				cmds = append(cmds, &commands.SetParameter{ID: n.ID, Name: k, Value: nv})
			}
		}
	}
	return cmds, nil
}

// ---- generation --------------------------------------------------------------------------------

type stampGen struct {
	nodes    []*cadir.Node
	summary  string
	notes    []string
	warnings []string
}

func generateStamp(env Env, st *recipe.Stamp, id string, existing []*cadir.Node) (*stampGen, error) {
	g := &stampGen{}
	exists := map[string]bool{}
	for _, n := range existing {
		exists[n.ID] = true
	}
	// every id this recipe may produce must be free (or already belong to it)
	check := func(nid string) error {
		if !exists[nid] && env.Graph().Get(nid) != nil {
			return fmt.Errorf("feature %q already exists: pick another stamp id", nid)
		}
		return nil
	}
	sub := Args{"intent": "stamp " + id}
	add := func(typ, nid string, params map[string]any, deps ...string) (*cadir.Node, error) {
		if err := check(nid); err != nil {
			return nil, err
		}
		n := newNode(env, sub, nid, typ, params, deps...)
		n.Group = id
		g.nodes = append(g.nodes, n)
		return n, nil
	}
	T := st.Base.Thickness
	W, H := st.Base.Width, st.Base.Height
	var baseEnt map[string]any
	if st.Base.Shape == "circle" {
		baseEnt = map[string]any{"id": "plate", "kind": "circle", "cx": 0.0, "cy": 0.0, "r": st.Base.Diameter / 2}
	} else {
		baseEnt = map[string]any{"id": "plate", "kind": "rectangle", "x": -W / 2, "y": -H / 2, "w": W, "h": H}
	}
	// which node is the final body decides which one carries the bare id
	last := "relief"
	if st.Handle.Enabled {
		last = "handle"
	}
	if st.Shrinkage > 0 {
		last = "scaled"
	}
	nidOf := func(kind string) string {
		if kind == last {
			return id
		}
		return id + "_" + kind
	}
	if _, err := add("sketch", id+"_base_sketch", map[string]any{"plane": "XY", "entities": []any{baseEnt}}); err != nil {
		return nil, err
	}
	if _, err := add("extrude", id+"_base", map[string]any{"sketch": id + "_base_sketch", "depth": T}, id+"_base_sketch"); err != nil {
		return nil, err
	}
	cur := id + "_base"
	if st.Base.RoundedEdges {
		if _, err := add("fillet", id+"_base_round", map[string]any{"target": cur, "radius": st.Base.EdgeRadius}, cur); err != nil {
			return nil, err
		}
		cur = id + "_base_round"
	}

	// ---- the design
	designSketch := st.Design.Source.Sketch
	var x0, y0 float64
	if designSketch == "" {
		src, err := traceSourceFromRecipe(env, st.Design.Source)
		if err != nil {
			return nil, err
		}
		wPx, hPx := src.size()
		if wPx <= 0 || hPx <= 0 {
			return nil, fmt.Errorf("the design has no size (an SVG needs a viewBox or width and height)")
		}
		availW, availH := W-2*st.Design.Margin, H-2*st.Design.Margin
		if st.Base.Shape == "circle" {
			side := (st.Base.Diameter - 2*st.Design.Margin) / math.Sqrt2 // the square inscribed in the circle
			availW, availH = side, side
		}
		aspect := float64(hPx) / float64(wPx)
		wmm := math.Min(availW, availH/aspect)
		if st.Design.WidthMM > 0 {
			wmm = st.Design.WidthMM
			if wmm > availW+1e-9 || wmm*aspect > availH+1e-9 {
				g.warnings = append(g.warnings, fmt.Sprintf("DESIGN_OVERFLOWS: the design (%s × %s mm) is larger than the free area (%s × %s mm) of the plate",
					fmtMM(wmm), fmtMM(wmm*aspect), fmtMM(availW), fmtMM(availH)))
			}
		}
		px := float64(wPx) / wmm
		opt := vectorize.Options{Mode: vectorize.FilledRegions, PxPerMM: px}
		if st.Design.Mode == recipe.ModeCenterline {
			opt.Mode = vectorize.Centerline
		}
		if t := st.Design.Trace; t != nil {
			opt.Threshold, opt.Adaptive, opt.Invert, opt.MinArea, opt.Smooth, opt.Tol = t.Threshold, t.Adaptive, t.Invert, t.MinArea, t.Smooth, t.Tolerance
		}
		res, err := runTrace(src, opt)
		if err != nil {
			return nil, err
		}
		x0, y0 = -float64(res.WidthPx)*res.Scale/2, -float64(res.HeightPx)*res.Scale/2
		for _, wmsg := range res.Warnings {
			g.warnings = append(g.warnings, wmsg)
		}
		switch opt.Mode {
		case vectorize.FilledRegions:
			if len(res.Regions) == 0 {
				return nil, features.Coded(features.CodeVectorizeEmpty, "check the threshold and invert settings with vectorize_image", "no design was found in %s", src.name)
			}
			if len(res.Regions) > maxTracedLoops {
				return nil, fmt.Errorf("%d regions found: raise min_area or tolerance", len(res.Regions))
			}
			var ents, regions []any
			plain := func(p vectorize.Pt) (float64, float64) { return x0 + p[0], y0 + p[1] }
			for i, rg := range res.Regions {
				oid := fmt.Sprintf("r%d", i+1)
				ents = append(ents, curveEntity(oid, rg.Outer, plain))
				var holes []any
				for k, hole := range rg.Holes {
					hid := fmt.Sprintf("r%d_h%d", i+1, k+1)
					ents = append(ents, curveEntity(hid, hole, plain))
					holes = append(holes, []any{hid})
				}
				rgn := map[string]any{"id": oid, "outer": []any{oid}}
				if len(holes) > 0 {
					rgn["holes"] = holes
				}
				regions = append(regions, rgn)
			}
			sk := map[string]any{"plane": "XY", "offset": T, "entities": ents, "regions": regions}
			if err := features.ValidateSketch(features.Params(sk)); err != nil {
				return nil, features.Coded(features.CodeVectorizeEmpty, "raise tolerance or smooth", "the traced design is not a valid sketch: %v", err)
			}
			designSketch = id + "_design"
			if _, err := add("sketch", designSketch, sk); err != nil {
				return nil, err
			}
			g.notes = append(g.notes, fmt.Sprintf("design %s × %s mm from %s (%d region(s))", fmtMM(float64(res.WidthPx)*res.Scale), fmtMM(float64(res.HeightPx)*res.Scale), src.name, len(res.Regions)))
		default:
			if len(res.Strokes) == 0 {
				return nil, features.Coded(features.CodeVectorizeEmpty, "check the threshold and invert settings", "no strokes were found in %s", src.name)
			}
			if len(res.Strokes) > maxTracedShapes {
				return nil, fmt.Errorf("%d strokes found: too many (raise min_area or use close_gaps)", len(res.Strokes))
			}
			// the ridges are the area a pen of stroke_width leaves along the strokes: drawn in 2D
			// and traced back, so crossings merge cleanly and the relief is one extrusion
			rr, err := vectorize.StrokesToRegions(context.Background(), res.Strokes, st.Design.StrokeWidth, vectorize.Options{})
			if err != nil {
				return nil, err
			}
			if len(rr.Regions) == 0 {
				return nil, features.Coded(features.CodeVectorizeEmpty, "use a smaller stroke_width or a larger design", "the strokes leave no ridge at %s mm", fmtMM(st.Design.StrokeWidth))
			}
			var ents, regions []any
			plain := func(p vectorize.Pt) (float64, float64) { return x0 + p[0], y0 + p[1] }
			for i, rg := range rr.Regions {
				oid := fmt.Sprintf("r%d", i+1)
				ents = append(ents, curveEntity(oid, rg.Outer, plain))
				var holes []any
				for k, hole := range rg.Holes {
					hid := fmt.Sprintf("r%d_h%d", i+1, k+1)
					ents = append(ents, curveEntity(hid, hole, plain))
					holes = append(holes, []any{hid})
				}
				rgn := map[string]any{"id": oid, "outer": []any{oid}}
				if len(holes) > 0 {
					rgn["holes"] = holes
				}
				regions = append(regions, rgn)
			}
			sk := map[string]any{"plane": "XY", "offset": T, "entities": ents, "regions": regions}
			if err := features.ValidateSketch(features.Params(sk)); err != nil {
				return nil, features.Coded(features.CodeVectorizeEmpty, "raise tolerance or smooth", "the ridges do not form a valid sketch: %v", err)
			}
			designSketch = id + "_design"
			if _, err := add("sketch", designSketch, sk); err != nil {
				return nil, err
			}
			g.notes = append(g.notes, fmt.Sprintf("%d stroke(s) from %s raised as %s mm ridges (%d region(s))", len(res.Strokes), src.name, fmtMM(st.Design.StrokeWidth), len(rr.Regions)))
		}
	} else {
		if sk := env.Graph().Get(designSketch); sk == nil || sk.Type != "sketch" {
			return nil, fmt.Errorf("design.source.sketch %q is not a sketch", designSketch)
		}
		g.notes = append(g.notes, "the existing sketch "+designSketch+" is raised as it is (it must lie on the top face at z = "+fmtMM(T)+" mm)")
	}

	rp := map[string]any{"source": designSketch, "target": cur, "op": "emboss", "depth": st.Design.ReliefHeight}
	if st.Mirror {
		rp["mirror"] = true
	}
	if _, err := add("relief", nidOf("relief"), rp, designSketch, cur); err != nil {
		return nil, err
	}
	cur = nidOf("relief")

	if st.Handle.Enabled {
		hs := id + "_handle_sketch"
		if _, err := add("sketch", hs, map[string]any{"plane": "XY", "entities": []any{map[string]any{"id": "grip", "kind": "circle", "cx": 0.0, "cy": 0.0, "r": st.Handle.Diameter / 2}}}); err != nil {
			return nil, err
		}
		// from 0.5 mm inside the plate down by the handle height, so it overlaps the plate
		hp := map[string]any{"sketch": hs, "target": cur, "mode": "add", "depth": st.Handle.Height + 0.5, "offset": -0.5, "reverse": true}
		if _, err := add("extrude", nidOf("handle"), hp, hs, cur); err != nil {
			return nil, err
		}
		cur = nidOf("handle")
	}
	if st.Shrinkage > 0 {
		f := round6(st.ScaleFactor())
		if _, err := add("scale", nidOf("scaled"), map[string]any{"target": cur, "factor": f, "about": "origin"}, cur); err != nil {
			return nil, err
		}
		g.notes = append(g.notes, fmt.Sprintf("scaled by %g to compensate %g%% shrinkage", f, st.Shrinkage))
		cur = nidOf("scaled")
	}
	if _, err := add(features.RecipeType, id+"_recipe", st.Map()); err != nil {
		return nil, err
	}
	// the recipe node carries no body and depends on nothing: it only remembers the settings
	shape := fmt.Sprintf("%s × %s mm", fmtMM(W), fmtMM(H))
	if st.Base.Shape == "circle" {
		shape = "Ø" + fmtMM(st.Base.Diameter) + " mm"
	}
	mir := "mirrored"
	if !st.Mirror {
		mir = "not mirrored"
	}
	g.summary = fmt.Sprintf("%s plate %s thick, relief %s mm, %s", shape, fmtMM(T), fmtMM(st.Design.ReliefHeight), mir)
	_ = json.Marshal
	return g, nil
}

func traceSourceFromRecipe(env Env, s recipe.Source) (*traceSource, error) {
	a := Args{}
	switch {
	case s.ImageID != "":
		a["image_id"] = s.ImageID
	case s.Path != "":
		a["path"] = s.Path
	case s.SVG != "":
		a["svg"] = s.SVG
	}
	return loadTraceSource(env, a)
}
