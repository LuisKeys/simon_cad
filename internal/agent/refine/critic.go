package refine

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/accelone/simoncad/internal/agent/runtime"
	"github.com/accelone/simoncad/internal/visual"
)

// Critique is the VisionCritic's structured judgement.
type Critique struct {
	OverallScore  float64        `json:"overall_score"`
	LargestErrors []CritiqueItem `json:"largest_errors"`
	GoodRegions   []string       `json:"good_regions"`
	NextActions   []string       `json:"recommended_next_actions"`
	// RenderLandmarks are the landmarks the critic reads on the side render that matches the
	// side reference (same names and coordinate system as the reference landmarks).
	RenderLandmarks visual.Landmarks `json:"render_landmarks,omitempty"`
}

// CritiqueItem is one visual error.
type CritiqueItem struct {
	Region     string  `json:"region"`
	Problem    string  `json:"problem"`
	Severity   float64 `json:"severity"`
	Correction *struct {
		Axis      string  `json:"axis,omitempty"`
		DeltaMM   float64 `json:"delta_mm,omitempty"`
		Parameter string  `json:"parameter,omitempty"`
		Value     float64 `json:"value,omitempty"`
	} `json:"estimated_correction,omitempty"`
}

const criticPrompt = `You are the VisionCritic of a CAD system. You compare reference images of an object with renders of the current CAD model and report what differs. You never modify geometry and you have no CAD tools: answer only by calling submit_critique with structured data.

Priorities
- Global shape first: overall proportions, silhouette, major volumes and their positions (e.g. roof height, wheelbase, overhangs, hood slope). Report details (lights, vents, trim) only when the global shape is already right, and never as the largest errors while global errors remain.
- Judge only what is appropriate for the current modeling stage; elements the stage does not include yet are not errors.
- Use the region names given (they map to CAD objects and parameters). Prefer an estimated_correction that names a design parameter and its new value, or an axis and delta in millimetres, computed from the known dimensions and the measurements given.
- severity is 0..1 (1 = dominates the visual mismatch). overall_score is 0..1 visual similarity for this stage.
- good_regions lists regions that already match and should be preserved.
- render_landmarks: the same landmark names as the side reference, read on the render labelled "side render matching the side reference", in that render's normalized coordinates.`

func critiqueTool() runtime.ToolDef {
	return runtime.ToolDef{Name: "submit_critique", Description: "Submit the visual comparison.", InputSchema: obj(map[string]any{
		"overall_score": num(),
		"largest_errors": arr(obj(map[string]any{
			"region": str(), "problem": str(), "severity": num(),
			"estimated_correction": obj(map[string]any{"axis": map[string]any{"type": "string", "enum": []string{"x", "y", "z"}}, "delta_mm": num(), "parameter": str(), "value": num()}),
		}, "region", "problem", "severity")),
		"good_regions":             arr(str()),
		"recommended_next_actions": arr(str()),
		"render_landmarks":         pointMap(),
	}, "overall_score", "largest_errors", "good_regions", "recommended_next_actions")}
}

// CriticInput is everything the critic sees.
type CriticInput struct {
	Goal              string
	Stage             visual.Stage
	References        []LabeledImage
	Renders           []LabeledImage
	ObjectTree        string // compact model summary
	Parameters        map[string]float64
	Regions           []*visual.Region
	Dimensions        map[string]float64 // known real dimensions
	Metrics           map[string]float64 // deterministic scores measured so far
	SideLandmarkNames []string
}

// LabeledImage is an image with the caption the critic sees before it.
type LabeledImage struct {
	Label     string
	Data      []byte
	MediaType string
}

// VisionCritic evaluates renders against references with a vision model.
type VisionCritic struct {
	Vision runtime.Provider
	Usage  runtime.Usage
}

// Evaluate returns the critique.
func (vc *VisionCritic) Evaluate(ctx context.Context, in CriticInput) (*Critique, json.RawMessage, error) {
	var blocks []runtime.Block
	for _, im := range in.References {
		blocks = append(blocks, runtime.Block{Type: "text", Text: "REFERENCE — " + im.Label}, imageBlock(im.Data, im.MediaType))
	}
	for _, im := range in.Renders {
		blocks = append(blocks, runtime.Block{Type: "text", Text: "CAD RENDER — " + im.Label}, imageBlock(im.Data, im.MediaType))
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "## Requirements\n%s\n\n## Current stage: %s\nThis stage builds: %s.", in.Goal, in.Stage.Name, in.Stage.Build)
	if in.Stage.Forbidden != "" {
		fmt.Fprintf(&sb, " Not part of this stage yet (not errors): %s.", in.Stage.Forbidden)
	}
	sb.WriteString("\n\n## Regions (name → objects, parameters)\n")
	for _, r := range in.Regions {
		lock := ""
		if r.Locked {
			lock = " [locked: already correct]"
		}
		fmt.Fprintf(&sb, "- %s%s → objects %v, parameters %v\n", r.Name, lock, r.Objects, r.Parameters)
	}
	if len(in.Regions) == 0 {
		sb.WriteString("(none defined: use short descriptive region names)\n")
	}
	if len(in.Parameters) > 0 {
		b, _ := json.Marshal(in.Parameters)
		fmt.Fprintf(&sb, "\n## Design parameters\n%s\n", b)
	}
	if len(in.Dimensions) > 0 {
		b, _ := json.Marshal(in.Dimensions)
		fmt.Fprintf(&sb, "\n## Known real dimensions (mm)\n%s\n", b)
	}
	if len(in.Metrics) > 0 {
		keys := make([]string, 0, len(in.Metrics))
		for k := range in.Metrics {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		sb.WriteString("\n## Measured scores (deterministic)\n")
		for _, k := range keys {
			fmt.Fprintf(&sb, "- %s: %.3f\n", k, in.Metrics[k])
		}
	}
	if len(in.SideLandmarkNames) > 0 {
		fmt.Fprintf(&sb, "\nSide reference landmark names: %s\n", strings.Join(in.SideLandmarkNames, ", "))
	}
	fmt.Fprintf(&sb, "\n## CAD object tree\n%s\n", in.ObjectTree)
	blocks = append(blocks, runtime.Block{Type: "text", Text: sb.String()})
	raw, err := structured(ctx, vc.Vision, criticPrompt, blocks, critiqueTool(), &vc.Usage)
	if err != nil {
		return nil, nil, err
	}
	var c Critique
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, nil, fmt.Errorf("critique: %w", err)
	}
	c.OverallScore = clamp01(c.OverallScore)
	for i := range c.LargestErrors {
		c.LargestErrors[i].Severity = clamp01(c.LargestErrors[i].Severity)
	}
	sort.SliceStable(c.LargestErrors, func(i, j int) bool { return c.LargestErrors[i].Severity > c.LargestErrors[j].Severity })
	return &c, raw, nil
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}
