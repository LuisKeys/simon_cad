// Package rebuild evaluates the CAD-IR feature DAG into kernel geometry.
//
// Evaluation is deterministic (topological order with insertion-order tie-breaking),
// incremental (a content-hash cache keyed by node content plus upstream keys), and
// failure-isolating (a failing node marks its dependents failed with a cause chain
// instead of aborting the rebuild).
package rebuild

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"sync"

	"github.com/accelone/simoncad/internal/cadir"
	"github.com/accelone/simoncad/internal/expr"
	"github.com/accelone/simoncad/internal/geometry/api"
	"github.com/accelone/simoncad/internal/geometry/features"
	"github.com/accelone/simoncad/internal/selectors"
	"github.com/accelone/simoncad/internal/xform"
)

// builderVersion invalidates caches if builder semantics change.
const builderVersion = "1"

// cacheWindow is how many rebuild generations a cached body survives unused. It lets
// preview rebuilds of candidate graphs coexist with the committed graph's bodies.
const cacheWindow = 8

type Body = features.Body

// NodeResult is the outcome of one node in a rebuild.
type NodeResult struct {
	ID     string
	Status string
	Errors []string
	Key    string
	Body   *Body // nil unless Status is ok (or suppressed pass-through)
	Params features.Params
}

// Model is the derived result of a rebuild. Its bodies are owned by the engine and stay
// valid for cacheWindow further rebuilds.
type Model struct {
	Order  []string
	Nodes  map[string]*NodeResult
	Leaves []string // solid-producing nodes nothing else consumes, in order
	// Sketches are sketch nodes nothing consumes (drawn as outlines, never as geometry).
	Sketches []string
}

// Stats counts cache behaviour; used to verify incremental rebuilds.
type Stats struct{ Hits, Misses int }

type entry struct {
	body *Body
	gen  int
}

// Engine evaluates a graph. It is NOT reentrant: Rebuild holds mu for the whole pass, builders
// included, so a builder must never call back into the engine it is running on. A
// component_instance resolves through Components, which uses a separate engine per component.
type Engine struct {
	K *api.Kernel
	// Components resolves component_instance nodes. Nil means component instances fail with a
	// clear error instead of being silently skipped.
	Components features.ComponentResolver
	mu         sync.Mutex
	cache      map[string]*entry
	gen        int
	Stats      Stats
}

func New(k *api.Kernel) *Engine { return &Engine{K: k, cache: map[string]*entry{}} }

// Close releases every cached shape, and the component resolver's nested engines with it.
func (e *Engine) Close() {
	e.mu.Lock()
	for k, en := range e.cache {
		en.body.Shape.Release()
		delete(e.cache, k)
	}
	e.mu.Unlock()
	if c, ok := e.Components.(interface{ Close() }); ok {
		c.Close()
	}
}

// CacheSize is the number of live cached bodies (leak diagnostics).
func (e *Engine) CacheSize() int { e.mu.Lock(); defer e.mu.Unlock(); return len(e.cache) }

