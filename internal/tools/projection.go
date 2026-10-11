package tools

import (
	"errors"
	"fmt"
	"math"
	"reflect"
	"sort"
	"strings"

	"github.com/accelone/simoncad/internal/commands"

	"github.com/accelone/simoncad/internal/geometry/curve"
	"github.com/accelone/simoncad/internal/geometry/features"
	"github.com/accelone/simoncad/internal/geometry/project"
)

func projectionTools() []*Tool {
	return []*Tool{{
		Name: "project_path_to_surface",
		Description: "Drop a path onto the surface of a body so it lies on it (the first step to engrave, emboss or run a bead along a curved part). `path` is an existing path (or give inline `points`); " +
			"`target` the body. mode=planar casts each control point along `dir` (e.g. [-1,0,0] looks at the part from +X), keeping the point count; mode=normal moves each point to the nearest surface point (no `dir`); " +
			"mode=conform is planar plus extra points wherever a straight chord would leave the surface by more than `tolerance` mm, so the result hugs a cylinder or sphere. `offset` lifts the result along the surface normal (negative sinks it). " +
			"Creates a NEW path (the original is kept) that is a snapshot of the projection: re-run the tool if the body changes. The answer gives the mean surface_normal, to pass to engrave_surface. " +
			"Fails with SURFACE_PROJECTION_FAILED listing the points that miss the surface.",
		Capability: capGeometry,
		Schema: object(withMeta(props{
			"path":       text("Existing path feature to project"),
			"points":     array("Inline control points [[x,y,z], ...] instead of `path`", pointSchema, 2, 5000),
			"path_type":  enum("Interpolation of an inline path (default catmull_rom)", pathTypeEnum...),
			"target":     text("Body to project onto"),
			"mode":       enum("planar (default), normal or conform", "planar", "normal", "conform"),
			"dir":        array("Projection direction [x,y,z] (planar, conform)", number("component"), 3, 3),
			"offset":     number("Lift along the surface normal in mm (default 0)"),
			"tolerance":  positive("conform: largest chord-to-surface gap in mm (default 0.05)"),
			"deflection": positive("Mesh deflection of the target used for the projection in mm (default 0.02)"),
		}), "target"),
		Plan: planProjectPath,
	}, {
		Name: "refresh_projection",
		Description: "Re-run the projection of a path made by project_path_to_surface with the same settings: use it after the target body or the source path changed, so the projected path follows. " +
			"Everything built on the projected path (engravings, sweeps) regenerates. One undo step.",
		Capability: capGeometry,
		Schema:     object(props{"path": text("The projected path feature"), "intent": text("Why")}, "path"),
		Plan:       planRefreshProjection,
	}}
}

func planRefreshProjection(env Env, a Args) (*Plan, error) {
	n, err := requirePath(env, a.Str("path"))
	if err != nil {
		return nil, err
	}
	rec, ok := n.Parameters["projection"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%q was not made by project_path_to_surface", n.ID)
	}
	args := Args{"id": n.ID + "_refresh_tmp"}
	for k, v := range rec {
		switch k {
		case "source":
			args["path"] = v
		default:
			args[k] = v
		}
	}
	inner, err := planProjectPath(env, args)
	if err != nil {
		return nil, err
	}
	var fresh map[string]any
	for _, c := range inner.Commands {
		if af, ok := c.(*commands.AddFeature); ok {
			fresh = af.Node.Parameters
		}
	}
	if fresh == nil {
		return nil, fmt.Errorf("the projection produced no path")
	}
	plan := &Plan{Intent: intentOr(a, "Refresh projection "+n.ID), Explanation: "Re-projects " + n.ID + " onto " + fmt.Sprint(rec["target"]) + ".", Warnings: inner.Warnings}
	cur, next := normalizeJSON(n.Parameters).(map[string]any), normalizeJSON(fresh).(map[string]any)
	for _, k := range []string{"type", "closed", "points"} {
		if !reflect.DeepEqual(cur[k], next[k]) {
			plan.Commands = append(plan.Commands, &commands.SetParameter{ID: n.ID, Name: k, Value: next[k]})
		}
	}
	if len(plan.Commands) == 0 {
		return nil, fmt.Errorf("%s is already up to date with %v", n.ID, rec["target"])
	}
	plan.LowRisk = len(plan.Warnings) == 0
	return plan, nil
}

