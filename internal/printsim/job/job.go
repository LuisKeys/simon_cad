// Package job runs slicing jobs off the UI thread. A job exports the model, slices it with
// the chosen provider and normalizes the result. The manager guarantees that
//   - starting a job cancels the previous one,
//   - a superseded (stale) job can never replace a newer result or announce itself,
//   - results are cached by geometry revision, print settings and provider,
//   - every job works in a private temporary directory that is always removed.
package job

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"

	"github.com/accelone/simoncad/internal/printsim/meshprep"
	"github.com/accelone/simoncad/internal/printsim/profile"
	"github.com/accelone/simoncad/internal/printsim/slicing"
	"github.com/accelone/simoncad/internal/printsim/toolpath"
)

// State is a job's lifecycle stage.
type State string

const (
	StateQueued    State = "queued"
	StatePreparing State = "preparing" // meshing and exporting the model
	StateSlicing   State = "slicing"
	StateParsing   State = "parsing"
	StateDone      State = "done"
	StateError     State = "error"
	StateCancelled State = "cancelled"
)

// Event reports progress. Only events of the current job are delivered.
type Event struct {
	Job      string   `json:"job"`
	State    State    `json:"state"`
	Message  string   `json:"message,omitempty"`
	Provider string   `json:"provider,omitempty"`
	Error    string   `json:"error,omitempty"`
	Cached   bool     `json:"cached,omitempty"`
	Summary  *Summary `json:"summary,omitempty"` // with StateDone
}

// Request describes what to slice.
type Request struct {
	// GeomHash identifies the geometry revision (cadir.Graph.GeometryHash).
	GeomHash string
	// Source exports the model. It must stay valid until Release is called.
	Source meshprep.Exporter
	// Release frees Source; it is called exactly once, on every path.
	Release func()
	Profile profile.PrintProfile
	// Provider is "auto", "prusaslicer" or "builtin".
	Provider string
}

// Result is a finished slice.
type Result struct {
	GeomHash    string
	ProfileHash string
	Profile     profile.PrintProfile
	Slice       *toolpath.SliceResult
	Placement   meshprep.Placement
	Min, Max    [3]float64

	once sync.Once
	bin  []byte
	err  error
}

// Binary is the compact wire form for the front end, encoded once.
func (r *Result) Binary() ([]byte, error) {
	r.once.Do(func() { r.bin, r.err = toolpath.Encode(r.Slice) })
	return r.bin, r.err
}

// Summary is the JSON the UI shows next to the toolpath.
type Summary struct {
	Job            string                `json:"job"`
	GeomHash       string                `json:"geom_hash"`
	Provider       string                `json:"provider"`
	Version        string                `json:"version,omitempty"`
	Preview        bool                  `json:"preview"`
	ProfileLabel   string                `json:"profile_label"`
	LayerHeight    float64               `json:"layer_height"`
	Build          [3]float64            `json:"build"`
	Layers         int                   `json:"layers"`
	Segments       int                   `json:"segments"`
	DurationS      float64               `json:"duration_s"`
	SlicerEstimate float64               `json:"slicer_estimate_s,omitempty"`
	FilamentMM     float64               `json:"filament_mm"`
	FilamentG      float64               `json:"filament_g"`
	Supports       bool                  `json:"supports"`
	BBox           [6]float32            `json:"bbox"`
	Placement      meshprep.Placement    `json:"placement"`
	Size           [3]float64            `json:"size"`
	Diagnostics    []toolpath.Diagnostic `json:"diagnostics"`
	Disclaimer     string                `json:"disclaimer"`
}

// Disclaimer is shown with every result: simulation is not manufacturing validation.
const Disclaimer = "Simulation of the slicer's toolpaths. It does not predict adhesion, warping, thermal behaviour or hardware faults, and is not a guarantee that the part will print."

