package tools

import (
	"fmt"
	"math"
	"strings"

	"github.com/accelone/simoncad/internal/commands"
	"github.com/accelone/simoncad/internal/transform"
	"github.com/accelone/simoncad/internal/workspace"
)

// The TransformService tools: move and rotate one object (a visible body) in world
// coordinates. They are what the viewport's interactive Move (G) and Rotate (R) commit and
// what the agent calls, so a drag and a typed command write the same CAD-IR: a `transform`
// feature on the object's body, or — for a component instance — its own placement. Either way
// one call is one ChangeSet, so one undo step.

// IsTransformTool reports whether name moves or rotates objects (a transform-completed event
// follows each committed call).
func IsTransformTool(name string) bool {
	switch name {
	case "transform", "move_object", "rotate_object":
		return true
	}
	return false
}

// IsDirectTransformTool reports whether name is a direct-manipulation transform: from the UI it
// is applied at once (it stays undoable), like a typed dimension.
func IsDirectTransformTool(name string) bool { return name == "move_object" || name == "rotate_object" }

// ObjectTransform is getObjectTransform: where a visible body is, and whether and how it can
// be moved now.
func ObjectTransform(env Env, id string) (*transform.Object, error) {
	if _, err := requireLeaf(env, id); err != nil {
		return nil, err
	}
	m, err := env.Model()
	if err != nil {
		return nil, err
	}
	o, err := transform.Get(env.Graph(), m, id)
	if err != nil {
		return nil, err
	}
	if v := env.Visual(); v != nil && o.Transformable {
		if r := v.LockedObject(id); r != "" {
			o.Transformable, o.Reason = false, fmt.Sprintf("it belongs to the locked region %q", r)
		}
	}
	return o, nil
}

// TranslateObject is translateObject: a plan moving object id by delta (mm, world). With snap,
// the project's SnapService snaps the move of the object's origin on the free axes.
func TranslateObject(env Env, a Args, id string, delta [3]float64, free [3]bool, snap bool) (*Plan, error) {
	o, err := ObjectTransform(env, id)
	if err != nil {
		return nil, err
	}
	if snap {
		delta, _ = workspace.SnapMove(o.Origin, delta, free, workspace.Context{Settings: env.Workspace()})
	}
	for i := range delta {
		if !free[i] {
			delta[i] = 0
		}
		delta[i] = transform.Round(delta[i], 6)
	}
	u := env.Workspace().Units
	what := make([]string, 0, 3)
	for i, v := range delta {
		if v != 0 {
			what = append(what, fmt.Sprintf("%c %s", "XYZ"[i], signed(u.FormatLength(v), v)))
		}
	}
	if len(what) == 0 {
		return nil, fmt.Errorf("nothing would change: the move is 0")
	}
	to := [3]float64{o.Origin[0] + delta[0], o.Origin[1] + delta[1], o.Origin[2] + delta[2]}
	return motionPlan(env, a, o, transform.Translation(delta), o.Origin, "move",
		intentOr(a, fmt.Sprintf("Move %s by %s", id, strings.Join(what, ", "))),
		fmt.Sprintf("Moves %q by %s; its origin goes to (%s).", id, strings.Join(what, ", "), formatPoint(u, to)))
}

// RotateObject is rotateObject: a plan rotating object id by rad radians about axis through
// the pivot (origin, center or an explicit point). With snap, the angle is snapped to the
// project's rotation increment.
func RotateObject(env Env, a Args, id string, rad float64, axis [3]float64, pivot string, point [3]float64, snap bool) (*Plan, error) {
	o, err := ObjectTransform(env, id)
	if err != nil {
		return nil, err
	}
	n := math.Sqrt(axis[0]*axis[0] + axis[1]*axis[1] + axis[2]*axis[2])
	if n < 1e-9 {
		return nil, fmt.Errorf("the rotation axis must be non-zero")
	}
	axis = [3]float64{axis[0] / n, axis[1] / n, axis[2] / n}
	if snap {
		rad, _ = workspace.SnapAngle(rad, workspace.Context{Settings: env.Workspace()})
	}
	// Whole turns change nothing; what is stored is the equivalent angle in (-180°, 180°].
	rad = workspace.NormalizeAngle(rad)
	if math.Abs(rad) < 1e-12 {
		return nil, fmt.Errorf("nothing would change: the rotation is 0 (or a whole number of turns)")
	}
	p, err := o.PivotPointOf(pivot, point)
	if err != nil {
		return nil, err
	}
	u := env.Workspace().Units
	about := axisName(axis)
	where := map[string]string{"": "origin", transform.PivotOrigin: "origin", transform.PivotCenter: "center"}[pivot]
	if where == "" {
		where = "(" + formatPoint(u, p) + ")"
	}
	return motionPlan(env, a, o, transform.Rotation(axis, rad, p), p, "rotate",
		intentOr(a, fmt.Sprintf("Rotate %s %s about %s", id, u.FormatAngle(rad), about)),
		fmt.Sprintf("Rotates %q by %s about %s through its %s.", id, u.FormatAngle(rad), about, where))
}

