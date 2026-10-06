package tools

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/accelone/simoncad/internal/cadir"
	"github.com/accelone/simoncad/internal/commands"
	"github.com/accelone/simoncad/internal/selectors"
	"github.com/accelone/simoncad/internal/vision"
)

// CapVision tags tools of the Vision capability.
const CapVision = "vision"

func unit(desc string) map[string]any {
	return map[string]any{"type": "number", "description": desc, "minimum": 0.0, "maximum": 1.0}
}

func imageEntitySchema() map[string]any {
	s := object(props{
		"id":   text("Entity id (e1, e2, ...), unique"),
		"kind": enum("Entity kind", "line", "arc", "circle", "rectangle", "polygon", "slot"),
		"p1":   array("line/arc start [x,y] in image pixels", number("px"), 2, 2),
		"p2":   array("line/arc end [x,y] in image pixels", number("px"), 2, 2),
		"mid":  array("arc: any point on the arc between p1 and p2 [x,y]", number("px"), 2, 2),
		"cx":   number("circle center x (px)"), "cy": number("circle center y (px)"), "r": positive("circle radius (px)"),
		"x": number("rectangle top-left x (px)"), "y": number("rectangle top-left y (px)"),
		"w": positive("rectangle width (px)"), "h": positive("rectangle height (px)"),
		"points": array("polygon vertices [[x,y],...] in px", array("[x,y]", number("px"), 2, 2), 3, 500),
		"x1":     number("slot start x"), "y1": number("slot start y"), "x2": number("slot end x"), "y2": number("slot end y"),
		"width":      positive("slot width (px)"),
		"confidence": unit("How sure you are this primitive exists and is right, 0..1"),
	}, "id", "kind", "confidence")
	return s
}

var proposalProps = props{
	"image_id":   text("Id of the attached image"),
	"image_size": array("[width,height] of the image in pixels", positive("px"), 2, 2),
	"entities":   array("Detected primitives in image pixels (x right, y DOWN)", imageEntitySchema(), 1, 500),
	"profiles": array("Closed regions: an outer loop of entity ids plus inner loops (holes/cutouts)", object(props{
		"id":    text("Profile id (p1, p2, ...)"),
		"outer": array("Entity ids forming the outer loop (one closed entity, or line/arc entities that chain into a loop)", text("entity id"), 1, 500),
		"holes": array("Inner loops", array("entity ids of one loop", text("entity id"), 1, 500), 0, 200),
	}, "id", "outer"), 1, 200),
	"relationships": array("Detected relations", object(props{
		"kind":       enum("Relation", "concentric", "equal", "horizontal", "vertical", "symmetric", "parallel", "perpendicular"),
		"entities":   array("Entity ids", text("entity id"), 1, 20),
		"confidence": unit("0..1"),
	}, "kind", "entities", "confidence"), 0, 200),
	"dimensions": array("Dimensions: ones written on the drawing (with value in mm) and ones that are still missing (no value, source=missing)", object(props{
		"id":          text("Dimension id (overall_width, thickness, ...)"),
		"kind":        enum("Kind", "length", "diameter", "radius", "depth", "distance"),
		"binds":       text("entity.field this measures: rectangle w|h, circle radius|diameter, slot width|length, line length (e1.w)"),
		"value":       positive("Value in mm, only when it is written on the drawing or the user said it"),
		"source":      enum("Where the value came from", "handwritten", "printed", "inferred", "missing", "confirmed", "user"),
		"confidence":  unit("0..1"),
		"bbox":        array("[x,y,w,h] of the annotation in px", number("px"), 4, 4),
		"measured_px": positive("Pixel length measured for an unbound dimension"),
		"label":       text("Short label used when asking the user, e.g. overall width"),
	}, "id", "kind", "source", "confidence"), 0, 200),
	"operations": array("How profiles combine into solids, in order (optional; default: extrude every profile by the depth dimension)", object(props{
		"id":        text("Operation id (o1, o2, ...); names the resulting solid"),
		"kind":      enum("Operation", "extrude", "cut", "union", "subtract", "intersect"),
		"profile":   text("extrude/cut: profile id"),
		"depth_dim": text("extrude/cut: id of a depth dimension"),
		"through":   boolean("cut: through everything"),
		"target":    text("cut: an earlier operation id, feature:<id> or selection"),
		"a":         text("boolean: kept operand (earlier operation id, feature:<id> or selection)"),
		"b":         text("boolean: merged / removed / intersected operand"),
	}, "id", "kind"), 0, 100),
	"placement": object(props{
		"plane":  enum("Sketch plane", "XY", "XZ", "YZ"),
		"offset": number("Offset along the plane normal (mm)"),
		"face":   text("Planar face selector, or 'selection' for the currently selected face"),
	}),
	"confidence":  unit("Overall confidence 0..1"),
	"description": text("One short sentence describing what you see (display only; not geometry)"),
}

