package tools

import (
	"fmt"
	"sort"

	"github.com/accelone/simoncad/internal/cadir"
	"github.com/accelone/simoncad/internal/commands"
	"github.com/accelone/simoncad/internal/selectors"
)

// Appearance tools only emit presentation commands: they never touch geometry and never
// invalidate the rebuild cache.

// appearanceTargets resolves the features an appearance tool applies to: the explicit
// `features` argument, or — when omitted — the bodies in the current selection.
func appearanceTargets(env Env, a Args) ([]*cadir.Node, error) {
	var ids []string
	if raw, ok := a["features"].([]any); ok {
		for _, v := range raw {
			s, _ := v.(string)
			ids = append(ids, s)
		}
	} else {
		for _, ref := range env.SelectionRefs() {
			if id := selectors.FeatureOf(ref); id != "" {
				ids = append(ids, id)
			}
		}
		if len(ids) == 0 {
			return nil, fmt.Errorf("no target: pass `features` or select objects first")
		}
	}
	seen := map[string]bool{}
	var out []*cadir.Node
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		n, err := requireFeature(env, id)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no target features")
	}
	return out, nil
}

func idsOf(ns []*cadir.Node) []string {
	ids := make([]string, len(ns))
	for i, n := range ns {
		ids[i] = n.ID
	}
	sort.Strings(ids)
	return ids
}

var featuresProp = array("Feature ids to change; omit to use the current selection", text("Feature id"), 1, 64)

func appearanceTool(name, desc string, extra props, required []string,
	build func(n *cadir.Node, a Args) (commands.Command, bool, error), explain string) *Tool {
	p := props{"features": featuresProp, "intent": text("Why")}
	for k, v := range extra {
		p[k] = v
	}
	return &Tool{
		Name: name, Description: desc, Capability: capGeometry, Schema: object(p, required...),
		Plan: func(env Env, a Args) (*Plan, error) {
			ts, err := appearanceTargets(env, a)
			if err != nil {
				return nil, err
			}
			var cmds []commands.Command
			for _, n := range ts {
				c, changed, err := build(n, a)
				if err != nil {
					return nil, err
				}
				if changed {
					cmds = append(cmds, c)
				}
			}
			if len(cmds) == 0 {
				return nil, fmt.Errorf("nothing to change: already as requested")
			}
			return &Plan{Intent: intentOr(a, name+" "+fmt.Sprint(idsOf(ts))),
				Explanation: fmt.Sprintf("%s for %v. Presentation only; geometry is not rebuilt.", explain, idsOf(ts)),
				Commands:    cmds}, nil
		},
	}
}

// IsAppearanceTool reports whether name is a presentation-only mutation tool.
func IsAppearanceTool(name string) bool {
	switch name {
	case "set_color", "set_opacity", "set_visibility", "reset_appearance", "set_material", "remove_material", "set_material_scale":
		return true
	}
	return false
}

func appearanceTools() []*Tool {
	return []*Tool{
		appearanceTool("set_color", "Set the display color of one or more features/bodies (e.g. 'make the bracket red'). Presentation only; does not change geometry. Defaults to the current selection when `features` is omitted.",
			props{"color": text("Color as #RRGGBB hex")}, []string{"color"},
			func(n *cadir.Node, a Args) (commands.Command, bool, error) {
				col, err := cadir.NormalizeColor(a.Str("color"))
				if err != nil {
					return nil, false, err
				}
				return &commands.SetColor{ID: n.ID, Color: col}, n.Appearance == nil || n.Appearance.Color != col, nil
			}, "Sets color"),
		appearanceTool("set_opacity", "Set display opacity (0 = invisible, 1 = opaque; ~0.5 for 'semi-transparent') of features/bodies. Presentation only. Defaults to the current selection.",
			props{"opacity": map[string]any{"type": "number", "description": "Opacity 0..1"}}, []string{"opacity"},
			func(n *cadir.Node, a Args) (commands.Command, bool, error) {
				o := a.Num("opacity")
				if err := cadir.ValidateOpacity(o); err != nil {
					return nil, false, err
				}
				return &commands.SetOpacity{ID: n.ID, Opacity: &o}, n.Appearance == nil || n.Appearance.Opacity == nil || *n.Appearance.Opacity != o, nil
			}, "Sets opacity"),
		appearanceTool("set_visibility", "Show or hide features/bodies in the viewport (e.g. 'hide the reference servo'). Hidden objects still exist and are still exported. Defaults to the current selection.",
			props{"visible": boolean("true to show, false to hide")}, []string{"visible"},
			func(n *cadir.Node, a Args) (commands.Command, bool, error) {
				v := a.Bool("visible")
				cur := n.Appearance == nil || n.Appearance.Visible == nil || *n.Appearance.Visible
				return &commands.SetVisibility{ID: n.ID, Visible: &v}, cur != v, nil
			}, "Sets visibility"),
		appearanceTool("reset_appearance", "Reset color, opacity and visibility of features/bodies to their defaults. Defaults to the current selection.",
			nil, nil,
			func(n *cadir.Node, a Args) (commands.Command, bool, error) {
				return &commands.ResetAppearance{ID: n.ID}, !n.Appearance.IsZero(), nil
			}, "Resets appearance"),
		{
			Name: "get_appearance", Description: "Read the appearance (color, opacity, visible) of features. Omit `features` to list all overridden ones.",
			Capability: capGeometry,
			Schema:     object(props{"features": featuresProp}),
			Query: func(env Env, a Args) (any, error) {
				var ns []*cadir.Node
				if a.Has("features") {
					var err error
					if ns, err = appearanceTargets(env, a); err != nil {
						return nil, err
					}
				} else {
					ns = env.Graph().Nodes
				}
				out := map[string]any{}
				for _, n := range ns {
					if a.Has("features") || !n.Appearance.IsZero() {
						out[n.ID] = AppearanceView(n.Appearance)
					}
				}
				return map[string]any{"appearance": out}, nil
			},
		},
	}
}

// AppearanceView is the effective appearance with defaults filled in.
func AppearanceView(a *cadir.Appearance) map[string]any {
	v := map[string]any{"color": nil, "opacity": 1.0, "visible": true, "material": nil, "texture_scale": 1.0, "overridden": !a.IsZero()}
	if a != nil {
		if a.MaterialID != "" {
			v["material"] = a.MaterialID
		}
		if a.TextureScale != nil {
			v["texture_scale"] = *a.TextureScale
		}
		if a.Color != "" {
			v["color"] = a.Color
		}
		if a.Opacity != nil {
			v["opacity"] = *a.Opacity
		}
		if a.Visible != nil {
			v["visible"] = *a.Visible
		}
	}
	return v
}
