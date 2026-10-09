// Package toolpath is the slicer-independent representation of a sliced print: layers of
// toolpath segments with movement type, extrusion, feed rate and tool. Every slicer
// adapter produces a SliceResult; the simulation only ever reads this form.
package toolpath

import (
	"fmt"
	"math"
)

// Kind classifies a segment. Slicers that do not supply a role map to Other.
type Kind uint8

const (
	Travel Kind = iota
	Perimeter
	ExternalPerimeter
	Infill
	SolidInfill
	TopBottom
	Support
	Bridge
	Skirt
	Other
	kindCount
)

var kindNames = [...]string{"travel", "perimeter", "external_perimeter", "infill", "solid_infill", "top_bottom", "support", "bridge", "skirt", "other"}

func (k Kind) String() string {
	if int(k) < len(kindNames) {
		return kindNames[k]
	}
	return "invalid"
}

// Extrudes reports whether the kind deposits material.
func (k Kind) Extrudes() bool { return k != Travel }

// Segment is one straight move. Positions are bed coordinates in mm. E is the filament
// length pushed (mm of filament, negative for a retraction). F is the feed rate in mm/s.
type Segment struct {
	X0, Y0, Z0 float32
	X1, Y1, Z1 float32
	Kind       Kind
	Tool       uint8
	E          float32
	F          float32
	// Derived by Finalize.
	T0    float32 // start time, seconds since print start
	Dur   float32 // duration, seconds
	Width float32 // estimated bead width, mm
}

// Length is the Euclidean length of the move.
func (s Segment) Length() float64 {
	dx, dy, dz := float64(s.X1-s.X0), float64(s.Y1-s.Y0), float64(s.Z1-s.Z0)
	return math.Sqrt(dx*dx + dy*dy + dz*dz)
}

// Layer indexes a run of Segments.
type Layer struct {
	Z     float32
	First int // index of the first segment
	Count int
	T0    float32 // start time of the layer
}

// Level is a diagnostic severity.
type Level string

const (
	LevelInfo    Level = "info"
	LevelWarning Level = "warning"
	LevelError   Level = "error"
)

// Diagnostic reports something the slicer or the parser noticed, including every
// command that was not interpreted.
type Diagnostic struct {
	Level   Level  `json:"level"`
	Code    string `json:"code"`
	Message string `json:"message"`
	Line    int    `json:"line,omitempty"`
	Count   int    `json:"count,omitempty"`
}

// SliceResult is the normalized output of any slicer.
type SliceResult struct {
	Provider    string // slicer adapter name, e.g. "prusaslicer" or "builtin-preview"
	Version     string
	Preview     bool // true when produced by the internal preview slicer
	Layers      []Layer
	Segments    []Segment
	Diagnostics []Diagnostic

	// Derived by Finalize.
	Duration   float64 // seconds, recomputed from length / feed rate
	FilamentMM float64 // net filament length consumed
	FilamentG  float64
	Supports   bool // any support segment present
	BBox       [6]float32

	// SlicerEstimate is the duration the slicer itself reported, when it did (seconds).
	SlicerEstimate float64
}

// FinalizeParams are the machine values needed to derive widths and mass.
type FinalizeParams struct {
	FilamentDiameter float64
	FilamentDensity  float64 // g/cm³
	LayerHeight      float64
	Nozzle           float64
	DefaultFeed      float64 // mm/s used when a segment has no feed rate
}

// Finalize computes per-segment times and widths, the layer start times, the totals and
// the bounding box. It is deterministic: the same segments always give the same times.
func (r *SliceResult) Finalize(p FinalizeParams) {
	if p.DefaultFeed <= 0 {
		p.DefaultFeed = 60
	}
	area := math.Pi * p.FilamentDiameter * p.FilamentDiameter / 4 // mm²
	var t, fil float64
	r.Supports = false
	for i := range r.Segments {
		s := &r.Segments[i]
		l := s.Length()
		f := float64(s.F)
		if f <= 0 {
			f = p.DefaultFeed
		}
		s.T0 = float32(t)
		d := l / f
		if l < 1e-9 { // an E-only move (retract, unretract, prime) takes |E| / F
			d = math.Abs(float64(s.E)) / f
		}
		s.Dur = float32(d)
		t += d
		fil += float64(s.E)
		s.Width = 0
		if s.Kind.Extrudes() && s.E > 0 && l > 1e-6 && p.LayerHeight > 0 {
			w := float64(s.E) * area / (l * p.LayerHeight)
			if hi := p.Nozzle * 2.5; p.Nozzle > 0 && w > hi {
				w = hi
			}
			if w < 0.05 {
				w = 0.05
			}
			s.Width = float32(w)
		}
		if s.Kind == Support {
			r.Supports = true
		}
	}
	for i := range r.Layers {
		l := &r.Layers[i]
		if l.Count > 0 && l.First < len(r.Segments) {
			l.T0 = r.Segments[l.First].T0
		}
	}
	r.Duration = t
	r.FilamentMM = fil
	r.FilamentG = fil * area / 1000 * p.FilamentDensity // mm³ → cm³ → g
	r.BBox = bounds(r.Segments)
}

func bounds(segs []Segment) [6]float32 {
	if len(segs) == 0 {
		return [6]float32{}
	}
	b := [6]float32{math.MaxFloat32, math.MaxFloat32, math.MaxFloat32, -math.MaxFloat32, -math.MaxFloat32, -math.MaxFloat32}
	for _, s := range segs {
		if !s.Kind.Extrudes() {
			continue
		}
		for _, p := range [2][3]float32{{s.X0, s.Y0, s.Z0}, {s.X1, s.Y1, s.Z1}} {
			for a := 0; a < 3; a++ {
				if p[a] < b[a] {
					b[a] = p[a]
				}
				if p[a] > b[a+3] {
					b[a+3] = p[a]
				}
			}
		}
	}
	if b[0] > b[3] {
		return [6]float32{}
	}
	return b
}

// Validate checks the layer index is consistent with the segments.
func (r *SliceResult) Validate() error {
	next := 0
	for i, l := range r.Layers {
		if l.First != next || l.Count < 0 || l.First+l.Count > len(r.Segments) {
			return fmt.Errorf("layer %d indexes segments [%d,+%d) out of order or out of range (have %d)", i, l.First, l.Count, len(r.Segments))
		}
		next = l.First + l.Count
	}
	if len(r.Layers) > 0 && next != len(r.Segments) {
		return fmt.Errorf("layers cover %d of %d segments", next, len(r.Segments))
	}
	return nil
}

// SegmentAt returns the index of the segment running at time t (clamped). Segments are
// ordered by T0, so this is a binary search; it is what makes seeking O(log n).
func (r *SliceResult) SegmentAt(t float64) int {
	n := len(r.Segments)
	if n == 0 {
		return -1
	}
	lo, hi := 0, n-1
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if float64(r.Segments[mid].T0) <= t {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	return lo
}

// LayerAt returns the layer index containing segment i.
func (r *SliceResult) LayerAt(i int) int {
	lo, hi := 0, len(r.Layers)-1
	if hi < 0 {
		return -1
	}
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if r.Layers[mid].First <= i {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	return lo
}
