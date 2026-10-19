package tools

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/accelone/simoncad/internal/cadir"
	"github.com/accelone/simoncad/internal/commands"
)

// knownParams lists the editable parameters of each feature type; unknown names are
// rejected so a typo cannot silently create a dead parameter.
var knownParams = map[string][]string{
	"box":              {"dx", "dy", "dz", "x", "y", "z"},
	"cylinder":         {"radius", "height", "axis", "x", "y", "z"},
	"cone":             {"radius", "top_radius", "height", "axis", "x", "y", "z"},
	"sphere":           {"radius", "x", "y", "z"},
	"torus":            {"major_radius", "minor_radius", "axis", "x", "y", "z"},
	"hole":             {"diameter", "depth", "kind", "cb_diameter", "cb_depth", "cs_diameter", "cs_angle_deg", "positions"},
	"extrude":          {"mode", "plane", "offset", "depth", "symmetric", "through", "reverse", "x", "y", "w", "h", "r", "x1", "y1", "x2", "y2", "width", "points"},
	"linear_pattern":   {"count", "dx", "dy", "dz"},
	"circular_pattern": {"count", "cx", "cy", "cz", "axis", "total_angle_deg"},
	"fillet":           {"radius"},
	"chamfer":          {"distance"},
	"transform":        {"dx", "dy", "dz", "angle_deg", "ax", "ay", "az", "ox", "oy", "oz"},
	"mirror":           {"plane", "offset"},
	"sketch":           {"plane", "offset"}, // plus its dim_* dimension parameters
	"union":            {},
	"subtract":         {},
	"intersect":        {},
	"scale":            {"factor", "sx", "sy", "sz", "about", "ox", "oy", "oz", "frame_ax", "frame_ay", "frame_az", "frame_angle_deg"},
	"copy":             {"dx", "dy", "dz"},
	"face_extrude":     {"distance"},
	"face_inset":       {"inset", "depth"},
	"face_offset":      {"distance"},
	"relief":           {"depth", "offset", "mirror", "reverse"},
	"sweep":            {"cap_length", "twist", "tolerance", "max_sections"},
	// An imported mesh exposes how it is read; the mesh itself is identified by its content hash.
	"import_mesh": {"units", "placement"},
	"import_step": {"placement"},
	// A component instance exposes only its placement. The reference itself (component,
	// version, content_hash) identifies what is being instantiated and is changed through
	// insert_component / sync_component, which validate it, not by editing a field.
	"component_instance": {"dx", "dy", "dz", "angle_deg", "ax", "ay", "az", "ox", "oy", "oz", "sx", "sy", "sz"},
}

// signedParams are exceptions to strictlyPositive: signed distances of face edits.
var signedParams = map[string]bool{"face_extrude.distance": true, "face_offset.distance": true, "face_inset.depth": true}

// strictlyPositive parameters must be > 0.
var strictlyPositive = map[string]bool{
	"dx": false, "dy": false, "dz": false, // box extents are positive but pattern/transform deltas are not; checked per type
	"radius": true, "height": true, "major_radius": true, "minor_radius": true, "diameter": true, "depth": true, "distance": true,
	"cb_diameter": true, "cb_depth": true, "cs_diameter": true, "w": true, "h": true, "r": true, "width": true,
	"factor": true, "sx": true, "sy": true, "sz": true, "inset": true,
}

func isKnown(typ, name string) bool {
	for _, k := range knownParams[typ] {
		if k == name {
			return true
		}
	}
	return false
}

// editable reports whether a parameter of an existing node may be edited. A sketch also
// exposes the dimensions its entities refer to (dim_*), which must already exist.
func editable(n *cadir.Node, name string) bool {
	if isKnown(n.Type, name) {
		return true
	}
	if (n.Type == "sketch" && strings.HasPrefix(name, "dim_")) || n.Type == "parameters" {
		_, ok := n.Parameters[name].(float64)
		return ok
	}
	return false
}

