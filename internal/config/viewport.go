package config

import (
	"fmt"
	"strconv"
)

// Viewport display preferences: visual aids drawn over the model (coordinate axes, the axis
// gizmo). They are presentation only and never reach the project, its history or an export.

// Axis gizmo sizes and screen corners.
const (
	GizmoSmall  = "small"
	GizmoMedium = "medium"
	GizmoLarge  = "large"

	GizmoBottomLeft  = "bottom-left"
	GizmoBottomRight = "bottom-right"
	GizmoTopLeft     = "top-left"
)

// DefaultAxisOpacity is the opacity of the coordinate axes, in percent, when none is saved.
const DefaultAxisOpacity = 70

// ViewportSettings are the Settings → Viewport preferences.
type ViewportSettings struct {
	ShowAxes      bool   `json:"showAxes"`
	ShowLabels    bool   `json:"showAxisLabels"`
	AxisOpacity   int    `json:"axisOpacity"` // 0–100
	ShowGizmo     bool   `json:"showGizmo"`
	GizmoSize     string `json:"gizmoSize"`
	GizmoPosition string `json:"gizmoPosition"`
}

const (
	keyAxes        = "SIMONCAD_VIEW_AXES"
	keyAxisLabels  = "SIMONCAD_VIEW_AXIS_LABELS"
	keyAxisOpacity = "SIMONCAD_VIEW_AXIS_OPACITY"
	keyGizmo       = "SIMONCAD_VIEW_GIZMO"
	keyGizmoSize   = "SIMONCAD_VIEW_GIZMO_SIZE"
	keyGizmoPos    = "SIMONCAD_VIEW_GIZMO_POSITION"
)

// The switches are stored as "on"/"off" rather than left empty when off: an empty value is
// removed on save and would fall back to the default (on).
func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

func flag(key string) bool { return GetOr(key, "on") != "off" }

// Viewport returns the effective viewport preferences, defaults filled in.
func Viewport() ViewportSettings {
	v := ViewportSettings{
		ShowAxes: flag(keyAxes), ShowLabels: flag(keyAxisLabels),
		ShowGizmo: flag(keyGizmo), AxisOpacity: DefaultAxisOpacity,
		GizmoSize: GizmoMedium, GizmoPosition: GizmoBottomLeft,
	}
	if n, err := strconv.Atoi(Get(keyAxisOpacity)); err == nil && n >= 0 && n <= 100 {
		v.AxisOpacity = n
	}
	switch s := Get(keyGizmoSize); s {
	case GizmoSmall, GizmoMedium, GizmoLarge:
		v.GizmoSize = s
	}
	switch p := Get(keyGizmoPos); p {
	case GizmoBottomLeft, GizmoBottomRight, GizmoTopLeft:
		v.GizmoPosition = p
	}
	return v
}

// viewportValues validates v and returns it as configuration values.
func viewportValues(v ViewportSettings) (map[string]string, error) {
	if v.AxisOpacity < 0 || v.AxisOpacity > 100 {
		return nil, fmt.Errorf("axis opacity %d is out of range (0–100)", v.AxisOpacity)
	}
	switch v.GizmoSize {
	case GizmoSmall, GizmoMedium, GizmoLarge:
	default:
		return nil, fmt.Errorf("unknown axis gizmo size %q", v.GizmoSize)
	}
	switch v.GizmoPosition {
	case GizmoBottomLeft, GizmoBottomRight, GizmoTopLeft:
	default:
		return nil, fmt.Errorf("unknown axis gizmo position %q", v.GizmoPosition)
	}
	return map[string]string{
		keyAxes: onOff(v.ShowAxes), keyAxisLabels: onOff(v.ShowLabels),
		keyGizmo: onOff(v.ShowGizmo), keyAxisOpacity: strconv.Itoa(v.AxisOpacity),
		keyGizmoSize: v.GizmoSize, keyGizmoPos: v.GizmoPosition,
	}, nil
}
