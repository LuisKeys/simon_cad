package components

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"sync"

	"github.com/accelone/simoncad/internal/cadir"
	"github.com/accelone/simoncad/internal/geometry/api"
	"github.com/accelone/simoncad/internal/geometry/features"
	"github.com/accelone/simoncad/internal/geometry/rebuild"
	"github.com/accelone/simoncad/internal/persistence"
)

// maxFrames bounds how many resolved component builds stay in memory. Each frame owns a
// rebuild engine holding OCCT shapes, and a nested engine's generation counter only advances
// when it rebuilds, so the engine's own cacheWindow eviction never ages these out: the bound
// has to live here.
const maxFrames = 16

// Resolver turns a component reference into geometry.
//
// Two properties are not optional. First, rebuild.Engine is not reentrant — Rebuild holds its
// mutex across the builders — so a component is never resolved on the engine that is asking
// for it: every (project, content, overrides) frame gets its own engine. Second, a cycle must
// be detected before entering a nested engine, or A → B → A would deadlock instead of
// returning an error.
type Resolver struct {
	lib *Library
	k   *api.Kernel

	mu       sync.Mutex
	frames   map[string]*frame
	order    []string        // insertion order, for eviction
	visiting map[string]bool // frames currently being built, on this call stack
	path     []string        // the chain being resolved, for the cycle message
}

type frame struct {
	engine *rebuild.Engine
	model  *rebuild.Model
}

func NewResolver(lib *Library, k *api.Kernel) *Resolver {
	return &Resolver{lib: lib, k: k, frames: map[string]*frame{}, visiting: map[string]bool{}}
}

// Library exposes the library the resolver reads from.
func (r *Resolver) Library() *Library { return r.lib }

// Close releases every nested engine. It is reached from the owning engine's Close, so the
// kernel ends up with no live shapes.
func (r *Resolver) Close() {
	r.mu.Lock()
	frames := r.frames
	r.frames, r.order = map[string]*frame{}, nil
	r.mu.Unlock()
	for _, f := range frames {
		f.engine.Close()
	}
}

// Hash is the component's current geometry hash on disk: what an instance would pin to if it
// were synchronised now.
func (r *Resolver) Hash(projectID, version string) (string, error) {
	dir, err := r.lib.Path(projectID)
	if err != nil {
		return "", err
	}
	_, _, h, err := r.lib.LoadGraph(dir)
	return h, err
}

// Shape builds the component and returns a shape the caller owns.
//
// contentHash is what the instance is pinned to. v1 keeps no archive of past versions, so the
// component on disk is always what gets built; a pin that no longer matches is reported by
// OutOfDate and resolved by an explicit, undoable re-pin, not by silently building something
// else. Keeping the pin in the node's parameters is what makes the rebuild cache, the graph
// hash and checkpoints notice a component edit at all.
func (r *Resolver) Shape(projectID, version, contentHash string, overrides map[string]any) (*api.Shape, error) {
	dir, err := r.lib.Path(projectID)
	if err != nil {
		return nil, err
	}
	meta, g, hash, err := r.lib.LoadGraph(dir)
	if err != nil {
		return nil, err
	}
	key := frameKey(projectID, hash, overrides)

	r.mu.Lock()
	if r.visiting[key] {
		chain := append(append([]string(nil), r.path...), projectID)
		r.mu.Unlock()
		return nil, cycleError(chain)
	}
	for _, p := range r.path {
		if p == projectID {
			chain := append(append([]string(nil), r.path...), projectID)
			r.mu.Unlock()
			return nil, cycleError(chain)
		}
	}
	if f, ok := r.frames[key]; ok && f.model != nil {
		r.mu.Unlock()
		return resultShape(f.model, r.k, meta, projectID)
	}
	r.visiting[key] = true
	r.path = append(r.path, projectID)
	r.mu.Unlock()

	defer func() {
		r.mu.Lock()
		delete(r.visiting, key)
		if n := len(r.path); n > 0 {
			r.path = r.path[:n-1]
		}
		r.mu.Unlock()
	}()

	built, err := applyOverrides(g, overrides)
	if err != nil {
		return nil, fmt.Errorf("component %q: %w", meta.Name, err)
	}
	// A fresh engine per frame: the caller's engine is mid-Rebuild and holds its own lock,
	// and so does every engine further up this chain.
	eng := rebuild.New(r.k)
	eng.Components = r
	m, err := eng.Rebuild(built)
	if err != nil {
		eng.Close()
		return nil, fmt.Errorf("component %q: %w", meta.Name, err)
	}
	if failed := m.Failed(); len(failed) > 0 {
		errs := m.Nodes[failed[0]].Errors
		eng.Close()
		return nil, fmt.Errorf("component %q failed to rebuild: %s: %s", meta.Name, failed[0], joinErrs(errs))
	}
	r.store(key, &frame{engine: eng, model: m})
	return resultShape(m, r.k, meta, projectID)
}

