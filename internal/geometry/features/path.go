package features

import (
	"encoding/json"
	"fmt"
	"math"

	"github.com/accelone/simoncad/internal/geometry/curve"
	"github.com/accelone/simoncad/internal/geometry/profile"
)

// KindData marks nodes that are pure data (a path, a profile): they build no geometry but
// other features read their resolved parameters, and dependents regenerate when they change.
const KindData = "data"

// Node types of the path/sweep family.
const (
	PathType    = "path"
	ProfileType = "profile"
)

func init() {
	register(&Feature{Type: PathType, Kind: KindData, KeepsInputs: true, Check: func(p Params) error {
		_, err := PathFromParams(p)
		return err
	}})
	register(&Feature{Type: ProfileType, Kind: KindData, KeepsInputs: true, Check: func(p Params) error {
		_, err := ProfileFromParams(p)
		return err
	}})
}

func vec3(v any) (curve.Vec, error) {
	a, ok := v.([]any)
	if !ok || len(a) != 3 {
		return curve.Vec{}, fmt.Errorf("expected [x, y, z]")
	}
	var out curve.Vec
	for i := range a {
		f, ok := a[i].(float64)
		if !ok || math.IsNaN(f) || math.IsInf(f, 0) {
			return out, fmt.Errorf("coordinates must be finite numbers")
		}
		out[i] = f
	}
	return out, nil
}

func optVec3(m map[string]any, key string) (*curve.Vec, error) {
	v, ok := m[key]
	if !ok || v == nil {
		return nil, nil
	}
	p, err := vec3(v)
	if err != nil {
		return nil, fmt.Errorf("%q: %w", key, err)
	}
	return &p, nil
}

func optNum(m map[string]any, key string) (float64, error) {
	v, ok := m[key]
	if !ok || v == nil {
		return 0, nil
	}
	f, ok := v.(float64)
	if !ok || math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, fmt.Errorf("%q must be a finite number", key)
	}
	return f, nil
}

// normalize round-trips v through JSON so typed in-memory values (ints, []float64, structs)
// become the float64/[]any/map[string]any shapes the parsers expect.
func normalize(v any) (any, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var out any
	err = json.Unmarshal(b, &out)
	return out, err
}

// PathFromParams reads a path node: type (default "linear"), closed, and points. A point is
// either [x, y, z] or {"p": [x,y,z], "in": [..], "out": [..], "w": 1, "h": 1, "twist": 0}.
func PathFromParams(p Params) (*curve.Path, error) {
	pathErr := func(hint, f string, a ...any) error { return Coded(CodePathInvalid, hint, f, a...) }
	t := p.Str("type")
	if t == "" {
		t = string(curve.Linear)
	}
	path := &curve.Path{Type: curve.Type(t), Closed: p.Bool("closed")}
	raw, ok := p["points"]
	if !ok {
		return nil, pathErr("pass points: [[x,y,z], ...]", "path has no points")
	}
	nv, err := normalize(raw)
	if err != nil {
		return nil, pathErr("", "points: %v", err)
	}
	list, ok := nv.([]any)
	if !ok {
		return nil, pathErr("pass points: [[x,y,z], ...]", "points must be a list")
	}
	for i, it := range list {
		var pt curve.Point
		switch v := it.(type) {
		case []any:
			pos, err := vec3(v)
			if err != nil {
				return nil, pathErr("", "point %d: %v", i, err)
			}
			pt.P = pos
		case map[string]any:
			pos, err := vec3(v["p"])
			if err != nil {
				return nil, pathErr("", "point %d position \"p\": %v", i, err)
			}
			pt.P = pos
			if pt.In, err = optVec3(v, "in"); err != nil {
				return nil, pathErr("", "point %d: %v", i, err)
			}
			if pt.Out, err = optVec3(v, "out"); err != nil {
				return nil, pathErr("", "point %d: %v", i, err)
			}
			for _, f := range []struct {
				k string
				d *float64
			}{{"w", &pt.W}, {"h", &pt.H}, {"twist", &pt.Twist}} {
				if *f.d, err = optNum(v, f.k); err != nil {
					return nil, pathErr("", "point %d: %v", i, err)
				}
			}
			if pt.W < 0 || pt.H < 0 {
				return nil, pathErr("width and height are scale factors and must be positive", "point %d has a negative w/h", i)
			}
		default:
			return nil, pathErr("", "point %d must be [x,y,z] or an object with \"p\"", i)
		}
		path.Points = append(path.Points, pt)
	}
	if err := path.Validate(); err != nil {
		return nil, pathErr("", "%v", err)
	}
	return path, nil
}

