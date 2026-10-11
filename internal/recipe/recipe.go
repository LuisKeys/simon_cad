// Package recipe is the lightweight declarative description of a reproducible object: a stamp
// today, other kinds later. A recipe is plain JSON with a schema; executing it (internal/tools)
// is deterministic and creates ordinary editable nodes. It is not a feature history or a visual
// programming system: it only remembers how a generated group of nodes was made, so it can be
// shared, edited and run again.
package recipe

import (
	"encoding/json"
	"fmt"
	"math"
)

// Types of recipe.
const TypeStamp = "stamp"

// Stamp design modes.
const (
	ModeFilled     = "filled_regions" // the ink areas become raised regions
	ModeCenterline = "centerline"     // strokes become raised ridges of stroke_width
)

// Source says where a design comes from: exactly one field.
type Source struct {
	ImageID string `json:"image_id,omitempty"`
	Path    string `json:"path,omitempty"`
	SVG     string `json:"svg,omitempty"`
	Sketch  string `json:"sketch,omitempty"` // an existing sketch node (regions are used as they are)
}

// Trace are the optional vectorization settings of an image source.
type Trace struct {
	Threshold int     `json:"threshold,omitempty"`
	Adaptive  bool    `json:"adaptive,omitempty"`
	Invert    bool    `json:"invert,omitempty"`
	MinArea   int     `json:"min_area,omitempty"`
	Smooth    float64 `json:"smooth,omitempty"`
	Tolerance float64 `json:"tolerance,omitempty"`
}

// Base is the plate of the stamp.
type Base struct {
	Shape        string  `json:"shape"`              // rectangle | circle
	Width        float64 `json:"width,omitempty"`    // rectangle
	Height       float64 `json:"height,omitempty"`   // rectangle
	Diameter     float64 `json:"diameter,omitempty"` // circle
	Thickness    float64 `json:"thickness"`
	RoundedEdges bool    `json:"rounded_edges,omitempty"`
	EdgeRadius   float64 `json:"edge_radius,omitempty"`
}

// Design is the relief.
type Design struct {
	Source       Source  `json:"source"`
	Mode         string  `json:"mode"`
	ReliefHeight float64 `json:"relief_height"`
	StrokeWidth  float64 `json:"stroke_width,omitempty"` // centerline ridges
	Margin       float64 `json:"margin"`
	WidthMM      float64 `json:"width_mm,omitempty"` // real width of the design; default: fit the base
	Trace        *Trace  `json:"trace,omitempty"`
}

// Handle is the optional grip on the back.
type Handle struct {
	Enabled  bool    `json:"enabled"`
	Height   float64 `json:"height,omitempty"`
	Diameter float64 `json:"diameter,omitempty"`
}

// Stamp is a printable stamp: plate, raised design, optional grip, shrinkage compensation.
type Stamp struct {
	Type      string  `json:"type"`
	Base      Base    `json:"base"`
	Design    Design  `json:"design"`
	Mirror    bool    `json:"mirror"`
	Handle    Handle  `json:"handle"`
	Shrinkage float64 `json:"shrinkage_percent"` // expected material shrinkage; the model is scaled up to compensate
}

// Defaults that are filled in when a recipe leaves them out. They are reported to the user by
// the tools (nothing is silently invented).
const (
	DefaultBaseThickness = 4.0
	DefaultReliefHeight  = 1.5
	DefaultMargin        = 3.0
	DefaultStrokeWidth   = 1.5
	DefaultHandleHeight  = 12.0
)

