package tools

import "fmt"

// meshDetailLevels are the display detail levels, finest first (see rendering.Detail).
var meshDetailLevels = []string{"fine", "normal", "coarse"}

// meshDetailTools adjust how finely the viewport meshes bodies. This is display only: it
// never touches the model, so it is neither a ChangeSet nor undoable.
func meshDetailTools() []*Tool {
	return []*Tool{{
		Name: "set_mesh_detail",
		Description: "Simplify or refine the displayed triangle mesh (not the geometry). Use level=coarse or normal when the " +
			"mesh looks over-tessellated or heavy (e.g. many segments on small cylinders, \"simplify the mesh\"), and fine to " +
			"restore smooth curves. Display only: geometry, selection and exports are unchanged. Returns the triangle count.",
		Capability: capGeometry,
		Schema:     object(props{"level": enum("Mesh detail: fine (default, smooth) | normal | coarse (fewest triangles)", meshDetailLevels...)}, "level"),
		Query: func(env Env, a Args) (any, error) {
			lv := a.Str("level")
			ok := false
			for _, l := range meshDetailLevels {
				ok = ok || l == lv
			}
			if !ok {
				return nil, fmt.Errorf("unknown level %q (use fine, normal or coarse)", lv)
			}
			prev := env.MeshDetail()
			env.SetMeshDetail(lv)
			return map[string]any{"mesh_detail": lv, "previous": prev, "note": "display mesh only; geometry unchanged"}, nil
		},
	}}
}
