// Package profile defines the FDM print profile used by print simulation. A profile
// describes a generic machine and process; it is a simulation input, not a verified
// hardware profile.
package profile

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// Adhesion is the build plate adhesion strategy.
type Adhesion string

const (
	AdhesionNone  Adhesion = "none"
	AdhesionSkirt Adhesion = "skirt"
	AdhesionBrim  Adhesion = "brim"
)

// InfillPattern names an infill pattern. Slicers that lack a pattern fall back to rectilinear.
type InfillPattern string

const (
	InfillRectilinear InfillPattern = "rectilinear"
	InfillGrid        InfillPattern = "grid"
	InfillGyroid      InfillPattern = "gyroid"
	InfillHoneycomb   InfillPattern = "honeycomb"
)

// SimulationLabel marks a profile as a simulation profile.
const SimulationLabel = "Generic FDM (simulation profile — not verified hardware)"

// PrintProfile holds machine and process settings. Lengths are mm, temperatures °C,
// speeds mm/s.
type PrintProfile struct {
	Label string `json:"label"`

	// Machine.
	BuildX float64 `json:"build_x"`
	BuildY float64 `json:"build_y"`
	BuildZ float64 `json:"build_z"`

	NozzleDiameter   float64 `json:"nozzle_diameter"`
	FilamentDiameter float64 `json:"filament_diameter"`
	// FilamentDensity in g/cm³ (PLA 1.24).
	FilamentDensity float64 `json:"filament_density"`

	// Process.
	LayerHeight  float64       `json:"layer_height"`
	Walls        int           `json:"walls"`
	InfillPct    float64       `json:"infill_pct"`
	InfillPat    InfillPattern `json:"infill_pattern"`
	PrintTemp    float64       `json:"print_temp"`
	BedTemp      float64       `json:"bed_temp"`
	PrintSpeed   float64       `json:"print_speed"`
	TravelSpeed  float64       `json:"travel_speed"`
	Supports     bool          `json:"supports"`
	Adhesion     Adhesion      `json:"adhesion"`
	TopBottomLay int           `json:"top_bottom_layers"`

	// Analysis thresholds (do not affect slicing, so they are left out of Hash).
	OverhangDeg    float64 `json:"overhang_deg"`
	MaxBridgeMM    float64 `json:"max_bridge_mm"`
	MinWallMM      float64 `json:"min_wall_mm"`
	MinFeatureMM   float64 `json:"min_feature_mm"`
	MaxAspectRatio float64 `json:"max_aspect_ratio"`
}

// GenericFDM returns the default simulation profile.
func GenericFDM() PrintProfile {
	return PrintProfile{
		Label:            SimulationLabel,
		BuildX:           220,
		BuildY:           220,
		BuildZ:           250,
		NozzleDiameter:   0.4,
		FilamentDiameter: 1.75,
		FilamentDensity:  1.24,
		LayerHeight:      0.2,
		Walls:            2,
		InfillPct:        20,
		InfillPat:        InfillRectilinear,
		PrintTemp:        210,
		BedTemp:          60,
		PrintSpeed:       60,
		TravelSpeed:      150,
		Supports:         false,
		Adhesion:         AdhesionSkirt,
		TopBottomLay:     3,
		OverhangDeg:      45,
		MaxBridgeMM:      10,
		MinWallMM:        0.8,
		MinFeatureMM:     0.4,
		MaxAspectRatio:   4,
	}
}

// Validate checks every field is inside a sane range and returns the first problem.
func (p PrintProfile) Validate() error {
	chk := func(name string, v, lo, hi float64) error {
		if !(v >= lo && v <= hi) { // also rejects NaN
			return fmt.Errorf("%s must be between %g and %g (got %g)", name, lo, hi, v)
		}
		return nil
	}
	for _, c := range []struct {
		n         string
		v, lo, hi float64
	}{
		{"build_x", p.BuildX, 10, 2000},
		{"build_y", p.BuildY, 10, 2000},
		{"build_z", p.BuildZ, 10, 2000},
		{"nozzle_diameter", p.NozzleDiameter, 0.1, 2},
		{"filament_diameter", p.FilamentDiameter, 1, 3.5},
		{"filament_density", p.FilamentDensity, 0.5, 5},
		{"layer_height", p.LayerHeight, 0.04, 1.5},
		{"infill_pct", p.InfillPct, 0, 100},
		{"print_temp", p.PrintTemp, 150, 450},
		{"bed_temp", p.BedTemp, 0, 150},
		{"print_speed", p.PrintSpeed, 1, 1000},
		{"travel_speed", p.TravelSpeed, 1, 2000},
	} {
		if err := chk(c.n, c.v, c.lo, c.hi); err != nil {
			return err
		}
	}
	if p.LayerHeight > p.NozzleDiameter*0.8+1e-9 {
		return fmt.Errorf("layer_height %g must not exceed 80%% of the nozzle diameter (%g)", p.LayerHeight, p.NozzleDiameter*0.8)
	}
	if p.Walls < 1 || p.Walls > 20 {
		return fmt.Errorf("walls must be between 1 and 20 (got %d)", p.Walls)
	}
	if p.TopBottomLay < 0 || p.TopBottomLay > 50 {
		return fmt.Errorf("top_bottom_layers must be between 0 and 50 (got %d)", p.TopBottomLay)
	}
	switch p.InfillPat {
	case InfillRectilinear, InfillGrid, InfillGyroid, InfillHoneycomb:
	default:
		return fmt.Errorf("unknown infill pattern %q", p.InfillPat)
	}
	switch p.Adhesion {
	case AdhesionNone, AdhesionSkirt, AdhesionBrim:
	default:
		return fmt.Errorf("unknown adhesion %q", p.Adhesion)
	}
	return nil
}

// slicing is the subset of fields that change the slicer output.
type slicing struct {
	BuildX, BuildY, BuildZ        float64
	Nozzle, Filament, Density     float64
	Layer                         float64
	Walls                         int
	Infill                        float64
	Pattern                       InfillPattern
	Temp, Bed, PrintSpd, TravelSp float64
	Supports                      bool
	Adhesion                      Adhesion
	TopBottom                     int
}

// Hash is a stable digest of the settings that affect slicing. It keys the result cache.
func (p PrintProfile) Hash() string {
	b, _ := json.Marshal(slicing{
		p.BuildX, p.BuildY, p.BuildZ, p.NozzleDiameter, p.FilamentDiameter, p.FilamentDensity,
		p.LayerHeight, p.Walls, p.InfillPct, p.InfillPat, p.PrintTemp, p.BedTemp,
		p.PrintSpeed, p.TravelSpeed, p.Supports, p.Adhesion, p.TopBottomLay,
	})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:8])
}