func planProjectPath(env Env, a Args) (*Plan, error) {
	if a.Has("path") == a.Has("points") {
		return nil, fmt.Errorf("give either `path` (an existing path) or `points`")
	}
	if _, err := requireBounds(env, a.Str("target")); err != nil {
		return nil, err
	}
	mode := project.Mode(orDefault(a.Str("mode"), "planar"))
	var dir project.Vec
	if mode != project.ModeNormal {
		v, ok := a["dir"].([]any)
		if !ok {
			return nil, fmt.Errorf("mode %s needs `dir`, the direction to look along (e.g. [0,0,-1] to project down onto a top face)", mode)
		}
		dir = project.Vec(vecArg(v))
	} else if a.Has("dir") {
		return nil, fmt.Errorf("mode normal goes to the nearest surface point; `dir` does not apply")
	}

	// the source path
	var src *curve.Path
	srcName := "inline points"
	if a.Has("path") {
		n, err := requirePath(env, a.Str("path"))
		if err != nil {
			return nil, err
		}
		if src, err = features.PathFromParams(features.Params(n.Parameters)); err != nil {
			return nil, err
		}
		srcName = n.ID
	} else {
		var err error
		src, err = features.PathFromParams(features.Params{"points": a["points"], "type": orDefault(a.Str("path_type"), "catmull_rom")})
		if err != nil {
			return nil, err
		}
	}
	if len(src.Points) < 2 {
		return nil, fmt.Errorf("a path needs at least 2 points")
	}

	m, err := env.Model()
	if err != nil {
		return nil, err
	}
	body := m.Nodes[a.Str("target")].Body
	if body == nil || body.Shape == nil {
		return nil, fmt.Errorf("%q has no solid to project onto", a.Str("target"))
	}
	mesh, err := body.Shape.Tessellate(a.NumOr("deflection", 0.02))
	if err != nil {
		return nil, fmt.Errorf("meshing %s: %v", a.Str("target"), err)
	}
	surf, err := project.New(mesh.Positions, mesh.Normals, mesh.Indices)
	if err != nil {
		return nil, err
	}

	// what is projected: the control points, or for conform a dense polyline of the whole curve
	tol := a.NumOr("tolerance", 0.05)
	var in []project.Vec
	var inSrc []int // index of the control point each input belongs to
	if mode == project.ModeConform {
		cv, err := curve.New(src)
		if err != nil {
			return nil, features.Coded(features.CodePathInvalid, "", "%v", err)
		}
		for _, p := range cv.Resample(math.Max(tol, 0.05)) {
			in = append(in, project.Vec(p))
		}
		if src.Closed && len(in) > 0 {
			in = append(in, in[0])
		}
		for _, p := range src.Points {
			if p.W != 0 || p.H != 0 || p.Twist != 0 {
				return nil, fmt.Errorf("path %s has per-point width/height/twist, which conform projection cannot carry across the extra points: use mode planar, or clear them first", srcName)
			}
		}
	} else {
		for i, p := range src.Points {
			in = append(in, project.Vec(p.P))
			inSrc = append(inSrc, i)
		}
	}
	res, err := surf.Project(in, project.Options{Mode: mode, Dir: dir, Offset: a.Num("offset"), Tol: tol})
	if err != nil {
		var fe *project.FailError
		if errors.As(err, &fe) {
			names := make([]string, 0, len(fe.Indices))
			for _, i := range fe.Indices {
				if len(names) < 8 {
					names = append(names, fmt.Sprint(i))
				}
			}
			hint := "check `dir`: the rays must cross the body (look at it from the side the path is on), or use mode=normal"
			if mode == project.ModeNormal {
				hint = "the target has no surface near those points"
			}
			return nil, features.Coded(features.CodeProjectionFailed, hint,
				"%d of %d point(s) do not reach %s (index %s)", len(fe.Indices), len(in), a.Str("target"), strings.Join(names, ", "))
		}
		return nil, err
	}

	// the projected path
	out := &curve.Path{Type: src.Type, Closed: src.Closed}
	if mode == project.ModeConform {
		out.Type = curve.CatmullRom
		pts := res.Points
		if src.Closed && len(pts) > 1 { // the closing duplicate point is not a control point
			pts = pts[:len(pts)-1]
		}
		for _, p := range pts {
			out.Points = append(out.Points, curve.Point{P: curve.Vec(p)})
		}
	} else {
		for i, p := range res.Points {
			pt := src.Points[inSrc[i]]
			pt.P = curve.Vec(p)
			pt.In, pt.Out = nil, nil // tangent handles belong to the old shape
			out.Points = append(out.Points, pt)
		}
		if src.Type == curve.Bezier {
			out.Type = curve.CatmullRom
		}
	}
	if err := out.Validate(); err != nil {
		return nil, features.Coded(features.CodePathInvalid, "the projection collapsed the path (points merged)", "%v", err)
	}
	params := normalizeJSON(features.PathToParams(out)).(map[string]any)
	// the recipe that refresh_projection replays when the body (or the source path) changes
	rec := map[string]any{"target": a.Str("target"), "mode": string(mode), "offset": a.Num("offset")}
	if a.Has("path") {
		rec["source"] = a.Str("path")
	} else {
		rec["points"], rec["path_type"] = a["points"], orDefault(a.Str("path_type"), "catmull_rom")
	}
	for _, k := range []string{"dir", "tolerance", "deflection"} {
		if a.Has(k) {
			rec[k] = a[k]
		}
	}
	params["projection"] = rec

	base := a.Str("id")
	if base == "" && a.Has("path") {
		base = a.Str("path") + "_projected"
	}
	id, err := uniqueID(env.Graph(), Args{"id": base, "name": a.Str("name")}, "projected_path")
	if err != nil {
		return nil, err
	}
	node := newNode(env, a, id, features.PathType, params)

	var mean project.Vec
	for _, n := range res.Normals {
		mean = project.Vec{mean[0] + n[0], mean[1] + n[1], mean[2] + n[2]}
	}
	ml := math.Sqrt(mean[0]*mean[0] + mean[1]*mean[1] + mean[2]*mean[2])
	var warns []string
	if ml > 1e-9 {
		mean = project.Vec{mean[0] / ml, mean[1] / ml, mean[2] / ml}
		// the spread of the normals tells the agent whether a single surface_normal is a fair summary
		worst := 1.0
		for _, n := range res.Normals {
			worst = math.Min(worst, n[0]*mean[0]+n[1]*mean[1]+n[2]*mean[2])
		}
		if worst < 0.94 { // > 20°
			warns = append(warns, fmt.Sprintf("the surface normal turns by up to %.0f° along the path: a profile swept along it uses one fixed up direction (surface_normal), so wide grooves distort; keep the path short or the groove narrow",
				math.Acos(math.Max(-1, worst))*180/math.Pi))
		}
	}
	cv, _ := curve.New(out)
	length := 0.0
	if cv != nil {
		length = cv.Length()
	}
	sort.Strings(warns)
	plan := addPlan(intentOr(a, fmt.Sprintf("Project %s onto %s", srcName, a.Str("target"))),
		fmt.Sprintf("Projects %s onto %s (%s): %d point(s), %s mm long, mean surface normal [%s, %s, %s].",
			srcName, a.Str("target"), mode, len(out.Points), fmtMM(length), fmtMM(mean[0]), fmtMM(mean[1]), fmtMM(mean[2])), node)
	plan.Warnings = warns
	plan.LowRisk = len(warns) == 0
	return plan, nil
}