// motionPlan records motion m for object o: a component instance's placement is composed with
// it; any other body gets a `transform` feature. pivot is the point the parameters are written
// about (the rotation pivot), which keeps them readable.
func motionPlan(env Env, a Args, o *transform.Object, m transform.Rigid, pivot [3]float64, base, intent, explanation string) (*Plan, error) {
	if !o.Transformable {
		return nil, fmt.Errorf("%q cannot be moved: %s", o.ID, o.Reason)
	}
	p := &Plan{Intent: intent, Explanation: explanation, LowRisk: true}
	if o.Write == transform.WriteInstance {
		mdl, err := env.Model()
		if err != nil {
			return nil, err
		}
		next := transform.Compose(transform.FromParams(mdl.Nodes[o.ID].Params), m, pivot).Params()
		n := env.Graph().Get(o.ID)
		for _, k := range transform.Keys {
			cur, has := n.Parameters[k]
			v, want := next[k]
			switch {
			case want && (!has || cur != any(v)):
				p.Commands = append(p.Commands, &commands.SetParameter{ID: o.ID, Name: k, Value: v})
			case !want && has:
				p.Commands = append(p.Commands, &commands.SetParameter{ID: o.ID, Name: k, Delete: true})
			}
		}
		if len(p.Commands) == 0 {
			return nil, fmt.Errorf("nothing would change")
		}
		p.Explanation += " The component instance's placement is updated; the component itself is unchanged."
		return p, nil
	}
	id, err := uniqueID(env.Graph(), a, base)
	if err != nil {
		return nil, err
	}
	params := map[string]any{"target": o.ID}
	for k, v := range transform.PlacementOf(m, pivot).Params() {
		params[k] = v
	}
	p.Commands = []commands.Command{&commands.AddFeature{Node: newNode(env, a, id, "transform", params, o.ID), Index: -1}}
	return p, nil
}

func signed(s string, v float64) string {
	if v > 0 {
		return "+" + s
	}
	return s
}

func formatPoint(u workspace.Units, p [3]float64) string {
	return fmt.Sprintf("%s, %s, %s", u.FormatLength(p[0]), u.FormatLength(p[1]), u.FormatLength(p[2]))
}

func axisName(axis [3]float64) string {
	for i, n := range []string{"X", "Y", "Z"} {
		if math.Abs(axis[i]-1) < 1e-12 {
			return n
		}
		if math.Abs(axis[i]+1) < 1e-12 {
			return "-" + n
		}
	}
	return fmt.Sprintf("the axis (%s, %s, %s)", fmtMM(axis[0]), fmtMM(axis[1]), fmtMM(axis[2]))
}

// lengths reads the lengths named keys (in the call's unit) as mm; set says which were given.
func lengths(a Args, keys [3]string) (v [3]float64, set [3]bool, err error) {
	unit := orDefault(a.Str("unit"), workspace.MM)
	for i, k := range keys {
		if !a.Has(k) {
			continue
		}
		if v[i], err = workspace.ToMM(a.Num(k), unit); err != nil {
			return v, set, err
		}
		set[i] = true
	}
	return v, set, nil
}

// ObjectTransformView is get_object_transform's answer: the transform plus the same values in
// the project's display units.
type ObjectTransformView struct {
	*transform.Object
	Display map[string]string `json:"display"`
}

