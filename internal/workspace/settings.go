package workspace

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
)

// SchemaVersion is the version of the serialized workspace settings (workspace.json and the
// global defaults). Readers accept any version: unknown fields are ignored and missing or
// invalid ones fall back to the defaults.
const SchemaVersion = 1

// Grid planes.
const (
	PlaneXY = "XY"
	PlaneXZ = "XZ"
	PlaneYZ = "YZ"
)

// Planes lists the grid planes.
var Planes = []string{PlaneXY, PlaneXZ, PlaneYZ}

// Limits of the configurable values. A grid is at most MaxGridLines cells across, so a tiny
// spacing over a huge extent cannot ask the viewport for millions of lines.
const (
	MaxLength        = 1e7 // mm: extent, origin
	MaxStep          = 1e6 // mm: spacing, translation increment
	MaxGridLines     = 10000
	MaxMajorInterval = 1000
)

// Grid is the workspace reference grid. It is a visual and spatial reference, never
// geometry. Lengths are canonical millimetres.
type Grid struct {
	// Enabled makes the grid exist as a spatial reference (snapping can use it); Visible
	// draws it. A disabled grid is neither drawn nor snapped to.
	Enabled       bool       `json:"enabled"`
	Visible       bool       `json:"visible"`
	Plane         string     `json:"plane"`
	Spacing       float64    `json:"spacing"`
	MajorInterval int        `json:"major_interval"` // cells between major lines
	Extent        float64    `json:"extent"`         // side of the square grid, centred on the origin
	Origin        [3]float64 `json:"origin"`
}

// Snap is the centralized snapping configuration. Snapping is active only when Enabled is
// set; the other flags say which kinds of snapping are available then.
type Snap struct {
	Enabled           bool    `json:"enabled"`
	Grid              bool    `json:"grid"`
	Translation       bool    `json:"translation"`
	Rotation          bool    `json:"rotation"`
	RotationIncrement float64 `json:"rotation_increment"` // canonical degrees
	// TranslationIncrement in mm; nil inherits the grid spacing.
	TranslationIncrement *float64 `json:"translation_increment"`
}

// Settings is one complete workspace configuration: a project's active settings, or the
// global defaults new projects start from.
type Settings struct {
	SchemaVersion int   `json:"schema_version"`
	Units         Units `json:"units"`
	Grid          Grid  `json:"grid"`
	Snap          Snap  `json:"snap"`
}

// Defaults are the factory settings: millimetres and degrees, a 10 mm grid on XY with a
// major line every 10 cells, snapping off.
func Defaults() Settings {
	return Settings{
		SchemaVersion: SchemaVersion,
		Units:         Units{Linear: MM, Angular: Deg, LinearPrecision: 2, AngularPrecision: 1},
		Grid:          Grid{Enabled: true, Visible: true, Plane: PlaneXY, Spacing: 10, MajorInterval: 10, Extent: 1000},
		Snap:          Snap{Enabled: false, Grid: true, Translation: true, Rotation: false, RotationIncrement: 15},
	}
}

// Clone returns a deep copy (the translation increment is a pointer).
func (s Settings) Clone() Settings {
	if s.Snap.TranslationIncrement != nil {
		v := *s.Snap.TranslationIncrement
		s.Snap.TranslationIncrement = &v
	}
	return s
}

// Equal reports whether two configurations are the same.
func (s Settings) Equal(o Settings) bool {
	a, _ := json.Marshal(s.Normalized())
	b, _ := json.Marshal(o.Normalized())
	return string(a) == string(b)
}

// TranslationStep is the effective translation increment in mm: the configured one, else
// the grid spacing.
func (s Settings) TranslationStep() float64 {
	if s.Snap.TranslationIncrement != nil {
		return *s.Snap.TranslationIncrement
	}
	return s.Grid.Spacing
}

// Normalized fills the schema version and spells enumerations canonically ("xy" → "XY").
func (s Settings) Normalized() Settings {
	s = s.Clone()
	s.SchemaVersion = SchemaVersion
	s.Units.Linear = strings.ToLower(strings.TrimSpace(s.Units.Linear))
	s.Units.Angular = strings.ToLower(strings.TrimSpace(s.Units.Angular))
	switch s.Units.Angular {
	case "degrees", "degree", "°":
		s.Units.Angular = Deg
	case "radians", "radian":
		s.Units.Angular = Rad
	}
	s.Grid.Plane = strings.ToUpper(strings.TrimSpace(s.Grid.Plane))
	return s
}

func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

// Validate checks every value. It is the one validation shared by the Settings screen, the
// agent tools, the global defaults and project loading.
func (s Settings) Validate() error {
	return errors.Join(s.Units.Validate(), s.Grid.Validate(), s.Snap.Validate())
}

