package tools

import (
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"sort"

	"github.com/accelone/simoncad/internal/cadir"
	"github.com/accelone/simoncad/internal/commands"
	"github.com/accelone/simoncad/internal/geometry/curve"
	"github.com/accelone/simoncad/internal/geometry/features"
	"github.com/accelone/simoncad/internal/geometry/profile"
)

const pathPointDoc = "[x,y,z] or {p:[x,y,z], in:[x,y,z], out:[x,y,z], w, h, twist}: in/out are absolute Bezier handle positions; w/h scale the swept profile at this point (default 1); twist is extra degrees of rotation about the path"

var pathTypeEnum = []string{"linear", "bezier", "catmull_rom", "bspline"}

var pointSchema = map[string]any{"description": pathPointDoc}

var pathOpSchema = object(props{
	"op":       enum("Edit operation", "add", "insert", "remove", "move", "set"),
	"index":    integer("Point index (0-based) for insert/remove/move/set", 0, 100000),
	"point":    pointSchema,
	"position": array("New position [x,y,z] for move", number("coordinate"), 3, 3),
	"in":       array("set: Bezier in-handle position [x,y,z]", number("coordinate"), 3, 3),
	"out":      array("set: Bezier out-handle position [x,y,z]", number("coordinate"), 3, 3),
	"w":        positive("set: profile width factor at this point"),
	"h":        positive("set: profile height factor at this point"),
	"twist":    number("set: twist at this point (degrees)"),
}, "op")

func profileProps() props {
	return props{
		"shape":        enum("Profile shape", profile.Types()...),
		"radius":       positive("circle / semicircle radius (mm)"),
		"radius_x":     positive("ellipse X radius (mm)"),
		"radius_y":     positive("ellipse Y radius (mm)"),
		"side":         positive("square side (mm)"),
		"width":        positive("rectangle / triangle width (mm)"),
		"height":       positive("rectangle / triangle height (mm)"),
		"outer_radius": positive("hollow_circle outer radius (mm)"),
		"inner_radius": positive("hollow_circle inner radius (mm)"),
		"orientation":  number("semicircle: rotation of the round side in degrees (0 = round side toward +Y)"),
		"segments":     integer("Segments of round profiles (default 48)", 3, 4096),
		"vertices":     array("polygon vertices [[x,y],...] in the profile plane", array("[x,y]", number("coordinate"), 2, 2), 3, 500),
		"contours": array("custom: closed loops [{points:[[x,y],...], hole:false}]; holes must lie inside an outer loop",
			object(props{
				"points": array("loop vertices [[x,y],...]", array("[x,y]", number("coordinate"), 2, 2), 3, 2000),
				"hole":   boolean("true for a hole"),
			}, "points"), 1, 50),
	}
}

var profileKeys = []string{"shape", "radius", "radius_x", "radius_y", "side", "width", "height", "outer_radius",
	"inner_radius", "orientation", "segments", "vertices", "contours"}

