package tools

import (
	"fmt"
	"math"
	"sort"

	"github.com/accelone/simoncad/internal/geometry/features"
	"github.com/accelone/simoncad/internal/meshcheck"
	"github.com/accelone/simoncad/internal/meshgeo"
)

type printIssue struct {
	Severity string `json:"severity"` // error | warning
	Code     string `json:"code"`
	Message  string `json:"message"`
}

func printabilityTools() []*Tool {
	return []*Tool{
		{
			Name: "validate_printability",
			Description: "Check that a body can be 3D printed: closed (watertight) manifold mesh, one connected piece, no features thinner than the nozzle (wall / ridge thickness measured by casting rays through the walls), " +
				"optional fit on the print bed, and for stamps the ridge width and relief aspect. Returns printable=true/false and a list of issues with codes (NON_MANIFOLD_MESH, DISCONNECTED, THIN_FEATURE, NOT_PRINTABLE, TOO_LARGE, FRAGILE_RELIEF). " +
				"`nozzle_mm` (default 0.4) and `min_wall_mm` (default 2 nozzle widths) set the limits; pass `bed` [x,y,z] mm to check the fit.",
			Capability: capGeometry,
			Schema: object(props{
				"object":      text("Feature id (default: every visible body)"),
				"nozzle_mm":   positive("Nozzle diameter in mm (default 0.4)"),
				"min_wall_mm": positive("Thinnest wall or ridge to accept as printable without a warning (default 2 × nozzle)"),
				"bed":         array("Print bed size [x,y,z] in mm", positive("mm"), 3, 3),
				"deflection":  positive("Mesh deflection in mm (default 0.05)"),
			}),
			Query: queryValidatePrintability,
		},
	}
}

func queryValidatePrintability(env Env, a Args) (any, error) {
	m, err := env.Model()
	if err != nil {
		return nil, err
	}
	ids := m.Leaves
	if id := a.Str("object"); id != "" {
		if _, err := requireBounds(env, id); err != nil {
			return nil, err
		}
		ids = []string{id}
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("the model has no solid geometry to check")
	}
	nozzle := a.NumOr("nozzle_mm", 0.4)
	minWall := a.NumOr("min_wall_mm", 2*nozzle)
	defl := a.NumOr("deflection", 0.05)
	var bodies []map[string]any
	printable := true
	for _, id := range ids {
		r := m.Nodes[id]
		if r == nil || r.Body == nil {
			continue
		}
		b := r.Body
		issues := []printIssue{} // never null in the JSON
		add := func(sev, code, f string, args ...any) {
			issues = append(issues, printIssue{sev, code, fmt.Sprintf(f, args...)})
		}
		valid, _ := b.Shape.IsValid()
		if !valid {
			add("error", "INVALID_BREP", "the kernel reports an invalid solid")
		}
		mesh, err := b.Shape.Tessellate(defl)
		if err != nil {
			return nil, fmt.Errorf("meshing %s: %v", id, err)
		}
		rep := meshcheck.Check(mesh.Positions, mesh.Indices)
		switch {
		case rep.OpenEdges > 0:
			add("error", features.CodeNonManifold, "the mesh is not watertight: %d open edges", rep.OpenEdges)
		case rep.NonManifold > 0:
			add("error", features.CodeNonManifold, "%d mesh edges are shared by more than two triangles", rep.NonManifold)
		}
		if rep.SignedVolume < 0 {
			add("error", features.CodeNonManifold, "the mesh normals point inward (negative volume)")
		}
		if rep.Components > 1 {
			add("error", "DISCONNECTED", "the body is %d separate pieces: a slicer prints them as loose parts (a floating relief or a cut that splits it)", rep.Components)
		}
		var thin map[string]any
		if rep.Watertight() {
			thin = thicknessReport(mesh.Positions, mesh.Indices, nozzle, minWall, &issues)
		}
		if bed, ok := a["bed"].([]any); ok && len(bed) == 3 {
			size := [3]float64{b.Props.MaxX - b.Props.MinX, b.Props.MaxY - b.Props.MinY, b.Props.MaxZ - b.Props.MinZ}
			sorted := func(v [3]float64) []float64 { s := v[:]; sort.Float64s(s); return s }
			bs := [3]float64{bed[0].(float64), bed[1].(float64), bed[2].(float64)}
			// the part may be turned: compare the sorted dimensions, XY of the bed with the two largest
			if ps, bb := sorted(size), sorted(bs); ps[0] > bb[0]+1e-9 || ps[1] > bb[1]+1e-9 || ps[2] > bb[2]+1e-9 {
				add("error", "TOO_LARGE", "the part (%s × %s × %s mm) does not fit the bed (%s × %s × %s mm) in any orientation",
					fmtMM(size[0]), fmtMM(size[1]), fmtMM(size[2]), fmtMM(bs[0]), fmtMM(bs[1]), fmtMM(bs[2]))
			}
		}
		issues = append(issues, stampPrintIssues(env, id, nozzle, minWall)...)
		ok := true
		for _, is := range issues {
			ok = ok && is.Severity != "error"
		}
		printable = printable && ok
		entry := map[string]any{"object": id, "printable": ok, "issues": issues,
			"metrics": map[string]any{"volume_mm3": b.Props.Volume, "triangles": rep.Triangles, "components": rep.Components, "watertight": rep.Watertight(),
				"size_mm": [3]float64{b.Props.MaxX - b.Props.MinX, b.Props.MaxY - b.Props.MinY, b.Props.MaxZ - b.Props.MinZ}}}
		if thin != nil {
			entry["thickness"] = thin
		}
		bodies = append(bodies, entry)
	}
	return map[string]any{"printable": printable, "nozzle_mm": nozzle, "min_wall_mm": minWall, "bodies": bodies}, nil
}

