package tools

import (
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"sort"

	"github.com/accelone/simoncad/internal/workspace"
)

// Workspace tools: the project's display units, reference grid and snapping. They are the
// one way to change these settings — the Settings screen calls the same tools — and they
// never touch the model: geometry is always millimetres, so switching a project to inches
// moves nothing. Like set_mesh_detail they are not ChangeSets (there is nothing to rebuild
// or undo in the model); each call is still recorded in the project's tool-call trace and
// the settings are saved with the project.

// IsWorkspaceTool reports whether name edits the workspace settings (applied directly from
// the UI, like a display preference).
func IsWorkspaceTool(name string) bool {
	switch name {
	case "set_units", "configure_grid", "configure_snapping", "restore_workspace_defaults":
		return true
	}
	return false
}

var (
	lengthUnitProp = enum("Unit of the lengths given in this call; default mm", workspace.LinearUnits...)
	angleUnitProp  = enum("Unit of the angles given in this call; default deg", workspace.AngularUnits...)
	pointProp      = array("A point or vector [x, y, z]", number("coordinate"), 3, 3)
	unitsNote      = "Lengths are stored in mm and angles in degrees whatever the display units; the result reports both."
)

func workspaceTools() []*Tool {
	return []*Tool{
		{
			Name: "get_workspace_settings",
			Description: "Read the project's workspace settings: display units (linear mm/cm/m/in/ft, angular deg/rad, decimal precision), " +
				"the reference grid (visibility, plane, spacing, major line interval, extent, origin) and snapping (enabled, grid/translation/rotation, increments). " +
				"Also returns the global defaults new projects start with. " + unitsNote,
			Capability: capGeometry,
			Schema:     object(props{}),
			Query: func(env Env, a Args) (any, error) {
				return workspaceView(env, nil), nil
			},
		},
		{
			Name: "set_units",
			Description: "Change the project's display units and precision ('switch the project to inches', 'show angles in radians', 'show 3 decimals'). " +
				"Display only: it changes how lengths and angles are shown and typed in the UI, never the geometry, positions or sizes (stored in mm).",
			Capability: capGeometry,
			Schema: object(props{
				"linear":            enum("Linear display unit", workspace.LinearUnits...),
				"angular":           enum("Angular display unit", workspace.AngularUnits...),
				"linear_precision":  integer("Decimal places of lengths", 0, workspace.MaxPrecision),
				"angular_precision": integer("Decimal places of angles", 0, workspace.MaxPrecision),
			}),
			Query: func(env Env, a Args) (any, error) {
				if len(a) == 0 {
					return nil, fmt.Errorf("give linear, angular, linear_precision and/or angular_precision")
				}
				return updateWorkspace(env, func(s *workspace.Settings) error {
					if a.Has("linear") {
						s.Units.Linear = a.Str("linear")
					}
					if a.Has("angular") {
						s.Units.Angular = a.Str("angular")
					}
					if a.Has("linear_precision") {
						s.Units.LinearPrecision = int(a.Num("linear_precision"))
					}
					if a.Has("angular_precision") {
						s.Units.AngularPrecision = int(a.Num("angular_precision"))
					}
					return nil
				})
			},
		},
		{
			Name: "configure_grid",
			Description: "Configure the viewport reference grid: show or hide it (visible), enable or disable it as a spatial reference (enabled), " +
				"its plane (XY, XZ, YZ), spacing between lines, major line interval (in cells), extent (side of the square grid) and origin. " +
				"'Set the grid spacing to 5 millimeters' → spacing=5, unit=mm; 'hide the grid' → visible=false. The grid is a visual reference, never geometry. " + unitsNote,
			Capability: capGeometry,
			Schema: object(props{
				"visible":        boolean("Draw the grid"),
				"enabled":        boolean("The grid exists as a spatial reference (drawn when visible; snapped to when snapping to the grid)"),
				"plane":          enum("Grid plane", workspace.Planes...),
				"spacing":        number("Distance between grid lines (> 0)"),
				"major_interval": integer("A major line every N cells", 1, workspace.MaxMajorInterval),
				"extent":         number("Side length of the square grid, centred on its origin (> 0)"),
				"origin":         pointProp,
				"unit":           lengthUnitProp,
			}),
			Query: func(env Env, a Args) (any, error) {
				if len(withoutKeys(a, "unit")) == 0 {
					return nil, fmt.Errorf("give at least one grid setting")
				}
				unit := strOr(a, "unit", workspace.MM)
				return updateWorkspace(env, func(s *workspace.Settings) error {
					g := &s.Grid
					if a.Has("visible") {
						g.Visible = a.Bool("visible")
						if g.Visible {
							g.Enabled = true // a grid shown is a grid that exists
						}
					}
					if a.Has("enabled") {
						g.Enabled = a.Bool("enabled")
					}
					if a.Has("plane") {
						g.Plane = a.Str("plane")
					}
					if a.Has("major_interval") {
						g.MajorInterval = int(a.Num("major_interval"))
					}
					for _, f := range []struct {
						key string
						dst *float64
					}{{"spacing", &g.Spacing}, {"extent", &g.Extent}} {
						if a.Has(f.key) {
							mm, err := workspace.ToMM(a.Num(f.key), unit)
							if err != nil {
								return err
							}
							*f.dst = mm
						}
					}
					if a.Has("origin") {
						p, err := pointArg(a, "origin", unit)
						if err != nil {
							return err
						}
						g.Origin = p
					}
					return nil
				})
			},
		},
		{
			Name: "configure_snapping",
			Description: "Configure snapping (the settings future interactive moves and rotations will use). enabled is the master switch; grid, translation and rotation say which kinds of snapping are available. " +
				"'Enable snapping every 10 millimeters' → enabled=true, translation=true, translation_increment=10, unit=mm. 'Set rotation snapping to 45 degrees' → enabled=true, rotation=true, rotation_increment=45. " +
				"translation_increment_from_grid=true makes the translation increment follow the grid spacing again. " + unitsNote,
			Capability: capGeometry,
			Schema: object(props{
				"enabled":                         boolean("Master switch: snapping is active only when on"),
				"grid":                            boolean("Snap positions to grid intersections"),
				"translation":                     boolean("Snap translations to the translation increment"),
				"rotation":                        boolean("Snap rotations to the rotation increment"),
				"rotation_increment":              number("Rotation increment (> 0, at most a full turn)"),
				"translation_increment":           number("Translation increment (> 0)"),
				"translation_increment_from_grid": boolean("true: the translation increment is the grid spacing"),
				"unit":                            lengthUnitProp,
				"angle_unit":                      angleUnitProp,
			}),
			Query: func(env Env, a Args) (any, error) {
				if len(withoutKeys(a, "unit", "angle_unit")) == 0 {
					return nil, fmt.Errorf("give at least one snapping setting")
				}
				if a.Has("translation_increment") && a.Bool("translation_increment_from_grid") {
					return nil, fmt.Errorf("give translation_increment or translation_increment_from_grid=true, not both")
				}
				return updateWorkspace(env, func(s *workspace.Settings) error {
					sn := &s.Snap
					for _, f := range []struct {
						key string
						dst *bool
					}{{"enabled", &sn.Enabled}, {"grid", &sn.Grid}, {"translation", &sn.Translation}, {"rotation", &sn.Rotation}} {
						if a.Has(f.key) {
							*f.dst = a.Bool(f.key)
						}
					}
					if a.Has("rotation_increment") {
						rad, err := workspace.ToRadians(a.Num("rotation_increment"), strOr(a, "angle_unit", workspace.Deg))
						if err != nil {
							return err
						}
						sn.RotationIncrement = clean6(rad * 180 / math.Pi)
					}
					if a.Has("translation_increment") {
						mm, err := workspace.ToMM(a.Num("translation_increment"), strOr(a, "unit", workspace.MM))
						if err != nil {
							return err
						}
						sn.TranslationIncrement = &mm
					}
					if a.Bool("translation_increment_from_grid") {
						sn.TranslationIncrement = nil
					}
					return nil
				})
			},
		},
		{
			Name: "restore_workspace_defaults",
			Description: "Restore the project's workspace settings (all, or one section: units, grid, snap) to the user's global defaults " +
				"(source=global, the default) or to the factory defaults (source=factory: mm, degrees, 10 mm XY grid, snapping off). Geometry is unchanged.",
			Capability: capGeometry,
			Schema: object(props{
				"section": enum("What to restore; default all", "all", "units", "grid", "snap"),
				"source":  enum("Which defaults; default global", "global", "factory"),
			}),
			Query: func(env Env, a Args) (any, error) {
				def := env.WorkspaceDefaults()
				if strOr(a, "source", "global") == "factory" {
					def = workspace.Defaults()
				}
				return updateWorkspace(env, func(s *workspace.Settings) error {
					switch strOr(a, "section", "all") {
					case "units":
						s.Units = def.Units
					case "grid":
						s.Grid = def.Grid
					case "snap":
						s.Snap = def.Clone().Snap
					default:
						*s = def.Clone()
					}
					return nil
				})
			},
		},
		{
			Name: "snap_calculate",
			Description: "Compute a snapping or grid calculation with the project's settings, without changing anything: " +
				"position (nearest grid intersection in the grid plane), translation (displacement rounded to the translation increment), " +
				"angle (rounded to the rotation increment), world_to_grid and grid_to_world (grid coordinates: u, v in the plane, w along its normal, from the grid origin). " +
				"Snapping only applies when enabled; force=true answers as if that kind of snapping were on. " + unitsNote,
			Capability: capGeometry,
			Schema: object(props{
				"operation":  enum("Calculation", "position", "translation", "angle", "world_to_grid", "grid_to_world"),
				"point":      pointProp,
				"angle":      number("Angle for operation=angle"),
				"unit":       lengthUnitProp,
				"angle_unit": angleUnitProp,
				"force":      boolean("Compute as if snapping (and this kind of snapping) were enabled"),
			}, "operation"),
			Query: func(env Env, a Args) (any, error) {
				return snapCalculate(env, a)
			},
		},
		{
			Name: "convert_units",
			Description: "Convert a length (mm, cm, m, in, ft) or an angle (deg, rad) between units with the application's conversion table. " +
				"Use it for user values in other units before passing them to tools, which take mm (lengths) and degrees (angles).",
			Capability: capGeometry,
			Schema: object(props{
				"value": number("Value to convert"),
				"from":  enum("Unit of value", append(append([]string{}, workspace.LinearUnits...), workspace.AngularUnits...)...),
				"to":    enum("Target unit", append(append([]string{}, workspace.LinearUnits...), workspace.AngularUnits...)...),
			}, "value", "from", "to"),
			Query: func(env Env, a Args) (any, error) {
				from, to, v := a.Str("from"), a.Str("to"), a.Num("value")
				switch {
				case workspace.ValidLinear(from) && workspace.ValidLinear(to):
					out, err := workspace.ConvertLength(v, from, to)
					return map[string]any{"value": out, "unit": to, "from": map[string]any{"value": v, "unit": from}}, err
				case workspace.ValidAngular(from) && workspace.ValidAngular(to):
					rad, _ := workspace.ToRadians(v, from)
					out, err := workspace.FromRadians(rad, to)
					return map[string]any{"value": out, "unit": to, "from": map[string]any{"value": v, "unit": from}}, err
				}
				return nil, fmt.Errorf("cannot convert %s to %s: one is a length and the other an angle", from, to)
			},
		},
	}
}

