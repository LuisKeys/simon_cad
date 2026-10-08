package refine

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"

	"github.com/accelone/simoncad/internal/agent/runtime"
	"github.com/accelone/simoncad/internal/visual"
)

// ReferenceAnalysis is what the vision model reads from one reference image.
type ReferenceAnalysis struct {
	View        string `json:"view"`
	ObjectClass string `json:"object_class"`
	Calibration struct {
		Azimuth    float64 `json:"azimuth"`
		Elevation  float64 `json:"elevation"`
		Projection string  `json:"projection"`
		FocalMM    float64 `json:"focal_length_mm"`
		Mirrored   bool    `json:"mirrored"`
	} `json:"calibration"`
	Landmarks  visual.Landmarks   `json:"landmarks"`
	Dimensions map[string]float64 `json:"known_dimensions_mm"`
	Notes      string             `json:"notes"`
}

const analyzerPrompt = `You analyze a reference image for CAD modeling. You never design anything; you measure what the image shows and answer only by calling submit_reference_analysis.

- view: which side of the object the camera sees (left, right, front, rear, top, front_3q, rear_3q, other). For vehicles "left" is the driver's left side (US); the front of the object is its direction of travel.
- calibration: estimate the camera. azimuth is measured around the vertical axis from the object's front (0) towards its left side (90), rear (180), right (270); elevation from the horizon (90 = looking straight down). projection is orthographic for drawings/blueprints and near-orthographic photos with negligible perspective, else perspective with an estimated 35 mm-equivalent focal_length_mm. mirrored=true when, in a side view, the object's front points to the RIGHT of the image.
- landmarks: points in normalized image coordinates (x 0..1 left→right, y 0..1 top→bottom), only for points actually visible. For vehicle-like objects in a side view use: front_tip, rear_tip, front_wheel_center, rear_wheel_center, wheel_radius ([radius as a fraction of the image width, 0]), hood_start, hood_end, windshield_lower, windshield_upper, roof_front, roof_rear, belt_line, body_bottom, roof_max, ground. In a front view: body_left, body_right, roof_left, roof_right, and the wheel contact points. For other objects use descriptive snake_case names for extreme points and major feature corners.
- known_dimensions_mm: only dimensions printed in the image or stated by the user. Never invent sizes.`

func analysisTool() runtime.ToolDef {
	return runtime.ToolDef{Name: "submit_reference_analysis", Description: "Submit the analysis of the reference image.", InputSchema: obj(map[string]any{
		"view":         map[string]any{"type": "string", "enum": []string{"left", "right", "front", "rear", "top", "front_3q", "rear_3q", "other"}},
		"object_class": str(),
		"calibration": obj(map[string]any{"azimuth": num(), "elevation": num(), "projection": map[string]any{"type": "string", "enum": []string{visual.Orthographic, visual.Perspective}},
			"focal_length_mm": num(), "mirrored": map[string]any{"type": "boolean"}}, "azimuth", "elevation", "projection"),
		"landmarks":           pointMap(),
		"known_dimensions_mm": map[string]any{"type": "object", "additionalProperties": num()},
		"notes":               str(),
	}, "view", "object_class", "calibration", "landmarks")}
}

// Analyzer extracts view, calibration and landmarks from reference images (vision model)
// and segments their silhouettes (deterministic).
type Analyzer struct {
	Vision runtime.Provider
	Usage  runtime.Usage
}

// Analyze fills one reference. A manual calibration is never overwritten.
func (an *Analyzer) Analyze(ctx context.Context, ref *visual.Reference, data []byte, mediaType, goal string) error {
	if m, err := visual.Segment(data); err == nil {
		ref.SetMask(m)
		ref.MaskError = ""
	} else {
		ref.MaskError = err.Error()
	}
	raw, err := structured(ctx, an.Vision, analyzerPrompt, []runtime.Block{imageBlock(data, mediaType),
		{Type: "text", Text: fmt.Sprintf("Reference image %s (%dx%d px). What the user wants to model: %s", ref.ImageID, ref.Width, ref.Height, goal)}}, analysisTool(), &an.Usage)
	if err != nil {
		return err
	}
	var ra ReferenceAnalysis
	if err := json.Unmarshal(raw, &ra); err != nil {
		return fmt.Errorf("reference analysis: %w", err)
	}
	ref.ObjectClass, ref.Notes, ref.Dimensions = ra.ObjectClass, ra.Notes, ra.Dimensions
	ref.Landmarks = visual.Landmarks{}
	for k, p := range ra.Landmarks {
		if k == "wheel_radius" || p[0] >= -0.05 && p[0] <= 1.05 && p[1] >= -0.05 && p[1] <= 1.05 {
			ref.Landmarks[k] = p
		}
	}
	if ref.Calibration == nil || !ref.Calibration.Manual {
		c := visual.Calibration{View: ra.View, Azimuth: math.Mod(ra.Calibration.Azimuth+360, 360), Elevation: ra.Calibration.Elevation,
			Projection: ra.Calibration.Projection, FocalMM: ra.Calibration.FocalMM, Scale: 1, Mirrored: ra.Calibration.Mirrored}
		if c.Projection != visual.Perspective {
			c.Projection = visual.Orthographic
		}
		ref.Calibration = &c
	}
	ref.Analyzed = true
	return nil
}

// ReferenceGeometry derives normalized proportions from the analyzed references: landmark
// ratios plus the silhouette aspect of each orthographic view.
func ReferenceGeometry(refs []*visual.Reference) map[string]float64 {
	var side, front *visual.Reference
	for _, r := range refs {
		if r.Calibration == nil {
			continue
		}
		switch r.Calibration.View {
		case "left", "right":
			if side == nil {
				side = r
			}
		case "front", "rear":
			if front == nil {
				front = r
			}
		}
	}
	out := map[string]float64{}
	var sl, fl visual.Landmarks
	sw, sh := 0, 0
	if side != nil {
		sl, sw, sh = side.Landmarks, side.Width, side.Height
	}
	if front != nil {
		fl = front.Landmarks
	}
	for k, v := range visual.Ratios(sl, sw, sh, fl) {
		out[k] = v
	}
	for _, r := range refs {
		if r.Mask() == nil || r.Calibration == nil || r.Calibration.Projection == visual.Perspective {
			continue
		}
		x0, y0, x1, y1, ok := r.Mask().BBox()
		if !ok || x1 == x0 {
			continue
		}
		key := strings.TrimSuffix(r.Calibration.SilhouetteMetric(), "_silhouette")
		if key != "" {
			out[key+"_aspect"] = math.Round(float64(y1-y0)/float64(x1-x0)*1000) / 1000
		}
	}
	return out
}
