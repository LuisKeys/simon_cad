package selectors

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Topology is the derived, topology-backed description of one feature's solid. Faces,
// edges and vertices keep the kernel's deterministic numbering, so Index is stable for a
// given rebuild and is what viewport pick IDs map to.
type Topology struct {
	Bounds   Bounds
	Faces    []FaceInfo
	Edges    []EdgeInfo
	Vertices [][3]float64
}

type FaceInfo struct {
	Surface  string // plane|cylinder|cone|sphere|torus|other
	Normal   [3]float64
	Centroid [3]float64
	Area     float64
	Radius   float64
	BBox     Bounds
}

type EdgeInfo struct {
	Curve      string // line|circle|other
	Length     float64
	Start, End [3]float64
	BBox       Bounds
}

const planeTol = 1e-6

// axisOf returns the axis and sign when n is (anti)parallel to a coordinate axis.
func axisOf(n [3]float64) (axis int, sign float64, ok bool) {
	for i := 0; i < 3; i++ {
		if math.Abs(math.Abs(n[i])-1) < planeTol {
			if n[i] > 0 {
				return i, 1, true
			}
			return i, -1, true
		}
	}
	return 0, 0, false
}

func sideName(axis int, sign float64) string {
	s := "+"
	if sign < 0 {
		s = "-"
	}
	return s + string("XYZ"[axis])
}

// planarCandidates lists planar faces facing (axis, sign), in index order.
func (t *Topology) planarCandidates(axis int, sign float64) []int {
	var out []int
	for i, f := range t.Faces {
		if f.Surface != "plane" {
			continue
		}
		if a, s, ok := axisOf(f.Normal); ok && a == axis && s == sign {
			out = append(out, i)
		}
	}
	return out
}

func (t *Topology) extremeCoord(axis int, sign float64, cands []int) float64 {
	best := math.Inf(-1)
	for _, i := range cands {
		if v := sign * t.Faces[i].Centroid[axis]; v > best {
			best = v
		}
	}
	return sign * best
}

func (f FaceInfo) descriptor(sel Selector) Descriptor {
	n := f.Normal
	d := Descriptor{Selector: sel.String(), Kind: sel.Kind, FeatureID: sel.Feature(), SurfaceType: f.Surface,
		Centroid: f.Centroid, BBox: f.BBox, Area: f.Area}
	if f.Surface == "plane" {
		d.Normal = &n
	}
	return d
}

func indexArg(sel Selector, n int, what string) (int, error) {
	raw, ok := sel.Args["index"]
	if !ok {
		return 0, fmt.Errorf("selector %s: %s requires index=", sel.String(), what)
	}
	i, err := strconv.Atoi(raw)
	if err != nil || i < 0 || i >= n {
		return 0, fmt.Errorf("selector %s: index %q is out of range (0..%d)", sel.String(), raw, n-1)
	}
	return i, nil
}

