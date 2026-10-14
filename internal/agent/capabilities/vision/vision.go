// Package vision is the Vision capability of the Simon CAD Agent: it reads images (sketches,
// scans, drawings, whiteboard photos) into structured sketch proposals. It never edits the
// model itself: proposals live outside the CAD-IR, and geometry only appears through
// create_from_image, which produces an ordinary ChangeSet the user previews and commits.
package vision

import (
	"fmt"
	"strings"

	"github.com/accelone/simoncad/internal/app"
	"github.com/accelone/simoncad/internal/tools"
	"github.com/accelone/simoncad/internal/vision"
)

const Name = "vision"

type Capability struct{}

func New() Capability { return Capability{} }

func (Capability) Name() string { return Name }

func (Capability) SystemPrompt() string {
	return strings.TrimSpace(`
Vision capability — turning an attached image (hand sketch, scan, simple technical drawing, photo of paper or a whiteboard; PNG/JPEG/WebP) into editable parametric geometry.

Workflow
1. Look at the image and call analyze_image ONCE with a structured proposal. Coordinates are image pixels, x to the right, y DOWN, from the top-left corner. image_id and image_size come from the "Attached image" line.
2. Describe geometry only with the allowed primitives (line, arc, circle, rectangle, polygon, slot). Group them into closed profiles: an outer loop plus holes/cutouts for loops nested inside it. Give every entity a confidence between 0 and 1; use lower numbers for wobbly or smudged strokes. Use relationships (concentric, equal, horizontal, vertical, symmetric) only when the drawing clearly shows them.
3. Dimensions: copy every dimension that is written or printed on the drawing with its value in millimetres, its source (handwritten | printed), your reading confidence and what it binds to (e1.w, e2.diameter...). Add the ones you still need as source=missing: overall size and, for a solid, the thickness as kind=depth.
4. NEVER invent a dimension. NEVER infer real size from the pixel size of the image or from "typical" sizes. A drawing without any real dimension has no scale.
5. Read the report returned by analyze_image. If it lists missing or unconfirmed items, ask the user ONE short question that covers exactly those items (for a plate: "What are the overall width and the thickness?" — one linear dimension fixes the scale for the whole drawing). If the only missing item is the thickness, ask only for that. Prefer a single concise question to guessing.
6. Record the user's answers: confirm_detected_dimension (value in mm; also to confirm or correct a low-confidence reading), set_sketch_scale if they give a length without naming a dimension. Corrections to the interpretation itself ("that circle should be a hole", "this edge should be vertical", "those two holes are symmetric", "make this 40 mm") go through update_sketch_proposal / confirm_detected_dimension; they change the proposal, not the model.
7. Boolean intent: nested closed contours are holes/cutouts of the outer profile; two overlapping closed shapes are ambiguous. If the user said what to do ("join these", "cut this circle out of the plate", "keep only the intersection", "remove the overlap") set operations accordingly (extrude each profile, then union / subtract / intersect). If the intent is unclear, ask: "I detected two overlapping closed shapes. Should I join them, subtract one from the other, or keep them separate?" — or put your best candidate in operations so the preview shows it; never silently pick one.
8. When the report says ready (or after the user supplied the missing values), call create_from_image. It builds a normal sketch plus extrude / cut / boolean features and always leaves the ChangeSet waiting for the user's approval: tell the user to review the preview and commit, and do not repeat the call.
9. To put the shape on an existing face ("create this on this face") pass face="selection" when a planar face is selected; to cut through a selected part use operations with kind=cut and target "selection" (or feature:<id>) and through=true.
10. Do not use more than one image for one proposal. If the image is not a flat 2D drawing (a photo of a 3D object), say so and ask for a flat sketch or the dimensions; 3D reconstruction is out of scope.

Results from image-derived tools are previews until the user commits them; if a call is refused, read the error — it names what is missing — and ask the user for exactly that.`)
}

func (Capability) ToolSubset(reg *tools.Registry) []*tools.Tool { return reg.Subset(tools.CapVision) }

// Relevant: this capability only has work to do while an image is in play: attached to this
// turn, or an interpretation still waiting to be used. Its tool schemas are among the largest
// in the catalog, so a turn about anything else (a material, a fillet) never pays for them,
// even in a project that started from an image.
func (Capability) Relevant(a *app.App, images []string) bool {
	return len(images) > 0 || a.Vision.HasOpen()
}

// BuildContext lists the open interpretations and what each still needs, in a stable order.
func (Capability) BuildContext(a *app.App) (string, error) {
	var items []*vision.Interpretation
	for _, it := range a.Vision.List() {
		if it.Open() || len(it.Used) > 0 {
			items = append(items, it)
		}
	}
	if len(items) == 0 {
		return "## Image interpretations\nNone yet.\n", nil
	}
	var sb strings.Builder
	sb.WriteString("## Image interpretations\n")
	for _, it := range items {
		r := it.Report()
		status := "ready"
		switch {
		case len(r.Errors) > 0:
			status = "has errors"
		case !r.Ready:
			status = "needs input"
		}
		fmt.Fprintf(&sb, "- %s (image %s, revision %d): %s\n", it.ID, it.Proposal.ImageID, it.Revision, status)
		for _, e := range r.Errors {
			fmt.Fprintf(&sb, "  error: %s\n", e)
		}
		for _, m := range r.Missing {
			fmt.Fprintf(&sb, "  missing: %s — %s\n", m.Dimension, m.Ask)
		}
		for _, u := range r.Unconfirmed {
			fmt.Fprintf(&sb, "  unconfirmed: %s\n", u)
		}
		for _, am := range r.Ambiguous {
			fmt.Fprintf(&sb, "  ambiguous: %s\n", am.Question)
		}
		if len(it.Used) > 0 {
			fmt.Fprintf(&sb, "  created features: %s\n", strings.Join(it.Used, ", "))
		}
	}
	return sb.String(), nil
}