func hashKey(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Resolve evaluates a node's expressions over already-resolved upstream parameters.
func resolveParams(n *cadir.Node, resolved map[string]features.Params) (features.Params, error) {
	p := features.Params{}
	for k, v := range n.Parameters {
		p[k] = v
	}
	names := make([]string, 0, len(n.Expressions))
	for k := range n.Expressions {
		names = append(names, k)
	}
	sort.Strings(names)
	env := func(name string) (float64, bool) {
		nodeID := expr.NodeOf(name)
		if nodeID == "" {
			return 0, false
		}
		up, ok := resolved[nodeID]
		if !ok {
			return 0, false
		}
		f, ok := up[name[len(nodeID)+1:]].(float64)
		return f, ok
	}
	for _, k := range names {
		v, err := expr.Eval(n.Expressions[k], env)
		if err != nil {
			return nil, fmt.Errorf("expression for %q: %w", k, err)
		}
		p[k] = v
	}
	return p, nil
}

// Rebuild evaluates g. Node Status/Errors on g are not modified; use Model.Apply.
func (e *Engine) Rebuild(g *cadir.Graph) (*Model, error) {
	order, err := g.TopoOrder()
	if err != nil {
		return nil, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.gen++

	m := &Model{Nodes: map[string]*NodeResult{}}
	bodies := map[string]*Body{}
	resolved := map[string]features.Params{}
	nodeByID := map[string]*cadir.Node{}
	for _, n := range g.Nodes {
		nodeByID[n.ID] = n
	}

	for _, n := range order {
		m.Order = append(m.Order, n.ID)
		res := &NodeResult{ID: n.ID}
		m.Nodes[n.ID] = res
		e.evalNode(g, n, res, m, bodies, resolved, nodeByID)
		if res.Body != nil {
			bodies[n.ID] = res.Body
		}
	}

	consumed := map[string]bool{}
	for _, n := range g.Nodes {
		if n.Suppressed || !features.Consumes(n.Type) {
			continue
		}
		for _, d := range cadir.EffectiveDeps(n) {
			consumed[d] = true
		}
	}
	for _, id := range m.Order {
		r := m.Nodes[id]
		if r.Status == cadir.StatusOK && r.Body != nil && !consumed[id] {
			if r.Body.Sketch {
				m.Sketches = append(m.Sketches, id)
			} else {
				m.Leaves = append(m.Leaves, id)
			}
		}
	}

	// Evict cache entries unused for cacheWindow generations.
	for k, en := range e.cache {
		if e.gen-en.gen > cacheWindow {
			en.body.Shape.Release()
			delete(e.cache, k)
		}
	}
	return m, nil
}

func (e *Engine) evalNode(g *cadir.Graph, n *cadir.Node, res *NodeResult, m *Model,
	bodies map[string]*Body, resolved map[string]features.Params, nodeByID map[string]*cadir.Node) {

	fail := func(format string, a ...any) {
		res.Status = cadir.StatusFailed
		res.Errors = append(res.Errors, fmt.Sprintf(format, a...))
	}

	feat, known := features.Lookup(n.Type)
	if !known {
		fail("unknown feature type %q", n.Type)
		return
	}

	// Upstream status: failures propagate with a cause chain.
	deps := cadir.EffectiveDeps(n)
	for _, d := range deps {
		up := m.Nodes[d]
		if up == nil {
			fail("dependency %q is missing", d)
			return
		}
		if up.Status == cadir.StatusFailed {
			fail("dependency %q failed", d)
			res.Errors = append(res.Errors, up.Errors...)
			return
		}
	}

	params, err := resolveParams(n, resolved)
	if err != nil {
		fail("%v", err)
		return
	}
	res.Params = params
	resolved[n.ID] = params
	if feat.Kind == features.KindParams || feat.Kind == features.KindData {
		// values only, no geometry; the key follows the parameters so dependents regenerate
		if feat.Check != nil {
			if err := feat.Check(params); err != nil {
				fail("%v", err)
				return
			}
		}
		pj, err := json.Marshal(params)
		if err != nil { // never key on an empty string: distinct parameters would share a cache entry
			fail("parameters cannot be serialized: %v", err)
			return
		}
		res.Status, res.Key = cadir.StatusOK, hashKey(feat.Type, string(pj))
		return
	}

	depKeys := make([]string, len(deps))
	for i, d := range deps {
		depKeys[i] = m.Nodes[d].Key
	}

	if n.Suppressed {
		res.Status = cadir.StatusSuppressed
		if feat.Target != nil {
			if t := feat.Target(params); t != "" {
				if tr := m.Nodes[t]; tr != nil && tr.Body != nil {
					res.Key, res.Body = tr.Key, tr.Body // pass-through shares the target's body
				}
			}
		}
		if res.Key == "" {
			res.Key = hashKey("suppressed", n.ID)
		}
		return
	}
	for _, d := range deps {
		if up := m.Nodes[d]; up.Status == cadir.StatusSuppressed && up.Body == nil {
			fail("dependency %q is suppressed", d)
			return
		}
	}

	pj, err := json.Marshal(params)
	if err != nil {
		fail("parameters cannot be serialized: %v", err)
		return
	}
	refs, err := json.Marshal(n.References)
	if err != nil {
		fail("references cannot be serialized: %v", err)
		return
	}
	res.Key = hashKey(builderVersion, n.Type, string(pj), string(refs), hashKey(depKeys...))
	if en, ok := e.cache[res.Key]; ok {
		en.gen = e.gen
		e.Stats.Hits++
		res.Status, res.Body = cadir.StatusOK, en.body
		return
	}
	e.Stats.Misses++

	ctx := &features.Ctx{
		K: e.K, Node: n, P: params, Bodies: bodies, Components: e.Components,
		ParamsOf: func(id string) (features.Params, bool) { p, ok := resolved[id]; return p, ok },
		NodeOf:   func(id string) *cadir.Node { return nodeByID[id] },
	}
	shape, err := safeBuild(feat, ctx)
	if err != nil {
		fail("%v", err)
		return
	}
	valid, verr := shape.IsValid()
	if verr != nil || !valid {
		shape.Release()
		fail("%s produced invalid geometry", n.Type)
		return
	}
	props, err := shape.Props()
	if err != nil {
		shape.Release()
		fail("%v", err)
		return
	}
	isSketch := feat.Kind == features.KindSketch
	if isSketch && props.Faces == 0 {
		shape.Release()
		fail("%s produced no faces (empty result)", n.Type)
		return
	}
	if !isSketch && props.Solids == 0 {
		shape.Release()
		fail("%s produced no solid (empty result)", n.Type)
		return
	}
	body := &Body{Shape: shape, Props: props, Sketch: isSketch}
	e.cache[res.Key] = &entry{body: body, gen: e.gen}
	res.Status, res.Body = cadir.StatusOK, body
}

func safeBuild(f *features.Feature, c *features.Ctx) (s *api.Shape, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("feature builder panic: %v", r)
		}
	}()
	return f.Build(c)
}

