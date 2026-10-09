package ui

import (
	"fmt"
	"os"

	"github.com/accelone/simoncad/internal/printsim/job"
	"github.com/accelone/simoncad/internal/printsim/profile"
	"github.com/accelone/simoncad/internal/printsim/slicing"
	"github.com/accelone/simoncad/internal/printsim/slicing/builtin"
	"github.com/accelone/simoncad/internal/printsim/slicing/prusaslicer"
)

// PrusaSlicerEnv names the environment variable that points to a PrusaSlicer binary. It is
// read once at startup; the slicer path is deliberately not accepted over HTTP, so a page
// can never choose which program the server runs.
const PrusaSlicerEnv = "SIMONCAD_PRUSASLICER"

// sim returns the print-simulation job manager, created on first use. Callers hold s.mu.
func (s *Service) sim() *job.Manager {
	if s.printsim == nil {
		reg := slicing.NewRegistry(&prusaslicer.Slicer{Path: os.Getenv(PrusaSlicerEnv)}, builtin.Slicer{})
		m := job.NewManager(reg)
		m.OnEvent = func(ev job.Event) { s.hub.Publish(Notice{Kind: "printsim", PrintSim: &ev}) }
		s.printsim = m
	}
	return s.printsim
}

// stopPrintSim cancels any slicing and waits for it to release the kernel shapes it holds.
// It must run before the project's kernel is closed. Callers hold s.mu.
func (s *Service) stopPrintSim() {
	if s.printsim == nil {
		return
	}
	s.printsim.Cancel()
	s.printsim.Wait()
	s.printsim.Invalidate()
}

// PrintSimInfo is what the print-simulation panel needs before slicing.
type PrintSimInfo struct {
	Profile   profile.PrintProfile `json:"profile"`
	Providers []slicing.Status     `json:"providers"`
	// GeomHash identifies the model as it is now; a result whose geom_hash differs is stale.
	GeomHash string    `json:"geom_hash"`
	State    job.Event `json:"state"`
	HasModel bool      `json:"has_model"`
}

// PrintSimInfo reports the default profile, the available slicers and the model revision.
func (s *Service) PrintSimInfo() (*PrintSimInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.sim()
	info := &PrintSimInfo{Profile: profile.GenericFDM(), Providers: m.Registry.Statuses(), State: m.State(),
		GeomHash: s.app.Snapshot().GeometryHash()}
	if mod, err := s.app.Model(); err == nil {
		info.HasModel = len(mod.Leaves) > 0
	}
	return info, nil
}

// PrintSimSliceRequest starts a slicing job.
type PrintSimSliceRequest struct {
	// Profile overrides the default simulation profile; nil uses it unchanged.
	Profile *profile.PrintProfile `json:"profile"`
	// Provider is "auto" (default), "prusaslicer" or "builtin".
	Provider string `json:"provider"`
}

// PrintSimSlice exports the current model and slices it in the background. It reads the
// model and never changes it; progress arrives as "printsim" notices. Starting a job
// cancels the previous one.
func (s *Service) PrintSimSlice(req PrintSimSliceRequest) (job.Started, error) {
	p := profile.GenericFDM()
	if req.Profile != nil {
		p = *req.Profile
		if p.Label == "" {
			p.Label = profile.SimulationLabel
		}
	}
	if err := p.Validate(); err != nil {
		return job.Started{}, fmt.Errorf("invalid print profile: %w", err)
	}
	// s.mu is held until the job exists: a project switch (which cancels jobs and then closes the
	// kernel) must not slip in between taking the shape and handing it to a job that would release it.
	// Start does not block: it launches a goroutine, and a cache hit only publishes a notice.
	s.mu.Lock()
	defer s.mu.Unlock()
	shape, hash, err := s.app.PrintShape()
	if err != nil {
		return job.Started{}, fmt.Errorf("cannot simulate printing: %w", err)
	}
	// The job owns the shape from here on and releases it as soon as it has exported it.
	return s.sim().Start(job.Request{GeomHash: hash, Source: shape, Release: shape.Release, Profile: p, Provider: req.Provider})
}

// PrintSimCancel stops the running slicing job.
func (s *Service) PrintSimCancel() {
	s.mu.Lock()
	jm := s.sim()
	s.mu.Unlock()
	jm.Cancel()
}

// PrintSimResult returns the compact binary toolpath of a finished job.
func (s *Service) PrintSimResult(id string) ([]byte, error) {
	s.mu.Lock()
	jm := s.sim()
	s.mu.Unlock()
	r := jm.Result(id)
	if r == nil {
		return nil, fmt.Errorf("no result for job %q (it failed, was cancelled or has been replaced by a newer one)", id)
	}
	return r.Binary()
}