func editableList(n *cadir.Node) string {
	list := append([]string(nil), knownParams[n.Type]...)
	if n.Type == "sketch" || n.Type == "parameters" {
		var dims []string
		for k := range n.Parameters {
			if strings.HasPrefix(k, "dim_") || n.Type == "parameters" {
				dims = append(dims, k)
			}
		}
		sort.Strings(dims)
		list = append(list, dims...)
	}
	return strings.Join(list, ", ")
}

func editTools() []*Tool {
	return []*Tool{
		{
			Name:        "modify_parameter",
			Description: "Change one parameter of an existing feature (e.g. plate dz from 4 to 5 mm). If the parameter was driven by an expression, the expression is replaced by the literal value.",
			Capability:  capGeometry,
			Schema: object(props{
				"feature": text("Feature id"), "parameter": text("Parameter name, e.g. dz, diameter, count"),
				"value":  map[string]any{"description": "New value (number, string, or boolean)"},
				"intent": text("Why, in natural language"),
			}, "feature", "parameter", "value"),
			Plan: func(env Env, a Args) (*Plan, error) {
				n, err := requireFeature(env, a.Str("feature"))
				if err != nil {
					return nil, err
				}
				name := a.Str("parameter")
				if !editable(n, name) {
					return nil, fmt.Errorf("%s features have no editable parameter %q (editable: %s)", n.Type, name, editableList(n))
				}
				val := a["value"]
				switch v := val.(type) {
				case float64:
					if !finite(v) {
						return nil, fmt.Errorf("value must be finite")
					}
					if (strictlyPositive[name] && !signedParams[n.Type+"."+name] || strings.HasPrefix(name, "dim_") || n.Type == "box" && (name == "dx" || name == "dy" || name == "dz")) && v <= 0 {
						return nil, fmt.Errorf("%s must be greater than 0", name)
					}
					if name == "count" && (v < 2 || v != math.Trunc(v)) {
						return nil, fmt.Errorf("count must be an integer >= 2")
					}
				case string, bool:
				default:
					return nil, fmt.Errorf("value must be a number, string or boolean")
				}
				p := &Plan{
					Intent:      intentOr(a, fmt.Sprintf("Set %s.%s = %v", n.ID, name, val)),
					Explanation: fmt.Sprintf("Sets %s.%s to %v.", n.ID, name, val),
				}
				if _, driven := n.Expressions[name]; driven {
					p.Commands = append(p.Commands, &commands.SetExpression{ID: n.ID, Name: name, Delete: true})
				}
				p.Commands = append(p.Commands, &commands.SetParameter{ID: n.ID, Name: name, Value: val})
				return p, nil
			},
		},
		{
			Name: "set_expression",
			Description: "Drive a numeric parameter by an expression over other features' parameters, e.g. depth = 'plate.dz * 0.5'. " +
				"Syntax: numbers, + - * / ^, parentheses, pi, sin cos tan sqrt abs min max round, and feature.parameter references. Omit `expression` to clear it.",
			Capability: capGeometry,
			Schema: object(props{
				"feature": text("Feature id"), "parameter": text("Parameter name"), "expression": text("Expression, or omit to clear"),
				"intent": text("Why, in natural language"),
			}, "feature", "parameter"),
			Plan: func(env Env, a Args) (*Plan, error) {
				n, err := requireFeature(env, a.Str("feature"))
				if err != nil {
					return nil, err
				}
				name := a.Str("parameter")
				if !a.Has("expression") {
					if _, ok := n.Expressions[name]; !ok {
						return nil, fmt.Errorf("%s.%s has no expression to clear", n.ID, name)
					}
					return &Plan{Intent: intentOr(a, "Clear expression"), Explanation: fmt.Sprintf("Clears the expression on %s.%s.", n.ID, name),
						Commands: []commands.Command{&commands.SetExpression{ID: n.ID, Name: name, Delete: true}}}, nil
				}
				if !editable(n, name) {
					return nil, fmt.Errorf("%s features have no editable parameter %q (editable: %s)", n.Type, name, editableList(n))
				}
				return &Plan{Intent: intentOr(a, "Drive "+n.ID+"."+name+" by expression"),
					Explanation: fmt.Sprintf("Drives %s.%s by %q.", n.ID, name, a.Str("expression")),
					Commands:    []commands.Command{&commands.SetExpression{ID: n.ID, Name: name, Expr: a.Str("expression")}}}, nil
			},
		},
		{
			Name: "suppress_feature", Description: "Suppress or unsuppress a feature. A suppressed modifying feature (hole, fillet, ...) passes its target through unchanged.",
			Capability: capGeometry,
			Schema:     object(props{"feature": text("Feature id"), "suppressed": boolean("true to suppress, false to restore"), "intent": text("Why")}, "feature", "suppressed"),
			Plan: func(env Env, a Args) (*Plan, error) {
				n, err := requireFeature(env, a.Str("feature"))
				if err != nil {
					return nil, err
				}
				if n.Suppressed == a.Bool("suppressed") {
					return nil, fmt.Errorf("%s is already %s", n.ID, map[bool]string{true: "suppressed", false: "active"}[n.Suppressed])
				}
				return &Plan{Intent: intentOr(a, "Suppress "+n.ID), Explanation: fmt.Sprintf("Sets %s suppressed=%v.", n.ID, a.Bool("suppressed")),
					Commands: []commands.Command{&commands.Suppress{ID: n.ID, Suppressed: a.Bool("suppressed")}}}, nil
			},
		},
		{
			Name: "rename_feature", Description: "Change a feature's human-readable name (its id does not change).",
			Capability: capGeometry,
			Schema:     object(props{"feature": text("Feature id"), "name": text("New name")}, "feature", "name"),
			Plan: func(env Env, a Args) (*Plan, error) {
				n, err := requireFeature(env, a.Str("feature"))
				if err != nil {
					return nil, err
				}
				return &Plan{Intent: "Rename " + n.ID, Explanation: fmt.Sprintf("Renames %s to %q.", n.ID, a.Str("name")),
					Commands: []commands.Command{&commands.Rename{ID: n.ID, Name: a.Str("name")}}}, nil
			},
		},
		{
			Name: "remove_feature", Description: "Delete a feature. Fails if other features depend on it; remove or re-point those first.",
			Capability: capGeometry,
			Schema:     object(props{"feature": text("Feature id"), "intent": text("Why")}, "feature"),
			Plan: func(env Env, a Args) (*Plan, error) {
				n, err := requireFeature(env, a.Str("feature"))
				if err != nil {
					return nil, err
				}
				if deps := env.Graph().Dependents(n.ID); len(deps) > 0 {
					return nil, fmt.Errorf("%s is used by %s; remove those first", n.ID, strings.Join(deps, ", "))
				}
				return &Plan{Intent: intentOr(a, "Remove "+n.ID), Explanation: fmt.Sprintf("Removes feature %s.", n.ID),
					Commands: []commands.Command{&commands.RemoveFeature{ID: n.ID}}}, nil
			},
		},
		{
			Name: "restore_checkpoint",
			Description: "Return the model to an earlier semantic checkpoint, given by id (cp_002), number, 'previous', or part of its label " +
				"(e.g. 'before we reinforced the shoulder' means the checkpoint preceding the one labelled 'Reinforced shoulder'). Use get_model_summary to list checkpoints.",
			Capability: capGeometry,
			Schema:     object(props{"checkpoint": text("Checkpoint id, number, 'previous', or label text"), "intent": text("Why")}, "checkpoint"),
			Plan: func(env Env, a Args) (*Plan, error) {
				cp, err := env.Checkpoints().Find(a.Str("checkpoint"))
				if err != nil {
					return nil, err
				}
				if cp.GraphHash == env.Graph().Hash() {
					return nil, fmt.Errorf("the model already matches checkpoint %s", cp.ID)
				}
				return &Plan{Intent: intentOr(a, "Restore checkpoint "+cp.ID), Explanation: fmt.Sprintf("Restores the model to checkpoint %s (%q).", cp.ID, cp.Label),
					Commands: []commands.Command{&commands.RestoreSnapshot{Graph: cp.Snapshot, From: cp.ID}}}, nil
			},
		},
	}
}
