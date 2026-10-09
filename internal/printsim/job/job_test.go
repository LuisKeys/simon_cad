package job

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/accelone/simoncad/internal/printsim/profile"
	"github.com/accelone/simoncad/internal/printsim/slicing"
	"github.com/accelone/simoncad/internal/printsim/slicing/builtin"
	"github.com/accelone/simoncad/internal/printsim/testmesh"
	"github.com/accelone/simoncad/internal/printsim/toolpath"
)

// src stands in for a kernel shape.
type src struct {
	tris     []testmesh.Tri
	err      error
	released atomic.Int32
}

func (s *src) ExportSTL(path string, _ float64) error {
	if s.err != nil {
		return s.err
	}
	return testmesh.WriteSTL(path, s.tris)
}

func cube() *src { return &src{tris: testmesh.Box(0, 0, 0, 10, 10, 2)} }

func (s *src) req(geom string, p profile.PrintProfile, provider string) Request {
	return Request{GeomHash: geom, Source: s, Release: func() { s.released.Add(1) }, Profile: p, Provider: provider}
}

type events struct {
	mu  sync.Mutex
	ch  chan Event
	all []Event
}

func newManager(t *testing.T, sl ...slicing.Slicer) (*Manager, *events, string) {
	t.Helper()
	tmp := t.TempDir()
	m := NewManager(slicing.NewRegistry(sl...))
	m.TempDir = tmp
	ev := &events{ch: make(chan Event, 64)}
	m.OnEvent = func(e Event) {
		ev.mu.Lock()
		ev.all = append(ev.all, e)
		ev.mu.Unlock()
		ev.ch <- e
	}
	return m, ev, tmp
}

// until waits for an event in one of the given states.
func (e *events) until(t *testing.T, states ...State) Event {
	t.Helper()
	timeout := time.After(10 * time.Second)
	for {
		select {
		case ev := <-e.ch:
			for _, s := range states {
				if ev.State == s {
					return ev
				}
			}
		case <-timeout:
			t.Fatalf("no %v event; saw %+v", states, e.all)
		}
	}
}

func (e *events) count(s State) (n int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, ev := range e.all {
		if ev.State == s {
			n++
		}
	}
	return
}

func empty(t *testing.T, dir string) {
	t.Helper()
	ents, _ := os.ReadDir(dir)
	if len(ents) != 0 {
		t.Fatalf("temporary files left behind: %v", ents)
	}
}

// fakeSlicer counts calls and can block.
type fakeSlicer struct {
	name    string
	calls   atomic.Int32
	started chan struct{}
	release chan struct{} // nil: return immediately
	ignore  bool          // keep going after ctx is cancelled (a slow, uncooperative slicer)
	sawCtx  atomic.Bool
	err     error
}

func (f *fakeSlicer) Name() string     { return f.name }
func (f *fakeSlicer) Available() error { return nil }
func (f *fakeSlicer) Slice(ctx context.Context, in slicing.Input, p profile.PrintProfile) (*toolpath.SliceResult, error) {
	f.calls.Add(1)
	if f.started != nil {
		f.started <- struct{}{}
	}
	if f.release != nil {
		if f.ignore {
			<-f.release
		} else {
			select {
			case <-f.release:
			case <-ctx.Done():
				f.sawCtx.Store(true)
				return nil, ctx.Err()
			}
		}
	}
	if f.err != nil {
		return nil, f.err
	}
	if _, err := os.Stat(in.STLPath); err != nil {
		return nil, fmt.Errorf("slicer was not given the prepared mesh: %w", err)
	}
	return &toolpath.SliceResult{
		Provider: f.name,
		Layers:   []toolpath.Layer{{Z: 0.2, First: 0, Count: 2}},
		Segments: []toolpath.Segment{
			{X1: 10, Kind: toolpath.ExternalPerimeter, E: 1, F: 10},
			{X0: 10, X1: 10, Y1: 10, Kind: toolpath.Infill, E: 1, F: 10},
		},
	}, nil
}