// updateWorkspace applies one edit to a copy of the project's settings, validates the result
// (workspace.Validate, the one validation the Settings screen and global defaults also use)
// and stores it. Nothing is stored when the result is invalid.
func updateWorkspace(env Env, edit func(*workspace.Settings) error) (any, error) {
	before := env.Workspace()
	next := before.Clone()
	if err := edit(&next); err != nil {
		return nil, err
	}
	next = next.Normalized()
	if err := next.Validate(); err != nil {
		return nil, err
	}
	if err := env.SetWorkspace(next); err != nil {
		return nil, err
	}
	return workspaceView(env, changes(before, next)), nil
}

// workspaceView is what the workspace tools return: the canonical settings plus how they
// read in the display units.
func workspaceView(env Env, changed []string) map[string]any {
	s := env.Workspace()
	u := s.Units
	out := map[string]any{
		"settings": s,
		"display": map[string]any{
			"linear_unit": u.Linear, "angular_unit": u.Angular, "mm_per_unit": workspace.MMPer(u.Linear),
			"grid_spacing": u.FormatLength(s.Grid.Spacing), "grid_extent": u.FormatLength(s.Grid.Extent),
			"translation_increment": u.FormatLength(s.TranslationStep()),
			"rotation_increment":    u.FormatAngle(s.Snap.RotationIncrement * math.Pi / 180),
		},
		"effective_translation_increment_mm": s.TranslationStep(),
		"global_defaults":                    env.WorkspaceDefaults(),
		"note":                               "Workspace settings only: geometry is stored in mm and was not changed.",
	}
	if changed != nil {
		out["changed"] = changed
		if len(changed) == 0 {
			out["note"] = "Nothing changed: the settings already had these values."
		}
	}
	return out
}

