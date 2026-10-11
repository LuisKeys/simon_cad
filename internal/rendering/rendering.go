// Package rendering turns a rebuilt model into viewport-ready meshes and keeps pick IDs
// and semantic selectors in one-to-one correspondence: a triangle the user clicks belongs
// to a face, and every face has a selector; a selector resolves back to the face to
// highlight. Meshes are derived data, cached per node content key, so a rebuild that
// leaves a body unchanged never re-tessellates it or resends it to the client.
package rendering

import (
	"fmt"
	"math"
	"sort"
	"sync"

	"github.com/accelone/simoncad/internal/cadir"
	"github.com/accelone/simoncad/internal/geometry/api"
	"github.com/accelone/simoncad/internal/geometry/features"
	"github.com/accelone/simoncad/internal/geometry/rebuild"
	"github.com/accelone/simoncad/internal/selectors"
)

// cacheCap bounds retained body meshes (current model + a few previews).
const cacheCap = 64

type Vec = [3]float64

// FaceRange is one pickable face: a contiguous run of triangles.
type FaceRange struct {
	Index    int     `json:"index"`
	TriStart int     `json:"tri_start"`
	TriCount int     `json:"tri_count"`
	Selector string  `json:"selector"`
	Surface  string  `json:"surface"`
	Normal   *Vec    `json:"normal,omitempty"`
	Centroid Vec     `json:"centroid"`
	Area     float64 `json:"area"`
}

// EdgeLine is one pickable edge: a polyline in EdgePoints.
type EdgeLine struct {
	Index    int     `json:"index"`
	PtStart  int     `json:"pt_start"`
	PtCount  int     `json:"pt_count"`
	Selector string  `json:"selector"`
	Curve    string  `json:"curve"`
	Length   float64 `json:"length"`
}

// VertexPoint is one pickable vertex.
type VertexPoint struct {
	Index    int    `json:"index"`
	Pos      Vec    `json:"pos"`
	Selector string `json:"selector"`
}

// BodyMesh is everything the viewport needs to draw and pick one body.
type BodyMesh struct {
	ID         string           `json:"id"`  // feature that produced the body
	Key        string           `json:"key"` // content key; unchanged key ⇒ unchanged geometry
	Selector   string           `json:"selector"`
	Bounds     selectors.Bounds `json:"bounds"`
	Positions  []float32        `json:"positions"`
	Normals    []float32        `json:"normals"`
	Indices    []uint32         `json:"indices"`
	Faces      []FaceRange      `json:"faces"`
	EdgePoints []float32        `json:"edge_points"`
	Edges      []EdgeLine       `json:"edges"`
	Vertices   []VertexPoint    `json:"vertices"`
	Triangles  int              `json:"triangles"`

	topo *selectors.Topology
}

// Topology is the selector-facing description of the body.
func (b *BodyMesh) Topology() *selectors.Topology { return b.topo }

// Delta is what a client needs to bring its scene up to date.
type Delta struct {
	Order   []string          `json:"order"`   // visible body ids, draw order
	Keys    map[string]string `json:"keys"`    // id -> current key
	Changed []*BodyMesh       `json:"changed"` // bodies whose key the client did not have
	Removed []string          `json:"removed"` // ids the client has that are gone
	Stats   Stats             `json:"stats"`
	// Appearance overrides per body id, filled by the caller from the CAD-IR. It is
	// independent of mesh keys: changing it never changes Keys/Changed.
	Appearance map[string]*cadir.Appearance `json:"appearance,omitempty"`
	// Sketches are the outlines of sketch nodes no feature consumes yet. They are tiny, so
	// they are always sent whole rather than keyed.
	Sketches []*SketchLines `json:"sketches"`
	// Paths are the path nodes of the model (curves with control points; no geometry). Filled
	// by the caller from the CAD-IR, like Appearance.
	Paths []*PathLines `json:"paths"`
}

// SketchLines is one sketch drawn as line segments (xyz pairs, 6 floats per segment).
type SketchLines struct {
	ID    string    `json:"id"`
	Lines []float32 `json:"lines"`
}

// Stats reports work done, for budgets and tests.
type Stats struct {
	Tessellated int `json:"tessellated"` // bodies meshed by this call
	Reused      int `json:"reused"`      // bodies served from cache
	Triangles   int `json:"triangles"`   // total in the scene
}

// Renderer caches meshes by (node key, deflection).
type Renderer struct {
	mu    sync.Mutex
	cache map[string]*BodyMesh
	order []string // LRU, oldest first
	// DetailFn reports the current display detail; nil means DetailFine.
	DetailFn func() Detail
}

func (r *Renderer) detail() Detail {
	if r.DetailFn == nil {
		return DetailFine
	}
	return r.DetailFn()
}

func New() *Renderer { return &Renderer{cache: map[string]*BodyMesh{}} }

// Detail is how finely bodies are meshed for display. It only changes the viewport mesh
// (and so the triangle count); geometry, selection and exports are unaffected.
type Detail string

