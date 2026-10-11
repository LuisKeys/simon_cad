package tools

import (
	"fmt"
	"math"
	"reflect"
	"sort"

	"github.com/accelone/simoncad/internal/cadir"
	"github.com/accelone/simoncad/internal/commands"
	"github.com/accelone/simoncad/internal/geometry/curve"
	"github.com/accelone/simoncad/internal/geometry/features"
	"github.com/accelone/simoncad/internal/geometry/sweep"
	"github.com/accelone/simoncad/internal/meshcheck"
)

var capEnum = []string{"flat", "round", "point"}

// pathSource are the arguments that say where a sweep's spine comes from: an existing path, or
// inline points that create a path node in the same ChangeSet (one undo step).
func pathSourceProps(p props) props {
	p["path"] = text("Existing path feature id to sweep along (or give `points`)")
	p["points"] = array("Inline spine points [x,y,z] (creates a path), or "+pathPointDoc, pointSchema, 2, 5000)
	p["path_type"] = enum("Interpolation of inline points (default catmull_rom: smooth through the points)", pathTypeEnum...)
	p["closed"] = boolean("Inline points form a closed loop")
	return p
}

func sweepOptionProps(p props) props {
	p["tolerance"] = positive("Chord deviation of the tessellated sweep in mm (default 0.05)")
	p["max_sections"] = integer("Upper bound of sections along the path (default 600)", 2, 5000)
	return p
}

func sweepTools() []*Tool {
	return []*Tool{
		{
			Name: "create_sweep",
			Description: "Sweep a profile along a path into a solid. The path and profile stay editable: update_path / update_profile / update_sweep regenerate it. Per-point w/h/twist on the path vary the section. " +
				"Smooth paths (bezier, catmull_rom, bspline) give smooth surfaces; a linear path gives straight runs with mitered corners. cap_start/cap_end: flat (default), round or point; hollow profiles take flat caps only. " +
				"`twist` adds degrees of rotation spread along the path. `up` seeds the initial profile orientation (the profile's local X direction).",
			Capability: capGeometry,
			Schema: object(withMeta(sweepOptionProps(props{
				"path":       text("Path feature id"),
				"profile":    text("Profile feature id"),
				"cap_start":  enum("Start cap (open paths)", capEnum...),
				"cap_end":    enum("End cap (open paths)", capEnum...),
				"cap_length": positive("Height of a pointed cap (default: the profile radius)"),
				"twist":      number("Extra twist along the path (degrees; whole turns for a closed path)"),
				"up":         array("Initial direction of the profile's X axis [x,y,z]", number("coordinate"), 3, 3),
			})), "path", "profile"),
			Plan: planCreateSweep,
		},
		{
			Name:        "update_sweep",
			Description: "Change the options of a sweep (caps, twist, tolerance, orientation). To use another path or profile edit those, or create a new sweep.",
			Capability:  capGeometry,
			Schema: object(sweepOptionProps(props{
				"sweep":      text("Sweep feature id"),
				"cap_start":  enum("Start cap", capEnum...),
				"cap_end":    enum("End cap", capEnum...),
				"cap_length": positive("Height of a pointed cap"),
				"twist":      number("Extra twist along the path (degrees)"),
				"up":         array("Initial direction of the profile's X axis [x,y,z]", number("coordinate"), 3, 3),
				"intent":     text("Why"),
			}), "sweep"),
			Plan: planUpdateSweep,
		},
		{
			Name: "create_tube",
			Description: "Pipe, cable or duct along a path: hollow when inner_diameter or wall_thickness is given, solid otherwise. Builds the profile and sweep for you (and the path from `points`). " +
				"cap_mode: flat (default, open ends of a hollow tube), round or point (solid tubes only).",
			Capability: capGeometry,
			Schema: object(withMeta(sweepOptionProps(pathSourceProps(props{
				"outer_diameter": positive("Outer diameter (mm)"),
				"inner_diameter": positive("Inner diameter (mm); hollow tube"),
				"wall_thickness": positive("Wall thickness (mm); alternative to inner_diameter"),
				"cap_mode":       enum("End caps", capEnum...),
			}))), "outer_diameter"),
			Plan: planCreateTube,
		},
		{
			Name:        "create_wire",
			Description: "Solid round wire of the given diameter along a path (wires, organic or decorative curves). end_style: round (default), flat or point.",
			Capability:  capGeometry,
			Schema: object(withMeta(sweepOptionProps(pathSourceProps(props{
				"diameter":  positive("Wire diameter (mm)"),
				"end_style": enum("Ends", capEnum...),
			}))), "diameter"),
			Plan: planCreateWire,
		},
		{
			Name:        "create_ribbon",
			Description: "Flat strip with thickness along a path. `width` is across the strip, `thickness` through it. orientation: flat (width horizontal, default) or upright (width vertical); `twist` rotates the strip along the path (degrees).",
			Capability:  capGeometry,
			Schema: object(withMeta(sweepOptionProps(pathSourceProps(props{
				"width":       positive("Ribbon width (mm)"),
				"thickness":   positive("Ribbon thickness (mm)"),
				"orientation": enum("Initial orientation of the width", "flat", "upright"),
				"twist":       number("Total twist along the path (degrees)"),
			}))), "width", "thickness"),
			Plan: planCreateRibbon,
		},
		{
			Name: "validate_geometry",
			Description: "Check a body the way a slicer would: valid B-Rep, volume and area, bounding box, watertight / non-manifold mesh, degenerate triangles, disconnected components. " +
				"`object` is a feature id; default every visible body.",
			Capability: capGeometry,
			Schema: object(props{
				"object":     text("Feature id (default: all visible bodies)"),
				"deflection": positive("Mesh deflection in mm (default 0.05)"),
			}),
			Query: queryValidateGeometry,
		},
	}
}

