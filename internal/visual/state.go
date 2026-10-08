package visual

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// Calibration is the camera that approximates how a reference image was taken.
type Calibration struct {
	View       string  `json:"view,omitempty"` // left | right | front | rear | top | front_3q | rear_3q | other
	Azimuth    float64 `json:"azimuth"`
	Elevation  float64 `json:"elevation"`
	Projection string  `json:"projection"`
	FocalMM    float64 `json:"focal_length_mm,omitempty"` // 35 mm equivalent, perspective only
	Scale      float64 `json:"scale"`
	Mirrored   bool    `json:"mirrored,omitempty"` // the object faces the opposite way
	Manual     bool    `json:"manual,omitempty"`   // set by the user, never overwritten automatically
}

// Camera turns a calibration into a render camera.
func (c Calibration) Camera() Camera {
	cam := Camera{Name: "ref_" + c.View, Azimuth: c.Azimuth, Elevation: c.Elevation, Projection: c.Projection, Scale: 1}
	if cam.Projection == "" {
		cam.Projection = Orthographic
	}
	if cam.Projection == Perspective {
		cam.FOV = 30
		if c.FocalMM > 0 { // vertical FOV of a 24 mm-high frame
			cam.FOV = 2 * atanDeg(12/c.FocalMM)
		}
	}
	return cam
}

// SilhouetteMetric is the metric a reference view feeds (empty for perspective views).
func (c Calibration) SilhouetteMetric() string {
	if c.Projection == Perspective {
		return ""
	}
	switch c.View {
	case "left", "right":
		return MetricSide
	case "front", "rear":
		return MetricFront
	case "top":
		return MetricTop
	}
	return ""
}

// Reference is one reference image and what was learned from it. References stay active
// constraints for the whole modeling process.
type Reference struct {
	ImageID     string             `json:"image_id"`
	Width       int                `json:"width"`
	Height      int                `json:"height"`
	ObjectClass string             `json:"object_class,omitempty"`
	Calibration *Calibration       `json:"calibration,omitempty"`
	Landmarks   Landmarks          `json:"landmarks,omitempty"`
	Dimensions  map[string]float64 `json:"known_dimensions_mm,omitempty"`
	Notes       string             `json:"notes,omitempty"`
	Analyzed    bool               `json:"analyzed"`
	MaskError   string             `json:"mask_error,omitempty"`

	mask *Mask
}

// Mask is the segmented silhouette (nil until computed or when segmentation failed).
func (r *Reference) Mask() *Mask { return r.mask }

// SetMask stores the segmented silhouette.
func (r *Reference) SetMask(m *Mask) { r.mask = m }

// Region maps a visual region to the CAD objects and parameters that shape it, so a critic's
// "the roof is too high" becomes an actionable edit. Locked regions must not be modified.
type Region struct {
	Name       string   `json:"name"`
	Objects    []string `json:"objects,omitempty"`
	Parameters []string `json:"parameters,omitempty"` // "set.param" or bare parameter names
	Locked     bool     `json:"locked,omitempty"`
}

// Render is one offscreen render kept for the agent and the UI.
type Render struct {
	ID        string `json:"id"`
	View      string `json:"view"`
	Mode      string `json:"mode"`
	Camera    Camera `json:"camera"`
	Width     int    `json:"width"`
	Height    int    `json:"height"`
	Iteration int    `json:"iteration,omitempty"`
	PNG       []byte `json:"-"`
	Mask      *Mask  `json:"-"`
}

// Correction is one change chosen from the critic's output.
type Correction struct {
	Region    string  `json:"region"`
	Problem   string  `json:"problem"`
	Severity  float64 `json:"severity"`
	Action    string  `json:"action,omitempty"`
	Axis      string  `json:"axis,omitempty"`
	DeltaMM   float64 `json:"delta_mm,omitempty"`
	Parameter string  `json:"parameter,omitempty"`
	Value     float64 `json:"value,omitempty"`
}

