package tools

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/accelone/simoncad/internal/commands"
	"github.com/accelone/simoncad/internal/dimensions"
	"github.com/accelone/simoncad/internal/geometry/features"
	"github.com/accelone/simoncad/internal/selectors"
)

// Interactive dimensions: measure an object along its local axes and resize it to a given
// width, depth or height. Measuring is the DimensionService (internal/dimensions); a resize
// is a MeshResizeService plan: the factors come from dimensions.Factors and become an ordinary
// `scale` feature (in the object's local frame), so it is previewable, undoable and replayable
// like every other edit, and other objects are never touched.

// resizeTag marks a scale feature created by a dimension edit. Consecutive edits of the same
// object fold into it instead of stacking one scale per keystroke.
const resizeTag = "resize"

// IsDimensionEditTool reports whether name is a direct dimension edit. Like a design-tree
// parameter edit it is applied at once from the UI (and stays undoable).
func IsDimensionEditTool(name string) bool {
	switch name {
	case "set_object_dimension", "resize_object", "set_object_proportional_scaling":
		return true
	}
	return false
}

var objectProp = text("Visible body (feature id). Omit it to use the selected object.")
var anchorProp = enum("Fixed point of the resize: center (default, the center of the object's box) or object_origin (the object's own origin, e.g. a box's placement corner)", dimensions.Anchors...)