func (r *Result) Summary(job string) *Summary {
	s := r.Slice
	return &Summary{
		Job: job, GeomHash: r.GeomHash, Provider: s.Provider, Version: s.Version, Preview: s.Preview,
		ProfileLabel: r.Profile.Label, LayerHeight: r.Profile.LayerHeight, Build: [3]float64{r.Profile.BuildX, r.Profile.BuildY, r.Profile.BuildZ}, Layers: len(s.Layers), Segments: len(s.Segments), DurationS: s.Duration,
		SlicerEstimate: s.SlicerEstimate, FilamentMM: s.FilamentMM, FilamentG: s.FilamentG, Supports: s.Supports,
		BBox: s.BBox, Placement: r.Placement, Size: [3]float64{r.Max[0] - r.Min[0], r.Max[1] - r.Min[1], r.Max[2] - r.Min[2]},
		Diagnostics: s.Diagnostics, Disclaimer: Disclaimer,
	}
}

const cacheSize = 4

type run struct {
	id     string
	gen    uint64
	cancel context.CancelFunc
}

// Manager owns the job lifecycle.
type Manager struct {
	Registry *slicing.Registry
	// TempDir is the parent of per-job directories ("" for the system default).
	TempDir string
	// OnEvent receives events of the current job. It is called without locks held.
	OnEvent func(Event)

	mu      sync.Mutex
	gen     uint64
	seq     int
	cur     *run
	cache   []*Result // most recent first
	last    *Result   // newest finished result of the current generation
	lastJob string
	state   Event
	wg      sync.WaitGroup
}

// NewManager creates a manager over the registry.
func NewManager(reg *slicing.Registry) *Manager { return &Manager{Registry: reg} }

func cacheKey(geom, prof, provider string) string { return geom + "|" + prof + "|" + provider }

func (m *Manager) lookup(key string) *Result {
	for i, r := range m.cache {
		if cacheKey(r.GeomHash, r.ProfileHash, r.Slice.Provider) == key {
			m.cache = append([]*Result{r}, append(m.cache[:i:i], m.cache[i+1:]...)...)
			return r
		}
	}
	return nil
}

func (m *Manager) store(r *Result) {
	key := cacheKey(r.GeomHash, r.ProfileHash, r.Slice.Provider)
	for i, c := range m.cache {
		if cacheKey(c.GeomHash, c.ProfileHash, c.Slice.Provider) == key {
			m.cache = append(m.cache[:i], m.cache[i+1:]...)
			break
		}
	}
	m.cache = append([]*Result{r}, m.cache...)
	if len(m.cache) > cacheSize {
		m.cache = m.cache[:cacheSize]
	}
}

func (m *Manager) emit(ev Event) {
	m.mu.Lock()
	m.state = ev
	cb := m.OnEvent
	m.mu.Unlock()
	if cb != nil {
		cb(ev)
	}
}

// emitIfCurrent delivers ev only while the run is still the newest.
func (m *Manager) emitIfCurrent(r *run, ev Event) {
	m.mu.Lock()
	if m.cur != r {
		m.mu.Unlock()
		return
	}
	m.state = ev
	cb := m.OnEvent
	m.mu.Unlock()
	if cb != nil {
		cb(ev)
	}
}

// Started identifies the job Start created.
type Started struct {
	Job    string
	Cached bool
}

// Start begins slicing, cancelling whatever was running. A missing slicer or an invalid
// profile is reported here, synchronously, with an actionable message; everything else
// arrives as events. On a cache hit no work is done and a done event is emitted.
func (m *Manager) Start(req Request) (Started, error) {
	release := func() {
		if req.Release != nil {
			req.Release()
		}
	}
	if err := req.Profile.Validate(); err != nil {
		release()
		return Started{}, fmt.Errorf("invalid print profile: %w", err)
	}
	sl, fallback, err := m.Registry.Pick(req.Provider)
	if err != nil {
		release()
		return Started{}, err
	}
	ph := req.Profile.Hash()
	key := cacheKey(req.GeomHash, ph, sl.Name())

	m.mu.Lock()
	if m.cur != nil {
		m.cur.cancel()
	}
	m.gen++
	m.seq++
	r := &run{id: fmt.Sprintf("ps_%04d", m.seq), gen: m.gen, cancel: func() {}}
	m.cur = r
	if hit := m.lookup(key); hit != nil {
		m.last, m.lastJob = hit, r.id
		m.mu.Unlock()
		release()
		m.emit(Event{Job: r.id, State: StateDone, Provider: sl.Name(), Cached: true, Message: "Loaded from cache", Summary: hit.Summary(r.id)})
		return Started{Job: r.id, Cached: true}, nil
	}
	m.last, m.lastJob = nil, ""
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	m.mu.Unlock()

	m.emit(Event{Job: r.id, State: StateQueued, Provider: sl.Name()})
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		defer cancel()
		m.execute(ctx, r, req, sl, fallback, ph)
	}()
	return Started{Job: r.id}, nil
}