// Key identifies a correction for "do not repeat a failed one".
func (c Correction) Key() string {
	return strings.ToLower(strings.TrimSpace(c.Region)) + "|" + strings.ToLower(strings.TrimSpace(c.Problem))
}

// Iteration is one observe → measure → modify step.
type Iteration struct {
	N                int                        `json:"iteration"`
	Stage            string                     `json:"stage"`
	Kind             string                     `json:"kind"` // build | refine | baseline
	Renders          map[string]string          `json:"renders"`
	Silhouettes      map[string]SilhouetteScore `json:"silhouettes,omitempty"`
	Scores           map[string]float64         `json:"scores"`
	Gate             Gate                       `json:"gate"`
	Critique         json.RawMessage            `json:"critique,omitempty"`
	Changes          []Correction               `json:"changes,omitempty"`
	ParametersBefore map[string]float64         `json:"parameters_before,omitempty"`
	ParametersAfter  map[string]float64         `json:"parameters_after,omitempty"`
	ChangeSets       []string                   `json:"changesets,omitempty"`
	Checkpoint       string                     `json:"checkpoint,omitempty"`
	Reverted         bool                       `json:"reverted,omitempty"`
	Accepted         bool                       `json:"accepted,omitempty"`
	Note             string                     `json:"note,omitempty"`
}

// State is the visual-refinement state of one project. It is not CAD state: it never
// changes geometry, and it is saved with the agent history.
type State struct {
	mu                sync.Mutex
	Goal              string             `json:"goal,omitempty"`
	References        []*Reference       `json:"references"`
	ReferenceGeometry map[string]float64 `json:"reference_geometry,omitempty"`
	Regions           []*Region          `json:"regions"`
	Stage             int                `json:"stage"`
	Frame             *Frame             `json:"frame,omitempty"`
	History           []*Iteration       `json:"history"`
	Failed            []Correction       `json:"failed_corrections,omitempty"`

	renders     map[string]*Render
	renderOrder []string
	renderSeq   int
}

func NewState() *State {
	return &State{renders: map[string]*Render{}, References: []*Reference{}, Regions: []*Region{}, History: []*Iteration{}}
}

// Lock / Unlock guard the state for multi-step callers.
func (s *State) Lock()   { s.mu.Lock() }
func (s *State) Unlock() { s.mu.Unlock() }

// UnmarshalJSON keeps the in-memory stores initialised.
func (s *State) UnmarshalJSON(b []byte) error {
	type plain State
	var p plain
	if err := json.Unmarshal(b, &p); err != nil {
		return err
	}
	s.Goal, s.References, s.ReferenceGeometry, s.Regions = p.Goal, p.References, p.ReferenceGeometry, p.Regions
	s.Stage, s.Frame, s.History, s.Failed = p.Stage, p.Frame, p.History, p.Failed
	if s.renders == nil {
		s.renders = map[string]*Render{}
	}
	if s.References == nil {
		s.References = []*Reference{}
	}
	if s.Regions == nil {
		s.Regions = []*Region{}
	}
	if s.History == nil {
		s.History = []*Iteration{}
	}
	return nil
}

// Empty reports whether there is nothing worth saving.
func (s *State) Empty() bool {
	return len(s.References) == 0 && len(s.Regions) == 0 && len(s.History) == 0
}

// ---- references -----------------------------------------------------------------------

// AddReference registers an uploaded image as a reference (idempotent).
func (s *State) AddReference(imageID string, w, h int) *Reference {
	if r := s.Reference(imageID); r != nil {
		return r
	}
	r := &Reference{ImageID: imageID, Width: w, Height: h}
	s.References = append(s.References, r)
	return r
}

// Reference finds a reference by image id.
func (s *State) Reference(imageID string) *Reference {
	for _, r := range s.References {
		if r.ImageID == imageID {
			return r
		}
	}
	return nil
}

// RemoveReference forgets a reference.
func (s *State) RemoveReference(imageID string) {
	kept := s.References[:0]
	for _, r := range s.References {
		if r.ImageID != imageID {
			kept = append(kept, r)
		}
	}
	s.References = kept
}