func pathTools() []*Tool {
	return []*Tool{
		{
			Name: "create_path",
			Description: "Create an editable 3D path (a curve, no solid). `points` are [x,y,z] or {p, in, out, w, h, twist}. type: linear (polyline), bezier (cubic, handles in/out), " +
				"catmull_rom (smooth curve through the points), bspline (smooth curve pulled toward the points). A path is the spine for create_sweep / create_tube / create_ribbon / create_wire " +
				"and the source for engraving; per-point w/h scale the swept profile and twist rotates it. Edit later with update_path.",
			Capability: capGeometry,
			Schema: object(withMeta(props{
				"points": array("Control points", pointSchema, 2, 5000),
				"type":   enum("Interpolation (default linear)", pathTypeEnum...),
				"closed": boolean("Closed loop"),
			}), "points"),
			Plan: planCreatePath,
		},
		{
			Name: "update_path",
			Description: "Edit a path in place; sweeps/reliefs built on it regenerate. Give any of: `points` (replace all), `type` (convert; bezier keeps the exact shape), `closed`, " +
				"`ops` (add/insert/remove/move/set point edits applied in order; `set` changes w, h, twist or Bezier in/out handles of one point), `reverse`, `simplify` (tolerance mm, drops redundant points), `smooth` (iterations).",
			Capability: capGeometry,
			Schema: object(props{
				"path":     text("Path feature id"),
				"points":   array("Replace all control points", pointSchema, 2, 5000),
				"type":     enum("Convert interpolation", pathTypeEnum...),
				"closed":   boolean("Close or reopen the path"),
				"ops":      array("Point edits, applied in order", pathOpSchema, 1, 500),
				"reverse":  boolean("Reverse the direction of travel"),
				"simplify": positive("Douglas-Peucker tolerance (mm)"),
				"smooth":   integer("Smoothing iterations", 1, 100),
				"intent":   text("Why"),
			}, "path"),
			Plan: planUpdatePath,
		},
		{
			Name:        "delete_path",
			Description: "Delete a path. Fails if sweeps or other features still use it; remove those first.",
			Capability:  capGeometry,
			Schema:      object(props{"path": text("Path feature id"), "intent": text("Why")}, "path"),
			Plan: func(env Env, a Args) (*Plan, error) {
				n, err := requirePath(env, a.Str("path"))
				if err != nil {
					return nil, err
				}
				if deps := env.Graph().Dependents(n.ID); len(deps) > 0 {
					return nil, fmt.Errorf("path %s is used by %v; remove those first", n.ID, deps)
				}
				return &Plan{Intent: intentOr(a, "Delete path "+n.ID), Explanation: "Removes path " + n.ID + ".",
					Commands: []commands.Command{&commands.RemoveFeature{ID: n.ID}}}, nil
			},
		},
		{
			Name:        "evaluate_path",
			Description: "Read a path: total length, point count, bounding box, minimum radius of curvature, self-intersections, and position + tangent at normalized arc-length parameters `t` (0..1).",
			Capability:  capGeometry,
			Schema: object(props{
				"path": text("Path feature id"),
				"t":    array("Parameters in [0,1] to evaluate", number("t"), 1, 200),
			}, "path"),
			Query: queryEvaluatePath,
		},
		{
			Name: "create_profile",
			Description: "Create a reusable 2D cross-section for sweeps (centred on the origin of the section plane). shape: circle{radius}, ellipse{radius_x,radius_y}, square{side}, rectangle{width,height}, " +
				"triangle{width,height}, semicircle{radius,orientation}, polygon{vertices}, hollow_circle{outer_radius,inner_radius}, custom{contours with optional holes}. " +
				"One profile can be swept along many paths.",
			Capability: capGeometry,
			Schema:     object(withMeta(profileProps()), "shape"),
			Plan:       planCreateProfile,
		},
		{
			Name:        "update_profile",
			Description: "Change a profile's parameters (same arguments as create_profile); every sweep using it regenerates.",
			Capability:  capGeometry,
			Schema: func() map[string]any {
				p := profileProps()
				p["profile"] = text("Profile feature id")
				p["intent"] = text("Why")
				return object(p, "profile")
			}(),
			Plan: planUpdateProfile,
		},
	}
}

func requirePath(env Env, id string) (*cadir.Node, error) {
	n, err := requireFeature(env, id)
	if err != nil {
		return nil, err
	}
	if n.Type != features.PathType {
		return nil, fmt.Errorf("%q is a %s, not a path (paths: %v)", id, n.Type, nodesOfType(env, features.PathType))
	}
	return n, nil
}

func requireProfile(env Env, id string) (*cadir.Node, error) {
	n, err := requireFeature(env, id)
	if err != nil {
		return nil, err
	}
	if n.Type != features.ProfileType {
		return nil, fmt.Errorf("%q is a %s, not a profile (profiles: %v)", id, n.Type, nodesOfType(env, features.ProfileType))
	}
	return n, nil
}

func nodesOfType(env Env, typ string) []string {
	var ids []string
	for _, n := range env.Graph().Nodes {
		if n.Type == typ {
			ids = append(ids, n.ID)
		}
	}
	return ids
}

func planCreatePath(env Env, a Args) (*Plan, error) {
	p := map[string]any{"points": a["points"], "type": orDefault(a.Str("type"), "linear")}
	if a.Bool("closed") {
		p["closed"] = true
	}
	pa, err := features.PathFromParams(features.Params(p))
	if err != nil {
		return nil, err
	}
	id, err := uniqueID(env.Graph(), a, "path")
	if err != nil {
		return nil, err
	}
	n := newNode(env, a, id, features.PathType, p)
	c, err := curve.New(pa)
	if err != nil {
		return nil, err
	}
	return addPlan(intentOr(a, "Create path"),
		fmt.Sprintf("Creates a %s path with %d points, length %s mm.", pa.Type, len(pa.Points), fmtMM(c.Length())), n), nil
}

