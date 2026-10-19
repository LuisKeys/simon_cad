package tools

import (
	"cmp"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/accelone/simoncad/internal/commands"
	"github.com/accelone/simoncad/internal/geometry/features"
	"github.com/accelone/simoncad/internal/meshimport"
)

// importTools brings external geometry in. Nothing about the outside file is trusted: it only
// becomes a node after meshimport has proved it is one closed, consistently oriented,
// non-self-intersecting solid (see internal/meshimport).
// orEmpty lets an enum also accept "": a model that has no value for an optional field often sends
// an empty string, which the tool treats as absent.
func orEmpty(e map[string]any) map[string]any {
	e["enum"] = append(e["enum"].([]any), "")
	return e
}

func importTools() []*Tool {
	return []*Tool{importSTL(), importOBJ(), import3MF(), importSTEP(), decimateMesh()}
}

func importSTL() *Tool {
	return &Tool{
		Name: "import_stl",
		Description: "Import an STL file as a solid body. The file is validated before it is accepted: it must be one closed (watertight), consistently oriented, manifold, non-self-intersecting solid " +
			"within the triangle limit; a file that is not is refused with what is wrong and where (holes, flipped triangles, several shells, self-intersections...), and the model is not changed. " +
			"STL carries no units: ask the user if they are not obviously millimetres, and pass `units`. Give `path` (a file under the project, ./projects_output or ./out) or `mesh` (the id of a file the user already uploaded). " +
			"A refusal that is only about holes can be retried with fill_holes=true if the user wants the holes closed. The imported body is a mesh-derived solid: move, rotate, scale and combine it with the usual tools (transform, boolean, add_hole...). `placement`: file (as authored), center (bounding box centred on the origin) or base (centred on X/Y, resting on Z=0).",
		Capability: capGeometry,
		Schema: object(withMeta(props{
			"path":        text("Path of an .stl file (binary or ASCII)."),
			"mesh":        text("Id (mesh_...) of an STL file the user uploaded in the UI."),
			"filename":    text("Original file name of an uploaded mesh, for display."),
			"units":       orEmpty(enum("Length unit of the file (default mm).", "mm", "cm", "m", "in", "ft")),
			"placement":   orEmpty(enum("Where to put the body (default file).", features.MeshPlacements...)),
			"decimate_to": integer("Reduce the mesh to about this many triangles while importing it (a 70 000-triangle scan takes about 30 s to turn into a solid, 10 000 take 3 s). Needs `path`; for a mesh already imported use decimate_mesh. 0 or omitted: do not reduce.", 0, meshimport.DefaultMaxTriangles),
			"fill_holes":  boolean("Close the holes of a mesh that is otherwise sound (a scan with gaps, a model without its base) by adding flat patches. Off by default because it invents surface: use it only when the user agrees after a refusal for holes. Refused anyway when the patches would add more than half the model's surface."),
		})),
		Plan: func(env Env, a Args) (*Plan, error) { return planMeshImport(env, a, ".stl") },
	}
}

// importOBJ reads a Wavefront OBJ through the same validation and the same import_mesh node as an
// STL. OBJ has no units, so they are asked like an STL's.
func importOBJ() *Tool {
	return &Tool{
		Name: "import_obj",
		Description: "Import a Wavefront OBJ file as a solid body. Only the polygon geometry is read (quads and larger faces are triangulated; normals, texture coordinates, groups and materials are ignored; curves and free-form surfaces are refused). " +
			"The mesh goes through the same strict validation as an STL: it must be one closed (watertight), consistently oriented, manifold, non-self-intersecting solid within the triangle limit; a file that is not is refused with what is wrong and where, and the model is not changed. " +
			"OBJ carries no units: ask the user if they are not obviously millimetres, and pass `units`. Give `path` (a file under the project, ./projects_output or ./out) or `mesh` (the id of a file the user already uploaded). " +
			"A refusal that is only about holes can be retried with fill_holes=true if the user wants the holes closed. The imported body is a mesh-derived solid, like an STL import. `placement`: file (as authored), center (bounding box centred on the origin) or base (centred on X/Y, resting on Z=0).",
		Capability: capGeometry,
		Schema: object(withMeta(props{
			"path":        text("Path of an .obj file."),
			"mesh":        text("Id (mesh_...) of an OBJ file the user uploaded in the UI."),
			"filename":    text("Original file name of an uploaded mesh, for display."),
			"units":       orEmpty(enum("Length unit of the file (default mm).", "mm", "cm", "m", "in", "ft")),
			"placement":   orEmpty(enum("Where to put the body (default file).", features.MeshPlacements...)),
			"decimate_to": integer("Reduce the mesh to about this many triangles while importing it. Needs `path`. 0 or omitted: do not reduce.", 0, meshimport.DefaultMaxTriangles),
			"fill_holes":  boolean("Close the holes of a mesh that is otherwise sound by adding flat patches. Off by default because it invents surface: use it only when the user agrees after a refusal for holes."),
		})),
		Plan: func(env Env, a Args) (*Plan, error) { return planMeshImport(env, a, ".obj") },
	}
}