// ResolveTopo resolves any selector kind against real topology.
//
//	body(feature=F)
//	face(feature=F, side=+Z)            planar face facing +Z at the extreme of the part
//	face(feature=F, side=+Z, at=2.5)    planar face facing +Z lying in the plane z=2.5
//	face(feature=F, index=N)            any face by kernel index (curved faces, ambiguous planes)
//	edge(feature=F, index=N), vertex(feature=F, index=N)
func ResolveTopo(sel Selector, t *Topology) (Descriptor, error) {
	if t == nil {
		return Descriptor{}, fmt.Errorf("selector %s: feature has no geometry", sel.String())
	}
	switch sel.Kind {
	case "body":
		return Resolve(sel, t.Bounds)
	case "face":
		if _, ok := sel.Args["index"]; ok {
			i, err := indexArg(sel, len(t.Faces), "face")
			if err != nil {
				return Descriptor{}, err
			}
			d := t.Faces[i].descriptor(sel)
			d.Index = i
			return d, nil
		}
		side, ok := sel.Args["side"]
		if !ok {
			return Descriptor{}, fmt.Errorf("selector %s: face requires side= or index=", sel.String())
		}
		axis, sign, err := parseSide(side)
		if err != nil {
			return Descriptor{}, fmt.Errorf("selector %s: %w", sel.String(), err)
		}
		cands := t.planarCandidates(axis, sign)
		if len(cands) == 0 {
			return Descriptor{}, fmt.Errorf("selector %s: no planar face faces %s", sel.String(), sideName(axis, sign))
		}
		at := t.extremeCoord(axis, sign, cands)
		if raw, ok := sel.Args["at"]; ok {
			if at, err = strconv.ParseFloat(raw, 64); err != nil {
				return Descriptor{}, fmt.Errorf("selector %s: at=%q is not a number", sel.String(), raw)
			}
		}
		best := -1
		for _, i := range cands {
			if math.Abs(t.Faces[i].Centroid[axis]-at) > 1e-4 {
				continue
			}
			if best < 0 || t.Faces[i].Area > t.Faces[best].Area+1e-9 { // ties keep the lowest index
				best = i
			}
		}
		if best < 0 {
			return Descriptor{}, fmt.Errorf("selector %s: no %s face in the plane %c=%g", sel.String(), sideName(axis, sign), "xyz"[axis], at)
		}
		d := t.Faces[best].descriptor(sel)
		d.Index = best
		return d, nil
	case "edge", "edges":
		i, err := indexArg(sel, len(t.Edges), "edge")
		if err != nil {
			return Descriptor{}, err
		}
		e := t.Edges[i]
		c := e.BBox.Center()
		return Descriptor{Selector: sel.String(), Kind: sel.Kind, FeatureID: sel.Feature(), SurfaceType: e.Curve,
			Centroid: c, BBox: e.BBox, Area: e.Length, Index: i}, nil
	case "vertex":
		i, err := indexArg(sel, len(t.Vertices), "vertex")
		if err != nil {
			return Descriptor{}, err
		}
		p := t.Vertices[i]
		return Descriptor{Selector: sel.String(), Kind: "vertex", FeatureID: sel.Feature(), Centroid: p,
			BBox: Bounds{Min: p, Max: p}, Index: i}, nil
	}
	return Descriptor{}, fmt.Errorf("selector kind %q is not resolvable", sel.Kind)
}

func fmtCoord(v float64) string {
	s := strconv.FormatFloat(math.Round(v*1e6)/1e6, 'f', -1, 64)
	if s == "-0" {
		return "0"
	}
	return s
}

// FaceSelector is the inverse of ResolveTopo for faces: the most semantic selector that
// resolves back to face i. Axis-aligned planar faces get side= (plus at= when they are not
// the unique extreme face); everything else falls back to index=.
func (t *Topology) FaceSelector(feature string, i int) Selector {
	sel := Selector{Kind: "face", Args: map[string]string{"feature": feature}}
	f := t.Faces[i]
	if f.Surface == "plane" {
		if axis, sign, ok := axisOf(f.Normal); ok {
			cands := t.planarCandidates(axis, sign)
			sel.Args["side"] = sideName(axis, sign)
			at := f.Centroid[axis]
			same := 0
			for _, c := range cands {
				if math.Abs(t.Faces[c].Centroid[axis]-at) <= 1e-4 {
					same++
				}
			}
			if same == 1 {
				if math.Abs(t.extremeCoord(axis, sign, cands)-at) > 1e-4 {
					sel.Args["at"] = fmtCoord(at)
				}
				return sel
			}
		}
	}
	sel.Args = map[string]string{"feature": feature, "index": strconv.Itoa(i)}
	return sel
}

// EdgeSelector and VertexSelector name an edge or vertex by kernel index.
func EdgeSelector(feature string, i int) Selector {
	return Selector{Kind: "edge", Args: map[string]string{"feature": feature, "index": strconv.Itoa(i)}}
}

func VertexSelector(feature string, i int) Selector {
	return Selector{Kind: "vertex", Args: map[string]string{"feature": feature, "index": strconv.Itoa(i)}}
}

// BodySelector names a whole feature body.
func BodySelector(feature string) Selector {
	return Selector{Kind: "body", Args: map[string]string{"feature": feature}}
}

// IndexOf parses the index= argument of a face/edge/vertex selector (-1 when absent).
func (s Selector) IndexOf() int {
	i, err := strconv.Atoi(strings.TrimSpace(s.Args["index"]))
	if err != nil {
		return -1
	}
	return i
}
