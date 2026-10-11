package tools

import (
	"fmt"
	"math"

	"github.com/accelone/simoncad/internal/cadir"
	"github.com/accelone/simoncad/internal/geometry/curve"
	"github.com/accelone/simoncad/internal/geometry/features"
)

func reliefTools() []*Tool {
	return []*Tool{
		{
			Name: "create_relief",
			Description: "Raise (emboss) or recess (deboss / engrave) a shape on an existing body, keeping both editable. `source` is a sketch (its regions are extruded from the sketch plane: " +
				"emboss grows along the sketch normal, deboss/engrave cuts into the material on the other side; `reverse` flips that) or a solid such as a sweep. `depth` is the height of a raised relief or the depth of a cut; " +
				"`offset` slides the start along the sketch normal. `mirror` flips a sketch about its vertical axis (for stamps: the relief must be mirrored so the impression reads correctly). " +
				"The sketch must lie on the target's surface (draw it on a face, or on a plane at the surface height). Fails with RELIEF_DISCONNECTED when a raised shape floats off the target and RELIEF_NO_EFFECT when nothing changes.",
			Capability: capGeometry,
			Schema: object(withMeta(props{
				"source":  text("Sketch or solid feature id to emboss / cut"),
				"target":  text("Body to add to / cut into"),
				"op":      enum("emboss adds material; deboss and engrave remove it", "emboss", "deboss", "engrave"),
				"depth":   positive("Relief height or cut depth in mm (sketch sources)"),
				"offset":  number("Start offset along the sketch normal (mm)"),
				"region":  text("Only this sketch region (default: all)"),
				"mirror":  boolean("Mirror the sketch about its vertical axis (sketch sources)"),
				"reverse": boolean("Flip the direction relative to the sketch normal"),
			}), "source", "target", "op"),
			Plan: planCreateRelief,
		},
		{
			Name: "engrave_surface",
			Description: "Engrave a groove along a path into a body (or raise a bead along it with op=emboss). The path must lie on the surface: give `points` (inline) or an existing `path`. " +
				"`width` is the groove width, `depth` its depth (default: half the width for round); style round (half-round groove), v (V groove) or flat (rectangular). " +
				"`surface_normal` is the outward normal of the surface the path lies on (default [0,0,1], a top face). Creates the profile, the sweep and the relief in one step; all stay editable.",
			Capability: capGeometry,
			Schema: object(withMeta(sweepOptionProps(pathSourceProps(props{
				"target":         text("Body to engrave"),
				"width":          positive("Groove width (mm)"),
				"depth":          positive("Groove depth (mm); default half the width"),
				"style":          enum("Groove shape (default round)", "round", "v", "flat"),
				"op":             enum("engrave (default) cuts a groove; emboss raises a bead", "engrave", "emboss"),
				"surface_normal": array("Outward normal of the surface the path lies on [x,y,z] (default [0,0,1])", number("component"), 3, 3),
			}))), "target", "width"),
			Plan: planEngraveSurface,
		},
	}
}

func planCreateRelief(env Env, a Args) (*Plan, error) {
	src, err := requireFeature(env, a.Str("source"))
	if err != nil {
		return nil, err
	}
	if src.Type == features.PathType || src.Type == features.ProfileType {
		return nil, fmt.Errorf("%q is a %s, which has no volume: sweep it first (create_sweep / engrave_surface) or use a sketch", src.ID, src.Type)
	}
	if _, err := requireBounds(env, a.Str("target")); err != nil {
		return nil, err
	}
	if a.Str("target") == src.ID {
		return nil, fmt.Errorf("source and target must be different features")
	}
	if src.Type == "sketch" && !a.Has("depth") {
		return nil, fmt.Errorf("a sketch source needs `depth` (the relief height or cut depth)")
	}
	if src.Type != "sketch" && (a.Bool("mirror") || a.Has("offset") || a.Has("region")) {
		return nil, fmt.Errorf("mirror, offset and region apply to sketch sources")
	}
	p := map[string]any{"source": src.ID, "target": a.Str("target"), "op": a.Str("op")}
	copyOpt(p, a, "depth", "offset", "region", "mirror", "reverse")
	id, err := uniqueID(env.Graph(), a, a.Str("op"))
	if err != nil {
		return nil, err
	}
	n := newNode(env, a, id, features.ReliefType, p, src.ID, a.Str("target"))
	return addPlan(intentOr(a, fmt.Sprintf("%s %s on %s", a.Str("op"), src.ID, a.Str("target"))),
		fmt.Sprintf("%ss %s on %s.", a.Str("op"), src.ID, a.Str("target")), n), nil
}