// ---- regions --------------------------------------------------------------------------

// DefineRegion creates or replaces a region's ownership (the lock flag is kept).
func (s *State) DefineRegion(name string, objects, params []string) (*Region, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("region name is required")
	}
	r := s.Region(name)
	if r == nil {
		r = &Region{Name: name}
		s.Regions = append(s.Regions, r)
	}
	r.Objects, r.Parameters = uniq(objects), uniq(params)
	return r, nil
}

// Region finds a region by name (case-insensitive).
func (s *State) Region(name string) *Region {
	for _, r := range s.Regions {
		if strings.EqualFold(r.Name, strings.TrimSpace(name)) {
			return r
		}
	}
	return nil
}

// SetLocked locks or unlocks a region.
func (s *State) SetLocked(name string, locked bool) (*Region, error) {
	r := s.Region(name)
	if r == nil {
		return nil, fmt.Errorf("unknown region %q (define it first; known: %s)", name, strings.Join(s.RegionNames(), ", "))
	}
	r.Locked = locked
	return r, nil
}

// RegionNames lists region names in definition order.
func (s *State) RegionNames() []string {
	out := make([]string, len(s.Regions))
	for i, r := range s.Regions {
		out[i] = r.Name
	}
	return out
}

// LockedObject returns the locked region owning a feature ("" if none).
func (s *State) LockedObject(id string) string {
	for _, r := range s.Regions {
		if r.Locked && contains(r.Objects, id) {
			return r.Name
		}
	}
	return ""
}

// LockedParameter returns the locked region owning a parameter of a parameter set.
func (s *State) LockedParameter(set, name string) string {
	for _, r := range s.Regions {
		if r.Locked && (contains(r.Parameters, set+"."+name) || contains(r.Parameters, name)) {
			return r.Name
		}
	}
	return ""
}

// ---- renders --------------------------------------------------------------------------

// maxRenders bounds the in-memory render store.
const maxRenders = 400

// PutRender stores a render and assigns its id.
func (s *State) PutRender(r *Render) *Render {
	if s.renders == nil {
		s.renders = map[string]*Render{}
	}
	s.renderSeq++
	r.ID = fmt.Sprintf("rnd_%04d", s.renderSeq)
	s.renders[r.ID] = r
	s.renderOrder = append(s.renderOrder, r.ID)
	for len(s.renderOrder) > maxRenders {
		delete(s.renders, s.renderOrder[0])
		s.renderOrder = s.renderOrder[1:]
	}
	return r
}

// Render returns a stored render.
func (s *State) Render(id string) (*Render, bool) {
	r, ok := s.renders[id]
	return r, ok
}

// ---- history --------------------------------------------------------------------------

// AddIteration appends an iteration and numbers it.
func (s *State) AddIteration(it *Iteration) *Iteration {
	it.N = len(s.History) + 1
	s.History = append(s.History, it)
	return it
}

// Iteration returns iteration n (1-based).
func (s *State) Iteration(n int) (*Iteration, bool) {
	if n < 1 || n > len(s.History) {
		return nil, false
	}
	return s.History[n-1], true
}

// Last returns the latest iteration that was not reverted.
func (s *State) Last() *Iteration {
	for i := len(s.History) - 1; i >= 0; i-- {
		if !s.History[i].Reverted {
			return s.History[i]
		}
	}
	return nil
}

// RecordFailed remembers a correction that made the model worse.
func (s *State) RecordFailed(c Correction) {
	for _, f := range s.Failed {
		if f.Key() == c.Key() {
			return
		}
	}
	s.Failed = append(s.Failed, c)
}

// HasFailed reports whether an equivalent correction already failed.
func (s *State) HasFailed(c Correction) bool {
	for _, f := range s.Failed {
		if f.Key() == c.Key() {
			return true
		}
	}
	return false
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func uniq(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, v := range in {
		v = strings.TrimSpace(v)
		if v != "" && !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	sort.Strings(out)
	return out
}

func jsonMarshal(v any) ([]byte, error) { return json.Marshal(v) }
