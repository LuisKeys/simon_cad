package tools

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/accelone/simoncad/internal/cadir"
	"github.com/accelone/simoncad/internal/commands"
	"github.com/accelone/simoncad/internal/visual"
)

// CapVisual tags the tools of visual closed-loop refinement: design parameters, region
// ownership and locks, offscreen renders and reference calibration.
const CapVisual = "visual"

// ParamSets lists the design-parameter nodes in graph order.
func ParamSets(g *cadir.Graph) []*cadir.Node {
	var out []*cadir.Node
	for _, n := range g.Nodes {
		if n.Type == "parameters" {
			out = append(out, n)
		}
	}
	return out
}

// ParameterValues flattens every design parameter as "set.name" → value.
func ParameterValues(g *cadir.Graph) map[string]float64 {
	out := map[string]float64{}
	for _, n := range ParamSets(g) {
		for k, v := range n.Parameters {
			if f, ok := v.(float64); ok {
				out[n.ID+"."+k] = f
			}
		}
	}
	return out
}

// findParameter resolves "set.name" or a bare name unique across sets.
func findParameter(g *cadir.Graph, ref string) (*cadir.Node, string, error) {
	if set, name, ok := strings.Cut(ref, "."); ok {
		n := g.Get(set)
		if n == nil || n.Type != "parameters" {
			return nil, "", fmt.Errorf("%q is not a parameter set", set)
		}
		if _, ok := n.Parameters[name]; !ok {
			return nil, "", fmt.Errorf("parameter set %s has no parameter %q (has: %s)", set, name, paramNames(n))
		}
		return n, name, nil
	}
	var hits []*cadir.Node
	for _, n := range ParamSets(g) {
		if _, ok := n.Parameters[ref]; ok {
			hits = append(hits, n)
		}
	}
	switch len(hits) {
	case 0:
		var all []string
		for k := range ParameterValues(g) {
			all = append(all, k)
		}
		sort.Strings(all)
		if len(all) == 0 {
			return nil, "", fmt.Errorf("there are no design parameters yet; call define_parameters first")
		}
		return nil, "", fmt.Errorf("unknown parameter %q (known: %s)", ref, strings.Join(all, ", "))
	case 1:
		return hits[0], ref, nil
	}
	return nil, "", fmt.Errorf("parameter %q exists in several sets; use set.name", ref)
}

func paramNames(n *cadir.Node) string {
	var out []string
	for k := range n.Parameters {
		out = append(out, k)
	}
	sort.Strings(out)
	return strings.Join(out, ", ")
}

