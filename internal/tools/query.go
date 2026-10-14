package tools

import (
	"math"
	"strings"

	"github.com/accelone/simoncad/internal/selectors"
)

func queryTools() []*Tool {
	return []*Tool{
		{
			Name:        "get_model_summary",
			Description: "Describe the current model: every feature (id, type, key parameters, status), bounding boxes and volumes of the resulting bodies, and recent checkpoints. Call this before editing an unfamiliar model.",
			Capability:  capGeometry,
			Schema:      object(props{}),
			Query:       func(env Env, a Args) (any, error) { return ModelSummary(env) },
		},
		{
			Name: "measure_distance",
			Description: "Measure between two things. Each of `from`/`to` is a selector like face(feature=plate,side=+Z) / body(feature=plate) or a bare feature id (= its body). " +
				"For two parallel faces the result is the perpendicular gap; otherwise the distance between centroids. Also returns the minimum gap between the bounding boxes.",
			Capability: capGeometry,
			Schema:     object(props{"from": text("Selector or feature id"), "to": text("Selector or feature id")}, "from", "to"),
			Query: func(env Env, a Args) (any, error) {
				d1, err := describe(env, a.Str("from"))
				if err != nil {
					return nil, err
				}
				d2, err := describe(env, a.Str("to"))
				if err != nil {
					return nil, err
				}
				out := map[string]any{"from": d1.Selector, "to": d2.Selector}
				kind := "centroid_distance"
				dist := vecDist(d1.Centroid, d2.Centroid)
				if d1.Normal != nil && d2.Normal != nil {
					cross := math.Abs(d1.Normal[0]*d2.Normal[0] + d1.Normal[1]*d2.Normal[1] + d1.Normal[2]*d2.Normal[2])
					if cross > 1-1e-9 {
						kind = "parallel_face_gap"
						dist = math.Abs((d2.Centroid[0]-d1.Centroid[0])*d1.Normal[0] + (d2.Centroid[1]-d1.Centroid[1])*d1.Normal[1] + (d2.Centroid[2]-d1.Centroid[2])*d1.Normal[2])
					}
				}
				var gap2 float64
				for i := 0; i < 3; i++ {
					g := math.Max(0, math.Max(d1.BBox.Min[i]-d2.BBox.Max[i], d2.BBox.Min[i]-d1.BBox.Max[i]))
					gap2 += g * g
				}
				out["distance_mm"], out["measure"], out["bbox_gap_mm"] = round6(dist), kind, round6(math.Sqrt(gap2))
				return out, nil
			},
		},
		{
			Name:        "export_step",
			Description: "Export the current model to a STEP file at `path` (does not change the model).",
			Capability:  capGeometry,
			Schema:      object(props{"path": text("Output file path, e.g. out/plate.step")}, "path"),
			Query: func(env Env, a Args) (any, error) {
				p, err := env.ResolveIOPath(a.Str("path"), true)
				if err != nil {
					return nil, err
				}
				return env.Export("step", p, 0)
			},
		},
		{
			Name:        "export_stl",
			Description: "Export the current model to a binary STL file at `path` for 3D printing (does not change the model).",
			Capability:  capGeometry,
			Schema: object(props{"path": text("Output file path, e.g. out/plate.stl"),
				"deflection_mm": map[string]any{"type": "number", "description": "Mesh deflection in mm (default 0.05)", "exclusiveMinimum": 0.0, "maximum": 5.0}}, "path"),
			Query: func(env Env, a Args) (any, error) {
				p, err := env.ResolveIOPath(a.Str("path"), true)
				if err != nil {
					return nil, err
				}
				return env.Export("stl", p, a.NumOr("deflection_mm", 0.05))
			},
		},
	}
}

func round6(v float64) float64 { return math.Round(v*1e6) / 1e6 }

func vecDist(a, b [3]float64) float64 {
	return math.Sqrt((a[0]-b[0])*(a[0]-b[0]) + (a[1]-b[1])*(a[1]-b[1]) + (a[2]-b[2])*(a[2]-b[2]))
}

func describe(env Env, ref string) (selectors.Descriptor, error) {
	if !strings.Contains(ref, "(") {
		ref = "body(feature=" + ref + ")"
	}
	sel, err := selectors.Parse(ref)
	if err != nil {
		return selectors.Descriptor{}, err
	}
	return resolveSelector(env, sel)
}

// ModelSummary is the deterministic description used by get_model_summary and the
// agent's context builder.
func ModelSummary(env Env) (map[string]any, error) {
	g := env.Graph()
	m, err := env.Model()
	if err != nil {
		return nil, err
	}
	var feats []map[string]any
	for _, n := range g.Nodes {
		f := map[string]any{"id": n.ID, "type": n.Type, "name": n.Metadata.Name}
		if len(n.Parameters) > 0 {
			f["parameters"] = n.Parameters
		}
		if len(n.Expressions) > 0 {
			f["expressions"] = n.Expressions
		}
		if len(n.Dependencies) > 0 {
			f["depends_on"] = n.Dependencies
		}
		if len(n.References) > 0 {
			f["references"] = n.References
		}
		if n.Intent != "" {
			f["intent"] = n.Intent
		}
		status := "ok"
		if r := m.Nodes[n.ID]; r != nil {
			status = r.Status
			if len(r.Errors) > 0 {
				f["errors"] = r.Errors
			}
		}
		f["status"] = status
		if !n.Appearance.IsZero() {
			f["appearance"] = AppearanceView(n.Appearance)
		}
		if n.Group != "" {
			f["group"] = n.Group
		}
		feats = append(feats, f)
	}
	var bodies []map[string]any
	for _, id := range m.Leaves {
		p := m.Nodes[id].Body.Props
		bodies = append(bodies, map[string]any{
			"feature": id, "volume_mm3": round6(p.Volume), "area_mm2": round6(p.Area),
			"bbox_min": []float64{round6(p.MinX), round6(p.MinY), round6(p.MinZ)},
			"bbox_max": []float64{round6(p.MaxX), round6(p.MaxY), round6(p.MaxZ)},
			"faces":    p.Faces,
		})
	}
	var cps []map[string]any
	for _, c := range env.Checkpoints().Recent(5) {
		cps = append(cps, map[string]any{"id": c.ID, "label": c.Label})
	}
	out := map[string]any{"units": g.Units, "features": feats, "bodies": bodies, "recent_checkpoints": cps}
	if len(feats) == 0 {
		out["note"] = "The model is empty."
	}
	if failed := m.Failed(); len(failed) > 0 {
		out["failed_features"] = failed
	}
	return out, nil
}
