// Package visual is the Visual capability of the Simon CAD Agent: design parameters, region
// ownership and locks, offscreen renders and reference calibration, with the rules of
// visual closed-loop refinement. It never judges the model from the object tree alone.
package visual

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/accelone/simoncad/internal/app"
	"github.com/accelone/simoncad/internal/tools"
	vis "github.com/accelone/simoncad/internal/visual"
)

const Name = "visual"

type Capability struct{}

func New() Capability { return Capability{} }

func (Capability) Name() string { return Name }

// Rules are the explicit modeling rules of visual refinement.
const Rules = `Visual modeling rules
- Fix global shape before details: proportions and silhouette first, small features last.
- Never assume the model looks correct without rendering it (render_model); do not self-evaluate from the object tree alone.
- Do not make more than 3 major geometry changes per iteration; re-render after every structural change.
- Preserve regions that are already correct; never modify a locked region (unlock only when the user asks).
- Prefer adjusting design parameters (set_parameter) over rebuilding objects; build major geometry from parameters (define_parameters + bind_parameters).
- Revert changes that reduce visual similarity. Do not advance modeling stages until the threshold scores are reached.
- Never add small details to compensate for incorrect global proportions.`

func (Capability) SystemPrompt() string {
	return strings.TrimSpace(`
Visual capability — reference-driven modeling with design parameters, regions and renders.
- Major geometry should be controlled by design parameters: define_parameters (e.g. set "vehicle": length, width, height, wheelbase, front_overhang, rear_overhang, hood_height, hood_length, roof_height, cabin_start, cabin_end, windshield_angle), create the bodies, then bind_parameters so their dimensions and positions are expressions over the set. Corrections are then one set_parameter each.
- Map every visual region to the objects and parameters that shape it with define_region (e.g. roof → objects [roof, cabin], parameters [vehicle.roof_height, vehicle.roof_length]). lock_region protects a region that is already correct.
- render_model renders fixed cameras (left/front/top orthographic, front_3q perspective); silhouette mode gives a binary mask. Reference images are calibrated with calibrate_reference_image.

` + Rules)
}

func (Capability) ToolSubset(reg *tools.Registry) []*tools.Tool { return reg.Subset(tools.CapVisual) }

// Relevant: reference-driven modeling starts from an image, and its state (design
// parameters, regions, references) persists once it has. The condition mirrors
// BuildContext, so a capability that would report "nothing yet" sends no tools either.
func (Capability) Relevant(a *app.App, images []string) bool {
	if len(images) > 0 || len(tools.ParameterValues(a.Graph)) > 0 {
		return true
	}
	vs := a.Visual
	vs.Lock()
	defer vs.Unlock()
	return len(vs.Regions) > 0 || len(vs.References) > 0
}

// BuildContext lists parameters, regions, references and the latest scores (deterministic).
func (Capability) BuildContext(a *app.App) (string, error) {
	params := tools.ParameterValues(a.Graph)
	vs := a.Visual
	vs.Lock()
	defer vs.Unlock()
	if len(params) == 0 && len(vs.Regions) == 0 && len(vs.References) == 0 {
		return "## Visual refinement\nNo design parameters, regions or reference images yet.\n", nil
	}
	var sb strings.Builder
	sb.WriteString("## Visual refinement\n")
	if len(params) > 0 {
		keys := make([]string, 0, len(params))
		for k := range params {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		sb.WriteString("Design parameters: ")
		for i, k := range keys {
			if i > 0 {
				sb.WriteString(", ")
			}
			fmt.Fprintf(&sb, "%s=%g", k, params[k])
		}
		sb.WriteString("\n")
	}
	for _, r := range vs.Regions {
		lock := ""
		if r.Locked {
			lock = " [LOCKED]"
		}
		fmt.Fprintf(&sb, "Region %s%s: objects %v, parameters %v\n", r.Name, lock, r.Objects, r.Parameters)
	}
	for _, r := range vs.References {
		view := "uncalibrated"
		if r.Calibration != nil {
			view = fmt.Sprintf("%s (az %.0f°, el %.0f°, %s)", r.Calibration.View, r.Calibration.Azimuth, r.Calibration.Elevation, r.Calibration.Projection)
		}
		fmt.Fprintf(&sb, "Reference image %s: %s\n", r.ImageID, view)
	}
	if len(vs.ReferenceGeometry) > 0 {
		b, _ := json.Marshal(vs.ReferenceGeometry)
		fmt.Fprintf(&sb, "Reference proportions: %s\n", b)
	}
	if vs.Stage < len(vis.Stages) && len(vs.History) > 0 {
		fmt.Fprintf(&sb, "Modeling stage: %s\n", vis.Stages[vs.Stage].Name)
	}
	if last := vs.Last(); last != nil {
		b, _ := json.Marshal(last.Scores)
		fmt.Fprintf(&sb, "Latest visual scores (iteration %d): %s\n", last.N, b)
	}
	return sb.String(), nil
}
