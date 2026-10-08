// Package vision turns an image interpretation into editable CAD-IR sketch parameters.
//
// It is pure Go: no kernel, no LLM. The model only ever produces a Proposal (structured
// JSON in pixel space); everything that follows — validation, scale, conversion to
// millimetres — is deterministic code. A Proposal never mutates CAD state.
package vision

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
)

// ConfidenceThreshold is the minimum confidence for a detected dimension to be used without confirmation.
const ConfidenceThreshold = 0.75

// Entity is one detected primitive in image pixels (y grows downwards). It uses the same
// vocabulary as the CAD-IR sketch entity: line{p1,p2} arc{p1,mid,p2} circle{cx,cy,r}
// rectangle{x,y,w,h} polygon{points} slot{x1,y1,x2,y2,width}, plus id, kind and confidence.
type Entity map[string]any

func (e Entity) ID() string   { s, _ := e["id"].(string); return s }
func (e Entity) Kind() string { s, _ := e["kind"].(string); return s }
func (e Entity) Confidence() float64 {
	f, _ := e["confidence"].(float64)
	return f
}

func (e Entity) num(k string) (float64, bool) {
	f, ok := e[k].(float64)
	return f, ok && !math.IsNaN(f) && !math.IsInf(f, 0)
}

func (e Entity) pt(k string) ([2]float64, bool) {
	a, ok := e[k].([]any)
	if !ok || len(a) != 2 {
		return [2]float64{}, false
	}
	x, ok1 := a[0].(float64)
	y, ok2 := a[1].(float64)
	return [2]float64{x, y}, ok1 && ok2
}

// Profile is a closed region: an outer loop of entity ids and inner loops (holes, cutouts).
type Profile struct {
	ID    string     `json:"id"`
	Outer []string   `json:"outer"`
	Holes [][]string `json:"holes,omitempty"`
}

// Relationship is a detected geometric relation between entities.
type Relationship struct {
	Kind       string   `json:"kind"` // concentric | equal | horizontal | vertical | symmetric | parallel | perpendicular
	Entities   []string `json:"entities"`
	Confidence float64  `json:"confidence"`
}

// Dimension is a measurement of the drawing. Value is in millimetres; a dimension with no
// Value is a question for the user.
type Dimension struct {
	ID         string    `json:"id"`
	Kind       string    `json:"kind"` // length | diameter | radius | depth | distance
	Binds      string    `json:"binds,omitempty"`
	Value      *float64  `json:"value,omitempty"`
	Source     string    `json:"source"` // handwritten | printed | inferred | missing | confirmed | user
	Confidence float64   `json:"confidence"`
	BBox       []float64 `json:"bbox,omitempty"` // [x,y,w,h] of the annotation in image pixels
	MeasuredPx *float64  `json:"measured_px,omitempty"`
	Label      string    `json:"label,omitempty"` // e.g. "overall width"; shown when asking
}

// Operation composes profiles into solids, in order. Operation ids name the resulting solids.
//
//	extrude   profile (or "" for every profile), depth_dim
//	cut       profile, target, depth_dim | through   — remove the extruded profile from target
//	union | subtract | intersect   a (kept), b (merged / removed / intersected) — earlier operation ids
//
// target / a / b are an earlier operation id, "feature:<id>" (an existing body) or "selection".
type Operation struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"`
	Profile  string `json:"profile,omitempty"`
	DepthDim string `json:"depth_dim,omitempty"`
	Through  bool   `json:"through,omitempty"`
	Target   string `json:"target,omitempty"`
	A        string `json:"a,omitempty"`
	B        string `json:"b,omitempty"`
}

// Placement says where the sketch goes.
type Placement struct {
	Plane  string  `json:"plane,omitempty"` // XY | XZ | YZ
	Offset float64 `json:"offset,omitempty"`
	Face   string  `json:"face,omitempty"` // face selector; "selection" means the selected planar face
}

// Proposal is the structured interpretation of one image.
type Proposal struct {
	ImageID       string         `json:"image_id"`
	ImageSize     [2]float64     `json:"image_size"` // [width, height] px
	Entities      []Entity       `json:"entities"`
	Profiles      []Profile      `json:"profiles"`
	Relationships []Relationship `json:"relationships,omitempty"`
	Dimensions    []Dimension    `json:"dimensions,omitempty"`
	Operations    []Operation    `json:"operations,omitempty"`
	Placement     Placement      `json:"placement,omitempty"`
	Confidence    float64        `json:"confidence"`
	// Description is a short human-readable gloss for the UI. It is never geometry.
	Description string `json:"description,omitempty"`
}

var entityFields = map[string][]string{
	"line":      {"p1", "p2"},
	"arc":       {"p1", "mid", "p2"},
	"circle":    {"cx", "cy", "r"},
	"rectangle": {"x", "y", "w", "h"},
	"polygon":   {"points"},
	"slot":      {"x1", "y1", "x2", "y2", "width"},
}

var (
	dimKinds     = map[string]bool{"length": true, "diameter": true, "radius": true, "depth": true, "distance": true}
	dimSources   = map[string]bool{"handwritten": true, "printed": true, "inferred": true, "missing": true, "confirmed": true, "user": true}
	opKinds      = map[string]bool{"extrude": true, "cut": true, "union": true, "subtract": true, "intersect": true}
	relKinds     = map[string]bool{"concentric": true, "equal": true, "horizontal": true, "vertical": true, "symmetric": true, "parallel": true, "perpendicular": true}
	idPattern    = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,39}$`)
	bindPattern  = regexp.MustCompile(`^([A-Za-z][A-Za-z0-9_]*)\.([a-z]+)$`)
	planeAllowed = map[string]bool{"": true, "XY": true, "XZ": true, "YZ": true}
)

// bindFields lists the entity fields a dimension may bind to, and whether the value
// drives the geometry (true) or only calibrates the scale (false).
var bindFields = map[string]map[string]bool{
	"rectangle": {"w": true, "h": true},
	"circle":    {"radius": true, "diameter": true},
	"slot":      {"width": true, "length": false},
	"line":      {"length": false},
}

// Clone deep-copies a proposal.
func (p *Proposal) Clone() *Proposal {
	b, _ := json.Marshal(p)
	var out Proposal
	_ = json.Unmarshal(b, &out)
	return &out
}

// Decode parses and normalises a proposal from decoded JSON (a tool input).
func Decode(raw any) (*Proposal, error) {
	b, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	var p Proposal
	if err := json.Unmarshal(b, &p); err != nil {
		return nil, fmt.Errorf("proposal: %w", err)
	}
	return &p, nil
}

func (p *Proposal) entity(id string) Entity {
	for _, e := range p.Entities {
		if e.ID() == id {
			return e
		}
	}
	return nil
}

func (p *Proposal) profile(id string) *Profile {
	for i := range p.Profiles {
		if p.Profiles[i].ID == id {
			return &p.Profiles[i]
		}
	}
	return nil
}

func (p *Proposal) dimension(id string) *Dimension {
	for i := range p.Dimensions {
		if p.Dimensions[i].ID == id {
			return &p.Dimensions[i]
		}
	}
	return nil
}

func (p *Proposal) operation(id string) *Operation {
	for i := range p.Operations {
		if p.Operations[i].ID == id {
			return &p.Operations[i]
		}
	}
	return nil
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
