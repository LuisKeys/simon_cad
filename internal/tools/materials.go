package tools

import (
	"fmt"

	"github.com/accelone/simoncad/internal/cadir"
	"github.com/accelone/simoncad/internal/commands"
	"github.com/accelone/simoncad/internal/materials"
)

// Material tools assign visual (PBR) materials. Like every appearance tool they are
// presentation only: they never touch geometry and never invalidate the rebuild cache.

func materialView(m materials.Material) map[string]any {
	v := map[string]any{"id": m.ID, "name": m.Name, "category": m.Category, "tags": m.Tags,
		"base_color": m.BaseColor, "metalness": m.Metalness, "roughness": m.Roughness,
		"textured": len(m.TextureFiles()) > 0, "project": m.Project}
	if m.Opacity != nil {
		v["opacity"] = *m.Opacity
	}
	return v
}

func unknownMaterial(env Env, id string) error {
	hint := ""
	if ms := env.Materials().Search(id, 3); len(ms) > 0 {
		hint = " Did you mean:"
		for _, m := range ms {
			hint += " " + m.Material.ID
		}
		hint += "?"
	}
	return fmt.Errorf("unknown material %q.%s Use search_materials or list_materials.", id, hint)
}

func materialTools() []*Tool {
	return []*Tool{
		appearanceToolEnv("set_material", "Assign a visual material (e.g. brushed_aluminum, black_abs, stainless_steel, carbon_fiber, clear_glass) to features/bodies. Presentation only; does not change geometry or engineering properties. Defaults to the current selection. If the user's wording could match several materials, call search_materials first and offer the options instead of guessing.",
			props{"material_id": text("Material id from list_materials / search_materials"),
				"keep_color": boolean("true to keep the object's color override on top of the material; default false (the material's own color shows)")}, []string{"material_id"},
			func(env Env, n *cadir.Node, a Args) (commands.Command, bool, error) {
				id := a.Str("material_id")
				if _, ok := env.Materials().Get(id); !ok {
					return nil, false, unknownMaterial(env, id)
				}
				clear := !a.Bool("keep_color") && n.Appearance != nil && n.Appearance.Color != ""
				return &commands.SetMaterial{ID: n.ID, MaterialID: id, ClearColor: clear}, n.Appearance == nil || n.Appearance.MaterialID != id || clear, nil
			}, "Sets material"),
		appearanceToolEnv("remove_material", "Remove the visual material from features/bodies (color/opacity overrides stay). Defaults to the current selection.",
			nil, nil,
			func(env Env, n *cadir.Node, a Args) (commands.Command, bool, error) {
				return &commands.RemoveMaterial{ID: n.ID}, n.Appearance != nil && n.Appearance.MaterialID != "", nil
			}, "Removes material"),
		appearanceToolEnv("set_material_scale", "Set the texture scale multiplier (>0; 1 = the material's natural scale) of features/bodies. Presentation only.",
			props{"scale": map[string]any{"type": "number", "description": "Texture scale multiplier, > 0"}}, []string{"scale"},
			func(env Env, n *cadir.Node, a Args) (commands.Command, bool, error) {
				s := a.Num("scale")
				if err := cadir.ValidateTextureScale(s); err != nil {
					return nil, false, err
				}
				return &commands.SetMaterialScale{ID: n.ID, Scale: &s}, n.Appearance == nil || n.Appearance.TextureScale == nil || *n.Appearance.TextureScale != s, nil
			}, "Sets texture scale"),
		{
			Name: "list_materials", Description: "List the visual materials in the library (built-in plus project materials), optionally for one category (Metals, Plastics, Rubber, Carbon Fiber, Glass, Electronics, Wood, Generic).",
			Capability: capGeometry,
			Schema:     object(props{"category": text("Category filter, optional")}),
			Query: func(env Env, a Args) (any, error) {
				var out []map[string]any
				for _, m := range env.Materials().List(a.Str("category")) {
					out = append(out, map[string]any{"id": m.ID, "name": m.Name, "category": m.Category})
				}
				return map[string]any{"materials": out, "categories": materials.Categories}, nil
			},
		},
		{
			Name: "search_materials", Description: "Search materials by free text (e.g. 'metallic', 'black plastic', 'brushed aluminum'). Returns up to 5 ranked candidates; when `ambiguous` is true, show the options to the user instead of choosing one.",
			Capability: capGeometry,
			Schema:     object(props{"query": text("What the material should look like or be called")}, "query"),
			Query: func(env Env, a Args) (any, error) {
				ms := env.Materials().Search(a.Str("query"), 5)
				var out []map[string]any
				for _, m := range ms {
					v := materialView(m.Material)
					v["score"] = m.Score
					out = append(out, v)
				}
				return map[string]any{"matches": out, "ambiguous": materials.Ambiguous(ms)}, nil
			},
		},
		{
			Name: "get_material", Description: "Read one library material's properties (color, metalness, roughness, opacity, tags, license).",
			Capability: capGeometry,
			Schema:     object(props{"material_id": text("Material id")}, "material_id"),
			Query: func(env Env, a Args) (any, error) {
				m, ok := env.Materials().Get(a.Str("material_id"))
				if !ok {
					return nil, unknownMaterial(env, a.Str("material_id"))
				}
				v := materialView(m)
				v["license"] = m.License
				return v, nil
			},
		},
	}
}

// appearanceToolEnv is appearanceTool for builders that need the Env (library lookups).
func appearanceToolEnv(name, desc string, extra props, required []string,
	build func(env Env, n *cadir.Node, a Args) (commands.Command, bool, error), explain string) *Tool {
	t := appearanceTool(name, desc, extra, required, nil, explain)
	t.Plan = func(env Env, a Args) (*Plan, error) {
		ts, err := appearanceTargets(env, a)
		if err != nil {
			return nil, err
		}
		var cmds []commands.Command
		for _, n := range ts {
			c, changed, err := build(env, n, a)
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
	}
	return t
}
