package tools

import (
	"fmt"
	"math"
	"strings"

	"github.com/accelone/simoncad/internal/selectors"
)

// NewRegistry builds the default registry with every Phase 1 tool.
func Default() *Registry {
	r := NewRegistry()
	for _, t := range geometryTools() {
		r.Register(t)
	}
	for _, t := range sketchTools() {
		r.Register(t)
	}
	for _, t := range visionTools() {
		r.Register(t)
	}
	for _, t := range editTools() {
		r.Register(t)
	}
	for _, t := range deleteTools() {
		r.Register(t)
	}
	for _, t := range appearanceTools() {
		r.Register(t)
	}
	for _, t := range materialTools() {
		r.Register(t)
	}
	for _, t := range queryTools() {
		r.Register(t)
	}
	return r
}

func geometryTools() []*Tool {
	return []*Tool{
		{
			Name:        "create_box",
			Description: "Create a rectangular block (e.g. a plate). Dimensions in mm; dx/dy/dz are the extents along X/Y/Z. The block's minimum corner sits at (x,y,z), default the origin, so 'a 100 x 60 x 4 plate' is dx=100, dy=60, dz=4.",
			Capability:  capGeometry,
			Schema: object(withMeta(props{
				"dx": positive("Length along X (mm)"), "dy": positive("Length along Y (mm)"), "dz": positive("Length along Z / thickness (mm)"),
				"x": number("Min-corner X (mm), default 0"), "y": number("Min-corner Y (mm), default 0"), "z": number("Min-corner Z (mm), default 0"),
			}), "dx", "dy", "dz"),
			Plan: func(env Env, a Args) (*Plan, error) {
				id, err := uniqueID(env.Graph(), a, "box")
				if err != nil {
					return nil, err
				}
				p := map[string]any{"dx": a.Num("dx"), "dy": a.Num("dy"), "dz": a.Num("dz")}
				copyOpt(p, a, "x", "y", "z")
				return addPlan(intentOr(a, fmt.Sprintf("Create %s x %s x %s mm block", fmtMM(a.Num("dx")), fmtMM(a.Num("dy")), fmtMM(a.Num("dz")))),
					fmt.Sprintf("Adds block %q (%s x %s x %s mm).", id, fmtMM(a.Num("dx")), fmtMM(a.Num("dy")), fmtMM(a.Num("dz"))),
					newNode(env, a, id, "box", p)), nil
			},
		},
		{
			Name:        "create_cylinder",
			Description: "Create a cylinder (shaft, boss, standoff, pin). The base circle is centered at (x,y,z) and the cylinder extends `height` along `axis` (default z).",
			Capability:  capGeometry,
			Schema: object(withMeta(props{
				"radius": positive("Radius (mm)"), "height": positive("Length along the axis (mm)"),
				"axis": enum("Axis direction", "x", "y", "z"),
				"x":    number("Base center X (mm)"), "y": number("Base center Y (mm)"), "z": number("Base center Z (mm)"),
			}), "radius", "height"),
			Plan: func(env Env, a Args) (*Plan, error) {
				id, err := uniqueID(env.Graph(), a, "cylinder")
				if err != nil {
					return nil, err
				}
				p := map[string]any{"radius": a.Num("radius"), "height": a.Num("height")}
				copyOpt(p, a, "axis", "x", "y", "z")
				return addPlan(intentOr(a, "Create cylinder"),
					fmt.Sprintf("Adds cylinder %q (r=%s, h=%s mm).", id, fmtMM(a.Num("radius")), fmtMM(a.Num("height"))),
					newNode(env, a, id, "cylinder", p)), nil
			},
		},
		{
			Name:        "create_cone",
			Description: "Create a cone or frustum (taper, funnel, chamfer cutter). The base circle (`radius`) is centered at (x,y,z) and the cone extends `height` along `axis` (default z), narrowing to `top_radius` (default 0 = a point).",
			Capability:  capGeometry,
			Schema: object(withMeta(props{
				"radius": positive("Base radius (mm)"), "top_radius": number("Top radius (mm), default 0 (pointed)"), "height": positive("Length along the axis (mm)"),
				"axis": enum("Axis direction", "x", "y", "z"),
				"x":    number("Base center X (mm)"), "y": number("Base center Y (mm)"), "z": number("Base center Z (mm)"),
			}), "radius", "height"),
			Plan: func(env Env, a Args) (*Plan, error) {
				if a.Has("top_radius") && a.Num("top_radius") < 0 {
					return nil, fmt.Errorf("top_radius must be >= 0")
				}
				id, err := uniqueID(env.Graph(), a, "cone")
				if err != nil {
					return nil, err
				}
				p := map[string]any{"radius": a.Num("radius"), "height": a.Num("height")}
				copyOpt(p, a, "top_radius", "axis", "x", "y", "z")
				return addPlan(intentOr(a, "Create cone"),
					fmt.Sprintf("Adds cone %q (r=%s, h=%s mm).", id, fmtMM(a.Num("radius")), fmtMM(a.Num("height"))),
					newNode(env, a, id, "cone", p)), nil
			},
		},
		{
			Name:        "create_sphere",
			Description: "Create a sphere (ball, dome when cut, rounded end). Centered at (x,y,z), default the origin.",
			Capability:  capGeometry,
			Schema: object(withMeta(props{
				"radius": positive("Radius (mm)"),
				"x":      number("Center X (mm)"), "y": number("Center Y (mm)"), "z": number("Center Z (mm)"),
			}), "radius"),
			Plan: func(env Env, a Args) (*Plan, error) {
				id, err := uniqueID(env.Graph(), a, "sphere")
				if err != nil {
					return nil, err
				}
				p := map[string]any{"radius": a.Num("radius")}
				copyOpt(p, a, "x", "y", "z")
				return addPlan(intentOr(a, "Create sphere"),
					fmt.Sprintf("Adds sphere %q (r=%s mm).", id, fmtMM(a.Num("radius"))),
					newNode(env, a, id, "sphere", p)), nil
			},
		},
		{
			Name:        "create_torus",
			Description: "Create a torus (ring, O-ring seat, donut). Centered at (x,y,z); `major_radius` is the distance from the center to the middle of the tube, `minor_radius` the tube radius (must be smaller). The ring lies perpendicular to `axis` (default z).",
			Capability:  capGeometry,
			Schema: object(withMeta(props{
				"major_radius": positive("Ring radius, center to tube center (mm)"), "minor_radius": positive("Tube radius (mm), < major_radius"),
				"axis": enum("Axis the ring is perpendicular to", "x", "y", "z"),
				"x":    number("Center X (mm)"), "y": number("Center Y (mm)"), "z": number("Center Z (mm)"),
			}), "major_radius", "minor_radius"),
			Plan: func(env Env, a Args) (*Plan, error) {
				if a.Num("minor_radius") >= a.Num("major_radius") {
					return nil, fmt.Errorf("minor_radius must be smaller than major_radius")
				}
				id, err := uniqueID(env.Graph(), a, "torus")
				if err != nil {
					return nil, err
				}
				p := map[string]any{"major_radius": a.Num("major_radius"), "minor_radius": a.Num("minor_radius")}
				copyOpt(p, a, "axis", "x", "y", "z")
				return addPlan(intentOr(a, "Create torus"),
					fmt.Sprintf("Adds torus %q (R=%s, r=%s mm).", id, fmtMM(a.Num("major_radius")), fmtMM(a.Num("minor_radius"))),
					newNode(env, a, id, "torus", p)), nil
			},
		},
		{
			Name: "add_hole",
			Description: "Drill one or more holes into a feature's face. `positions` are world coordinates [x,y] (or [x,y,z]); the coordinate along the face normal is ignored. Omit positions for one hole at the face center. " +
				"The face defaults to the top (+Z) of `target`; pass `face` as a selector such as face(feature=plate,side=-X) to drill elsewhere. Omit depth for a through hole. " +
				"Use kind=counterbore (cb_diameter, cb_depth) or countersink (cs_diameter, optional cs_angle_deg).",
			Capability: capGeometry,
			Schema: object(withMeta(props{
				"target":       text("Feature id to drill into (required unless face is given)"),
				"face":         text("Face selector, e.g. face(feature=plate,side=+Z)"),
				"diameter":     positive("Hole diameter (mm). M3 clearance is 3.2-3.4"),
				"positions":    array("Hole centers in world coordinates", array("[x,y] or [x,y,z]", number("coordinate"), 2, 3), 1, 200),
				"depth":        positive("Blind depth (mm); omit for through"),
				"kind":         enum("Hole style", "simple", "counterbore", "countersink"),
				"cb_diameter":  positive("Counterbore diameter (mm)"),
				"cb_depth":     positive("Counterbore depth (mm)"),
				"cs_diameter":  positive("Countersink outer diameter (mm)"),
				"cs_angle_deg": map[string]any{"type": "number", "description": "Countersink included angle (deg), default 90", "exclusiveMinimum": 0.0, "maximum": 179.0},
			}), "diameter"),
			Plan: planHole,
		},
		{
			Name: "extrude_profile",
			Description: "Sketch a planar profile and extrude it, adding material (mode=add, optionally merged into `target`) or cutting (mode=cut, requires `target`). " +
				"Profiles (plane coordinates u,v = x,y): rectangle{x,y,w,h} (x,y = min corner); circle{x,y,r}; slot{x1,y1,x2,y2,width}; polygon{points:[[u,v],...]}. " +
				"plane XY has normal +Z (u=X,v=Y), YZ normal +X (u=Y,v=Z), XZ normal +Y (u=Z,v=X); `offset` moves the sketch along the normal. Use through=true for a cut through everything.",
			Capability: capGeometry,
			Schema: object(withMeta(props{
				"mode":      enum("add or cut", "add", "cut"),
				"target":    text("Feature to add to / cut from"),
				"profile":   enum("Profile kind", "rectangle", "circle", "slot", "polygon"),
				"plane":     enum("Sketch plane", "XY", "XZ", "YZ"),
				"offset":    number("Sketch offset along the plane normal (mm)"),
				"depth":     positive("Extrusion depth (mm); not needed for through cuts"),
				"symmetric": boolean("Extrude half the depth each way about the plane"),
				"through":   boolean("Cut through the entire target"),
				"x":         number("u of rectangle min corner / circle center"), "y": number("v of rectangle min corner / circle center"),
				"w": positive("rectangle size along u"), "h": positive("rectangle size along v"),
				"r":  positive("circle radius"),
				"x1": number("slot start u"), "y1": number("slot start v"), "x2": number("slot end u"), "y2": number("slot end v"),
				"width":  positive("slot width"),
				"points": array("polygon vertices [[u,v],...]", array("[u,v]", number("coordinate"), 2, 2), 3, 200),
			}), "profile"),
			Plan: planExtrude,
		},
		{
			Name:        "linear_pattern",
			Description: "Repeat a hole or extrude-cut feature `count` times in total, each copy offset by (dx,dy,dz) mm from the previous.",
			Capability:  capGeometry,
			Schema: object(withMeta(props{
				"source": text("Id of the hole/extrude feature to repeat"), "count": integer("Total instances including the original", 2, 500),
				"dx": number("Spacing along X (mm)"), "dy": number("Spacing along Y (mm)"), "dz": number("Spacing along Z (mm)"),
			}), "source", "count"),
			Plan: func(env Env, a Args) (*Plan, error) {
				if a.NumOr("dx", 0) == 0 && a.NumOr("dy", 0) == 0 && a.NumOr("dz", 0) == 0 {
					return nil, fmt.Errorf("give a non-zero spacing in dx, dy or dz")
				}
				if err := requirePatternable(env, a.Str("source")); err != nil {
					return nil, err
				}
				id, err := uniqueID(env.Graph(), a, "linear_pattern")
				if err != nil {
					return nil, err
				}
				p := map[string]any{"source": a.Str("source"), "count": a.Num("count")}
				copyOpt(p, a, "dx", "dy", "dz")
				return addPlan(intentOr(a, "Repeat "+a.Str("source")+" in a line"),
					fmt.Sprintf("Repeats %q %d times in a line.", a.Str("source"), int(a.Num("count"))),
					newNode(env, a, id, "linear_pattern", p, a.Str("source"))), nil
			},
		},
		{
			Name:        "circular_pattern",
			Description: "Repeat a hole or extrude-cut feature around an axis through (cx,cy,cz). count is the total number of instances; total_angle_deg defaults to 360 (evenly around the circle).",
			Capability:  capGeometry,
			Schema: object(withMeta(props{
				"source": text("Id of the hole/extrude feature to repeat"), "count": integer("Total instances including the original", 2, 500),
				"cx": number("Axis point X (mm)"), "cy": number("Axis point Y (mm)"), "cz": number("Axis point Z (mm)"),
				"axis":            enum("Axis direction", "x", "y", "z"),
				"total_angle_deg": map[string]any{"type": "number", "description": "Angular span (deg), default 360", "exclusiveMinimum": 0.0, "maximum": 360.0},
			}), "source", "count"),
			Plan: func(env Env, a Args) (*Plan, error) {
				if err := requirePatternable(env, a.Str("source")); err != nil {
					return nil, err
				}
				id, err := uniqueID(env.Graph(), a, "circular_pattern")
				if err != nil {
					return nil, err
				}
				p := map[string]any{"source": a.Str("source"), "count": a.Num("count")}
				copyOpt(p, a, "cx", "cy", "cz", "axis", "total_angle_deg")
				return addPlan(intentOr(a, "Repeat "+a.Str("source")+" around an axis"),
					fmt.Sprintf("Repeats %q %d times around an axis.", a.Str("source"), int(a.Num("count"))),
					newNode(env, a, id, "circular_pattern", p, a.Str("source"))), nil
			},
		},
		{
			Name:        "boolean",
			Description: "Combine two features: op=union merges `tool` into `target`; op=subtract removes `tool` from `target`; op=intersect keeps only the volume shared by both. Both inputs are consumed (they stop appearing as separate bodies).",
			Capability:  capGeometry,
			Schema: object(withMeta(props{
				"op": enum("Boolean operation", "union", "subtract", "intersect"), "target": text("Feature to keep"), "tool": text("Feature to merge or remove"),
			}), "op", "target", "tool"),
			Plan: func(env Env, a Args) (*Plan, error) {
				if a.Str("target") == a.Str("tool") {
					return nil, fmt.Errorf("target and tool must be different features")
				}
				for _, id := range []string{a.Str("target"), a.Str("tool")} {
					if _, err := requireBounds(env, id); err != nil {
						return nil, err
					}
				}
				id, err := uniqueID(env.Graph(), a, a.Str("op"))
				if err != nil {
					return nil, err
				}
				p := map[string]any{"target": a.Str("target"), "tool": a.Str("tool")}
				return addPlan(intentOr(a, a.Str("op")+" "+a.Str("tool")+" with "+a.Str("target")),
					fmt.Sprintf("%s %q %s %q.", strings.ToUpper(a.Str("op")[:1])+a.Str("op")[1:], a.Str("tool"), map[string]string{"union": "into", "subtract": "from", "intersect": "with"}[a.Str("op")], a.Str("target")),
					newNode(env, a, id, a.Str("op"), p, a.Str("target"), a.Str("tool"))), nil
			},
		},
		{
			Name:        "mirror",
			Description: "Mirror a feature across a coordinate plane (XY, XZ or YZ, shifted by `offset` along its normal) and merge the copy with the original.",
			Capability:  capGeometry,
			Schema: object(withMeta(props{
				"target": text("Feature to mirror"), "plane": enum("Mirror plane", "XY", "XZ", "YZ"), "offset": number("Plane offset along its normal (mm)"),
			}), "target", "plane"),
			Plan: func(env Env, a Args) (*Plan, error) {
				if _, err := requireBounds(env, a.Str("target")); err != nil {
					return nil, err
				}
				id, err := uniqueID(env.Graph(), a, "mirror")
				if err != nil {
					return nil, err
				}
				p := map[string]any{"target": a.Str("target"), "plane": a.Str("plane")}
				copyOpt(p, a, "offset")
				return addPlan(intentOr(a, "Mirror "+a.Str("target")),
					fmt.Sprintf("Mirrors %q across the %s plane.", a.Str("target"), a.Str("plane")),
					newNode(env, a, id, "mirror", p, a.Str("target"))), nil
			},
		},
		edgeTool("fillet", "radius", "Round all edges of a feature with the given radius (mm)."),
		edgeTool("chamfer", "distance", "Chamfer all edges of a feature with the given distance (mm)."),
		{
			Name:        "transform",
			Description: "Move and/or rotate a feature: rotation by angle_deg about the axis (ax,ay,az) through (ox,oy,oz) is applied first, then translation by (dx,dy,dz).",
			Capability:  capGeometry,
			Schema: object(withMeta(props{
				"target": text("Feature to transform"),
				"dx":     number("Translate X (mm)"), "dy": number("Translate Y (mm)"), "dz": number("Translate Z (mm)"),
				"angle_deg": number("Rotation angle (deg)"),
				"ax":        number("Rotation axis X, default 0"), "ay": number("Rotation axis Y, default 0"), "az": number("Rotation axis Z, default 1"),
				"ox": number("Rotation origin X"), "oy": number("Rotation origin Y"), "oz": number("Rotation origin Z"),
			}), "target"),
			Plan: func(env Env, a Args) (*Plan, error) {
				if _, err := requireBounds(env, a.Str("target")); err != nil {
					return nil, err
				}
				if a.NumOr("dx", 0) == 0 && a.NumOr("dy", 0) == 0 && a.NumOr("dz", 0) == 0 && a.NumOr("angle_deg", 0) == 0 {
					return nil, fmt.Errorf("specify a translation (dx/dy/dz) or a rotation (angle_deg)")
				}
				if a.Has("angle_deg") && a.NumOr("ax", 0) == 0 && a.NumOr("ay", 0) == 0 && a.NumOr("az", 1) == 0 {
					return nil, fmt.Errorf("rotation axis must be non-zero")
				}
				id, err := uniqueID(env.Graph(), a, "transform")
				if err != nil {
					return nil, err
				}
				p := map[string]any{"target": a.Str("target")}
				copyOpt(p, a, "dx", "dy", "dz", "angle_deg", "ax", "ay", "az", "ox", "oy", "oz")
				return addPlan(intentOr(a, "Move "+a.Str("target")), fmt.Sprintf("Transforms %q.", a.Str("target")),
					newNode(env, a, id, "transform", p, a.Str("target"))), nil
			},
		},
	}
}