// grooveProfile is the cross-section of an engraving or bead in the sweep's local frame: +Y is the
// working direction (into the material for a groove, out of it for a bead), y=0 the surface. The
// section also pokes `ext` the other way so it overlaps the surface instead of just touching it.
func grooveProfile(style string, width, depth float64) map[string]any {
	const ext = 0.3
	r := width / 2
	pt := func(x, y float64) []any { return []any{round6(x), round6(y)} }
	var pts []any
	switch style {
	case "v":
		pts = []any{pt(-r, -ext), pt(-r, 0), pt(0, depth), pt(r, 0), pt(r, -ext)}
	case "flat":
		pts = []any{pt(-r, -ext), pt(-r, depth), pt(r, depth), pt(r, -ext)}
	default: // half-round
		pts = []any{pt(-r, -ext)}
		const steps = 24
		for i := 0; i <= steps; i++ {
			a := math.Pi * float64(i) / steps
			pts = append(pts, pt(-r*math.Cos(a), depth*math.Sin(a)))
		}
		pts = append(pts, pt(r, -ext))
	}
	return map[string]any{"shape": "custom", "contours": []any{map[string]any{"points": pts}}}
}

func planEngraveSurface(env Env, a Args) (*Plan, error) {
	if _, err := requireBounds(env, a.Str("target")); err != nil {
		return nil, err
	}
	style := orDefault(a.Str("style"), "round")
	op := orDefault(a.Str("op"), "engrave")
	width := a.Num("width")
	depth := a.NumOr("depth", width/2)
	if style == "round" && !a.Has("depth") {
		depth = width / 2
	}
	id, err := uniqueID(env.Graph(), a, op)
	if err != nil {
		return nil, err
	}
	taken := map[string]bool{id: true}
	pathID, pathNode, pathParams, err := resolveSpine(env, a, id, taken)
	if err != nil {
		return nil, err
	}
	// orient the profile: its +Y axis points into the material (groove) or out of it (bead)
	nrm := curve.Vec{0, 0, 1}
	if v, ok := a["surface_normal"].([]any); ok {
		nrm = vecArg(v)
	}
	if l := math.Sqrt(nrm[0]*nrm[0] + nrm[1]*nrm[1] + nrm[2]*nrm[2]); l < 1e-9 {
		return nil, fmt.Errorf("surface_normal must not be zero")
	} else {
		nrm = curve.Vec{nrm[0] / l, nrm[1] / l, nrm[2] / l}
	}
	pa, err := features.PathFromParams(features.Params(pathParams))
	if err != nil {
		return nil, err
	}
	cv, err := curve.New(pa)
	if err != nil {
		return nil, err
	}
	t0 := cv.At(0).T
	cross := func(u, v curve.Vec) curve.Vec {
		return curve.Vec{u[1]*v[2] - u[2]*v[1], u[2]*v[0] - u[0]*v[2], u[0]*v[1] - u[1]*v[0]}
	}
	seed := cross(t0, nrm) // N = T × n  →  B = T × N points into the material
	if op == "emboss" {
		seed = cross(nrm, t0) // N = n × T  →  B = n: out of the material
	}
	if math.Sqrt(seed[0]*seed[0]+seed[1]*seed[1]+seed[2]*seed[2]) < 1e-6 {
		return nil, fmt.Errorf("the path starts along the surface normal: it must run along the surface")
	}
	prof := grooveProfile(style, width, depth)
	profID := derivedID(env, id, "_profile", taken)
	sweepID := derivedID(env, id, "_sweep", taken)
	if _, err := features.ProfileFromParams(features.Params(prof)); err != nil {
		return nil, err
	}
	sp := map[string]any{"path": pathID, "profile": profID, "up": []any{round6(seed[0]), round6(seed[1]), round6(seed[2])}}
	copyOpt(sp, a, "tolerance", "max_sections")
	plan, err := features.PlanSweep(features.Params(pathParams), features.Params(prof), features.Params(sp))
	if err != nil {
		return nil, err
	}
	var nodes []*cadir.Node
	sub := subArgs(a)
	if pathNode != nil {
		nodes = append(nodes, pathNode)
	}
	nodes = append(nodes, newNode(env, sub, profID, features.ProfileType, prof))
	sweepNode := newNode(env, sub, sweepID, features.SweepType, sp, pathID, profID)
	nodes = append(nodes, sweepNode)
	reliefOp := "engrave"
	if op == "emboss" {
		reliefOp = "emboss"
	}
	rp := map[string]any{"source": sweepID, "target": a.Str("target"), "op": reliefOp}
	nodes = append(nodes, newNode(env, a, id, features.ReliefType, rp, sweepID, a.Str("target")))
	verb := map[string]string{"engrave": "Engraves", "emboss": "Raises"}[op]
	out := addPlan(intentOr(a, fmt.Sprintf("%s a %s groove along %s", op, style, pathID)),
		fmt.Sprintf("%s a %s %s mm wide, %s mm deep along %s on %s (%s).", verb, style, fmtMM(width), fmtMM(depth), pathID, a.Str("target"), describePlan(plan)), nodes...)
	out.Warnings = warningsOf(plan)
	out.LowRisk = true
	return out, nil
}
