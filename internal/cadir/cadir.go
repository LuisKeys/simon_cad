// Package cadir is the authoritative CAD intermediate representation: a feature DAG
// of semantically typed nodes. The B-Rep is always derived from it and disposable.
package cadir

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/accelone/simoncad/internal/expr"
	"github.com/accelone/simoncad/internal/selectors"
)

// Node status values (derived at rebuild time; never authoritative input).
const (
	StatusOK         = "ok"
	StatusFailed     = "failed"
	StatusStale      = "stale"
	StatusSuppressed = "suppressed"
)

// Creators recorded in provenance.
const (
	CreatedByUser  = "user"
	CreatedByAgent = "agent"
	CreatedByCLI   = "cli"
)

type Constraint struct {
	Kind   string   `json:"kind"` // symmetry|centering|alignment|equal_spacing|clearance
	Refs   []string `json:"refs,omitempty"`
	Value  float64  `json:"value,omitempty"`
	Detail string   `json:"detail,omitempty"`
}

type Provenance struct {
	CreatedBy  string `json:"created_by,omitempty"`
	Capability string `json:"capability,omitempty"`
	ToolCallID string `json:"tool_call_id,omitempty"`
	ChangeSet  string `json:"changeset_id,omitempty"`
	Model      string `json:"model,omitempty"`
}

type Metadata struct {
	Name     string   `json:"name,omitempty"`
	Created  string   `json:"created,omitempty"`
	Modified string   `json:"modified,omitempty"`
	Tags     []string `json:"tags,omitempty"`
}

// Node is one CAD-IR feature. The full schema exists from day one even where
// fields (component, manufacturing, ...) are not yet consumed.
type Node struct {
	ID            string            `json:"id"`
	Type          string            `json:"type"`
	Parameters    map[string]any    `json:"parameters,omitempty"`
	Expressions   map[string]string `json:"expressions,omitempty"`
	Dependencies  []string          `json:"dependencies,omitempty"`
	References    map[string]string `json:"references,omitempty"`
	Constraints   []Constraint      `json:"constraints,omitempty"`
	Intent        string            `json:"intent,omitempty"`
	Manufacturing map[string]any    `json:"manufacturing,omitempty"`
	Component     string            `json:"component,omitempty"`
	SelectionRefs []string          `json:"selection_refs,omitempty"`
	Provenance    Provenance        `json:"provenance"`
	Metadata      Metadata          `json:"metadata"`
	Suppressed    bool              `json:"suppressed,omitempty"`
	Status        string            `json:"status,omitempty"`
	Errors        []string          `json:"errors,omitempty"`
}

// Graph is the ordered feature DAG. Order is insertion order and is the tie-breaker
// for evaluation, which makes rebuilds deterministic.
type Graph struct {
	Units string  `json:"units"`
	Nodes []*Node `json:"nodes"`
}

func NewGraph() *Graph { return &Graph{Units: "mm", Nodes: []*Node{}} }

// Clone deep-copies the graph via its canonical encoding.
func (g *Graph) Clone() *Graph {
	b, err := json.Marshal(g)
	if err != nil {
		panic(err) // Graph is plain data; marshal cannot fail
	}
	var c Graph
	if err := json.Unmarshal(b, &c); err != nil {
		panic(err)
	}
	if c.Nodes == nil {
		c.Nodes = []*Node{}
	}
	return &c
}

func (g *Graph) Index(id string) int {
	for i, n := range g.Nodes {
		if n.ID == id {
			return i
		}
	}
	return -1
}

func (g *Graph) Get(id string) *Node {
	if i := g.Index(id); i >= 0 {
		return g.Nodes[i]
	}
	return nil
}

// Dependents returns IDs of nodes that depend (explicitly or via expressions) on id.
func (g *Graph) Dependents(id string) []string {
	var out []string
	for _, n := range g.Nodes {
		for _, d := range EffectiveDeps(n) {
			if d == id {
				out = append(out, n.ID)
				break
			}
		}
	}
	return out
}