var interpretationID = text("Interpretation id (int_0001); default: the latest")

func visionTools() []*Tool {
	return []*Tool{
		{
			Name: "analyze_image",
			Description: "Record what you see in an attached image as a structured sketch proposal (this does NOT change the model). Use image pixel coordinates (x right, y DOWN). " +
				"Group primitives into closed `profiles` (outer loop + holes). Put every dimension you can read in `dimensions` with its value in mm and a confidence; add dimensions you still need as source=missing (overall width/height, thickness as kind=depth). " +
				"Never invent a value or infer real size from pixels. The result says what is missing; ask the user only for that.",
			Capability: CapVision,
			Schema:     object(proposalProps, "image_id", "image_size", "entities", "profiles", "confidence"),
			Query:      queryAnalyzeImage,
		},
		{
			Name:        "get_sketch_proposal",
			Description: "Read an interpretation's current proposal and its validation report (missing / unconfirmed dimensions, ambiguities, errors).",
			Capability:  CapVision,
			Schema:      object(props{"interpretation_id": interpretationID}),
			Query:       queryGetProposal,
		},
		{
			Name: "update_sketch_proposal",
			Description: "Correct the proposal (not the model). Each edit: op + id + value. Ops: set_entity, remove_entity, set_profile, remove_profile, set_dimension, remove_dimension, set_operation, remove_operation, set_relationship (id = index or new), remove_relationship (id = index), set_placement. " +
				"set_* merges `value` onto the item (a null field removes it) or creates it. Examples: 'that circle is a hole' = set_profile outer profile holes + remove_profile for the circle's own profile; 'left edge vertical' = set_relationship new {kind:vertical,entities:[...],confidence:1}.",
			Capability: CapVision,
			Schema: object(props{
				"interpretation_id": interpretationID,
				"edits": array("Corrections applied in order, all or nothing", object(props{
					"op": enum("Edit", "set_entity", "remove_entity", "set_profile", "remove_profile", "set_dimension", "remove_dimension",
						"set_operation", "remove_operation", "set_relationship", "remove_relationship", "set_placement"),
					"id":    text("Item id (or relationship index)"),
					"value": map[string]any{"type": "object", "description": "Fields to merge"},
				}, "op"), 1, 100),
			}, "edits"),
			Query: queryUpdateProposal,
		},
		{
			Name: "confirm_detected_dimension",
			Description: "Record the user's answer about a dimension: confirm the detected value, or give the corrected value in mm (then it is trusted). " +
				"Use this for 'it is 80 mm wide', 'yes that 12 is right', 'make this dimension 40 mm'.",
			Capability: CapVision,
			Schema: object(props{
				"interpretation_id": interpretationID,
				"dimension":         text("Dimension id"),
				"value":             positive("Value in mm; omit to confirm the detected value"),
			}, "dimension"),
			Query: queryConfirmDimension,
		},
		{
			Name: "set_sketch_scale",
			Description: "Set the real scale of the drawing explicitly, either as mm_per_px, or as a length measured in the image (`pixels`) that equals `mm`. " +
				"Prefer confirm_detected_dimension when a dimension exists. clear=true removes an explicit scale.",
			Capability: CapVision,
			Schema: object(props{
				"interpretation_id": interpretationID,
				"mm_per_px":         positive("Millimetres per image pixel"),
				"pixels":            positive("A length measured in the image (px)"),
				"mm":                positive("The real length of that distance (mm)"),
				"clear":             boolean("Remove the explicit scale"),
			}),
			Query: querySetScale,
		},
		{
			Name:        "create_sketch_from_image",
			Description: "Create only the planar sketch (no solid) from a ready interpretation, as a normal editable sketch feature. The result stays proposed until the user commits it.",
			Capability:  CapVision,
			Schema:      createSchema(false),
			Plan:        func(env Env, a Args) (*Plan, error) { return planFromInterpretation(env, a, "sketch", "") },
		},
		{
			Name: "create_from_image",
			Description: "Turn a ready interpretation into editable parametric geometry: a sketch, then extrude / cut / boolean features in order (normal CAD-IR features, no image object). " +
				"Refuses and lists what is missing or unconfirmed until the interpretation is ready. `extrusion_depth` (mm) is the user's explicit thickness for operations that lack one. The ChangeSet is always left proposed for preview and commit.",
			Capability: CapVision,
			Schema:     createSchema(true),
			Plan: func(env Env, a Args) (*Plan, error) {
				return planFromInterpretation(env, a, orDefault(a.Str("mode"), "solid"), "")
			},
		},
		{
			Name:        "extrude_detected_profile",
			Description: "Extrude / cut / combine the profiles of an interpretation whose sketch already exists (created by create_sketch_from_image). Same operations and rules as create_from_image, without creating the sketch again.",
			Capability:  CapVision,
			Schema:      createSchema(false),
			Plan: func(env Env, a Args) (*Plan, error) {
				return planFromInterpretation(env, a, "solid", "existing")
			},
		},
	}
}