// OutOfDate reports the instances whose pinned content hash no longer matches their component
// on disk, mapped to the hash they would move to.
func (r *Resolver) OutOfDate(g *cadir.Graph) map[string]string {
	out := map[string]string{}
	for _, n := range g.Nodes {
		if n.Type != features.TypeComponentInstance {
			continue
		}
		id, _ := n.Parameters[features.ParamComponent].(string)
		pinned, _ := n.Parameters[features.ParamContentHash].(string)
		if id == "" || pinned == "" {
			continue
		}
		version, _ := n.Parameters[features.ParamVersion].(string)
		if h, err := r.Hash(id, version); err == nil && h != pinned {
			out[n.ID] = h
		}
	}
	return out
}

func (r *Resolver) store(key string, f *frame) {
	r.mu.Lock()
	var evict []*frame
	if _, exists := r.frames[key]; !exists {
		r.order = append(r.order, key)
	}
	r.frames[key] = f
	for len(r.order) > maxFrames {
		oldest := r.order[0]
		r.order = r.order[1:]
		if old, ok := r.frames[oldest]; ok && old != f {
			evict = append(evict, old)
			delete(r.frames, oldest)
		}
	}
	r.mu.Unlock()
	for _, e := range evict {
		e.engine.Close()
	}
}

// resultShape unions the component's leaf bodies into one shape the caller owns. A component
// instance is one body: one tree row, one mesh, one appearance, and face selectors that
// resolve exactly like any other feature's.
func resultShape(m *rebuild.Model, k *api.Kernel, meta persistence.Meta, projectID string) (*api.Shape, error) {
	if len(m.Leaves) == 0 {
		return nil, fmt.Errorf("component %q (%s) has no solid geometry to instantiate", meta.Name, projectID)
	}
	return m.ResultShape(k)
}

// applyOverrides returns the component's graph with the instance's parameter overrides applied
// to its design-parameter nodes. The shared cached graph is never mutated.
func applyOverrides(g *cadir.Graph, overrides map[string]any) (*cadir.Graph, error) {
	if len(overrides) == 0 {
		return g, nil
	}
	out := g.Clone()
	applied := map[string]bool{}
	for _, n := range out.Nodes {
		if n.Type != features.ParamsType {
			continue
		}
		for k, v := range overrides {
			if _, declared := n.Parameters[k]; declared {
				n.Parameters[k] = v
				applied[k] = true
			}
		}
	}
	var unknown []string
	for k := range overrides {
		if !applied[k] {
			unknown = append(unknown, k)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return nil, fmt.Errorf("the component declares no parameter(s) %v", unknown)
	}
	return out, nil
}

// frameKey identifies one resolved build: the project, the exact content it was built from,
// and the overrides applied to it.
func frameKey(projectID, contentHash string, overrides map[string]any) string {
	if len(overrides) == 0 {
		return projectID + "@" + contentHash
	}
	b, _ := json.Marshal(overrides) // map keys are sorted by encoding/json
	sum := sha256.Sum256(b)
	return projectID + "@" + contentHash + "#" + hex.EncodeToString(sum[:8])
}

func joinErrs(errs []string) string {
	if len(errs) == 0 {
		return "unknown error"
	}
	return errs[0]
}