func TestEndToEndWithBuiltinSlicer(t *testing.T) {
	m, ev, tmp := newManager(t, builtin.Slicer{})
	s := cube()
	st, err := m.Start(s.req("g1", profile.GenericFDM(), "auto"))
	if err != nil || st.Cached {
		t.Fatalf("%v %+v", err, st)
	}
	done := ev.until(t, StateDone, StateError)
	if done.State != StateDone {
		t.Fatalf("job failed: %s", done.Error)
	}
	sum := done.Summary
	if sum.Layers != 10 || !sum.Preview || sum.Provider != "builtin" || sum.DurationS <= 0 || sum.Disclaimer == "" {
		t.Fatalf("summary: %+v", sum)
	}
	if sum.LayerHeight != 0.2 || sum.Build != [3]float64{220, 220, 250} {
		t.Fatalf("the front end needs the layer height and build volume: %+v", sum)
	}
	if sum.Placement.DX != 105 || sum.Size[2] != 2 {
		t.Fatalf("placement/size: %+v", sum)
	}
	res := m.Result(st.Job)
	if res == nil {
		t.Fatal("result must be retrievable")
	}
	bin, err := res.Binary()
	if err != nil || len(bin) == 0 {
		t.Fatal(err)
	}
	if again, _ := res.Binary(); &again[0] != &bin[0] {
		t.Fatal("the binary must be encoded once")
	}
	m.Wait()
	empty(t, tmp)
	if s.released.Load() != 1 {
		t.Fatalf("release called %d times", s.released.Load())
	}
	for _, want := range []State{StateQueued, StatePreparing, StateSlicing, StateParsing, StateDone} {
		if ev.count(want) != 1 {
			t.Errorf("state %s emitted %d times", want, ev.count(want))
		}
	}
	if m.Result("ps_9999") != nil {
		t.Fatal("unknown job has no result")
	}
}

func TestCacheByGeometryAndProfile(t *testing.T) {
	f := &fakeSlicer{name: "fake"}
	m, ev, _ := newManager(t, f)
	p := profile.GenericFDM()
	run := func(geom string, p profile.PrintProfile) Started {
		t.Helper()
		s := cube()
		st, err := m.Start(s.req(geom, p, "fake"))
		if err != nil {
			t.Fatal(err)
		}
		ev.until(t, StateDone, StateError)
		m.Wait()
		if s.released.Load() != 1 {
			t.Fatalf("release called %d times (cached=%v)", s.released.Load(), st.Cached)
		}
		return st
	}
	if st := run("g1", p); st.Cached {
		t.Fatal("first run cannot be cached")
	}
	if st := run("g1", p); !st.Cached || f.calls.Load() != 1 {
		t.Fatalf("same geometry and profile must hit the cache (calls=%d)", f.calls.Load())
	}
	if st := run("g2", p); st.Cached {
		t.Fatal("a modified model (new geometry hash) must not reuse the old toolpath")
	}
	p2 := p
	p2.InfillPct = 50
	if st := run("g1", p2); st.Cached {
		t.Fatal("a slicing-relevant setting change must invalidate")
	}
	p3 := p
	p3.OverhangDeg = 30 // analysis only: same slice
	if st := run("g1", p3); !st.Cached {
		t.Fatal("analysis thresholds must not invalidate the slice")
	}
	if st := run("g1", p); !st.Cached {
		t.Fatal("older entries stay cached")
	}
	// the cached result is served under the new job id
	st := run("g1", p)
	if m.Result(st.Job) == nil {
		t.Fatal("cached result must be reachable by the new job id")
	}
	m.Invalidate()
	if st := run("g1", p); st.Cached {
		t.Fatal("Invalidate must empty the cache")
	}
}