// ParseStamp validates and normalizes a stamp recipe given as decoded JSON. The returned recipe
// has every default filled in; Assumed lists the ones that were not given.
func ParseStamp(m map[string]any) (*Stamp, []string, error) {
	if err := validateKeys(m); err != nil {
		return nil, nil, err
	}
	b, err := json.Marshal(m)
	if err != nil {
		return nil, nil, err
	}
	var s Stamp
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, nil, fmt.Errorf("recipe does not match the stamp schema: %v", err)
	}
	if s.Type != TypeStamp {
		return nil, nil, fmt.Errorf("recipe type must be %q (got %q)", TypeStamp, s.Type)
	}
	var assumed []string
	given := func(path ...string) bool {
		var cur any = m
		for _, k := range path {
			mm, ok := cur.(map[string]any)
			if !ok {
				return false
			}
			if cur, ok = mm[k]; !ok {
				return false
			}
		}
		return true
	}
	pos := func(name string, v float64) error {
		if !(v > 0) || math.IsInf(v, 0) || math.IsNaN(v) {
			return fmt.Errorf("%s must be a positive number (got %v)", name, v)
		}
		return nil
	}
	// ---- base
	switch s.Base.Shape {
	case "":
		s.Base.Shape = "rectangle"
	case "rectangle", "circle":
	default:
		return nil, nil, fmt.Errorf("base.shape must be rectangle or circle (got %q)", s.Base.Shape)
	}
	if s.Base.Shape == "rectangle" {
		if err := pos("base.width", s.Base.Width); err != nil {
			return nil, nil, fmt.Errorf("%v (give the stamp size: width and height in mm)", err)
		}
		if err := pos("base.height", s.Base.Height); err != nil {
			return nil, nil, fmt.Errorf("%v (give the stamp size: width and height in mm)", err)
		}
		s.Base.Diameter = 0
	} else {
		if err := pos("base.diameter", s.Base.Diameter); err != nil {
			return nil, nil, fmt.Errorf("%v (give the stamp diameter in mm)", err)
		}
		s.Base.Width, s.Base.Height = 0, 0
	}
	if !given("base", "thickness") {
		s.Base.Thickness = DefaultBaseThickness
		assumed = append(assumed, fmt.Sprintf("base thickness %g mm", DefaultBaseThickness))
	}
	if err := pos("base.thickness", s.Base.Thickness); err != nil {
		return nil, nil, err
	}
	small := s.Base.minSide()
	if s.Base.RoundedEdges {
		if s.Base.EdgeRadius == 0 {
			s.Base.EdgeRadius = math.Round(math.Min(1, s.Base.Thickness/3)*100) / 100
		}
		if err := pos("base.edge_radius", s.Base.EdgeRadius); err != nil {
			return nil, nil, err
		}
		if s.Base.EdgeRadius >= s.Base.Thickness/2 || s.Base.EdgeRadius >= small/2 {
			return nil, nil, fmt.Errorf("base.edge_radius %g mm is too large for a %g mm thick plate (must be below %g mm)",
				s.Base.EdgeRadius, s.Base.Thickness, math.Min(s.Base.Thickness/2, small/2))
		}
	} else {
		s.Base.EdgeRadius = 0
	}
	// ---- design
	d := &s.Design
	if n := d.Source.count(); n != 1 {
		return nil, nil, fmt.Errorf("design.source needs exactly one of image_id, path, svg or sketch (got %d)", n)
	}
	switch d.Mode {
	case "":
		d.Mode = ModeFilled
	case ModeFilled, ModeCenterline:
	default:
		return nil, nil, fmt.Errorf("design.mode must be filled_regions or centerline (got %q)", d.Mode)
	}
	if d.Mode == ModeCenterline && d.Source.Sketch != "" {
		return nil, nil, fmt.Errorf("design.mode centerline traces an image or SVG; an existing sketch has regions, use filled_regions")
	}
	if !given("design", "relief_height") {
		d.ReliefHeight = DefaultReliefHeight
		assumed = append(assumed, fmt.Sprintf("relief height %g mm", DefaultReliefHeight))
	}
	if err := pos("design.relief_height", d.ReliefHeight); err != nil {
		return nil, nil, err
	}
	if !given("design", "margin") {
		d.Margin = DefaultMargin
		assumed = append(assumed, fmt.Sprintf("margin %g mm around the design", DefaultMargin))
	}
	if d.Margin < 0 || d.Margin >= small/2 {
		return nil, nil, fmt.Errorf("design.margin %g mm leaves no room on a %g mm stamp (must be 0 to %g)", d.Margin, small, small/2)
	}
	if d.Mode == ModeCenterline {
		if !given("design", "stroke_width") {
			d.StrokeWidth = DefaultStrokeWidth
			assumed = append(assumed, fmt.Sprintf("stroke width %g mm", DefaultStrokeWidth))
		}
		if err := pos("design.stroke_width", d.StrokeWidth); err != nil {
			return nil, nil, err
		}
	} else {
		d.StrokeWidth = 0
	}
	if d.WidthMM != 0 {
		if err := pos("design.width_mm", d.WidthMM); err != nil {
			return nil, nil, err
		}
	}
	// ---- handle
	h := &s.Handle
	if h.Enabled {
		if h.Height == 0 {
			h.Height = DefaultHandleHeight
			assumed = append(assumed, fmt.Sprintf("handle height %g mm", DefaultHandleHeight))
		}
		if err := pos("handle.height", h.Height); err != nil {
			return nil, nil, err
		}
		if h.Diameter == 0 {
			h.Diameter = math.Round(0.4*small*10) / 10
		}
		if err := pos("handle.diameter", h.Diameter); err != nil {
			return nil, nil, err
		}
		if h.Diameter >= small {
			return nil, nil, fmt.Errorf("handle.diameter %g mm does not fit the %g mm stamp (use less than %g)", h.Diameter, small, small)
		}
	} else {
		*h = Handle{}
	}
	// ---- shrinkage
	if s.Shrinkage < 0 || s.Shrinkage >= 40 {
		return nil, nil, fmt.Errorf("shrinkage_percent must be between 0 and 40 (got %g)", s.Shrinkage)
	}
	return &s, assumed, nil
}