func (m *Manager) execute(ctx context.Context, r *run, req Request, sl slicing.Slicer, fallback *toolpath.Diagnostic, ph string) {
	defer func() {
		if req.Release != nil {
			req.Release()
		}
	}()
	fail := func(err error) {
		if ctx.Err() != nil || errors.Is(err, context.Canceled) {
			m.emitIfCurrent(r, Event{Job: r.id, State: StateCancelled, Provider: sl.Name(), Message: "Cancelled"})
			return
		}
		m.emitIfCurrent(r, Event{Job: r.id, State: StateError, Provider: sl.Name(), Error: err.Error()})
	}
	dir, err := os.MkdirTemp(m.TempDir, "simoncad-printsim-*")
	if err != nil {
		fail(fmt.Errorf("cannot create a temporary directory: %w", err))
		return
	}
	defer os.RemoveAll(dir)

	m.emitIfCurrent(r, Event{Job: r.id, State: StatePreparing, Provider: sl.Name(), Message: "Preparing the model"})
	prep, err := meshprep.Prepare(ctx, req.Source, req.Profile, dir)
	// the kernel shape is no longer needed once exported
	if req.Release != nil {
		req.Release()
		req.Release = nil
	}
	if err != nil {
		fail(err)
		return
	}
	m.emitIfCurrent(r, Event{Job: r.id, State: StateSlicing, Provider: sl.Name(), Message: "Slicing with " + sl.Name()})
	res, err := sl.Slice(ctx, slicing.Input{STLPath: prep.STLPath, Placement: prep.Placement, Min: prep.Min, Max: prep.Max, WorkDir: dir}, req.Profile)
	if err != nil {
		fail(err)
		return
	}
	m.emitIfCurrent(r, Event{Job: r.id, State: StateParsing, Provider: sl.Name(), Message: "Preparing the simulation"})
	res.Finalize(toolpath.FinalizeParams{
		FilamentDiameter: req.Profile.FilamentDiameter, FilamentDensity: req.Profile.FilamentDensity,
		LayerHeight: req.Profile.LayerHeight, Nozzle: req.Profile.NozzleDiameter, DefaultFeed: req.Profile.PrintSpeed,
	})
	if err := res.Validate(); err != nil {
		fail(fmt.Errorf("the slicer produced an inconsistent toolpath: %w", err))
		return
	}
	if fallback != nil {
		res.Diagnostics = append([]toolpath.Diagnostic{*fallback}, res.Diagnostics...)
	}
	if ctx.Err() != nil {
		fail(ctx.Err())
		return
	}
	out := &Result{GeomHash: req.GeomHash, ProfileHash: ph, Profile: req.Profile, Slice: res,
		Placement: prep.Placement, Min: prep.Min, Max: prep.Max}
	m.mu.Lock()
	m.store(out) // valid for its own geometry and settings even if a newer job replaced it
	current := m.cur == r
	if current {
		m.last, m.lastJob = out, r.id
	}
	m.mu.Unlock()
	if current {
		m.emitIfCurrent(r, Event{Job: r.id, State: StateDone, Provider: sl.Name(), Summary: out.Summary(r.id)})
	}
}

// Cancel stops the running job, if any.
func (m *Manager) Cancel() {
	m.mu.Lock()
	r := m.cur
	m.mu.Unlock()
	if r != nil {
		r.cancel()
	}
}

// State is the latest event of the current job.
func (m *Manager) State() Event {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.state
}

// Result returns the finished result of the given job. It is nil for a stale, failed or
// unknown job.
func (m *Manager) Result(jobID string) *Result {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.last != nil && m.lastJob == jobID {
		return m.last
	}
	return nil
}

// Wait blocks until every started job has returned (for tests and shutdown).
func (m *Manager) Wait() { m.wg.Wait() }

// Invalidate drops everything cached; used when the project is replaced.
func (m *Manager) Invalidate() {
	m.mu.Lock()
	m.cache, m.last, m.lastJob = nil, nil, ""
	m.mu.Unlock()
}