func createSchema(withMode bool) map[string]any {
	p := props{
		"interpretation_id": interpretationID,
		"extrusion_depth":   positive("Thickness / depth in mm, as stated by the user"),
		"plane":             enum("Override the sketch plane", "XY", "XZ", "YZ"),
		"offset":            number("Override the plane offset (mm)"),
		"face":              text("Place the sketch on this planar face selector, or 'selection' for the selected face"),
	}
	if withMode {
		p["mode"] = enum("solid builds the geometry; sketch stops at the sketch", "solid", "sketch")
	}
	return object(withMeta(p))
}

// ---- query tools --------------------------------------------------------------------------

func pickInterpretation(env Env, a Args) (*vision.Interpretation, error) {
	id := a.Str("interpretation_id")
	if id == "" {
		id = env.Vision().Latest()
	}
	if id == "" {
		return nil, fmt.Errorf("there is no interpretation yet: call analyze_image first")
	}
	it, ok := env.Vision().Get(id)
	if !ok {
		return nil, fmt.Errorf("unknown interpretation %q", id)
	}
	return it, nil
}

// proposalView is the tool result. The full proposal is only echoed by get_sketch_proposal:
// the model just wrote it, so repeating it would only cost tokens.
func proposalView(it *vision.Interpretation, full bool) map[string]any {
	rep := it.Report()
	m := map[string]any{"interpretation_id": it.ID, "revision": it.Revision, "report": rep}
	if full {
		m["proposal"] = it.Proposal
	}
	if len(it.Used) > 0 {
		m["created_nodes"] = it.Used
	}
	return m
}

func queryAnalyzeImage(env Env, a Args) (any, error) {
	p, err := vision.Decode(map[string]any(a))
	if err != nil {
		return nil, err
	}
	im, ok := env.Images().Get(p.ImageID)
	if !ok {
		return nil, fmt.Errorf("image %q is not attached to this project session; ask the user to attach it", p.ImageID)
	}
	if p.ImageSize[0] != float64(im.Width) || p.ImageSize[1] != float64(im.Height) {
		return nil, fmt.Errorf("image_size must be [%d,%d] (the attached image's pixel size)", im.Width, im.Height)
	}
	it := env.Vision().Add(p)
	return proposalView(it, false), nil
}

