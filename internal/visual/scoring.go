package visual

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

// Metric names.
const (
	MetricSide       = "side_silhouette"
	MetricFront      = "front_silhouette"
	MetricTop        = "top_silhouette"
	MetricProportion = "proportions"
	MetricLandmarks  = "landmark_alignment"
	MetricWheels     = "wheel_placement"
	MetricSemantic   = "semantic_visual_score"
	MetricOverall    = "overall"
)

// Stage is one step of coarse-to-fine modeling.
type Stage struct {
	Name string `json:"name"`
	// Build says what may be created in this stage (and, implicitly, what may not).
	Build string `json:"build"`
	// Forbidden lists what must NOT be added yet.
	Forbidden string `json:"forbidden,omitempty"`
	// Thresholds must all pass (metrics without a measurement are skipped) to advance.
	Thresholds map[string]float64 `json:"thresholds"`
	// Weights combine the metrics into the overall score; early stages favour global shape.
	Weights map[string]float64 `json:"weights"`
}

// Stages is the coarse-to-fine sequence.
var Stages = []Stage{
	{
		Name:       "BLOCKOUT",
		Build:      "only the main body, cabin, major volumes and wheels, as simple primitives driven by design parameters; basic colors only",
		Forbidden:  "headlights, mirrors, vents, trim, small details, materials",
		Thresholds: map[string]float64{MetricSide: 0.90, MetricFront: 0.85, MetricWheels: 0.90, MetricProportion: 0.90},
		Weights:    map[string]float64{MetricSide: 0.30, MetricFront: 0.15, MetricTop: 0.10, MetricProportion: 0.20, MetricLandmarks: 0.15, MetricSemantic: 0.10},
	},
	{
		Name:       "PRIMARY",
		Build:      "primary geometry: windshield, windows, wheel arches, doors, hood, major air intakes, bumpers",
		Forbidden:  "headlights, tail lights, mirrors, exhausts, vents, trim, materials",
		Thresholds: map[string]float64{MetricSide: 0.90, MetricFront: 0.85, MetricSemantic: 0.75, MetricOverall: 0.85},
		Weights:    map[string]float64{MetricSide: 0.25, MetricFront: 0.12, MetricTop: 0.08, MetricProportion: 0.18, MetricLandmarks: 0.15, MetricSemantic: 0.22},
	},
	{
		Name:       "SECONDARY",
		Build:      "secondary details: headlights, tail lights, mirrors, exhausts, vents, trim",
		Forbidden:  "materials",
		Thresholds: map[string]float64{MetricSemantic: 0.82, MetricOverall: 0.87},
		Weights:    map[string]float64{MetricSide: 0.20, MetricFront: 0.10, MetricTop: 0.07, MetricProportion: 0.15, MetricLandmarks: 0.13, MetricSemantic: 0.35},
	},
	{
		Name:       "MATERIALS",
		Build:      "materials: paint, glass, rubber, metal, plastic, emissive lights (set_material / set_color only)",
		Thresholds: map[string]float64{MetricSemantic: 0.88, MetricOverall: 0.90},
		Weights:    map[string]float64{MetricSide: 0.15, MetricFront: 0.08, MetricTop: 0.05, MetricProportion: 0.12, MetricLandmarks: 0.10, MetricSemantic: 0.50},
	},
}

// StageIndex finds a stage by name (-1 if unknown).
func StageIndex(name string) int {
	for i, s := range Stages {
		if strings.EqualFold(s.Name, name) {
			return i
		}
	}
	return -1
}

// Overall combines the available metrics with the stage weights, renormalized over the
// metrics that were actually measured.
func (s Stage) Overall(scores map[string]float64) float64 {
	sum, wsum := 0.0, 0.0
	for k, w := range s.Weights {
		if v, ok := scores[k]; ok {
			sum += v * w
			wsum += w
		}
	}
	if wsum == 0 {
		return 0
	}
	return round3(sum / wsum)
}

// Gate is the result of checking a stage's thresholds.
type Gate struct {
	Passed  bool     `json:"passed"`
	Failing []string `json:"failing,omitempty"` // "side_silhouette 0.82 < 0.90"
	Skipped []string `json:"skipped,omitempty"` // thresholds without a measurement
}

// Check tests the thresholds. A stage never passes on nothing: at least one threshold
// must have been measured.
func (s Stage) Check(scores map[string]float64) Gate {
	g := Gate{}
	keys := make([]string, 0, len(s.Thresholds))
	for k := range s.Thresholds {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	measured := 0
	for _, k := range keys {
		v, ok := scores[k]
		if !ok {
			g.Skipped = append(g.Skipped, k)
			continue
		}
		measured++
		if v+1e-9 < s.Thresholds[k] {
			g.Failing = append(g.Failing, fmt.Sprintf("%s %.2f < %.2f", k, v, s.Thresholds[k]))
		}
	}
	g.Passed = measured > 0 && len(g.Failing) == 0
	return g
}

// Percent formats a 0..1 score.
func Percent(v float64) string { return fmt.Sprintf("%d%%", int(math.Round(v*100))) }