func vecList(v curve.Vec) []any { return []any{v[0], v[1], v[2]} }

// PathToParams is the inverse of PathFromParams (canonical form with object points).
func PathToParams(path *curve.Path) map[string]any {
	pts := make([]any, len(path.Points))
	for i, pt := range path.Points {
		m := map[string]any{"p": vecList(pt.P)}
		if pt.In != nil {
			m["in"] = vecList(*pt.In)
		}
		if pt.Out != nil {
			m["out"] = vecList(*pt.Out)
		}
		if pt.W != 0 {
			m["w"] = pt.W
		}
		if pt.H != 0 {
			m["h"] = pt.H
		}
		if pt.Twist != 0 {
			m["twist"] = pt.Twist
		}
		pts[i] = m
	}
	return map[string]any{"type": string(path.Type), "closed": path.Closed, "points": pts}
}

func vec2List(v any) ([]profile.Vec2, error) {
	a, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("expected a list of [x, y]")
	}
	out := make([]profile.Vec2, len(a))
	for i, e := range a {
		pt, ok := e.([]any)
		if !ok || len(pt) != 2 {
			return nil, fmt.Errorf("point %d must be [x, y]", i)
		}
		x, ok1 := pt[0].(float64)
		y, ok2 := pt[1].(float64)
		if !ok1 || !ok2 || math.IsNaN(x+y) || math.IsInf(x+y, 0) {
			return nil, fmt.Errorf("point %d has a non-finite coordinate", i)
		}
		out[i] = profile.Vec2{x, y}
	}
	return out, nil
}

// ProfileFromParams reads a profile node: "shape" is a preset name with its numeric
// parameters beside it ("radius", "width", ...), or "custom" with "contours":
// [{"points": [[x,y],...], "hole": false}].
func ProfileFromParams(p Params) (*profile.Profile, error) {
	bad := func(hint, f string, a ...any) error { return Coded(CodeProfileInvalid, hint, f, a...) }
	shape := p.Str("shape")
	if shape == "" {
		return nil, bad("shape is one of "+fmt.Sprint(profile.Types()), "profile has no shape")
	}
	var pr *profile.Profile
	if shape == "custom" {
		raw, ok := p["contours"]
		if !ok {
			return nil, bad("pass contours: [{points:[[x,y],...], hole:false}]", "custom profile has no contours")
		}
		nv, err := normalize(raw)
		if err != nil {
			return nil, bad("", "contours: %v", err)
		}
		list, ok := nv.([]any)
		if !ok {
			return nil, bad("", "contours must be a list")
		}
		var cs []profile.Contour
		for i, it := range list {
			m, ok := it.(map[string]any)
			if !ok {
				return nil, bad("", "contour %d must be an object", i)
			}
			pts, err := vec2List(m["points"])
			if err != nil {
				return nil, bad("", "contour %d: %v", i, err)
			}
			hole, _ := m["hole"].(bool)
			cs = append(cs, profile.Contour{Points: pts, Closed: true, Hole: hole})
		}
		var err2 error
		if pr, err2 = profile.Custom(cs); err2 != nil {
			return nil, bad("fix the contour (no self-intersection, no duplicate points, holes inside an outer contour)", "%v", err2)
		}
	} else {
		pp := profile.Params{Num: map[string]float64{}}
		for k, v := range p {
			if f, ok := v.(float64); ok {
				pp.Num[k] = f
			}
		}
		if v, ok := p["vertices"]; ok {
			nv, err := normalize(v)
			if err == nil {
				pp.Vertices, err = vec2List(nv)
			}
			if err != nil {
				return nil, bad("", "vertices: %v", err)
			}
		}
		var err error
		if pr, err = profile.New(shape, pp); err != nil {
			return nil, bad("", "%v", err)
		}
	}
	if is := pr.Validate(); profile.HasError(is) {
		for _, i := range is {
			if i.Severity == "error" {
				return nil, bad("", "%s", i.Message)
			}
		}
	}
	return pr, nil
}

// RecipeType is the node type of a stored geometry recipe: pure data that remembers how a group of
// nodes was generated (see internal/recipe). It builds nothing.
const RecipeType = "recipe"

func init() {
	register(&Feature{Type: RecipeType, Kind: KindData, KeepsInputs: true, Check: func(p Params) error {
		if t := p.Str("type"); t == "" {
			return fmt.Errorf("a recipe needs a type")
		}
		return nil
	}})
}