func subArgs(a Args) Args { return Args{"intent": a.Str("intent")} }

// derivedID picks a free id base+suffix that is also unused by the nodes already planned.
func derivedID(env Env, base, suffix string, taken map[string]bool) string {
	id := base + suffix
	for i := 2; env.Graph().Get(id) != nil || taken[id]; i++ {
		id = fmt.Sprintf("%s%s_%d", base, suffix, i)
	}
	taken[id] = true
	return id
}

// resolveSpine returns the path id and, when the spine is inline, the node to create.
func resolveSpine(env Env, a Args, baseID string, taken map[string]bool) (string, *cadir.Node, map[string]any, error) {
	switch {
	case a.Has("path") && a.Has("points"):
		return "", nil, nil, fmt.Errorf("give either `path` or `points`, not both")
	case a.Has("path"):
		n, err := requirePath(env, a.Str("path"))
		if err != nil {
			return "", nil, nil, err
		}
		return n.ID, nil, n.Parameters, nil
	case a.Has("points"):
		p := map[string]any{"points": a["points"], "type": orDefault(a.Str("path_type"), "catmull_rom")}
		if a.Bool("closed") {
			p["closed"] = true
		}
		if _, err := features.PathFromParams(features.Params(p)); err != nil {
			return "", nil, nil, err
		}
		id := derivedID(env, baseID, "_path", taken)
		return id, newNode(env, subArgs(a), id, features.PathType, p), p, nil
	}
	return "", nil, nil, fmt.Errorf("give `path` (an existing path id) or `points` (inline spine)")
}

func sweepParams(a Args, pathID, profID string, extra map[string]any) map[string]any {
	p := map[string]any{"path": pathID, "profile": profID}
	copyOpt(p, a, "cap_start", "cap_end", "cap_length", "twist", "up", "tolerance", "max_sections")
	for k, v := range extra {
		p[k] = v
	}
	return p
}

func warningsOf(plan *sweep.Plan) []string {
	var out []string
	for _, w := range plan.Warnings {
		out = append(out, w.Code+": "+w.Message)
	}
	return out
}

func describePlan(plan *sweep.Plan) string {
	return fmt.Sprintf("path length %s mm, %d sections", fmtMM(plan.Length), plan.Sections)
}

func planCreateSweep(env Env, a Args) (*Plan, error) {
	path, err := requirePath(env, a.Str("path"))
	if err != nil {
		return nil, err
	}
	prof, err := requireProfile(env, a.Str("profile"))
	if err != nil {
		return nil, err
	}
	sp := sweepParams(a, path.ID, prof.ID, nil)
	plan, err := features.PlanSweep(features.Params(path.Parameters), features.Params(prof.Parameters), features.Params(sp))
	if err != nil {
		return nil, err
	}
	id, err := uniqueID(env.Graph(), a, "sweep")
	if err != nil {
		return nil, err
	}
	n := newNode(env, a, id, features.SweepType, sp, path.ID, prof.ID)
	out := addPlan(intentOr(a, "Sweep "+prof.ID+" along "+path.ID), "Creates a sweep ("+describePlan(plan)+").", n)
	out.Warnings = warningsOf(plan)
	return out, nil
}

