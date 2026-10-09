// Package slicing defines the provider-independent slicer interface. Adapters
// (prusaslicer, builtin) turn a bed-placed STL and a print profile into a normalized
// toolpath.SliceResult; nothing above this package knows which engine ran.
package slicing

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/accelone/simoncad/internal/printsim/meshprep"
	"github.com/accelone/simoncad/internal/printsim/profile"
	"github.com/accelone/simoncad/internal/printsim/toolpath"
)

// Input is the mesh to slice. The STL is already placed on the bed (see meshprep).
type Input struct {
	STLPath   string
	Placement meshprep.Placement
	Min, Max  [3]float64
	// WorkDir is a private temporary directory the adapter may use; the caller removes it.
	WorkDir string
}

// Slicer turns a mesh into toolpaths.
type Slicer interface {
	Name() string
	// Available reports why the slicer cannot run (nil when it can). The error text is
	// meant for the user and says how to fix the problem.
	Available() error
	// Slice must honour ctx: when it is cancelled the adapter stops all work and any
	// subprocess, and returns ctx.Err().
	Slice(ctx context.Context, in Input, p profile.PrintProfile) (*toolpath.SliceResult, error)
}

// ErrUnavailable marks a slicer that is not installed or not runnable.
var ErrUnavailable = errors.New("slicer unavailable")

// Provider names.
const (
	ProviderAuto        = "auto"
	ProviderPrusaSlicer = "prusaslicer"
	ProviderBuiltin     = "builtin"
)

// Registry holds the known slicers.
type Registry struct {
	byName map[string]Slicer
	order  []string
}

// NewRegistry builds a registry from slicers in order of preference.
func NewRegistry(slicers ...Slicer) *Registry {
	r := &Registry{byName: map[string]Slicer{}}
	for _, s := range slicers {
		r.byName[s.Name()] = s
		r.order = append(r.order, s.Name())
	}
	return r
}

// Status describes one provider for the UI.
type Status struct {
	Name      string `json:"name"`
	Available bool   `json:"available"`
	Problem   string `json:"problem,omitempty"`
	Preview   bool   `json:"preview"`
}

// Statuses lists every provider and whether it can run.
func (r *Registry) Statuses() []Status {
	out := make([]Status, 0, len(r.order))
	for _, n := range r.order {
		s := r.byName[n]
		st := Status{Name: n, Available: true, Preview: n == ProviderBuiltin}
		if err := s.Available(); err != nil {
			st.Available, st.Problem = false, err.Error()
		}
		out = append(out, st)
	}
	return out
}

// Pick chooses a slicer. "auto" (or "") takes the first available in registration order;
// a name must be available or the error says why. The returned diagnostic is non-nil when
// auto fell back from a missing slicer.
func (r *Registry) Pick(pref string) (Slicer, *toolpath.Diagnostic, error) {
	pref = strings.ToLower(strings.TrimSpace(pref))
	if pref != "" && pref != ProviderAuto {
		s, ok := r.byName[pref]
		if !ok {
			return nil, nil, fmt.Errorf("unknown slicer %q", pref)
		}
		if err := s.Available(); err != nil {
			return nil, nil, err
		}
		return s, nil, nil
	}
	var firstProblem error
	for _, n := range r.order {
		s := r.byName[n]
		err := s.Available()
		if err == nil {
			var d *toolpath.Diagnostic
			if firstProblem != nil {
				d = &toolpath.Diagnostic{Level: toolpath.LevelWarning, Code: "slicer_fallback",
					Message: "using the " + n + " slicer because " + firstProblem.Error()}
			}
			return s, d, nil
		}
		if firstProblem == nil {
			firstProblem = err
		}
	}
	if firstProblem == nil {
		firstProblem = fmt.Errorf("%w: no slicer is registered", ErrUnavailable)
	}
	return nil, nil, firstProblem
}