func TestCacheEvictsOldest(t *testing.T) {
	f := &fakeSlicer{name: "fake"}
	m, ev, _ := newManager(t, f)
	for i := 0; i < cacheSize+1; i++ {
		if _, err := m.Start(cube().req(fmt.Sprintf("g%d", i), profile.GenericFDM(), "fake")); err != nil {
			t.Fatal(err)
		}
		ev.until(t, StateDone)
		m.Wait()
	}
	st, _ := m.Start(cube().req("g0", profile.GenericFDM(), "fake"))
	if st.Cached {
		t.Fatal("the oldest entry must have been evicted")
	}
	ev.until(t, StateDone)
}

func TestCancelStopsSlicerAndCleansUp(t *testing.T) {
	f := &fakeSlicer{name: "fake", started: make(chan struct{}, 1), release: make(chan struct{})}
	m, ev, tmp := newManager(t, f)
	s := cube()
	st, err := m.Start(s.req("g1", profile.GenericFDM(), "fake"))
	if err != nil {
		t.Fatal(err)
	}
	<-f.started
	m.Cancel()
	got := ev.until(t, StateCancelled, StateDone, StateError)
	if got.State != StateCancelled || got.Job != st.Job {
		t.Fatalf("want cancelled, got %+v", got)
	}
	m.Wait()
	if !f.sawCtx.Load() {
		t.Fatal("the slicer must observe the cancellation")
	}
	if m.Result(st.Job) != nil {
		t.Fatal("a cancelled job has no result")
	}
	empty(t, tmp)
	if s.released.Load() != 1 {
		t.Fatalf("release called %d times", s.released.Load())
	}
	// and the manager is usable again
	f.release = nil
	if _, err := m.Start(cube().req("g1", profile.GenericFDM(), "fake")); err != nil {
		t.Fatal(err)
	}
	ev.until(t, StateDone)
}

func TestStaleJobNeverReplacesNewerResult(t *testing.T) {
	// job A keeps running after being superseded and finishes last
	slow := &fakeSlicer{name: "slow", started: make(chan struct{}, 1), release: make(chan struct{}), ignore: true}
	fast := &fakeSlicer{name: "fast"}
	m, ev, tmp := newManager(t, slow, fast)
	a := cube()
	stA, err := m.Start(a.req("old-geometry", profile.GenericFDM(), "slow"))
	if err != nil {
		t.Fatal(err)
	}
	<-slow.started
	stB, err := m.Start(cube().req("new-geometry", profile.GenericFDM(), "fast"))
	if err != nil {
		t.Fatal(err)
	}
	doneB := ev.until(t, StateDone)
	if doneB.Job != stB.Job {
		t.Fatalf("expected job B, got %+v", doneB)
	}
	close(slow.release) // A now finishes successfully, too late
	m.Wait()
	if m.Result(stA.Job) != nil {
		t.Fatal("the stale job's result must not be served")
	}
	if r := m.Result(stB.Job); r == nil || r.GeomHash != "new-geometry" {
		t.Fatal("the newer result must survive")
	}
	if ev.count(StateDone) != 1 {
		t.Fatalf("the stale job announced itself: %+v", ev.all)
	}
	for _, e := range ev.all {
		if e.Job == stA.Job && (e.State == StateDone || e.State == StateCancelled || e.State == StateError) {
			t.Fatalf("stale job emitted a terminal event: %+v", e)
		}
	}
	if m.State().Job != stB.Job || m.State().State != StateDone {
		t.Fatalf("state: %+v", m.State())
	}
	empty(t, tmp)
	if a.released.Load() != 1 {
		t.Fatal("the superseded job must still release its shape")
	}
}