// EffectiveDeps is the explicit dependency list plus nodes referenced from expressions
// and selector references, de-duplicated in a stable order.
func EffectiveDeps(n *Node) []string {
	seen := map[string]bool{}
	var out []string
	add := func(id string) {
		if id != "" && id != n.ID && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	for _, d := range n.Dependencies {
		add(d)
	}
	keys := make([]string, 0, len(n.Expressions))
	for k := range n.Expressions {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		for _, v := range expr.Variables(n.Expressions[k]) {
			add(expr.NodeOf(v))
		}
	}
	refKeys := make([]string, 0, len(n.References))
	for k := range n.References {
		refKeys = append(refKeys, k)
	}
	sort.Strings(refKeys)
	for _, k := range refKeys {
		add(selectors.FeatureOf(n.References[k]))
	}
	return out
}

// TopoOrder returns a deterministic topological order (Kahn, insertion-order tie-break).
func (g *Graph) TopoOrder() ([]*Node, error) {
	idx := map[string]int{}
	for i, n := range g.Nodes {
		if _, dup := idx[n.ID]; dup {
			return nil, fmt.Errorf("duplicate node id %q", n.ID)
		}
		idx[n.ID] = i
	}
	indeg := make([]int, len(g.Nodes))
	out := make([][]int, len(g.Nodes))
	for i, n := range g.Nodes {
		for _, d := range EffectiveDeps(n) {
			j, ok := idx[d]
			if !ok {
				return nil, fmt.Errorf("node %q depends on unknown node %q", n.ID, d)
			}
			indeg[i]++
			out[j] = append(out[j], i)
		}
	}
	done := make([]bool, len(g.Nodes))
	order := make([]*Node, 0, len(g.Nodes))
	for len(order) < len(g.Nodes) {
		pick := -1
		for i := range g.Nodes {
			if !done[i] && indeg[i] == 0 {
				pick = i
				break
			}
		}
		if pick < 0 {
			return nil, fmt.Errorf("dependency cycle in feature graph")
		}
		done[pick] = true
		order = append(order, g.Nodes[pick])
		for _, k := range out[pick] {
			indeg[k]--
		}
	}
	return order, nil
}

// Validate checks structural invariants (ids, dependencies, acyclicity, expressions).
func (g *Graph) Validate() error {
	for _, n := range g.Nodes {
		if n.ID == "" || n.Type == "" {
			return fmt.Errorf("node requires id and type")
		}
		for name, e := range n.Expressions {
			if err := expr.Check(e); err != nil {
				return fmt.Errorf("node %q expression %q: %w", n.ID, name, err)
			}
		}
	}
	_, err := g.TopoOrder()
	return err
}

// Canonical returns the byte-stable encoding: sorted map keys, 2-space indent, trailing newline.
// Derived fields (status, errors) are included only if present; callers that hash use Hash.
func (g *Graph) Canonical() ([]byte, error) {
	b, err := json.MarshalIndent(g, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// Hash is a content hash of the authoritative content (excludes derived status/errors and timestamps).
func (g *Graph) Hash() string {
	c := g.Clone()
	for _, n := range c.Nodes {
		n.Status, n.Errors = "", nil
		n.Metadata.Created, n.Metadata.Modified = "", ""
	}
	b, _ := json.Marshal(c)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// ContentKey hashes a single node's behavior-defining content (not its provenance/metadata).
func (n *Node) ContentKey() string {
	type key struct {
		Type        string
		Params      map[string]any
		Expressions map[string]string
		Refs        map[string]string
		Deps        []string
		Suppressed  bool
	}
	b, _ := json.Marshal(key{n.Type, n.Parameters, n.Expressions, n.References, n.Dependencies, n.Suppressed})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Num reads a numeric literal parameter.
func (n *Node) Num(name string) (float64, bool) {
	v, ok := n.Parameters[name]
	if !ok {
		return 0, false
	}
	f, ok := v.(float64)
	return f, ok
}
