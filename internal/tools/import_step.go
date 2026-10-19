package tools

import (
	"cmp"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/accelone/simoncad/internal/geometry/features"
	"github.com/accelone/simoncad/internal/stepimport"
)

// importSTEP brings in a STEP file. Unlike an STL it carries exact geometry (real cylinders,
// planes, B-splines) and its own units, so fillets, holes and face selectors work on the result
// as on any modelled body.
func importSTEP() *Tool {
	return &Tool{
		Name: "import_step",
		Description: "Import a STEP file (.step or .stp) as a solid body with its exact geometry: cylinders stay cylinders and planes stay planes, so fillet, chamfer, add_hole and face selectors work on it much better than on an imported STL. " +
			"STEP declares its own units, which are converted to millimetres: do not ask the user for units. The file is checked first: it must be readable, geometrically valid and hold at least one solid (surface-only or empty files are refused with the reason, and the model is not changed). " +
			"Give `path` (a file under the project, ./projects_output or ./out) or `step` (the id of a file the user already uploaded). A file with several solids (an assembly) becomes one body holding all of them. " +
			"`placement`: file (as authored, default), center (bounding box centred on the origin) or base (centred on X/Y, resting on Z=0). Move, rotate, scale and combine the result with the usual tools.",
		Capability: capGeometry,
		Schema: object(withMeta(props{
			"path":      text("Path of a .step or .stp file."),
			"step":      text("Id (step_...) of a STEP file the user uploaded in the UI."),
			"filename":  text("Original file name of an uploaded file, for display."),
			"placement": orEmpty(enum("Where to put the body (default file).", features.MeshPlacements...)),
		})),
		Plan: func(env Env, a Args) (*Plan, error) {
			// Empty strings count as absent: models fill every field they know about.
			if (a.Str("path") != "") == (a.Str("step") != "") {
				return nil, errors.New("give exactly one of path (a .step file) or step (the id of an uploaded one)")
			}
			var id, source string
			var rep stepimport.Report
			if a.Str("step") != "" {
				id = a.Str("step")
				r, ok := env.StepInfo(id)
				if !ok {
					return nil, fmt.Errorf("STEP file %q is not in this project: ask the user to upload it again", id)
				}
				rep, source = r, cmp.Or(a.Str("filename"), id)
			} else {
				p := a.Str("path")
				if ext := strings.ToLower(filepath.Ext(p)); ext != ".step" && ext != ".stp" {
					return nil, fmt.Errorf("path must be a .step or .stp file (got %q)", filepath.Ext(p))
				}
				rp, err := env.ResolveIOPath(p, false)
				if err != nil {
					return nil, err
				}
				st, err := os.Stat(rp)
				if err != nil {
					return nil, fmt.Errorf("cannot read %s: %v", rp, err)
				}
				if st.Size() > stepimport.MaxBytes {
					return nil, fmt.Errorf("%s is larger than %d MB", rp, stepimport.MaxBytes>>20)
				}
				data, err := os.ReadFile(rp)
				if err != nil {
					return nil, fmt.Errorf("cannot read %s: %v", rp, err)
				}
				pr, err := env.StoreStep(data)
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
			params := map[string]any{features.ParamStep: id, "source": source}
			copyOpt(params, a, "placement")
			size := rep.Size()
			explanation := fmt.Sprintf("Imports %s as %q: %d solid(s), %d faces, %s x %s x %s mm, volume %s mm³ (units converted to mm).", source, nid,
				rep.Solids, rep.Faces, fmtMM(size[0]), fmtMM(size[1]), fmtMM(size[2]), fmtMM(rep.Volume))
			return addPlan(intentOr(a, fmt.Sprintf("Import %s", source)), explanation,
				newNode(env, a, nid, features.TypeImportStep, params)), nil
		},
	}
}