// import3MF reads a 3MF package through the same validation and the same import_mesh node as an
// STL. 3MF states its unit, so the mesh is converted to mm on reading and there is no units field.
func import3MF() *Tool {
	return &Tool{
		Name: "import_3mf",
		Description: "Import a 3MF file (the standard 3D-printing format) as a solid body. 3MF declares its own length unit, which is converted to millimetres: do not ask the user for units. " +
			"The file goes through the same strict validation as an STL: it must be one closed (watertight), consistently oriented, manifold, non-self-intersecting solid within the triangle limit; build transforms and component trees are applied; " +
			"a file that is not valid is refused with what is wrong and where, and the model is not changed. Colours and materials are ignored; external model parts and unknown required extensions are refused. " +
			"Give `path` (a file under the project, ./projects_output or ./out) or `mesh` (the id of a file the user already uploaded). A refusal that is only about holes can be retried with fill_holes=true if the user wants the holes closed. " +
			"The imported body is a mesh-derived solid, like an STL import. `placement`: file (as authored), center (bounding box centred on the origin) or base (centred on X/Y, resting on Z=0).",
		Capability: capGeometry,
		Schema: object(withMeta(props{
			"path":        text("Path of a .3mf file."),
			"mesh":        text("Id (mesh_...) of a 3MF file the user uploaded in the UI."),
			"filename":    text("Original file name of an uploaded mesh, for display."),
			"placement":   orEmpty(enum("Where to put the body (default file).", features.MeshPlacements...)),
			"decimate_to": integer("Reduce the mesh to about this many triangles while importing it. Needs `path`. 0 or omitted: do not reduce.", 0, meshimport.DefaultMaxTriangles),
			"fill_holes":  boolean("Close the holes of a mesh that is otherwise sound by adding flat patches. Off by default because it invents surface: use it only when the user agrees after a refusal for holes."),
		})),
		Plan: func(env Env, a Args) (*Plan, error) { return planMeshImport(env, a, ".3mf") },
	}
}

// planMeshImport is the shared body of import_stl, import_obj and import_3mf: ext is the file kind the tool takes.
func planMeshImport(env Env, a Args, ext string) (*Plan, error) {
	// Empty strings count as absent: models fill every field they know about.
	if (a.Str("path") != "") == (a.Str("mesh") != "") {
		return nil, fmt.Errorf("give exactly one of path (a %s file) or mesh (the id of an uploaded one)", ext)
	}
	var id, source string
	var rep meshimport.Report
	if a.Str("mesh") != "" {
		id = a.Str("mesh")
		r, ok := env.MeshInfo(id)
		if !ok {
			return nil, fmt.Errorf("mesh %q is not in this project: ask the user to upload the file again", id)
		}
		rep, source = r, cmp.Or(a.Str("filename"), id)
	} else {
		p := a.Str("path")
		if !strings.EqualFold(filepath.Ext(p), ext) {
			return nil, fmt.Errorf("path must be a %s file (got %q)", ext, filepath.Ext(p))
		}
		rp, err := env.ResolveIOPath(p, false)
		if err != nil {
			return nil, err
		}
		st, err := os.Stat(rp)
		if err != nil {
			return nil, fmt.Errorf("cannot read %s: %v", rp, err)
		}
		if st.Size() > meshimport.MaxBytes {
			return nil, fmt.Errorf("%s is larger than %d MB", rp, meshimport.MaxBytes>>20)
		}
		data, err := os.ReadFile(rp)
		if err != nil {
			return nil, fmt.Errorf("cannot read %s: %v", rp, err)
		}
		pr, err := env.StoreMesh(data, meshimport.Options{FillHoles: a.Bool("fill_holes"), DecimateTo: int(a.Num("decimate_to"))})
		if err != nil {
			return nil, fmt.Errorf("%s was not imported: %w", filepath.Base(rp), err)
		}
		id, rep, source = pr.ID, pr.Report, filepath.Base(rp)
	}
	if a.Str("name") == "" {
		a["name"] = strings.TrimSuffix(source, filepath.Ext(source))
	}
	nid, err := uniqueID(env.Graph(), a, "import")
	if err != nil {
		return nil, err
	}
	params := map[string]any{features.ParamMesh: id, "source": source}
	copyOpt(params, a, "units", "placement")
	units := a.Str("units")
	if units == "" || ext == ".3mf" { // a 3MF reaches the store already converted to mm
		units = "mm"
	}
	f := features.MeshUnits[units]
	size := rep.Size()
	unitNote := "file units: " + units
	if rep.FileUnit != "" {
		unitNote = "the file declares " + rep.FileUnit + ", converted to mm"
	}
	explanation := fmt.Sprintf("Imports %s as %q: %d triangles, %s x %s x %s mm, volume %s mm³ (%s).", source, nid, rep.Triangles,
		fmtMM(size[0]*f), fmtMM(size[1]*f), fmtMM(size[2]*f), fmtMM(rep.Volume*f*f*f), unitNote)
	if r := rep.Reduced; r != nil {
		explanation += fmt.Sprintf(" Reduced from %d to %d triangles (volume %+.2f%%).", r.From, r.To, 100*r.VolumeChange)
	}
	// What was repaired is said, not raised as a warning: a warning would stop the import
	// from committing, and the user asked for the repair.
	if len(rep.Warnings) > 0 {
		explanation += " Repaired: " + strings.Join(rep.Warnings, "; ") + "."
	}
	return addPlan(intentOr(a, fmt.Sprintf("Import %s", source)), explanation,
		newNode(env, a, nid, features.TypeImportMesh, params)), nil
}