func queryGetProposal(env Env, a Args) (any, error) {
	it, err := pickInterpretation(env, a)
	if err != nil {
		return nil, err
	}
	return proposalView(it, true), nil
}

func queryUpdateProposal(env Env, a Args) (any, error) {
	it, err := pickInterpretation(env, a)
	if err != nil {
		return nil, err
	}
	var edits []vision.Edit
	if err := roundTrip(a["edits"], &edits); err != nil {
		return nil, err
	}
	out, err := env.Vision().Modify(it.ID, func(i *vision.Interpretation) error {
		return vision.ApplyEdits(i.Proposal, edits)
	})
	if err != nil {
		return nil, err
	}
	return proposalView(out, false), nil
}

func queryConfirmDimension(env Env, a Args) (any, error) {
	it, err := pickInterpretation(env, a)
	if err != nil {
		return nil, err
	}
	var v *float64
	if a.Has("value") {
		f := a.Num("value")
		v = &f
	}
	out, err := env.Vision().Modify(it.ID, func(i *vision.Interpretation) error {
		return vision.ConfirmDimension(i.Proposal, a.Str("dimension"), v)
	})
	if err != nil {
		return nil, err
	}
	return proposalView(out, false), nil
}

func querySetScale(env Env, a Args) (any, error) {
	it, err := pickInterpretation(env, a)
	if err != nil {
		return nil, err
	}
	var sc *vision.Scale
	switch {
	case a.Bool("clear"):
	case a.Has("mm_per_px"):
		sc = &vision.Scale{MMPerPx: a.Num("mm_per_px"), Source: "explicit"}
	case a.Has("pixels") && a.Has("mm"):
		sc = &vision.Scale{MMPerPx: a.Num("mm") / a.Num("pixels"), Source: "explicit"}
	default:
		return nil, fmt.Errorf("give mm_per_px, or pixels together with mm (or clear=true)")
	}
	out, err := env.Vision().Modify(it.ID, func(i *vision.Interpretation) error { i.Scale = sc; return nil })
	if err != nil {
		return nil, err
	}
	return proposalView(out, false), nil
}

// ---- create from an interpretation -----------------------------------------------------------

var safeID = regexp.MustCompile(`[^a-z0-9]+`)

// notReady explains, deterministically, everything between the proposal and a model.
func notReady(it *vision.Interpretation, rep *vision.Report) error {
	var parts []string
	if len(rep.Errors) > 0 {
		parts = append(parts, "errors: "+strings.Join(rep.Errors, "; "))
	}
	if len(rep.Missing) > 0 {
		var q []string
		for _, m := range rep.Missing {
			q = append(q, fmt.Sprintf("%s (%s)", m.Dimension, m.Ask))
		}
		parts = append(parts, "missing: "+strings.Join(q, "; ")+" — ask the user for exactly these")
	}
	if len(rep.Unconfirmed) > 0 {
		parts = append(parts, fmt.Sprintf("unconfirmed low-confidence dimensions %v — have the user confirm them with confirm_detected_dimension", rep.Unconfirmed))
	}
	for _, am := range rep.Ambiguous {
		parts = append(parts, "ambiguous: "+am.Question+" (set operations with update_sketch_proposal once decided)")
	}
	if len(parts) == 0 {
		parts = append(parts, "no usable scale")
	}
	return fmt.Errorf("interpretation %s is not ready: %s", it.ID, strings.Join(parts, " | "))
}

func roundTrip(in, out any) error {
	b, err := jsonMarshal(in)
	if err != nil {
		return err
	}
	return jsonUnmarshal(b, out)
}