// changes lists the settings that differ, as "grid.spacing: 10 → 5".
func changes(a, b workspace.Settings) []string {
	fa, fb := flatten(a), flatten(b)
	out := []string{}
	for k, v := range fb {
		if w, ok := fa[k]; !ok || w != v {
			out = append(out, fmt.Sprintf("%s: %s → %s", k, fa[k], v))
		}
	}
	sort.Strings(out)
	return out
}

func flatten(s workspace.Settings) map[string]string {
	var m map[string]map[string]any
	b, _ := json.Marshal(s)
	_ = json.Unmarshal(b, &m)
	out := map[string]string{}
	for sec, fields := range m {
		for k, v := range fields {
			j, _ := json.Marshal(v)
			out[sec+"."+k] = string(j)
		}
	}
	return out
}

func snapCalculate(env Env, a Args) (any, error) {
	s := env.Workspace()
	op := a.Str("operation")
	if a.Bool("force") {
		s.Snap.Enabled, s.Grid.Enabled = true, true
		switch op {
		case "position":
			s.Snap.Grid = true
		case "translation":
			s.Snap.Translation = true
		case "angle":
			s.Snap.Rotation = true
		}
	}
	ctx := workspace.Context{Settings: s}
	if op == "angle" {
		if !a.Has("angle") {
			return nil, fmt.Errorf("operation=angle needs angle")
		}
		unit := strOr(a, "angle_unit", workspace.Deg)
		rad, err := workspace.ToRadians(a.Num("angle"), unit)
		if err != nil {
			return nil, err
		}
		out, snapped := workspace.SnapAngle(rad, ctx)
		shown, _ := workspace.FromRadians(out, unit)
		return map[string]any{"operation": op, "snapped": snapped, "angle": clean6(shown), "angle_unit": unit,
			"angle_deg": clean6(out * 180 / math.Pi), "angle_rad": out, "display": s.Units.FormatAngle(out)}, nil
	}
	if !a.Has("point") {
		return nil, fmt.Errorf("operation=%s needs point [x, y, z]", op)
	}
	unit := strOr(a, "unit", workspace.MM)
	p, err := pointArg(a, "point", unit)
	if err != nil {
		return nil, err
	}
	var out [3]float64
	snapped := false
	switch op {
	case "position":
		out, snapped = workspace.SnapPosition(p, ctx)
	case "translation":
		out, snapped = workspace.SnapTranslation(p, ctx)
	case "world_to_grid":
		out = s.Grid.WorldToGrid(p)
	case "grid_to_world":
		out = s.Grid.GridToWorld(p)
	default:
		return nil, fmt.Errorf("unknown operation %q", op)
	}
	res := map[string]any{"operation": op, "result_mm": out, "result": inUnit(out, unit), "unit": unit, "grid_plane": s.Grid.Plane}
	if op == "position" || op == "translation" {
		res["snapped"] = snapped
		if !snapped {
			res["note"] = "Snapping of this kind is off; the value is unchanged (use force=true to see where it would snap)."
		}
	}
	return res, nil
}

// pointArg reads an [x, y, z] argument given in unit as millimetres.
func pointArg(a Args, key, unit string) ([3]float64, error) {
	var p [3]float64
	raw, _ := a[key].([]any)
	if len(raw) != 3 {
		return p, fmt.Errorf("%s must be [x, y, z]", key)
	}
	for i, v := range raw {
		f, _ := v.(float64)
		mm, err := workspace.ToMM(f, unit)
		if err != nil {
			return p, err
		}
		p[i] = mm
	}
	return p, nil
}

func inUnit(p [3]float64, unit string) [3]float64 {
	for i := range p {
		v, _ := workspace.FromMM(p[i], unit)
		p[i] = clean6(v)
	}
	return p
}

// clean6 trims binary noise from a converted value (25.4 mm → 1 in, not 0.9999999999).
func clean6(v float64) float64 { return math.Round(v*1e9) / 1e9 }

func strOr(a Args, k, def string) string {
	if s := a.Str(k); s != "" {
		return s
	}
	return def
}

func withoutKeys(a Args, keys ...string) Args {
	out := Args{}
	for k, v := range a {
		if !slices.Contains(keys, k) {
			out[k] = v
		}
	}
	return out
}