func transformTools() []*Tool {
	snapProp := boolean("Apply the project's snapping settings (grid / translation increment, or the rotation increment); only when snapping is enabled")
	return []*Tool{
		{
			Name: "get_object_transform",
			Description: "Where an object is: its origin and box center (world mm), its orientation (local axes and one rotation axis/angle in degrees), " +
				"whether it can be moved now (and why not) and how a move is recorded (a transform feature, or a component instance's placement). " +
				"Use it before an absolute move or to pick a rotation pivot.",
			Capability: capGeometry,
			Schema:     object(props{"object_id": objectProp}),
			Query: func(env Env, a Args) (any, error) {
				id, err := objectID(env, a)
				if err != nil {
					return nil, err
				}
				o, err := ObjectTransform(env, id)
				if err != nil {
					return nil, err
				}
				u := env.Workspace().Units
				return ObjectTransformView{Object: o, Display: map[string]string{
					"origin": formatPoint(u, o.Origin), "center": formatPoint(u, o.Center),
					"rotation": u.FormatAngle(o.AngleDeg * math.Pi / 180)}}, nil
			},
		},
		{
			Name: "move_object",
			Description: "Move one object in world coordinates, as one undoable change. Relative: dx/dy/dz ('move it 20 mm along X' → dx=20). " +
				"Absolute: x/y/z place the object's origin there ('move it to X=100, Y=50' → x=100, y=50; omitted axes stay). Don't mix both on one axis. " +
				"Values are in `unit` (default mm). snap=true applies the project's grid/translation snapping to the moved axes. " +
				"A component instance's placement is updated; the component itself never changes. Omit object_id to move the selected object.",
			Capability: capGeometry,
			Schema: object(withMeta(props{
				"object_id": objectProp,
				"dx":        number("Move along X"), "dy": number("Move along Y"), "dz": number("Move along Z"),
				"x": number("Absolute X of the object's origin"), "y": number("Absolute Y of the object's origin"), "z": number("Absolute Z of the object's origin"),
				"unit": lengthUnitProp,
				"snap": snapProp,
			})),
			Plan: func(env Env, a Args) (*Plan, error) {
				id, err := objectID(env, a)
				if err != nil {
					return nil, err
				}
				rel, relSet, err := lengths(a, [3]string{"dx", "dy", "dz"})
				if err != nil {
					return nil, err
				}
				abs, absSet, err := lengths(a, [3]string{"x", "y", "z"})
				if err != nil {
					return nil, err
				}
				var free [3]bool
				delta := rel
				var origin [3]float64
				if absSet != [3]bool{} {
					o, err := ObjectTransform(env, id)
					if err != nil {
						return nil, err
					}
					origin = o.Origin
				}
				for i := range free {
					if relSet[i] && absSet[i] {
						return nil, fmt.Errorf("give either d%c or %c, not both", "xyz"[i], "xyz"[i])
					}
					if absSet[i] {
						delta[i] = abs[i] - origin[i]
					}
					free[i] = relSet[i] || absSet[i]
				}
				if free == [3]bool{} {
					return nil, fmt.Errorf("give a move (dx/dy/dz) or a position (x/y/z)")
				}
				return TranslateObject(env, a, id, delta, free, a.Bool("snap"))
			},
		},
		{
			Name: "rotate_object",
			Description: "Rotate one object about a world axis, as one undoable change ('rotate it 45 degrees around Z' → angle=45, axis=z). " +
				"`angle` is in angle_unit (default deg; positive = counter-clockwise looking down the axis). axis defaults to the normal of the workspace grid plane. " +
				"pivot: origin (default, the object's own origin), center (its box center) or point (`point`, in `unit`). snap=true snaps the angle to the rotation increment. " +
				"A component instance's placement is updated; the component itself never changes. Omit object_id to rotate the selected object.",
			Capability: capGeometry,
			Schema: object(withMeta(props{
				"object_id":   objectProp,
				"angle":       number("Rotation angle"),
				"angle_unit":  angleUnitProp,
				"axis":        enum("World axis to rotate about; default: the grid plane's normal", "x", "y", "z"),
				"axis_vector": pointProp,
				"pivot":       enum("Fixed point of the rotation", transform.Pivots...),
				"point":       pointProp,
				"unit":        lengthUnitProp,
				"snap":        snapProp,
			}), "angle"),
			Plan: func(env Env, a Args) (*Plan, error) {
				id, err := objectID(env, a)
				if err != nil {
					return nil, err
				}
				rad, err := workspace.ToRadians(a.Num("angle"), orDefault(a.Str("angle_unit"), workspace.Deg))
				if err != nil {
					return nil, err
				}
				axis := [3]float64{}
				axis[workspace.PlaneAxes(env.Workspace().Grid.Plane)[2]] = 1
				switch {
				case a.Has("axis") && a.Has("axis_vector"):
					return nil, fmt.Errorf("give either axis or axis_vector, not both")
				case a.Has("axis"):
					axis, _ = transform.AxisVector(a.Str("axis"))
				case a.Has("axis_vector"):
					axis = vec3(a["axis_vector"])
				}
				pivot := a.Str("pivot")
				var point [3]float64
				if a.Has("point") {
					if pivot == "" {
						pivot = transform.PivotPoint
					}
					if pivot != transform.PivotPoint {
						return nil, fmt.Errorf("`point` is only used with pivot=point")
					}
					unit := orDefault(a.Str("unit"), workspace.MM)
					for i, v := range vec3(a["point"]) {
						if point[i], err = workspace.ToMM(v, unit); err != nil {
							return nil, err
						}
					}
				} else if pivot == transform.PivotPoint {
					return nil, fmt.Errorf("pivot=point needs `point`")
				}
				return RotateObject(env, a, id, rad, axis, pivot, point, a.Bool("snap"))
			},
		},
	}
}

func vec3(v any) [3]float64 {
	var out [3]float64
	list, _ := v.([]any)
	for i := 0; i < 3 && i < len(list); i++ {
		out[i], _ = list[i].(float64)
	}
	return out
}