// firstSelectedFace returns the selected face selector, if any.
func firstSelected(env Env, kind string) (selectors.Selector, bool) {
	for _, raw := range env.SelectionRefs() {
		if sel, err := selectors.Parse(raw); err == nil && sel.Kind == kind {
			return sel, true
		}
	}
	return selectors.Selector{}, false
}

func planFromInterpretation(env Env, a Args, mode, existing string) (*Plan, error) {
	it, err := pickInterpretation(env, a)
	if err != nil {
		return nil, err
	}
	p := it.Proposal.Clone()

	// the user's explicit overrides
	if a.Has("extrusion_depth") {
		v := a.Num("extrusion_depth")
		if d := vision.DefaultDepth(p); d != nil {
			d.Value, d.Source, d.Confidence = &v, "user", 1
		} else {
			p.Dimensions = append(p.Dimensions, vision.Dimension{ID: "extrusion_depth", Kind: "depth", Value: &v, Source: "user", Confidence: 1})
		}
		for i := range p.Operations {
			op := &p.Operations[i]
			if (op.Kind == "extrude" || op.Kind == "cut") && !op.Through && op.DepthDim == "" {
				op.DepthDim = vision.DefaultDepth(p).ID
			}
		}
	}
	if a.Has("plane") {
		p.Placement.Plane = a.Str("plane")
	}
	if a.Has("offset") {
		p.Placement.Offset = a.Num("offset")
	}
	if a.Has("face") {
		p.Placement.Face = a.Str("face")
	}

	rep := vision.Validate(p, it.Scale)
	if !rep.Ready {
		return nil, notReady(it, rep)
	}
	ops := p.Operations
	if len(ops) == 0 {
		ops = []vision.Operation{{ID: "extrude1", Kind: "extrude", DepthDim: vision.DefaultDepth(p).ID}}
	}

	// node ids: <base>_sketch, <base>_<op>
	base := safeID.ReplaceAllString(strings.ToLower(firstNonEmpty(a.Str("id"), a.Str("name"), "image")), "_")
	base = strings.Trim(base, "_")
	if base == "" || base[0] < 'a' || base[0] > 'z' {
		base = "image"
	}
	g := env.Graph()
	sketchID := ""
	if existing == "" {
		for n := 1; ; n++ {
			cand := base
			if n > 1 {
				cand = fmt.Sprintf("%s_%d", base, n)
			}
			clash := g.Get(cand+"_sketch") != nil
			for _, op := range ops {
				clash = clash || g.Get(cand+"_"+op.ID) != nil
			}
			if !clash {
				base = cand
				break
			}
		}
		sketchID = base + "_sketch"
	} else {
		for _, n := range g.Nodes {
			if n.Type == "sketch" && n.Provenance.InterpretationID == it.ID {
				sketchID = n.ID
			}
		}
		if sketchID == "" {
			return nil, fmt.Errorf("no sketch was created from %s yet: use create_sketch_from_image or create_from_image", it.ID)
		}
		for n := 1; ; n++ { // keep operation node ids free
			cand := base
			if n > 1 {
				cand = fmt.Sprintf("%s_%d", base, n)
			}
			clash := false
			for _, op := range ops {
				clash = clash || g.Get(cand+"_"+op.ID) != nil
			}
			if !clash {
				base = cand
				break
			}
		}
	}

	stamp := func(n *cadir.Node) *cadir.Node {
		n.Provenance.Capability = CapVision
		n.Provenance.SourceImageID = p.ImageID
		n.Provenance.InterpretationID = it.ID
		return n
	}
	plan := &Plan{RequireApproval: true,
		Intent: intentOr(a, "Create geometry from image "+p.ImageID)}
	var nodes []*cadir.Node

	if existing == "" {
		sk, err := vision.ToSketch(p, rep.Scale)
		if err != nil {
			return nil, err
		}
		n := newNode(env, a, sketchID, "sketch", sk.Params)
		n.Metadata.Name = base + " sketch"
		n.Constraints = sk.Constraints
		if face := p.Placement.Face; face != "" {
			var sel selectors.Selector
			if face == "selection" {
				s, ok := firstSelected(env, "face")
				if !ok {
					return nil, fmt.Errorf("select a planar face first, or name the face selector")
				}
				sel = s
			} else if sel, err = selectors.Parse(face); err != nil || sel.Kind != "face" {
				return nil, fmt.Errorf("placement.face must be a face selector or 'selection' (got %q)", face)
			}
			d, err := resolveSelector(env, sel)
			if err != nil {
				return nil, err
			}
			if d.SurfaceType != "plane" {
				return nil, fmt.Errorf("%s is not a planar face (%s)", sel.String(), d.SurfaceType)
			}
			n.References = map[string]string{"face": sel.String()}
			n.Dependencies = []string{sel.Feature()}
		}
		nodes = append(nodes, stamp(n))
		plan.Warnings = append(plan.Warnings, sk.Warnings...)
	}
	if mode == "sketch" {
		plan.Explanation = "Creates a sketch from the image interpretation " + it.ID + "."
		plan.Commands = nodeCommands(nodes)
		return plan, nil
	}

	nodeOf := map[string]string{} // operation id -> node id
	resolve := func(ref string) (string, error) {
		switch {
		case ref == "selection":
			sel, ok := firstSelected(env, "body")
			if !ok {
				if f, ok2 := firstSelected(env, "face"); ok2 {
					sel, ok = f, true
				}
			}
			if !ok {
				return "", fmt.Errorf("nothing is selected: select the body first")
			}
			return sel.Feature(), nil
		case strings.HasPrefix(ref, "feature:"):
			return strings.TrimPrefix(ref, "feature:"), nil
		}
		id, ok := nodeOf[ref]
		if !ok {
			return "", fmt.Errorf("operation reference %q is unknown", ref)
		}
		return id, nil
	}
	depthOf := func(op vision.Operation) (float64, error) {
		dim := op.DepthDim
		if dim == "" && vision.DefaultDepth(p) != nil {
			dim = vision.DefaultDepth(p).ID
		}
		for _, d := range p.Dimensions {
			if d.ID == dim && d.Value != nil {
				return *d.Value, nil
			}
		}
		return 0, fmt.Errorf("operation %s has no depth", op.ID)
	}
	for _, op := range ops {
		id := base + "_" + op.ID
		params := map[string]any{}
		var deps []string
		typ := "extrude"
		switch op.Kind {
		case "extrude", "cut":
			params["sketch"] = sketchID
			deps = []string{sketchID}
			if op.Profile != "" {
				params["region"] = op.Profile
			}
			if op.Kind == "cut" {
				t, err := resolve(op.Target)
				if err != nil {
					return nil, err
				}
				if _, err := requireBounds(env, t); err != nil && g.Get(t) == nil {
					return nil, err
				}
				params["mode"], params["target"] = "cut", t
				deps = append(deps, t)
			} else {
				params["mode"] = "add"
			}
			if op.Through {
				params["through"] = true
			} else {
				d, err := depthOf(op)
				if err != nil {
					return nil, err
				}
				params["depth"] = d
			}
		default:
			typ = op.Kind
			ta, err := resolve(op.A)
			if err != nil {
				return nil, err
			}
			tb, err := resolve(op.B)
			if err != nil {
				return nil, err
			}
			params["target"], params["tool"] = ta, tb
			deps = []string{ta, tb}
		}
		n := stamp(newNode(env, a, id, typ, params, deps...))
		n.Metadata.Name = op.ID
		nodes = append(nodes, n)
		nodeOf[op.ID] = id
	}
	plan.Explanation = fmt.Sprintf("Builds %d feature(s) from the image interpretation %s.", len(nodes), it.ID)
	plan.Commands = nodeCommands(nodes)
	return plan, nil
}

func nodeCommands(nodes []*cadir.Node) []commands.Command {
	out := make([]commands.Command, len(nodes))
	for i, n := range nodes {
		out[i] = &commands.AddFeature{Node: n, Index: -1}
	}
	return out
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}