func planUpdatePath(env Env, a Args) (*Plan, error) {
	n, err := requirePath(env, a.Str("path"))
	if err != nil {
		return nil, err
	}
	pa, err := features.PathFromParams(features.Params(n.Parameters))
	if err != nil {
		return nil, err
	}
	if a.Has("points") {
		tmp, err := features.PathFromParams(features.Params{"type": string(pa.Type), "closed": pa.Closed, "points": a["points"]})
		if err != nil {
			return nil, err
		}
		pa.Points = tmp.Points
	}
	if a.Has("closed") {
		if err := pa.SetClosed(a.Bool("closed")); err != nil {
			return nil, err
		}
	}
	if t := a.Str("type"); t != "" {
		if err := pa.Convert(curve.Type(t)); err != nil {
			return nil, err
		}
	}
	if ops, ok := a["ops"].([]any); ok {
		for i, o := range ops {
			if err := applyPathOp(pa, o.(map[string]any)); err != nil {
				return nil, fmt.Errorf("ops[%d]: %w", i, err)
			}
		}
	}
	if a.Bool("reverse") {
		pa.Reverse()
	}
	if a.Has("simplify") {
		before := len(pa.Points)
		pa.Simplify(a.Num("simplify"))
		if len(pa.Points) == before && !a.Has("points") && !a.Has("ops") && !a.Has("type") && !a.Has("closed") && !a.Has("smooth") && !a.Bool("reverse") {
			return nil, fmt.Errorf("simplify with tolerance %g mm removed no points", a.Num("simplify"))
		}
	}
	if a.Has("smooth") {
		pa.Smooth(int(a.Num("smooth")))
	}
	if err := pa.Validate(); err != nil {
		return nil, features.Coded(features.CodePathInvalid, "", "%v", err)
	}
	next := normalizeJSON(features.PathToParams(pa)).(map[string]any)
	cur := normalizeJSON(features.PathToParams(mustPath(n))).(map[string]any)
	plan := &Plan{Intent: intentOr(a, "Edit path "+n.ID), Explanation: fmt.Sprintf("Updates path %s (%d points).", n.ID, len(pa.Points))}
	for _, k := range []string{"type", "closed", "points"} {
		if !reflect.DeepEqual(cur[k], next[k]) {
			plan.Commands = append(plan.Commands, &commands.SetParameter{ID: n.ID, Name: k, Value: next[k]})
		}
	}
	if len(plan.Commands) == 0 {
		return nil, fmt.Errorf("the update leaves path %s unchanged", n.ID)
	}
	return plan, nil
}

func mustPath(n *cadir.Node) *curve.Path {
	p, _ := features.PathFromParams(features.Params(n.Parameters))
	return p
}

// normalizeJSON converts typed values to the float64/[]any/map shapes stored in a node.
func normalizeJSON(v any) any {
	b, _ := json.Marshal(v)
	var out any
	_ = json.Unmarshal(b, &out)
	return out
}

func vecArg(v any) curve.Vec {
	l := v.([]any)
	return curve.Vec{l[0].(float64), l[1].(float64), l[2].(float64)}
}

func applyPathOp(pa *curve.Path, o map[string]any) error {
	idx := func() (int, error) {
		f, ok := o["index"].(float64)
		if !ok {
			return 0, fmt.Errorf("op %q needs `index`", o["op"])
		}
		return int(f), nil
	}
	point := func() (curve.Point, error) {
		raw, ok := o["point"]
		if !ok {
			return curve.Point{}, fmt.Errorf("op %q needs `point`", o["op"])
		}
		tmp, err := features.PathFromParams(features.Params{"type": "linear", "points": []any{raw, raw}})
		if err != nil {
			return curve.Point{}, err
		}
		return tmp.Points[0], nil
	}
	switch o["op"] {
	case "add":
		pt, err := point()
		if err != nil {
			return err
		}
		pa.Add(pt)
		return nil
	case "insert":
		i, err := idx()
		if err != nil {
			return err
		}
		pt, err := point()
		if err != nil {
			return err
		}
		return pa.Insert(i, pt)
	case "remove":
		i, err := idx()
		if err != nil {
			return err
		}
		return pa.Remove(i)
	case "move":
		i, err := idx()
		if err != nil {
			return err
		}
		pos, ok := o["position"]
		if !ok {
			return fmt.Errorf("move needs `position`")
		}
		return pa.Move(i, vecArg(pos))
	case "set":
		i, err := idx()
		if err != nil {
			return err
		}
		if i >= len(pa.Points) {
			return fmt.Errorf("point index %d out of range (path has %d points)", i, len(pa.Points))
		}
		pt := &pa.Points[i]
		if v, ok := o["w"].(float64); ok {
			pt.W = v
		}
		if v, ok := o["h"].(float64); ok {
			pt.H = v
		}
		if v, ok := o["twist"].(float64); ok {
			pt.Twist = v
		}
		if v, ok := o["in"]; ok {
			_ = pa.SetHandle(i, false, vecArg(v))
		}
		if v, ok := o["out"]; ok {
			_ = pa.SetHandle(i, true, vecArg(v))
		}
		return nil
	}
	return fmt.Errorf("unknown op %v", o["op"])
}