// decimateMesh really removes triangles from an imported mesh, unlike set_mesh_detail, which only
// changes how finely the viewport draws the same solid.
func decimateMesh() *Tool {
	return &Tool{
		Name: "decimate_mesh",
		Description: "REALLY reduce the number of triangles (faces) of an imported STL body, keeping its shape: edges are collapsed where they change the surface least, flat areas and sharp corners are kept, and the result is validated like an import (still one closed solid). " +
			"Use it when an imported model is too heavy or slow. This changes the model, unlike set_mesh_detail, which only changes how finely the viewport draws it. Give `target_triangles` or `ratio` (the fraction to keep). " +
			"The original mesh is kept, so undo restores it. If the surface cannot be reduced that far without damaging it, more triangles are kept and the result says so.",
		Capability: capGeometry,
		Schema: object(props{
			"feature":          text("Id of the imported body (an import_mesh feature)."),
			"target_triangles": integer("Triangles to keep, about. Give this or ratio; leave the other out (0 counts as left out).", 0, meshimport.DefaultMaxTriangles),
			"ratio":            map[string]any{"type": "number", "minimum": 0, "maximum": 0.99, "description": "Fraction of the triangles to keep, e.g. 0.2 keeps a fifth. Give this or target_triangles; leave the other out (0 counts as left out)."},
			"intent":           text("Why, in natural language."),
		}, "feature"),
		Plan: func(env Env, a Args) (*Plan, error) {
			// Models often send both fields, one of them 0 or empty: only a positive value counts, and
			// when both are given the explicit triangle count wins.
			target, ratio := int(a.Num("target_triangles")), a.Num("ratio")
			if target <= 0 && ratio <= 0 {
				return nil, errors.New("say how far to reduce: target_triangles (e.g. 10000) or ratio (the fraction to keep, e.g. 0.15)")
			}
			n, err := requireFeature(env, a.Str("feature"))
			if err != nil {
				return nil, err
			}
			if n.Type != features.TypeImportMesh {
				return nil, fmt.Errorf("%s is a %s, not an imported mesh: only imported STL bodies have triangles to reduce", n.ID, n.Type)
			}
			id, _ := n.Parameters[features.ParamMesh].(string)
			info, ok := env.MeshInfo(id)
			if !ok {
				return nil, fmt.Errorf("the mesh of %s is not in the project", n.ID)
			}
			if target <= 0 {
				if ratio >= 1 {
					return nil, errors.New("ratio must be between 0 and 1 (the fraction to keep)")
				}
				target = int(float64(info.Triangles) * ratio)
			}
			if target < meshimport.MinDecimateTarget {
				return nil, fmt.Errorf("a mesh cannot be reduced below %d triangles (asked for %d)", meshimport.MinDecimateTarget, target)
			}
			if target >= info.Triangles {
				return nil, fmt.Errorf("%s has %d triangles already, not more than the %d asked for", n.ID, info.Triangles, target)
			}
			p, err := env.ReduceMesh(id, target)
			if err != nil {
				return nil, fmt.Errorf("%s was not reduced: %w", n.ID, err)
			}
			r := p.Report.Reduced
			explanation := fmt.Sprintf("Reduced %s from %d to %d triangles (%.1f%% kept); its volume changed by %+.2f%%.",
				n.ID, r.From, r.To, 100*float64(r.To)/float64(r.From), 100*r.VolumeChange)
			if len(p.Report.Warnings) > 0 {
				explanation += " " + strings.Join(p.Report.Warnings, "; ") + "."
			}
			return &Plan{Intent: intentOr(a, fmt.Sprintf("Reduce %s to about %d triangles", n.ID, target)), Explanation: explanation,
				Commands: []commands.Command{&commands.SetParameter{ID: n.ID, Name: features.ParamMesh, Value: p.ID}}}, nil
		},
	}
}