func edgeTool(typ, param, desc string) *Tool {
	return &Tool{
		Name: typ, Description: desc + " Treats every edge of the body unless `edges` lists edge(feature=<target>,index=N) selectors (the user's selected edges), in which case only those.", Capability: capGeometry,
		Schema: object(withMeta(props{"target": text("Feature to treat"), param: positive("Size (mm)"),
			"edges": array("Only these edges: edge(feature=<target>,index=N) selectors from the selection context. Omit for every edge.", text("An edge selector"), 1, 200)}),
			"target", param),
		Plan: func(env Env, a Args) (*Plan, error) {
			b, err := requireBounds(env, a.Str("target"))
			if err != nil {
				return nil, err
			}
			var edgeSels []any
			if raw, ok := a["edges"].([]any); ok {
				m, err := env.Model()
				if err != nil {
					return nil, err
				}
				topo, ok := m.Topology(a.Str("target"))
				if !ok {
					return nil, fmt.Errorf("feature %q has no resolvable topology", a.Str("target"))
				}
				for _, it := range raw {
					sel, err := selectors.Parse(it.(string))
					if err != nil {
						return nil, err
					}
					if sel.Kind != "edge" || sel.Feature() != a.Str("target") {
						return nil, fmt.Errorf("%s must be an edge(feature=%s,index=N) selector", sel.String(), a.Str("target"))
					}
					if _, err := selectors.ResolveTopo(sel, topo); err != nil {
						return nil, err
					}
					edgeSels = append(edgeSels, sel.String())
				}
			}
			sz := b.Size()
			limit := math.Min(sz[0], math.Min(sz[1], sz[2])) / 2
			// With every edge treated, size is bounded by the thinnest dimension; for chosen
			// edges the kernel is the judge (a failure becomes a failed feature, not a crash).
			if edgeSels == nil && a.Num(param) >= limit {
				return nil, fmt.Errorf("%s %s mm is too large for %q: its thinnest dimension is %s mm, so use less than %s mm",
					param, fmtMM(a.Num(param)), a.Str("target"), fmtMM(limit*2), fmtMM(limit))
			}
			id, err := uniqueID(env.Graph(), a, typ)
			if err != nil {
				return nil, err
			}
			params := map[string]any{"target": a.Str("target"), param: a.Num(param)}
			what := "every edge"
			if edgeSels != nil {
				params["edges"] = edgeSels
				what = fmt.Sprintf("%d selected edge(s)", len(edgeSels))
			}
			return addPlan(intentOr(a, typ+" "+a.Str("target")), fmt.Sprintf("Applies a %s mm %s to %s of %q.", fmtMM(a.Num(param)), typ, what, a.Str("target")),
				newNode(env, a, id, typ, params, a.Str("target"))), nil
		},
	}
}

