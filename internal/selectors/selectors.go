// Package selectors implements semantic topology selectors such as
// face(feature=base_plate, side=+Z). They are the stable bridge between visual
// selection and agent references, resolved at rebuild time against derived geometry.
//
// Phase 1 resolves body and planar-face selectors from a feature's axis-aligned
// bounding box, which is exact for prismatic parts. Edge/vertex selectors parse but
// are reported as unsupported until Phase 2.
package selectors

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

type Selector struct {
	Kind string            // body|face|edge|edges|vertex
	Args map[string]string // feature, side, kind, ...
}

// Parse reads `kind(key=value, key=value)`.
func Parse(s string) (Selector, error) {
	s = strings.TrimSpace(s)
	open := strings.IndexByte(s, '(')
	if open <= 0 || !strings.HasSuffix(s, ")") {
		return Selector{}, fmt.Errorf("selector %q: expected kind(key=value, ...)", s)
	}
	sel := Selector{Kind: strings.ToLower(strings.TrimSpace(s[:open])), Args: map[string]string{}}
	body := strings.TrimSpace(s[open+1 : len(s)-1])
	if body != "" {
		for _, part := range strings.Split(body, ",") {
			k, v, ok := strings.Cut(part, "=")
			k, v = strings.TrimSpace(k), strings.TrimSpace(v)
			if !ok || k == "" || v == "" {
				return Selector{}, fmt.Errorf("selector %q: bad argument %q", s, strings.TrimSpace(part))
			}
			if _, dup := sel.Args[k]; dup {
				return Selector{}, fmt.Errorf("selector %q: duplicate argument %q", s, k)
			}
			sel.Args[k] = v
		}
	}
	switch sel.Kind {
	case "body", "face", "edge", "edges", "vertex":
	default:
		return Selector{}, fmt.Errorf("selector %q: unknown kind %q", s, sel.Kind)
	}
	if sel.Args["feature"] == "" {
		return Selector{}, fmt.Errorf("selector %q: missing feature=", s)
	}
	return sel, nil
}

// String is the canonical form (feature first, remaining keys sorted).
func (s Selector) String() string {
	keys := make([]string, 0, len(s.Args))
	for k := range s.Args {
		if k != "feature" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	parts := []string{"feature=" + s.Args["feature"]}
	for _, k := range keys {
		parts = append(parts, k+"="+s.Args[k])
	}
	return s.Kind + "(" + strings.Join(parts, ",") + ")"
}

func (s Selector) Feature() string { return s.Args["feature"] }

// FeatureOf extracts feature= from a selector string without full validation
// ("" when it does not parse).
func FeatureOf(raw string) string {
	s, err := Parse(raw)
	if err != nil {
		return ""
	}
	return s.Feature()
}

// Bounds is an axis-aligned box in mm.
type Bounds struct{ Min, Max [3]float64 }

func (b Bounds) Size() [3]float64 {
	return [3]float64{b.Max[0] - b.Min[0], b.Max[1] - b.Min[1], b.Max[2] - b.Min[2]}
}

func (b Bounds) Center() [3]float64 {
	return [3]float64{(b.Min[0] + b.Max[0]) / 2, (b.Min[1] + b.Max[1]) / 2, (b.Min[2] + b.Max[2]) / 2}
}

// Descriptor is the geometric description of a resolved entity. It is what the
// agent sees for a selection and what rebuilds use to place features.
type Descriptor struct {
	Selector    string      `json:"selector"`
	Kind        string      `json:"kind"`
	FeatureID   string      `json:"feature_id"`
	SurfaceType string      `json:"surface_type,omitempty"`
	Normal      *[3]float64 `json:"normal,omitempty"`
	Centroid    [3]float64  `json:"centroid"`
	BBox        Bounds      `json:"bbox"`
	Area        float64     `json:"area,omitempty"`
}

// Sides maps side names to (axis, sign).
func parseSide(side string) (axis int, sign float64, err error) {
	switch strings.ToUpper(side) {
	case "+X", "X+", "RIGHT":
		return 0, 1, nil
	case "-X", "X-", "LEFT":
		return 0, -1, nil
	case "+Y", "Y+", "BACK":
		return 1, 1, nil
	case "-Y", "Y-", "FRONT":
		return 1, -1, nil
	case "+Z", "Z+", "TOP":
		return 2, 1, nil
	case "-Z", "Z-", "BOTTOM":
		return 2, -1, nil
	}
	return 0, 0, fmt.Errorf("unknown side %q (use +X,-X,+Y,-Y,+Z,-Z)", side)
}

// Resolve describes the selected entity given the bounds of its feature.
func Resolve(sel Selector, b Bounds) (Descriptor, error) {
	d := Descriptor{Selector: sel.String(), Kind: sel.Kind, FeatureID: sel.Feature(), BBox: b, Centroid: b.Center()}
	sz := b.Size()
	switch sel.Kind {
	case "body":
		d.SurfaceType = ""
		return d, nil
	case "face":
		side, ok := sel.Args["side"]
		if !ok {
			return d, fmt.Errorf("selector %s: face requires side=", sel.String())
		}
		axis, sign, err := parseSide(side)
		if err != nil {
			return d, fmt.Errorf("selector %s: %w", sel.String(), err)
		}
		n := [3]float64{}
		n[axis] = sign
		c := b.Center()
		if sign > 0 {
			c[axis] = b.Max[axis]
		} else {
			c[axis] = b.Min[axis]
		}
		a1, a2 := (axis+1)%3, (axis+2)%3
		d.SurfaceType, d.Normal, d.Centroid, d.Area = "plane", &n, c, sz[a1]*sz[a2]
		d.BBox = b
		d.BBox.Min[axis], d.BBox.Max[axis] = c[axis], c[axis]
		return d, nil
	}
	return d, fmt.Errorf("selector kind %q is not resolvable yet (planned for Phase 2)", sel.Kind)
}

// Near reports whether a ≈ b within tol.
func Near(a, b, tol float64) bool { return math.Abs(a-b) <= tol }