func dimensionTools() []*Tool {
	return []*Tool{
		{
			Name: "get_object_dimensions",
			Description: "Measure an object: width, depth and height in mm along its local X, Y and Z axes (Z up; the axes follow the object's own rotation), " +
				"its local axes, box, center, origin and whether proportional scaling is locked. Use it to check a resize.",
			Capability: capGeometry,
			Schema:     object(props{"object_id": objectProp}),
			Query: func(env Env, a Args) (any, error) {
				id, err := objectID(env, a)
				if err != nil {
					return nil, err
				}
				return ObjectDimensions(env, id)
			},
		},
		{
			Name: "set_object_dimension",
			Description: "Set one dimension of an object (e.g. 'make it 150 mm wide'). Only that dimension changes, unless the object has proportional scaling locked " +
				"(then the other two follow; `proportional` overrides the lock for this call). Non-parametric: the body is scaled along its local axis, so a cylinder " +
				"resized across its axis becomes elliptical, and holes or wall thicknesses scale with it.",
			Capability: capGeometry,
			Schema: object(props{
				"object_id":    objectProp,
				"axis":         enum("Which dimension: width (local X), depth (local Y) or height (local Z); x/y/z are accepted", dimensions.AxisNames...),
				"value":        positive("New size in mm"),
				"anchor":       anchorProp,
				"proportional": boolean("Scale the other two dimensions proportionally; defaults to the object's Lock Proportions setting"),
				"intent":       text("Why"),
			}, "axis", "value"),
			Plan: func(env Env, a Args) (*Plan, error) {
				id, err := objectID(env, a)
				if err != nil {
					return nil, err
				}
				i, _ := dimensions.AxisIndex(a.Str("axis"))
				prop := env.Graph().Get(id).LockProportions
				if a.Has("proportional") {
					prop = a.Bool("proportional")
				}
				return resizePlan(env, a, id, map[int]float64{i: a.Num("value")}, prop)
			},
		},
		{
			Name: "resize_object",
			Description: "Resize an object to the given width/depth/height in mm (local X/Y/Z); dimensions you omit keep their size. " +
				"With a single dimension and Lock Proportions on, the other two follow proportionally. Non-parametric (a scale of the body).",
			Capability: capGeometry,
			Schema: object(props{
				"object_id": objectProp,
				"width":     positive("New width in mm (local X)"),
				"depth":     positive("New depth in mm (local Y)"),
				"height":    positive("New height in mm (local Z)"),
				"anchor":    anchorProp,
				"intent":    text("Why"),
			}),
			Plan: func(env Env, a Args) (*Plan, error) {
				id, err := objectID(env, a)
				if err != nil {
					return nil, err
				}
				target := map[int]float64{}
				for i, k := range dimensions.Axes {
					if a.Has(k) {
						target[i] = a.Num(k)
					}
				}
				if len(target) == 0 {
					return nil, fmt.Errorf("give width, depth and/or height")
				}
				return resizePlan(env, a, id, target, env.Graph().Get(id).LockProportions)
			},
		},
		{
			Name: "set_object_proportional_scaling",
			Description: "Turn Lock Proportions on or off for an object: when on, changing one of its dimensions scales the other two by the same factor. " +
				"An editing preference stored with the object; it never changes geometry.",
			Capability: capGeometry,
			Schema:     object(props{"object_id": objectProp, "enabled": boolean("Lock proportions")}, "enabled"),
			Plan: func(env Env, a Args) (*Plan, error) {
				id, err := objectID(env, a)
				if err != nil {
					return nil, err
				}
				on := a.Bool("enabled")
				if env.Graph().Get(id).LockProportions == on {
					return nil, fmt.Errorf("lock proportions of %q is already %s", id, onOff(on))
				}
				return &Plan{Intent: intentOr(a, fmt.Sprintf("Lock proportions of %s: %s", id, onOff(on))),
					Explanation: fmt.Sprintf("Turns Lock Proportions %s for %q. Editing preference only; geometry is not rebuilt.", onOff(on), id),
					Commands:    []commands.Command{&commands.SetLockProportions{ID: id, Enabled: on}}}, nil
			},
		},
	}
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

// objectID is `object_id`, or the one object of the current selection. It must be a visible body.
func objectID(env Env, a Args) (string, error) {
	id := a.Str("object_id")
	if id == "" {
		var ids []string
		for _, ref := range env.SelectionRefs() {
			if f := selectors.FeatureOf(ref); f != "" && !contains(ids, f) {
				ids = append(ids, f)
			}
		}
		switch len(ids) {
		case 0:
			return "", fmt.Errorf("no object: pass object_id or select one object first")
		case 1:
			id = ids[0]
		default:
			return "", fmt.Errorf("%d objects are selected (%s): pass object_id", len(ids), quoteList(ids))
		}
	}
	if _, err := requireLeaf(env, id); err != nil {
		return "", err
	}
	return id, nil
}

// ObjectDimensions measures a visible body for the UI's dimension overlay and the agent.
func ObjectDimensions(env Env, id string) (*dimensions.Dimensions, error) {
	if _, err := requireLeaf(env, id); err != nil {
		return nil, err
	}
	m, err := env.Model()
	if err != nil {
		return nil, err
	}
	return dimensions.Measure(env.Graph(), m, id)
}

// resizePlan is the MeshResizeService plan: measure, compute the factors, anchor them and
// emit the scale — folded into the object's own resize feature when it is the last edit.
func resizePlan(env Env, a Args, id string, target map[int]float64, proportional bool) (*Plan, error) {
	d, err := ObjectDimensions(env, id)
	if err != nil {
		return nil, err
	}
	f, err := dimensions.Factors(d.Size(), target, proportional)
	if err != nil {
		return nil, err
	}
	after := dimensions.Resized(d.Size(), f)
	anchor := orDefault(a.Str("anchor"), "center")

	params := map[string]any{"target": id}
	switch anchor {
	case "center":
		params["about"] = "center"
	case "object_origin": // resolved now and recorded, so a replay never re-derives it
		params["about"] = "point"
		params["ox"], params["oy"], params["oz"] = d.Origin[0], d.Origin[1], d.Origin[2]
	}
	if d.Rotated {
		ax, deg := d.Frame().ToAxisAngle()
		params["frame_ax"], params["frame_ay"], params["frame_az"] = r9(ax[0]), r9(ax[1]), r9(ax[2])
		params["frame_angle_deg"] = r9(deg)
	}

	var what []string
	for i := range dimensions.Axes {
		if _, ok := target[i]; ok || f[i] != 1 {
			what = append(what, fmt.Sprintf("%s %s → %s mm", dimensions.Axes[i], fmtMM(d.Size()[i]), fmtMM(after[i])))
		}
	}
	name := objectName(env, id)
	intent := intentOr(a, fmt.Sprintf("Resize %s to %s", name, dimensions.FormatSize(after)))
	explain := fmt.Sprintf("Resizes %q to %s (%s), about its %s%s. Non-parametric: the body is scaled.",
		id, dimensions.FormatSize(after), strings.Join(what, ", "), strings.ReplaceAll(anchor, "_", " "),
		map[bool]string{true: ", along its local axes", false: ""}[d.Rotated])

	if p := foldResize(env, id, params, f, intent, explain); p != nil {
		return p, nil
	}
	for i, k := range []string{"sx", "sy", "sz"} {
		params[k] = r12(f[i])
	}
	nid, err := uniqueID(env.Graph(), Args{}, resizeTag)
	if err != nil {
		return nil, err
	}
	n := newNode(env, Args{"intent": a.Str("intent")}, nid, "scale", params, id)
	n.Metadata.Name = "Resize " + name
	n.Metadata.Tags = []string{resizeTag}
	return addPlan(intent, explain, n), nil
}

// foldResize updates the object's own resize feature in place when the object is one (its
// last edit was a dimension edit with the same anchor and frame, nothing builds on it and no
// expression drives it). The composition of two scales about the same fixed point is one
// scale with the product of the factors; it returns to the plain body when they cancel out.
func foldResize(env Env, id string, params map[string]any, f [3]float64, intent, explain string) *Plan {
	n := env.Graph().Get(id)
	if n == nil || n.Type != "scale" || n.Suppressed || !contains(n.Metadata.Tags, resizeTag) ||
		len(n.Expressions) > 0 || len(env.Graph().Dependents(id)) > 0 {
		return nil
	}
	for _, k := range []string{"about", "ox", "oy", "oz", "frame_ax", "frame_ay", "frame_az", "frame_angle_deg"} {
		if !sameParam(n.Parameters[k], params[k]) {
			return nil
		}
	}
	old, err := features.ScaleFactors(features.Params(n.Parameters))
	if err != nil {
		return nil
	}
	next := [3]float64{r12(old[0] * f[0]), r12(old[1] * f[1]), r12(old[2] * f[2])}
	if dimensions.Identity(next) {
		return &Plan{Intent: intent, Explanation: explain + " This cancels the earlier resize, so it is removed.",
			Commands: []commands.Command{&commands.RemoveFeature{ID: id}}, LowRisk: true}
	}
	p := &Plan{Intent: intent, Explanation: explain, LowRisk: true}
	if _, has := n.Parameters["factor"]; has {
		p.Commands = append(p.Commands, &commands.SetParameter{ID: id, Name: "factor", Delete: true})
	}
	for i, k := range []string{"sx", "sy", "sz"} {
		if cur, ok := n.Parameters[k].(float64); !ok || cur != next[i] {
			p.Commands = append(p.Commands, &commands.SetParameter{ID: id, Name: k, Value: next[i]})
		}
	}
	return p
}

// sameParam compares two optional parameter values (numbers within 1e-6).
func sameParam(a, b any) bool {
	fa, oka := a.(float64)
	fb, okb := b.(float64)
	if oka && okb {
		return math.Abs(fa-fb) <= 1e-6
	}
	return a == b
}

// objectName is how an object is called in intents: its name, without the "Resize " of a
// resize feature that stands in for it.
func objectName(env Env, id string) string {
	n := env.Graph().Get(id)
	if n == nil || n.Metadata.Name == "" {
		return id
	}
	if contains(n.Metadata.Tags, resizeTag) {
		if base, ok := strings.CutPrefix(n.Metadata.Name, "Resize "); ok && base != "" {
			return base
		}
	}
	return n.Metadata.Name
}

// r12 keeps 12 significant digits of a scale factor: tidy in the CAD-IR (2/3, not
// 0.6666666666666666), far below any measurable size difference.
func r12(v float64) float64 {
	r, _ := strconv.ParseFloat(strconv.FormatFloat(v, 'g', 12, 64), 64)
	return r
}

func r9(v float64) float64 {
	v = math.Round(v*1e9) / 1e9
	if v == 0 {
		return 0
	}
	return v
}
