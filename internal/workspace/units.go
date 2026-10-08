// Package workspace is the measurement, grid and snapping configuration of a project: how
// lengths and angles are shown and typed (UnitService), the reference grid (GridService) and
// deterministic snapping (SnapService). It is pure: no rendering, no kernel, no I/O.
//
// Canonical units never change: every length inside Simon CAD (geometry, parameters, grid
// spacing, snapping) is millimetres and every angle computed here is radians. Display units
// only decide how a value is converted at the input/output boundary, so switching a project
// to inches never rescales or moves anything.
package workspace

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Linear units.
const (
	MM   = "mm"
	CM   = "cm"
	M    = "m"
	Inch = "in"
	Foot = "ft"
)

// Angular units.
const (
	Deg = "deg"
	Rad = "rad"
)

// LinearUnits and AngularUnits list the supported units, canonical first.
var (
	LinearUnits  = []string{MM, CM, M, Inch, Foot}
	AngularUnits = []string{Deg, Rad}
)

// mmPer is the size of one unit in millimetres: the only conversion table there is.
var mmPer = map[string]float64{MM: 1, CM: 10, M: 1000, Inch: 25.4, Foot: 304.8}

// MaxPrecision is the most decimal places a display precision may ask for.
const MaxPrecision = 8

// ValidLinear and ValidAngular report whether u is a supported unit.
func ValidLinear(u string) bool  { _, ok := mmPer[u]; return ok }
func ValidAngular(u string) bool { return u == Deg || u == Rad }

// MMPerTable returns the conversion table (unit → millimetres), for clients that display
// values in any unit without keeping a second copy of the factors.
func MMPerTable() map[string]float64 {
	out := make(map[string]float64, len(mmPer))
	for k, v := range mmPer {
		out[k] = v
	}
	return out
}

// MMPer is the size of one linear unit in millimetres (0 for an unknown unit).
func MMPer(unit string) float64 { return mmPer[unit] }

// ToMM converts a length given in unit to canonical millimetres.
func ToMM(v float64, unit string) (float64, error) {
	k, ok := mmPer[unit]
	if !ok {
		return 0, fmt.Errorf("unknown length unit %q (use %s)", unit, strings.Join(LinearUnits, ", "))
	}
	return v * k, nil
}

// FromMM converts a canonical length in millimetres to unit.
func FromMM(mm float64, unit string) (float64, error) {
	k, ok := mmPer[unit]
	if !ok {
		return 0, fmt.Errorf("unknown length unit %q (use %s)", unit, strings.Join(LinearUnits, ", "))
	}
	return mm / k, nil
}

// ConvertLength converts v from one linear unit to another (through millimetres).
func ConvertLength(v float64, from, to string) (float64, error) {
	mm, err := ToMM(v, from)
	if err != nil {
		return 0, err
	}
	return FromMM(mm, to)
}

// ToRadians converts an angle given in unit to canonical radians.
func ToRadians(v float64, unit string) (float64, error) {
	switch unit {
	case Deg:
		return v * math.Pi / 180, nil
	case Rad:
		return v, nil
	}
	return 0, fmt.Errorf("unknown angle unit %q (use deg or rad)", unit)
}

// FromRadians converts a canonical angle in radians to unit.
func FromRadians(rad float64, unit string) (float64, error) {
	switch unit {
	case Deg:
		return rad * 180 / math.Pi, nil
	case Rad:
		return rad, nil
	}
	return 0, fmt.Errorf("unknown angle unit %q (use deg or rad)", unit)
}

// NormalizeAngle maps an angle in radians to (-π, π]. Every angle normalizes to one value,
// so two orientations compare equal however many turns they were reached by.
func NormalizeAngle(rad float64) float64 {
	a := math.Mod(rad, 2*math.Pi)
	switch {
	case a > math.Pi:
		a -= 2 * math.Pi
	case a <= -math.Pi:
		a += 2 * math.Pi
	}
	return a
}

// Symbol is how a unit is written after a value.
func Symbol(unit string) string {
	if unit == Deg {
		return "°"
	}
	return unit
}

// formatNumber prints v with exactly prec decimals, never "-0".
func formatNumber(v float64, prec int) string {
	s := strconv.FormatFloat(v, 'f', prec, 64)
	if strings.Trim(s, "-0.") == "" {
		s = strings.TrimPrefix(s, "-")
	}
	return s
}

// Units is the display configuration: which units values are shown and typed in, and with
// how many decimals.
type Units struct {
	Linear           string `json:"linear"`
	Angular          string `json:"angular"`
	LinearPrecision  int    `json:"linear_precision"`
	AngularPrecision int    `json:"angular_precision"`
}

// FormatLength shows a canonical length (mm) in the display unit, e.g. "3.94 in".
func (u Units) FormatLength(mm float64) string {
	v, err := FromMM(mm, u.Linear)
	if err != nil {
		v, u.Linear = mm, MM
	}
	return formatNumber(v, u.LinearPrecision) + " " + Symbol(u.Linear)
}

// FormatAngle shows a canonical angle (radians) in the display unit, e.g. "45.0°".
func (u Units) FormatAngle(rad float64) string {
	v, err := FromRadians(rad, u.Angular)
	if err != nil {
		v, u.Angular = rad, Rad
	}
	if u.Angular == Deg {
		return formatNumber(v, u.AngularPrecision) + "°"
	}
	return formatNumber(v, u.AngularPrecision) + " rad"
}

// LengthToMM reads a length typed in the display unit.
func (u Units) LengthToMM(v float64) (float64, error) { return ToMM(v, u.Linear) }

// AngleToRadians reads an angle typed in the display unit.
func (u Units) AngleToRadians(v float64) (float64, error) { return ToRadians(v, u.Angular) }