func TestErrors(t *testing.T) {
	t.Run("slicer failure", func(t *testing.T) {
		m, ev, tmp := newManager(t, &fakeSlicer{name: "fake", err: errors.New("object is not manifold")})
		s := cube()
		if _, err := m.Start(s.req("g", profile.GenericFDM(), "fake")); err != nil {
			t.Fatal(err)
		}
		e := ev.until(t, StateError)
		if !strings.Contains(e.Error, "not manifold") {
			t.Fatalf("%+v", e)
		}
		m.Wait()
		empty(t, tmp)
		if s.released.Load() != 1 {
			t.Fatalf("release=%d", s.released.Load())
		}
		if m.Result(e.Job) != nil {
			t.Fatal("no result for a failed job")
		}
	})
	t.Run("export failure", func(t *testing.T) {
		m, ev, tmp := newManager(t, &fakeSlicer{name: "fake"})
		s := &src{err: errors.New("kernel exploded")}
		if _, err := m.Start(s.req("g", profile.GenericFDM(), "fake")); err != nil {
			t.Fatal(err)
		}
		e := ev.until(t, StateError)
		if !strings.Contains(e.Error, "kernel exploded") {
			t.Fatalf("%+v", e)
		}
		m.Wait()
		empty(t, tmp)
		if s.released.Load() != 1 {
			t.Fatal("shape not released")
		}
	})
	t.Run("model too big", func(t *testing.T) {
		m, ev, _ := newManager(t, &fakeSlicer{name: "fake"})
		s := &src{tris: testmesh.Box(0, 0, 0, 500, 10, 10)}
		m.Start(s.req("g", profile.GenericFDM(), "fake"))
		if e := ev.until(t, StateError); !strings.Contains(e.Error, "build volume") {
			t.Fatalf("%+v", e)
		}
	})
	t.Run("missing slicer is synchronous and releases", func(t *testing.T) {
		missing := fmt.Errorf("%w: PrusaSlicer was not found", slicing.ErrUnavailable)
		m, _, _ := newManager(t, unavailable{missing})
		s := cube()
		_, err := m.Start(s.req("g", profile.GenericFDM(), "auto"))
		if !errors.Is(err, slicing.ErrUnavailable) || !strings.Contains(err.Error(), "not found") {
			t.Fatalf("%v", err)
		}
		if s.released.Load() != 1 {
			t.Fatal("shape not released on a rejected start")
		}
	})
	t.Run("invalid profile", func(t *testing.T) {
		m, _, _ := newManager(t, &fakeSlicer{name: "fake"})
		p := profile.GenericFDM()
		p.NozzleDiameter = 0
		s := cube()
		if _, err := m.Start(s.req("g", p, "fake")); err == nil || !strings.Contains(err.Error(), "nozzle") {
			t.Fatalf("%v", err)
		}
		if s.released.Load() != 1 {
			t.Fatal("shape not released")
		}
	})
}

type unavailable struct{ err error }

func (u unavailable) Name() string     { return "prusaslicer" }
func (u unavailable) Available() error { return u.err }
func (u unavailable) Slice(context.Context, slicing.Input, profile.PrintProfile) (*toolpath.SliceResult, error) {
	return nil, u.err
}

func TestFallbackDiagnosticIsKept(t *testing.T) {
	missing := fmt.Errorf("%w: PrusaSlicer was not found", slicing.ErrUnavailable)
	m, ev, _ := newManager(t, unavailable{missing}, builtin.Slicer{})
	if _, err := m.Start(cube().req("g", profile.GenericFDM(), "auto")); err != nil {
		t.Fatal(err)
	}
	d := ev.until(t, StateDone, StateError)
	if d.State != StateDone {
		t.Fatal(d.Error)
	}
	if d.Summary.Diagnostics[0].Code != "slicer_fallback" {
		t.Fatalf("the user must be told the preview slicer was used: %+v", d.Summary.Diagnostics)
	}
}

func TestCancelWithNothingRunning(t *testing.T) {
	m, _, _ := newManager(t, &fakeSlicer{name: "fake"})
	m.Cancel() // must not panic
	if _, err := m.Start(cube().req("g", profile.GenericFDM(), "fake")); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	m.Cancel()
	m.Start(cube().req("g", profile.GenericFDM(), "fake")) // cache hit
	m.Cancel()                                             // cancelling a cache-hit run must not panic
	m.Start(cube().req("g2", profile.GenericFDM(), "fake"))
	m.Wait()
}