const (
	DetailFine   Detail = "fine" // default: smooth curves
	DetailNormal Detail = "normal"
	DetailCoarse Detail = "coarse" // fewest triangles
)

// Details lists the valid levels, finest first.
var Details = []Detail{DetailFine, DetailNormal, DetailCoarse}

// ParseDetail maps a level name to a Detail; ok is false for an unknown name.
func ParseDetail(s string) (Detail, bool) {
	for _, d := range Details {
		if string(d) == s {
			return d, true
		}
	}
	return DetailFine, false
}

// deflectionFor picks a linear deflection from the body size: fine enough to look smooth,
// coarse enough to hold the triangle budget.
func deflectionFor(b selectors.Bounds) float64 { return deflectionAt(b, DetailFine) }

// angularAt is the widest angle (radians) one mesh segment of a curve may span.
func angularAt(d Detail) float64 {
	switch d {
	case DetailNormal:
		return 0.8
	case DetailCoarse:
		return 1.2
	}
	return 0.5
}

func deflectionAt(b selectors.Bounds, d Detail) float64 {
	sz := b.Size()
	diag := math.Sqrt(sz[0]*sz[0] + sz[1]*sz[1] + sz[2]*sz[2])
	rel, floor := 0.0015, 0.02
	switch d {
	case DetailNormal:
		rel, floor = 0.006, 0.08
	case DetailCoarse:
		rel, floor = 0.02, 0.3
	}
	return math.Max(floor, rel*diag)
}

func (r *Renderer) touch(k string) {
	for i, x := range r.order {
		if x == k {
			r.order = append(r.order[:i], r.order[i+1:]...)
			break
		}
	}
	r.order = append(r.order, k)
	for len(r.order) > cacheCap {
		delete(r.cache, r.order[0])
		r.order = r.order[1:]
	}
}

// Body returns the mesh of one built node, tessellating on a cache miss.
func (r *Renderer) Body(id string, res *rebuild.NodeResult) (*BodyMesh, bool, error) {
	if res == nil || res.Body == nil {
		return nil, false, fmt.Errorf("feature %q has no geometry", id)
	}
	bounds := res.Body.Bounds()
	det := r.detail()
	defl := deflectionAt(bounds, det)
	key := res.Key
	if det != DetailFine {
		key += "~" + string(det) // the client re-fetches a body when its mesh detail changes
	}
	ck := fmt.Sprintf("%s|%s|%g", id, key, defl)
	r.mu.Lock()
	defer r.mu.Unlock()
	if m, ok := r.cache[ck]; ok {
		r.touch(ck)
		return m, true, nil
	}
	mesh, err := res.Body.Shape.TessellateAngular(defl, angularAt(det))
	if err != nil {
		return nil, false, err
	}
	m := build(id, key, bounds, mesh)
	r.cache[ck] = m
	r.touch(ck)
	return m, false, nil
}

func build(id, key string, bounds selectors.Bounds, mesh *api.Mesh) *BodyMesh {
	topo := features.TopologyOf(bounds, mesh)
	m := &BodyMesh{ID: id, Key: key, Selector: selectors.BodySelector(id).String(), Bounds: bounds,
		Positions: mesh.Positions, Normals: mesh.Normals, Indices: mesh.Indices, EdgePoints: mesh.EdgePoints,
		Triangles: len(mesh.Indices) / 3, topo: topo}
	for i, f := range mesh.Faces {
		fi := topo.Faces[i]
		fr := FaceRange{Index: i, TriStart: f.TriStart, TriCount: f.TriCount,
			Selector: topo.FaceSelector(id, i).String(), Surface: fi.Surface, Centroid: fi.Centroid, Area: fi.Area}
		if fi.Surface == "plane" {
			n := fi.Normal
			fr.Normal = &n
		}
		m.Faces = append(m.Faces, fr)
	}
	for i, e := range mesh.Edges {
		m.Edges = append(m.Edges, EdgeLine{Index: i, PtStart: e.PtStart, PtCount: e.PtCount,
			Selector: selectors.EdgeSelector(id, i).String(), Curve: topo.Edges[i].Curve, Length: topo.Edges[i].Length})
	}
	for i, v := range topo.Vertices {
		m.Vertices = append(m.Vertices, VertexPoint{Index: i, Pos: v, Selector: selectors.VertexSelector(id, i).String()})
	}
	return m
}

// Sketch tessellates a sketch node into outline segments.
func (r *Renderer) Sketch(id string, res *rebuild.NodeResult) (*SketchLines, error) {
	if res == nil || res.Body == nil {
		return nil, fmt.Errorf("sketch %q has no geometry", id)
	}
	mesh, err := res.Body.Shape.Tessellate(deflectionFor(res.Body.Bounds()))
	if err != nil {
		return nil, err
	}
	out := &SketchLines{ID: id, Lines: []float32{}}
	for _, e := range mesh.Edges {
		for i := 0; i+1 < e.PtCount; i++ {
			a, b := (e.PtStart+i)*3, (e.PtStart+i+1)*3
			out.Lines = append(out.Lines, mesh.EdgePoints[a:a+3]...)
			out.Lines = append(out.Lines, mesh.EdgePoints[b:b+3]...)
		}
	}
	return out, nil
}