// Validate checks the display units and precisions.
func (u Units) Validate() error {
	if !ValidLinear(u.Linear) {
		return fmt.Errorf("unknown linear unit %q (use %s)", u.Linear, strings.Join(LinearUnits, ", "))
	}
	if !ValidAngular(u.Angular) {
		return fmt.Errorf("unknown angular unit %q (use deg or rad)", u.Angular)
	}
	if u.LinearPrecision < 0 || u.LinearPrecision > MaxPrecision {
		return fmt.Errorf("linear precision %d is out of range (0–%d decimals)", u.LinearPrecision, MaxPrecision)
	}
	if u.AngularPrecision < 0 || u.AngularPrecision > MaxPrecision {
		return fmt.Errorf("angular precision %d is out of range (0–%d decimals)", u.AngularPrecision, MaxPrecision)
	}
	return nil
}

// Validate checks the grid: a plane, a positive spacing and extent, a sane line count.
func (g Grid) Validate() error {
	switch g.Plane {
	case PlaneXY, PlaneXZ, PlaneYZ:
	default:
		return fmt.Errorf("unknown grid plane %q (use XY, XZ or YZ)", g.Plane)
	}
	if !finite(g.Spacing) || g.Spacing <= 0 {
		return fmt.Errorf("grid spacing must be greater than 0 mm")
	}
	if g.Spacing > MaxStep {
		return fmt.Errorf("grid spacing must be at most %g mm", float64(MaxStep))
	}
	if g.MajorInterval < 1 || g.MajorInterval > MaxMajorInterval {
		return fmt.Errorf("major line interval must be a whole number of cells from 1 to %d", MaxMajorInterval)
	}
	if !finite(g.Extent) || g.Extent <= 0 {
		return fmt.Errorf("grid extent must be greater than 0 mm")
	}
	if g.Extent > MaxLength {
		return fmt.Errorf("grid extent must be at most %g mm", float64(MaxLength))
	}
	if g.Extent/g.Spacing > MaxGridLines {
		return fmt.Errorf("a %s mm grid over %s mm would need %.0f lines per side (at most %d): increase the spacing or reduce the extent",
			trimFloat(g.Spacing), trimFloat(g.Extent), math.Ceil(g.Extent/g.Spacing), MaxGridLines)
	}
	for i, v := range g.Origin {
		if !finite(v) || math.Abs(v) > MaxLength {
			return fmt.Errorf("grid origin %c must be a number within ±%g mm", "XYZ"[i], float64(MaxLength))
		}
	}
	return nil
}

// Validate checks the snapping increments (the flags are always valid).
func (s Snap) Validate() error {
	if !finite(s.RotationIncrement) || s.RotationIncrement <= 0 || s.RotationIncrement > 360 {
		return fmt.Errorf("rotation increment must be greater than 0° and at most 360°")
	}
	if t := s.TranslationIncrement; t != nil {
		if !finite(*t) || *t <= 0 {
			return fmt.Errorf("translation increment must be greater than 0 mm (or empty to use the grid spacing)")
		}
		if *t > MaxStep {
			return fmt.Errorf("translation increment must be at most %g mm", float64(MaxStep))
		}
	}
	return nil
}

func trimFloat(v float64) string {
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.6f", v), "0"), ".")
}

// Decode reads serialized settings safely: any schema version is accepted, unknown fields
// are ignored, missing fields keep the value in base, and a section that is malformed or
// invalid falls back to base's as a whole. The warnings say what was replaced. Only input
// that is not a JSON object at all is an error.
func Decode(raw []byte, base Settings) (Settings, []string, error) {
	var sections map[string]json.RawMessage
	if err := json.Unmarshal(raw, &sections); err != nil {
		return base, nil, fmt.Errorf("workspace settings are not a JSON object: %w", err)
	}
	out := base.Clone()
	var warns []string
	section := func(name string, dst any, reset func(), check func() error) {
		r, ok := sections[name]
		if !ok || string(r) == "null" {
			return
		}
		if err := json.Unmarshal(r, dst); err != nil {
			reset()
			warns = append(warns, fmt.Sprintf("%s: unreadable (%v); defaults used", name, err))
			return
		}
		out = out.Normalized()
		if err := check(); err != nil {
			reset()
			warns = append(warns, fmt.Sprintf("%s: %v; defaults used", name, err))
		}
	}
	b := base.Clone()
	section("units", &out.Units, func() { out.Units = b.Units }, func() error { return out.Units.Validate() })
	section("grid", &out.Grid, func() { out.Grid = b.Grid }, func() error { return out.Grid.Validate() })
	section("snap", &out.Snap, func() { out.Snap = b.Clone().Snap }, func() error { return out.Snap.Validate() })
	if v, ok := sections["schema_version"]; ok {
		var n int
		if json.Unmarshal(v, &n) == nil && n > SchemaVersion {
			warns = append(warns, fmt.Sprintf("written by a newer version (schema %d); unknown settings were ignored", n))
		}
	}
	return out.Normalized(), warns, nil
}