func planUpdateSweep(env Env, a Args) (*Plan, error) {
	n, err := requireFeature(env, a.Str("sweep"))
	if err != nil {
		return nil, err
	}
	if n.Type != features.SweepType {
		return nil, fmt.Errorf("%q is a %s, not a sweep", n.ID, n.Type)
	}
	merged := map[string]any{}
	for k, v := range n.Parameters {
		merged[k] = v
	}
	var changed []string
	for _, k := range []string{"cap_start", "cap_end", "cap_length", "twist", "up", "tolerance", "max_sections"} {
		if v, ok := a[k]; ok && !reflect.DeepEqual(normalizeJSON(merged[k]), normalizeJSON(v)) {
			merged[k] = v
			changed = append(changed, k)
		}
	}
	if len(changed) == 0 {
		return nil, fmt.Errorf("the update leaves sweep %s unchanged", n.ID)
	}
	path, err := requirePath(env, fmt.Sprint(n.Parameters["path"]))
	if err != nil {
		return nil, err
	}
	prof, err := requireProfile(env, fmt.Sprint(n.Parameters["profile"]))
	if err != nil {
		return nil, err
	}
	plan, err := features.PlanSweep(features.Params(path.Parameters), features.Params(prof.Parameters), features.Params(merged))
	if err != nil {
		return nil, err
	}
	sort.Strings(changed)
	out := &Plan{Intent: intentOr(a, "Edit sweep "+n.ID), Explanation: fmt.Sprintf("Updates sweep %s (%v).", n.ID, changed), Warnings: warningsOf(plan)}
	for _, k := range changed {
		out.Commands = append(out.Commands, &commands.SetParameter{ID: n.ID, Name: k, Value: a[k]})
	}
	return out, nil
}

// buildDerived creates the nodes of a tube/wire/ribbon: optional path, profile, sweep.
func buildDerived(env Env, a Args, kind string, profileParams map[string]any, extra map[string]any, pathMod func(map[string]any) (map[string]any, error)) (*Plan, error) {
	id, err := uniqueID(env.Graph(), a, kind)
	if err != nil {
		return nil, err
	}
	taken := map[string]bool{id: true}
	pathID, pathNode, pathParams, err := resolveSpine(env, a, id, taken)
	if err != nil {
		return nil, err
	}
	if pathMod != nil && pathNode != nil {
		np, err := pathMod(pathNode.Parameters)
		if err != nil {
			return nil, err
		}
		pathNode.Parameters = np
		pathParams = np
	}
	if _, err := features.ProfileFromParams(features.Params(profileParams)); err != nil {
		return nil, err
	}
	profID := derivedID(env, id, "_profile", taken)
	profNode := newNode(env, subArgs(a), profID, features.ProfileType, profileParams)
	sp := sweepParams(a, pathID, profID, extra)
	delete(sp, "closed")
	plan, err := features.PlanSweep(features.Params(pathParams), features.Params(profileParams), features.Params(sp))
	if err != nil {
		return nil, err
	}
	sweepNode := newNode(env, a, id, features.SweepType, sp, pathID, profID)
	var nodes []*cadir.Node
	if pathNode != nil {
		nodes = append(nodes, pathNode)
	}
	nodes = append(nodes, profNode, sweepNode)
	out := addPlan(intentOr(a, "Create "+kind), fmt.Sprintf("Creates a %s (%s).", kind, describePlan(plan)), nodes...)
	out.Warnings = warningsOf(plan)
	out.LowRisk = true // path + profile + sweep are one gesture: commit it like a single feature
	return out, nil
}

func planCreateTube(env Env, a Args) (*Plan, error) {
	od := a.Num("outer_diameter")
	var prof map[string]any
	switch {
	case a.Has("inner_diameter") && a.Has("wall_thickness"):
		return nil, fmt.Errorf("give `inner_diameter` or `wall_thickness`, not both")
	case a.Has("inner_diameter") || a.Has("wall_thickness"):
		id := a.Num("inner_diameter")
		if a.Has("wall_thickness") {
			id = od - 2*a.Num("wall_thickness")
		}
		if !(id > 0) || id >= od {
			return nil, fmt.Errorf("the inner diameter (%s mm) must be positive and smaller than the outer diameter (%s mm); the wall thickness must be below %s mm",
				fmtMM(id), fmtMM(od), fmtMM(od/2))
		}
		prof = map[string]any{"shape": "hollow_circle", "outer_radius": od / 2, "inner_radius": id / 2}
	default:
		prof = map[string]any{"shape": "circle", "radius": od / 2}
	}
	cap := orDefault(a.Str("cap_mode"), "flat")
	if cap != "flat" && prof["shape"] == "hollow_circle" {
		return nil, features.Coded(features.CodeSweepFailed,
			"make the tube solid (omit inner_diameter / wall_thickness) for round or point ends, or use cap_mode flat",
			"a hollow tube supports flat end caps only")
	}
	return buildDerived(env, a, "tube", prof, map[string]any{"cap_start": cap, "cap_end": cap}, nil)
}