func (b Base) minSide() float64 {
	if b.Shape == "circle" {
		return b.Diameter
	}
	return math.Min(b.Width, b.Height)
}

// MinSide is the shorter side (or the diameter) of the plate.
func (b Base) MinSide() float64 { return b.minSide() }

func (s Source) count() int {
	n := 0
	for _, v := range []string{s.ImageID, s.Path, s.SVG, s.Sketch} {
		if v != "" {
			n++
		}
	}
	return n
}

// ScaleFactor is the uniform factor that compensates the material's shrinkage (1 when none).
func (s *Stamp) ScaleFactor() float64 { return 1 / (1 - s.Shrinkage/100) }

// Map is the canonical JSON form (what is stored in the recipe node).
func (s *Stamp) Map() map[string]any {
	b, _ := json.Marshal(s)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	return m
}

// validateKeys rejects unknown fields so typos do not silently change nothing.
func validateKeys(m map[string]any) error {
	allowed := map[string][]string{
		"":       {"type", "base", "design", "mirror", "handle", "shrinkage_percent"},
		"base":   {"shape", "width", "height", "diameter", "thickness", "rounded_edges", "edge_radius"},
		"design": {"source", "mode", "relief_height", "stroke_width", "margin", "width_mm", "trace"},
		"source": {"image_id", "path", "svg", "sketch"},
		"trace":  {"threshold", "adaptive", "invert", "min_area", "smooth", "tolerance"},
		"handle": {"enabled", "height", "diameter"},
	}
	check := func(scope string, v any) error {
		mm, ok := v.(map[string]any)
		if !ok {
			return nil
		}
		for k := range mm {
			found := false
			for _, a := range allowed[scope] {
				found = found || a == k
			}
			if !found {
				where := scope
				if where == "" {
					where = "recipe"
				}
				return fmt.Errorf("unknown field %q in %s (allowed: %v)", k, where, allowed[scope])
			}
		}
		return nil
	}
	if err := check("", m); err != nil {
		return err
	}
	for _, k := range []string{"base", "design", "handle"} {
		if err := check(k, m[k]); err != nil {
			return err
		}
	}
	if d, ok := m["design"].(map[string]any); ok {
		if err := check("source", d["source"]); err != nil {
			return err
		}
		if err := check("trace", d["trace"]); err != nil {
			return err
		}
	}
	return nil
}

// Merge applies a JSON merge patch (RFC 7386 style: objects merge, null deletes, anything else
// replaces) to a recipe map and returns the result: how update_recipe edits a recipe.
func Merge(base, patch map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range base {
		out[k] = v
	}
	for k, v := range patch {
		if v == nil {
			delete(out, k)
			continue
		}
		pm, pok := v.(map[string]any)
		bm, bok := out[k].(map[string]any)
		if pok && bok {
			out[k] = Merge(bm, pm)
		} else {
			out[k] = v
		}
	}
	return out
}