func strList(a Args, k string) []string {
	raw, _ := a[k].([]any)
	var out []string
	for _, v := range raw {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func strArray(desc string) map[string]any {
	return map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": desc}
}

func visualTools() []*Tool {
	return []*Tool{
		{
			Name: "define_parameters",
			Description: "Create or extend a named set of design parameters (numbers in mm or degrees) that control major geometry, e.g. set='vehicle' with length, width, height, wheelbase, front_overhang, roof_height, windshield_angle. " +
				"Then drive feature parameters from them with bind_parameters (dx = 'vehicle.length'), so later refinement edits one parameter (set_parameter) and every dependent feature regenerates. Prefer this over destructive edits.",
			Capability: CapVisual,
			Schema: object(props{
				"set":        text("Parameter set id, lowercase (e.g. vehicle)"),
				"parameters": map[string]any{"type": "object", "description": "name → number"},
				"intent":     text("Why, in natural language"),
			}, "set", "parameters"),
			Plan: func(env Env, a Args) (*Plan, error) {
				set := a.Str("set")
				raw, _ := a["parameters"].(map[string]any)
				if len(raw) == 0 {
					return nil, fmt.Errorf("give at least one parameter")
				}
				vals := map[string]any{}
				var names []string
				for k, v := range raw {
					f, ok := v.(float64)
					if !ok || !finite(f) {
						return nil, fmt.Errorf("parameter %q must be a finite number", k)
					}
					if !validID(k) {
						return nil, fmt.Errorf("parameter name %q must be lowercase letters, digits and underscores", k)
					}
					vals[k] = f
					names = append(names, k)
				}
				sort.Strings(names)
				if n := env.Graph().Get(set); n != nil {
					if n.Type != "parameters" {
						return nil, fmt.Errorf("%s is a %s feature, not a parameter set", set, n.Type)
					}
					p := &Plan{Intent: intentOr(a, "Update parameters of "+set), Explanation: fmt.Sprintf("Sets %s: %s.", set, strings.Join(names, ", "))}
					for _, k := range names {
						if _, driven := n.Expressions[k]; driven {
							p.Commands = append(p.Commands, &commands.SetExpression{ID: set, Name: k, Delete: true})
						}
						p.Commands = append(p.Commands, &commands.SetParameter{ID: set, Name: k, Value: vals[k]})
					}
					return p, nil
				}
				if !validID(set) {
					return nil, fmt.Errorf("set id %q must be lowercase letters, digits and underscores", set)
				}
				a["id"] = set
				return addPlan(intentOr(a, "Design parameters "+set), fmt.Sprintf("Adds parameter set %q (%s).", set, strings.Join(names, ", ")),
					newNode(env, a, set, "parameters", vals)), nil
			},
		},
		{
			Name: "set_parameter",
			Description: "Change one design parameter (name, or set.name when several sets have it); every feature bound to it regenerates. " +
				"This is the preferred way to correct proportions during visual refinement (e.g. roof_height 1020, windshield_angle 26).",
			Capability: CapVisual,
			Schema:     object(props{"name": text("Parameter name or set.name"), "value": number("New value"), "intent": text("Why")}, "name", "value"),
			Plan: func(env Env, a Args) (*Plan, error) {
				n, name, err := findParameter(env.Graph(), a.Str("name"))
				if err != nil {
					return nil, err
				}
				v := a.Num("value")
				if !finite(v) {
					return nil, fmt.Errorf("value must be finite")
				}
				p := &Plan{Intent: intentOr(a, fmt.Sprintf("Set %s.%s = %s", n.ID, name, fmtMM(v))),
					Explanation: fmt.Sprintf("Sets %s.%s from %v to %s.", n.ID, name, n.Parameters[name], fmtMM(v))}
				if _, driven := n.Expressions[name]; driven {
					p.Commands = append(p.Commands, &commands.SetExpression{ID: n.ID, Name: name, Delete: true})
				}
				p.Commands = append(p.Commands, &commands.SetParameter{ID: n.ID, Name: name, Value: v})
				return p, nil
			},
		},
		{
			Name:        "bind_parameters",
			Description: "Drive several parameters of one feature by expressions over design parameters in ONE ChangeSet, e.g. feature='body', bindings={dx:'vehicle.length', dz:'vehicle.height - vehicle.ground_clearance'}.",
			Capability:  CapVisual,
			Schema: object(props{
				"feature":  text("Feature id"),
				"bindings": map[string]any{"type": "object", "description": "parameter → expression"},
				"intent":   text("Why"),
			}, "feature", "bindings"),
			Plan: func(env Env, a Args) (*Plan, error) {
				n, err := requireFeature(env, a.Str("feature"))
				if err != nil {
					return nil, err
				}
				raw, _ := a["bindings"].(map[string]any)
				if len(raw) == 0 {
					return nil, fmt.Errorf("give at least one binding")
				}
				var names []string
				for k := range raw {
					names = append(names, k)
				}
				sort.Strings(names)
				p := &Plan{Intent: intentOr(a, "Bind "+n.ID+" to design parameters")}
				for _, k := range names {
					ex, ok := raw[k].(string)
					if !ok || strings.TrimSpace(ex) == "" {
						return nil, fmt.Errorf("binding for %q must be an expression string", k)
					}
					if !editable(n, k) {
						return nil, fmt.Errorf("%s features have no editable parameter %q (editable: %s)", n.Type, k, editableList(n))
					}
					p.Commands = append(p.Commands, &commands.SetExpression{ID: n.ID, Name: k, Expr: ex})
				}
				p.Explanation = fmt.Sprintf("Drives %s.{%s} by design parameters.", n.ID, strings.Join(names, ", "))
				return p, nil
			},
		},
		{
			Name: "define_region",
			Description: "Declare which CAD objects and design parameters shape a visual region (e.g. 'roof' → objects [roof, cabin], parameters [vehicle.roof_height]). " +
				"The vision critic reports errors per region, so regions make its corrections actionable. Redefining a region keeps its lock.",
			Capability: CapVisual,
			Schema: object(props{"name": text("Region name, e.g. roof, front_hood, wheels"), "objects": strArray("Feature ids"),
				"parameters": strArray("Design parameters (set.name)")}, "name"),
			Query: func(env Env, a Args) (any, error) {
				g := env.Graph()
				var unknown []string
				for _, id := range strList(a, "objects") {
					if g.Get(id) == nil {
						unknown = append(unknown, id)
					}
				}
				if len(unknown) > 0 {
					return nil, fmt.Errorf("unknown objects: %s", strings.Join(unknown, ", "))
				}
				vs := env.Visual()
				vs.Lock()
				defer vs.Unlock()
				return vs.DefineRegion(a.Str("name"), strList(a, "objects"), strList(a, "parameters"))
			},
		},
		{
			Name:        "lock_region",
			Description: "Lock a region whose geometry is already correct: its objects and parameters cannot be modified (appearance still can) until unlock_region.",
			Capability:  CapVisual,
			Schema:      object(props{"name": text("Region name")}, "name"),
			Query: func(env Env, a Args) (any, error) {
				vs := env.Visual()
				vs.Lock()
				defer vs.Unlock()
				return vs.SetLocked(a.Str("name"), true)
			},
		},
		{
			Name:        "unlock_region",
			Description: "Unlock a region. Only do this when the user explicitly asks.",
			Capability:  CapVisual,
			Schema:      object(props{"name": text("Region name")}, "name"),
			Query: func(env Env, a Args) (any, error) {
				vs := env.Visual()
				vs.Lock()
				defer vs.Unlock()
				return vs.SetLocked(a.Str("name"), false)
			},
		},
		{
			Name: "render_model",
			Description: "Render the current model offscreen from fixed cameras (left, right, front, rear, top orthographic; front_3q, rear_3q perspective). " +
				"mode=shaded for appearance, silhouette for a binary mask (object white, background black). Returns render ids; when references exist, each matching view also gets its silhouette score. Never assume the model looks right without rendering it.",
			Capability: CapVisual,
			Schema: object(props{
				"views":      map[string]any{"type": "array", "items": enum("View", visual.ViewNames...), "description": "Default: left, front, top, front_3q"},
				"resolution": map[string]any{"type": "array", "items": map[string]any{"type": "integer", "minimum": 64, "maximum": 2048}, "minItems": 2, "maxItems": 2, "description": "[width, height], default [1024, 1024]"},
				"mode":       enum("Render mode (default shaded)", visual.ModeShaded, visual.ModeSilhouette),
			}),
			Query: func(env Env, a Args) (any, error) {
				views := strList(a, "views")
				w, h := 1024, 1024
				if r, ok := a["resolution"].([]any); ok && len(r) == 2 {
					w, h = int(r[0].(float64)), int(r[1].(float64))
				}
				rs, err := env.Render(views, w, h, a.Str("mode"))
				if err != nil {
					return nil, err
				}
				var out []map[string]any
				for _, r := range rs {
					item := map[string]any{"view": r.View, "render_id": r.ID, "url": "/api/render/" + r.ID, "width": r.Width, "height": r.Height, "mode": r.Mode}
					if s, ok := env.SilhouetteAgainstReference(r); ok {
						item["silhouette_score"] = s
					}
					out = append(out, item)
				}
				return map[string]any{"renders": out}, nil
			},
		},
		{
			Name: "calibrate_reference_image",
			Description: "Read or manually set the camera of a reference image (azimuth 0=front, 90=left side, 180=rear, 270=right; elevation 90=top; projection orthographic|perspective; scale). " +
				"With only image_id it returns the current calibration; giving values stores a manual calibration that automatic analysis will not overwrite.",
			Capability: CapVisual,
			Schema: object(props{
				"image_id": text("Reference image id"), "view": enum("View label", "left", "right", "front", "rear", "top", "front_3q", "rear_3q", "other"),
				"azimuth": number("Degrees"), "elevation": number("Degrees"), "projection": enum("Projection", visual.Orthographic, visual.Perspective),
				"focal_length_mm": number("35 mm-equivalent focal length (perspective)"), "scale": number("Relative scale, default 1"), "mirrored": boolean("The object faces the other way"),
			}, "image_id"),
			Query: func(env Env, a Args) (any, error) {
				vs := env.Visual()
				vs.Lock()
				defer vs.Unlock()
				id := a.Str("image_id")
				ref := vs.Reference(id)
				if ref == nil {
					im, ok := env.Images().Get(id)
					if !ok {
						return nil, fmt.Errorf("image %q was not uploaded", id)
					}
					ref = vs.AddReference(id, im.Width, im.Height)
				}
				manual := false
				for _, k := range []string{"view", "azimuth", "elevation", "projection", "focal_length_mm", "scale", "mirrored"} {
					manual = manual || a.Has(k)
				}
				if !manual {
					if ref.Calibration == nil {
						return map[string]any{"image_id": id, "calibration": nil, "note": "not calibrated yet; run the visual refinement analysis or give values"}, nil
					}
					return map[string]any{"image_id": id, "calibration": ref.Calibration}, nil
				}
				c := visual.Calibration{Projection: visual.Orthographic, Scale: 1}
				if ref.Calibration != nil {
					c = *ref.Calibration
				}
				if a.Has("view") {
					c.View = a.Str("view")
				}
				if a.Has("azimuth") {
					c.Azimuth = math.Mod(a.Num("azimuth")+360, 360)
				}
				if a.Has("elevation") {
					c.Elevation = math.Max(-90, math.Min(90, a.Num("elevation")))
				}
				if a.Has("projection") {
					c.Projection = a.Str("projection")
				}
				if a.Has("focal_length_mm") {
					c.FocalMM = a.Num("focal_length_mm")
				}
				if a.Has("scale") && a.Num("scale") > 0 {
					c.Scale = a.Num("scale")
				}
				if a.Has("mirrored") {
					c.Mirrored = a.Bool("mirrored")
				}
				c.Manual = true
				ref.Calibration = &c
				return map[string]any{"image_id": id, "calibration": c}, nil
			},
		},
		{
			Name:        "get_visual_state",
			Description: "Describe the visual refinement state: references (view, calibration, landmarks), reference proportions, regions and locks, design parameters, current stage and the latest scores.",
			Capability:  CapVisual,
			Schema:      object(props{}),
			Query: func(env Env, a Args) (any, error) {
				vs := env.Visual()
				vs.Lock()
				defer vs.Unlock()
				out := map[string]any{"references": vs.References, "reference_geometry": vs.ReferenceGeometry, "regions": vs.Regions,
					"parameters": ParameterValues(env.Graph()), "iterations": len(vs.History)}
				if vs.Stage < len(visual.Stages) {
					out["stage"] = visual.Stages[vs.Stage].Name
				}
				if last := vs.Last(); last != nil {
					out["latest_scores"] = last.Scores
				}
				return out, nil
			},
		},
	}
}

func validID(s string) bool {
	if s == "" || len(s) > 48 {
		return false
	}
	for i, r := range s {
		if !(r >= 'a' && r <= 'z' || r == '_' || (i > 0 && r >= '0' && r <= '9')) {
			return false
		}
	}
	return true
}

// Structural reports whether a tool can change geometry (a mutating, non-appearance tool).
func (r *Registry) Structural(name string) bool {
	t, ok := r.tools[name]
	return ok && t.Mutating() && !IsAppearanceTool(name)
}