// Apply writes derived status/errors back onto the graph's nodes.
func (m *Model) Apply(g *cadir.Graph) {
	for _, n := range g.Nodes {
		if r := m.Nodes[n.ID]; r != nil {
			n.Status = r.Status
			n.Errors = r.Errors
		}
	}
}

// Failed lists failing node IDs in evaluation order.
func (m *Model) Failed() []string {
	var out []string
	for _, id := range m.Order {
		if m.Nodes[id].Status == cadir.StatusFailed {
			out = append(out, id)
		}
	}
	return out
}

// Bounds returns a node's bounding box (used as the selector resolver).
func (m *Model) Bounds(id string) (selectors.Bounds, bool) {
	r := m.Nodes[id]
	if r == nil || r.Body == nil {
		return selectors.Bounds{}, false
	}
	return r.Body.Bounds(), true
}

// BoundsInFrame returns a node's bounding box measured along the axes of r (world = r·local).
func (m *Model) BoundsInFrame(id string, r xform.Rot) (selectors.Bounds, bool) {
	n := m.Nodes[id]
	if n == nil || n.Body == nil {
		return selectors.Bounds{}, false
	}
	b, err := n.Body.BoundsInFrame(r)
	return b, err == nil
}

// Topology returns a feature's face/edge/vertex descriptors for selector resolution.
func (m *Model) Topology(id string) (*selectors.Topology, bool) {
	r := m.Nodes[id]
	if r == nil || r.Body == nil {
		return nil, false
	}
	t := r.Body.Topology()
	return t, t != nil
}

// Volume is the summed volume of the leaf bodies.
func (m *Model) Volume() float64 {
	var v float64
	for _, id := range m.Leaves {
		v += m.Nodes[id].Body.Props.Volume
	}
	return v
}

// ResultShape returns a single shape for the leaves (unioned when there are several).
// The caller owns and must Release it.
func (m *Model) ResultShape(k *api.Kernel) (*api.Shape, error) {
	if len(m.Leaves) == 0 {
		return nil, fmt.Errorf("model has no geometry")
	}
	cur, err := m.Nodes[m.Leaves[0]].Body.Shape.Translate(0, 0, 0)
	if err != nil {
		return nil, err
	}
	for _, id := range m.Leaves[1:] {
		next, err := k.Union(cur, m.Nodes[id].Body.Shape)
		cur.Release()
		if err != nil {
			return nil, err
		}
		cur = next
	}
	return cur, nil
}
