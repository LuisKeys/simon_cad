// Package cadir is the authoritative CAD intermediate representation: a feature DAG
// of semantically typed nodes. The B-Rep is always derived from it and disposable.
package cadir

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

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
	// Vision-derived geometry records where it came from; the geometry itself never needs these to rebuild.
	SourceImageID    string `json:"source_image_id,omitempty"`
	InterpretationID string `json:"interpretation_id,omitempty"`
}

type Metadata struct {
	Name     string   `json:"name,omitempty"`
	Created  string   `json:"created,omitempty"`
	Modified string   `json:"modified,omitempty"`
	Tags     []string `json:"tags,omitempty"`
}

// Appearance is presentation metadata. It is authoritative in the CAD-IR but is never an
// input to geometry: it is excluded from Node.ContentKey and the rebuild cache key, so
// changing it never invokes the geometry kernel. Nil fields mean "default".
type Appearance struct {
	Color   string   `json:"color,omitempty"`   // "#RRGGBB"
	Opacity *float64 `json:"opacity,omitempty"` // 0..1
	Visible *bool    `json:"visible,omitempty"`
	// MaterialID references a visual (PBR) material in the material library; Color, when
	// set, overrides the material's base color. TextureScale (>0) multiplies the material's own scale.
	MaterialID   string   `json:"material_id,omitempty"`
	TextureScale *float64 `json:"texture_scale,omitempty"`
}

// MaxGroupName bounds a group label.
const MaxGroupName = 64

// NormalizeGroup trims a group label and checks its length ("" means no group).
func NormalizeGroup(name string) (string, error) {
	name = strings.TrimSpace(name)
	if len(name) > MaxGroupName {
		return "", fmt.Errorf("group name is longer than %d characters", MaxGroupName)
	}
	return name, nil
}

var colorPattern = regexp.MustCompile(`^#[0-9A-Fa-f]{6}$`)

// NormalizeColor validates a "#RRGGBB" color and returns it upper-cased.
func NormalizeColor(c string) (string, error) {
	if !colorPattern.MatchString(c) {
		return "", fmt.Errorf("invalid color %q (want #RRGGBB)", c)
	}
	return strings.ToUpper(c), nil
}

// ValidateOpacity checks 0 <= o <= 1.
func ValidateOpacity(o float64) error {
	if !(o >= 0 && o <= 1) {
		return fmt.Errorf("opacity %v out of range [0,1]", o)
	}
	return nil
}

// ValidateTextureScale checks s > 0.
func ValidateTextureScale(s float64) error {
	if !(s > 0) || s > 1000 {
		return fmt.Errorf("texture scale %v out of range (0,1000]", s)
	}
	return nil
}

// IsZero reports whether the appearance carries no overrides.
func (a *Appearance) IsZero() bool {
	return a == nil || (a.Color == "" && a.Opacity == nil && a.Visible == nil && a.MaterialID == "" && a.TextureScale == nil)
}

// Clone returns a deep copy (nil-safe).
func (a *Appearance) Clone() *Appearance {
	if a == nil {
		return nil
	}
	c := Appearance{Color: a.Color, MaterialID: a.MaterialID}
	if a.Opacity != nil {
		v := *a.Opacity
		c.Opacity = &v
	}
	if a.Visible != nil {
		v := *a.Visible
		c.Visible = &v
	}
	if a.TextureScale != nil {
		v := *a.TextureScale
		c.TextureScale = &v
	}
	return &c
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
	Appearance    *Appearance       `json:"appearance,omitempty"`
	// Group is an organizational label shared by the nodes of one user group (design tree).
	// Like appearance it is never an input to geometry.
	Group string `json:"group,omitempty"`
	// LockProportions makes a dimension edit of this body scale the other two dimensions
	// proportionally. Like group it is an editing preference, never an input to geometry.
	LockProportions bool     `json:"lock_proportions,omitempty"`
	Status          string   `json:"status,omitempty"`
	Errors          []string `json:"errors,omitempty"`
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
func (g *Graph) Hash() string { return g.hash(false) }

// GeometryHash is Hash ignoring presentation-only appearance, grouping and editing
// preferences: it is unchanged by an appearance-only or grouping edit.
func (g *Graph) GeometryHash() string { return g.hash(true) }

func (g *Graph) hash(ignoreAppearance bool) string {
	c := g.Clone()
	for _, n := range c.Nodes {
		n.Status, n.Errors = "", nil
		n.Metadata.Created, n.Metadata.Modified = "", ""
		if ignoreAppearance {
			n.Appearance, n.Group, n.LockProportions = nil, "", false
		}
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