func planCreateWire(env Env, a Args) (*Plan, error) {
	cap := orDefault(a.Str("end_style"), "round")
	return buildDerived(env, a, "wire", map[string]any{"shape": "circle", "radius": a.Num("diameter") / 2},
		map[string]any{"cap_start": cap, "cap_end": cap}, nil)
}

func planCreateRibbon(env Env, a Args) (*Plan, error) {
	prof := map[string]any{"shape": "rectangle", "width": a.Num("width"), "height": a.Num("thickness")}
	extra := map[string]any{}
	if a.Has("twist") {
		extra["twist"] = a.Num("twist")
	}
	// Seed the width direction: flat lies the strip down (width horizontal), upright stands it up.
	seed := func(pathParams map[string]any) (curve.Vec, error) {
		pa, err := features.PathFromParams(features.Params(pathParams))
		if err != nil {
			return curve.Vec{}, err
		}
		c, err := curve.New(pa)
		if err != nil {
			return curve.Vec{}, err
		}
		t := c.At(0).T
		z := curve.Vec{0, 0, 1}
		if math.Abs(t[2]) > 0.99 { // vertical path: any horizontal direction
			return curve.Vec{1, 0, 0}, nil
		}
		if a.Str("orientation") == "upright" {
			return z, nil
		}
		up := curve.Vec{z[1]*t[2] - z[2]*t[1], z[2]*t[0] - z[0]*t[2], z[0]*t[1] - z[1]*t[0]}
		return up, nil
	}
	var pathParams map[string]any
	if a.Has("path") {
		n, err := requirePath(env, a.Str("path"))
		if err != nil {
			return nil, err
		}
		pathParams = n.Parameters
	} else if a.Has("points") {
		pathParams = map[string]any{"points": a["points"], "type": orDefault(a.Str("path_type"), "catmull_rom"), "closed": a.Bool("closed")}
	}
	if pathParams != nil {
		if up, err := seed(pathParams); err == nil {
			extra["up"] = []any{up[0], up[1], up[2]}
		}
	}
	return buildDerived(env, a, "ribbon", prof, extra, nil)
}

func queryValidateGeometry(env Env, a Args) (any, error) {
	m, err := env.Model()
	if err != nil {
		return nil, err
	}
	ids := m.Leaves
	if id := a.Str("object"); id != "" {
		if _, err := requireBounds(env, id); err != nil {
			return nil, err
		}
		ids = []string{id}
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("the model has no solid geometry to validate")
	}
	defl := a.NumOr("deflection", 0.05)
	var bodies []map[string]any
	allOK := true
	for _, id := range ids {
		r := m.Nodes[id]
		if r == nil || r.Body == nil {
			continue
		}
		b := r.Body
		valid, _ := b.Shape.IsValid()
		mesh, err := b.Shape.Tessellate(defl)
		if err != nil {
			return nil, fmt.Errorf("meshing %s: %v", id, err)
		}
		rep := meshcheck.Check(mesh.Positions, mesh.Indices)
		var issues []string
		if !valid {
			issues = append(issues, "INVALID_BREP: the kernel reports an invalid solid")
		}
		if rep.OpenEdges > 0 {
			issues = append(issues, fmt.Sprintf("NON_MANIFOLD_MESH: %d open edges (not watertight)", rep.OpenEdges))
		}
		if rep.NonManifold > 0 {
			issues = append(issues, fmt.Sprintf("NON_MANIFOLD_MESH: %d edges shared by more than two triangles", rep.NonManifold))
		}
		if rep.SignedVolume < 0 {
			issues = append(issues, "NON_MANIFOLD_MESH: negative signed volume (inverted normals)")
		}
		if rep.Components > b.Props.Solids {
			issues = append(issues, fmt.Sprintf("DISCONNECTED: %d separate mesh components", rep.Components))
		}
		if len(issues) > 0 {
			allOK = false
		}
		bodies = append(bodies, map[string]any{
			"object": id, "valid_brep": valid, "watertight": rep.Watertight(),
			"volume_mm3": b.Props.Volume, "mesh_volume_mm3": rep.SignedVolume, "area_mm2": b.Props.Area,
			"solids": b.Props.Solids, "faces": b.Props.Faces, "triangles": rep.Triangles, "degenerate_triangles": rep.Degenerate,
			"components": rep.Components, "open_edges": rep.OpenEdges, "non_manifold_edges": rep.NonManifold,
			"bbox_min": [3]float64{b.Props.MinX, b.Props.MinY, b.Props.MinZ},
			"bbox_max": [3]float64{b.Props.MaxX, b.Props.MaxY, b.Props.MaxZ},
			"issues":   issues,
		})
	}
	return map[string]any{"ok": allOK, "bodies": bodies}, nil
}