func requirePatternable(env Env, id string) error {
	n, err := requireFeature(env, id)
	if err != nil {
		return err
	}
	if n.Type != "hole" && !(n.Type == "extrude" && n.Parameters["mode"] == "cut") {
		return fmt.Errorf("%q is a %s; only hole and extrude(mode=cut) features can be patterned", id, n.Type)
	}
	if _, err := requireBounds(env, id); err != nil {
		return err
	}
	return nil
}

func planHole(env Env, a Args) (*Plan, error) {
	target := a.Str("target")
	var sel selectors.Selector
	if face := a.Str("face"); face != "" {
		s, err := selectors.Parse(face)
		if err != nil {
			return nil, err
		}
		if s.Kind != "face" {
			return nil, fmt.Errorf("face must be a face(...) selector, got %s", s.Kind)
		}
		if target != "" && target != s.Feature() {
			return nil, fmt.Errorf("target %q does not match the face selector's feature %q", target, s.Feature())
		}
		target, sel = s.Feature(), s
	} else {
		if target == "" {
			return nil, fmt.Errorf("give `target` (drills the +Z face) or a `face` selector")
		}
		sel = selectors.Selector{Kind: "face", Args: map[string]string{"feature": target, "side": "+Z"}}
	}
	d, err := resolveSelector(env, sel)
	if err != nil {
		return nil, err
	}
	if d.Normal == nil {
		return nil, fmt.Errorf("selector %s does not select a planar face", sel.String())
	}
	axis := 0
	for i, v := range d.Normal {
		if math.Abs(v) > 0.5 {
			axis = i
		}
	}
	dia := a.Num("diameter")
	// Face extents in the two in-plane axes.
	var inPlane [2]int
	k := 0
	for i := 0; i < 3; i++ {
		if i != axis {
			inPlane[k] = i
			k++
		}
	}
	for _, i := range inPlane {
		if ext := d.BBox.Max[i] - d.BBox.Min[i]; dia >= ext {
			return nil, fmt.Errorf("diameter %s mm does not fit the selected face (%s mm wide along %c)", fmtMM(dia), fmtMM(ext), "XYZ"[i])
		}
	}
	kind := a.Str("kind")
	switch kind {
	case "counterbore":
		if !a.Has("cb_diameter") || !a.Has("cb_depth") {
			return nil, fmt.Errorf("counterbore needs cb_diameter and cb_depth")
		}
		if a.Num("cb_diameter") <= dia {
			return nil, fmt.Errorf("cb_diameter must exceed the hole diameter")
		}
	case "countersink":
		if !a.Has("cs_diameter") || a.Num("cs_diameter") <= dia {
			return nil, fmt.Errorf("countersink needs cs_diameter greater than the hole diameter")
		}
	}
	p := map[string]any{"target": target, "diameter": dia}
	copyOpt(p, a, "depth", "kind", "cb_diameter", "cb_depth", "cs_diameter", "cs_angle_deg")
	count := 1
	if raw, ok := a["positions"].([]any); ok {
		count = len(raw)
		p["positions"] = raw
		for i, it := range raw {
			pt := it.([]any)
			for j, v := range pt {
				if j == axis {
					continue
				}
				c := v.(float64)
				margin := dia / 2
				if c-margin < d.BBox.Min[j]-1e-9 || c+margin > d.BBox.Max[j]+1e-9 {
					return nil, fmt.Errorf("hole %d at %c=%s mm would break the edge of the face (face spans %s..%s mm along %c; keep the hole's radius inside)",
						i+1, "XYZ"[j], fmtMM(c), fmtMM(d.BBox.Min[j]), fmtMM(d.BBox.Max[j]), "XYZ"[j])
				}
			}
		}
	}
	id, err := uniqueID(env.Graph(), a, "hole")
	if err != nil {
		return nil, err
	}
	node := newNode(env, a, id, "hole", p, target)
	if a.Str("face") != "" {
		node.References = map[string]string{"face": sel.String()}
		node.SelectionRefs = appendUnique(node.SelectionRefs, sel.String())
	}
	return addPlan(intentOr(a, fmt.Sprintf("Add %d x %s mm hole(s) to %s", count, fmtMM(dia), target)),
		fmt.Sprintf("Drills %d %s mm %s hole(s) into %q (%s).", count, fmtMM(dia), orDefault(kind, "simple"), target, sel.String()), node), nil
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

func appendUnique(list []string, s string) []string {
	for _, x := range list {
		if x == s {
			return list
		}
	}
	return append(list, s)
}

var profileFields = map[string][]string{
	"rectangle": {"x", "y", "w", "h"},
	"circle":    {"x", "y", "r"},
	"slot":      {"x1", "y1", "x2", "y2", "width"},
	"polygon":   {"points"},
}

func planExtrude(env Env, a Args) (*Plan, error) {
	mode := orDefault(a.Str("mode"), "add")
	profile := a.Str("profile")
	for _, f := range profileFields[profile] {
		if !a.Has(f) {
			return nil, fmt.Errorf("profile %s needs %s", profile, strings.Join(profileFields[profile], ", "))
		}
	}
	if mode == "cut" && a.Str("target") == "" {
		return nil, fmt.Errorf("a cut needs a `target`")
	}
	if a.Str("target") != "" {
		if _, err := requireBounds(env, a.Str("target")); err != nil {
			return nil, err
		}
	}
	if !a.Bool("through") && !a.Has("depth") {
		return nil, fmt.Errorf("give `depth`, or through=true for a cut")
	}
	if a.Bool("through") && mode != "cut" {
		return nil, fmt.Errorf("through=true only applies to cuts")
	}
	p := map[string]any{"profile": profile, "mode": mode}
	copyOpt(p, a, "target", "plane", "offset", "depth", "symmetric", "through")
	copyOpt(p, a, profileFields[profile]...)
	id, err := uniqueID(env.Graph(), a, "extrude")
	if err != nil {
		return nil, err
	}
	var deps []string
	if t := a.Str("target"); t != "" {
		deps = []string{t}
	}
	return addPlan(intentOr(a, fmt.Sprintf("Extrude a %s (%s)", profile, mode)),
		fmt.Sprintf("Extrudes a %s profile (%s).", profile, mode), newNode(env, a, id, "extrude", p, deps...)), nil
}