// thicknessReport casts a ray from sampled surface triangles inward through the solid; the
// distance to the opposite surface is the local wall thickness (ridge width for the side walls of
// a ridge). It appends warnings/errors for features thinner than the limits.
func thicknessReport(pos []float32, idx []uint32, nozzle, minWall float64, issues *[]printIssue) map[string]any {
	bvh := meshgeo.Build(pos, idx)
	n := bvh.Triangles()
	if n == 0 {
		return nil
	}
	const maxSamples = 4000
	stride := 1
	if n > maxSamples {
		stride = n / maxSamples
	}
	type sample struct {
		t  float64
		at meshgeo.Vec
	}
	var samples []sample
	for ti := 0; ti < n; ti += stride {
		a, b, c := bvh.Triangle(ti)
		cen := meshgeo.Vec{(a[0] + b[0] + c[0]) / 3, (a[1] + b[1] + c[1]) / 3, (a[2] + b[2] + c[2]) / 3}
		u, v := sub3(b, a), sub3(c, a)
		nrm := cross3(u, v)
		l := math.Sqrt(nrm[0]*nrm[0] + nrm[1]*nrm[1] + nrm[2]*nrm[2])
		if l < 1e-12 {
			continue
		}
		dir := meshgeo.Vec{-nrm[0] / l, -nrm[1] / l, -nrm[2] / l} // inward (outward winding)
		o := meshgeo.Vec{cen[0] + dir[0]*1e-4, cen[1] + dir[1]*1e-4, cen[2] + dir[2]*1e-4}
		if h, ok := bvh.Raycast(o, dir, math.Inf(1)); ok {
			samples = append(samples, sample{h.T + 1e-4, cen})
		}
	}
	if len(samples) == 0 {
		return nil
	}
	sort.Slice(samples, func(i, j int) bool { return samples[i].t < samples[j].t })
	minT := samples[0].t
	// A thin feature shows as a patch of thin samples. The tip of an acute wedge (two strokes
	// meeting at a shallow angle) is thin by definition but slicers close it: only a patch of at
	// least 1% of the sampled surface (and 3 samples) counts.
	thinN := sort.Search(len(samples), func(i int) bool { return samples[i].t >= minWall })
	report := map[string]any{"samples": len(samples), "min_mm": round6(minT), "p1_mm": round6(samples[len(samples)/100].t), "below_min_wall": thinN}
	if thinN >= 3 && float64(thinN) >= 0.01*float64(len(samples)) {
		at := samples[0].at
		loc := fmt.Sprintf("near (%s, %s, %s)", fmtMM(at[0]), fmtMM(at[1]), fmtMM(at[2]))
		if minT < nozzle*0.9 {
			*issues = append(*issues, printIssue{"error", features.CodeNotPrintable,
				fmt.Sprintf("features as thin as %s mm %s are below the %s mm nozzle and will not print: thicken them (stroke width / relief) or use a finer nozzle", fmtMM(minT), loc, fmtMM(nozzle))})
		} else {
			*issues = append(*issues, printIssue{"warning", "THIN_FEATURE",
				fmt.Sprintf("%d sampled spots are thinner than %s mm (thinnest %s mm %s): they print as a single perimeter and may be fragile", thinN, fmtMM(minWall), fmtMM(minT), loc)})
		}
	}
	return report
}

func sub3(a, b meshgeo.Vec) meshgeo.Vec { return meshgeo.Vec{a[0] - b[0], a[1] - b[1], a[2] - b[2]} }
func cross3(a, b meshgeo.Vec) meshgeo.Vec {
	return meshgeo.Vec{a[1]*b[2] - a[2]*b[1], a[2]*b[0] - a[0]*b[2], a[0]*b[1] - a[1]*b[0]}
}

// stampPrintIssues adds checks that only make sense when the body is a recipe's stamp: the ridge
// width against the nozzle, and tall thin relief that may snap.
func stampPrintIssues(env Env, id string, nozzle, minWall float64) []printIssue {
	n, err := findRecipeNode(env, id)
	if err != nil {
		return nil
	}
	var out []printIssue
	design, _ := n.Parameters["design"].(map[string]any)
	h, _ := design["relief_height"].(float64)
	sw, _ := design["stroke_width"].(float64)
	if sw > 0 {
		if sw < nozzle {
			out = append(out, printIssue{"error", features.CodeNotPrintable, fmt.Sprintf("the ridge width %s mm is below the %s mm nozzle", fmtMM(sw), fmtMM(nozzle))})
		} else if sw < minWall {
			out = append(out, printIssue{"warning", "THIN_FEATURE", fmt.Sprintf("the ridge width %s mm is under %s mm: it prints as one perimeter", fmtMM(sw), fmtMM(minWall))})
		}
		if h > 4*sw {
			out = append(out, printIssue{"warning", "FRAGILE_RELIEF", fmt.Sprintf("ridges %s mm tall and %s mm wide (aspect %.1f) may snap when pressed: lower the relief or widen the strokes", fmtMM(h), fmtMM(sw), h/sw)})
		}
	}
	return out
}