func queryEvaluatePath(env Env, a Args) (any, error) {
	n, err := requirePath(env, a.Str("path"))
	if err != nil {
		return nil, err
	}
	pa, err := features.PathFromParams(features.Params(n.Parameters))
	if err != nil {
		return nil, err
	}
	c, err := curve.New(pa)
	if err != nil {
		return nil, err
	}
	poly := c.Resample(0.05)
	lo, hi := curve.Vec{math.Inf(1), math.Inf(1), math.Inf(1)}, curve.Vec{math.Inf(-1), math.Inf(-1), math.Inf(-1)}
	for _, p := range poly {
		for k := 0; k < 3; k++ {
			lo[k], hi[k] = math.Min(lo[k], p[k]), math.Max(hi[k], p[k])
		}
	}
	out := map[string]any{
		"path": n.ID, "type": pa.Type, "closed": pa.Closed, "points": len(pa.Points),
		"length_mm": c.Length(), "bbox_min": lo, "bbox_max": hi,
	}
	dense := c.ResampleUniform(int(math.Max(64, math.Min(2000, c.Length()/0.25))))
	if r, at := curve.MinRadius(dense, pa.Closed); at >= 0 && !math.IsInf(r, 1) {
		out["min_curvature_radius_mm"] = r
	}
	if hits := curve.SelfIntersections(dense, pa.Closed, 1e-3); len(hits) > 0 {
		out["self_intersections"] = len(hits)
	}
	if ts, ok := a["t"].([]any); ok {
		var samples []map[string]any
		for _, t := range ts {
			e := c.At(t.(float64))
			samples = append(samples, map[string]any{"t": t, "position": e.P, "tangent": e.T})
		}
		out["samples"] = samples
	}
	return out, nil
}

func profileParamsFrom(a Args) map[string]any {
	p := map[string]any{}
	for _, k := range profileKeys {
		if v, ok := a[k]; ok {
			p[k] = v
		}
	}
	return p
}

func planCreateProfile(env Env, a Args) (*Plan, error) {
	p := profileParamsFrom(a)
	pr, err := features.ProfileFromParams(features.Params(p))
	if err != nil {
		return nil, err
	}
	id, err := uniqueID(env.Graph(), a, "profile")
	if err != nil {
		return nil, err
	}
	n := newNode(env, a, id, features.ProfileType, p)
	return addPlan(intentOr(a, "Create profile"),
		fmt.Sprintf("Creates a %s profile (area %s mm²).", a.Str("shape"), fmtMM(pr.Area())), n), nil
}

func planUpdateProfile(env Env, a Args) (*Plan, error) {
	n, err := requireProfile(env, a.Str("profile"))
	if err != nil {
		return nil, err
	}
	merged := map[string]any{}
	for k, v := range n.Parameters {
		merged[k] = v
	}
	changes := profileParamsFrom(a)
	if len(changes) == 0 {
		return nil, fmt.Errorf("give at least one parameter to change")
	}
	var keys []string
	for k, v := range changes {
		if !reflect.DeepEqual(normalizeJSON(merged[k]), normalizeJSON(v)) {
			merged[k] = v
			keys = append(keys, k)
		}
	}
	if len(keys) == 0 {
		return nil, fmt.Errorf("the update leaves profile %s unchanged", n.ID)
	}
	if _, err := features.ProfileFromParams(features.Params(merged)); err != nil {
		return nil, err
	}
	sort.Strings(keys)
	plan := &Plan{Intent: intentOr(a, "Edit profile "+n.ID), Explanation: fmt.Sprintf("Updates profile %s (%v).", n.ID, keys)}
	for _, k := range keys {
		if _, driven := n.Expressions[k]; driven {
			plan.Commands = append(plan.Commands, &commands.SetExpression{ID: n.ID, Name: k, Delete: true})
		}
		plan.Commands = append(plan.Commands, &commands.SetParameter{ID: n.ID, Name: k, Value: changes[k]})
	}
	return plan, nil
}