// Sketches renders every unconsumed sketch of the model.
func (r *Renderer) Sketches(m *rebuild.Model) ([]*SketchLines, error) {
	out := []*SketchLines{}
	for _, id := range m.Sketches {
		sl, err := r.Sketch(id, m.Nodes[id])
		if err != nil {
			return nil, err
		}
		out = append(out, sl)
	}
	return out, nil
}

// Scene meshes every visible body of the model (its leaves) and reports only what the
// client, which holds `known` (id → key), is missing.
func (r *Renderer) Scene(m *rebuild.Model, known map[string]string) (*Delta, error) {
	d := &Delta{Keys: map[string]string{}, Order: []string{}, Changed: []*BodyMesh{}, Removed: []string{}, Sketches: []*SketchLines{}, Paths: []*PathLines{}}
	for _, id := range m.Leaves {
		bm, hit, err := r.Body(id, m.Nodes[id])
		if err != nil {
			return nil, err
		}
		if hit {
			d.Stats.Reused++
		} else {
			d.Stats.Tessellated++
		}
		d.Order = append(d.Order, id)
		d.Keys[id] = bm.Key
		d.Stats.Triangles += bm.Triangles
		if known[id] != bm.Key {
			d.Changed = append(d.Changed, bm)
		}
	}
	for id := range known {
		if _, ok := d.Keys[id]; !ok {
			d.Removed = append(d.Removed, id)
		}
	}
	sort.Strings(d.Removed)
	var err error
	if d.Sketches, err = r.Sketches(m); err != nil {
		return nil, err
	}
	return d, nil
}

// ---- pick ID <-> selector ----------------------------------------------------------

// Kind of a pick.
const (
	PickBody   = "body"
	PickFace   = "face"
	PickEdge   = "edge"
	PickVertex = "vertex"
)

// SelectorFor maps a viewport pick (body, kind, kernel index) to its semantic selector.
func (b *BodyMesh) SelectorFor(kind string, index int) (string, error) {
	switch kind {
	case PickBody:
		return b.Selector, nil
	case PickFace:
		if index >= 0 && index < len(b.Faces) {
			return b.Faces[index].Selector, nil
		}
	case PickEdge:
		if index >= 0 && index < len(b.Edges) {
			return b.Edges[index].Selector, nil
		}
	case PickVertex:
		if index >= 0 && index < len(b.Vertices) {
			return b.Vertices[index].Selector, nil
		}
	}
	return "", fmt.Errorf("pick %s[%d] is out of range for body %s", kind, index, b.ID)
}

// FaceOfTriangle maps a triangle index to its face.
func (b *BodyMesh) FaceOfTriangle(tri int) (int, bool) {
	i := sort.Search(len(b.Faces), func(i int) bool { return b.Faces[i].TriStart+b.Faces[i].TriCount > tri })
	if i < len(b.Faces) && tri >= b.Faces[i].TriStart {
		return i, true
	}
	return 0, false
}

// Pick is a resolved selector: which body and which entity to highlight.
type Pick struct {
	Body  string `json:"body"`
	Kind  string `json:"kind"`
	Index int    `json:"index"` // -1 for a whole body
}

// PickFor is the inverse of SelectorFor, for highlighting selectors that came from the
// agent, the CLI or the conversation chips rather than from a click.
func (b *BodyMesh) PickFor(raw string) (Pick, error) {
	sel, err := selectors.Parse(raw)
	if err != nil {
		return Pick{}, err
	}
	if sel.Feature() != b.ID {
		return Pick{}, fmt.Errorf("selector %s is not on body %s", sel.String(), b.ID)
	}
	if sel.Kind == "body" {
		return Pick{Body: b.ID, Kind: PickBody, Index: -1}, nil
	}
	if sel.Kind == "edges" {
		sel.Kind = "edge"
	}
	if i := sel.IndexOf(); i >= 0 {
		if _, err := b.SelectorFor(sel.Kind, i); err != nil {
			return Pick{}, err
		}
		return Pick{Body: b.ID, Kind: sel.Kind, Index: i}, nil
	}
	d, err := selectors.ResolveTopo(sel, b.topo)
	if err != nil {
		return Pick{}, err
	}
	for i, f := range b.topo.Faces {
		if f.Centroid == d.Centroid && f.Area == d.Area && f.Surface == d.SurfaceType {
			return Pick{Body: b.ID, Kind: PickFace, Index: i}, nil
		}
	}
	return Pick{}, fmt.Errorf("selector %s resolved but matches no face of %s", sel.String(), b.ID)
}
