// Package commands defines the serializable, invertible edits to the CAD-IR and the
// ChangeSet lifecycle built on them. Every mutation of the model — from the UI, the CLI
// or the agent — is a Command inside a ChangeSet.
package commands

import (
	"encoding/json"
	"fmt"
	"regexp"

	"github.com/accelone/simoncad/internal/cadir"
	"github.com/accelone/simoncad/internal/expr"
)

var idPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,47}$`)

// ValidID reports whether id is an acceptable node identifier.
func ValidID(id string) bool { return idPattern.MatchString(id) }

// Command is one atomic graph edit. Apply mutates g and returns the exact inverse.
type Command interface {
	Type() string
	Apply(g *cadir.Graph) (inverse Command, err error)
}

// Record is the serialized form of a Command.
type Record struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

func Encode(c Command) Record {
	b, err := json.Marshal(c)
	if err != nil {
		panic(err)
	}
	return Record{Type: c.Type(), Data: b}
}

func Decode(r Record) (Command, error) {
	var c Command
	switch r.Type {
	case "AddFeature":
		c = &AddFeature{}
	case "RemoveFeature":
		c = &RemoveFeature{}
	case "SetParameter":
		c = &SetParameter{}
	case "SetExpression":
		c = &SetExpression{}
	case "Reorder":
		c = &Reorder{}
	case "Suppress":
		c = &Suppress{}
	case "Rename":
		c = &Rename{}
	case "SetColor":
		c = &SetColor{}
	case "SetOpacity":
		c = &SetOpacity{}
	case "SetVisibility":
		c = &SetVisibility{}
	case "SetMaterial":
		c = &SetMaterial{}
	case "RemoveMaterial":
		c = &RemoveMaterial{}
	case "SetMaterialScale":
		c = &SetMaterialScale{}
	case "ResetAppearance":
		c = &ResetAppearance{}
	case "SetGroup":
		c = &SetGroup{}
	case "SetLockProportions":
		c = &SetLockProportions{}
	case "RestoreSnapshot":
		c = &RestoreSnapshot{}
	default:
		return nil, fmt.Errorf("unknown command type %q", r.Type)
	}
	if err := json.Unmarshal(r.Data, c); err != nil {
		return nil, fmt.Errorf("decode %s: %w", r.Type, err)
	}
	return c, nil
}

func mustNode(g *cadir.Graph, id string) (*cadir.Node, error) {
	n := g.Get(id)
	if n == nil {
		return nil, fmt.Errorf("feature %q does not exist", id)
	}
	return n, nil
}

// checked re-validates the graph after a mutation and undoes it with revert on failure.
func checked(g *cadir.Graph, revert func()) error {
	if err := g.Validate(); err != nil {
		revert()
		return err
	}
	return nil
}

// ---- AddFeature / RemoveFeature -------------------------------------------------

type AddFeature struct {
	Node  *cadir.Node `json:"node"`
	Index int         `json:"index"` // -1 appends
}

func (*AddFeature) Type() string { return "AddFeature" }

func (c *AddFeature) Apply(g *cadir.Graph) (Command, error) {
	if c.Node == nil {
		return nil, fmt.Errorf("AddFeature: node is required")
	}
	if !ValidID(c.Node.ID) {
		return nil, fmt.Errorf("invalid feature id %q (use lowercase letters, digits, underscores)", c.Node.ID)
	}
	if g.Get(c.Node.ID) != nil {
		return nil, fmt.Errorf("feature %q already exists", c.Node.ID)
	}
	if c.Node.Type == "" {
		return nil, fmt.Errorf("feature %q has no type", c.Node.ID)
	}
	idx := c.Index
	if idx < 0 || idx > len(g.Nodes) {
		idx = len(g.Nodes)
	}
	n := cloneNode(c.Node)
	g.Nodes = append(g.Nodes, nil)
	copy(g.Nodes[idx+1:], g.Nodes[idx:])
	g.Nodes[idx] = n
	if err := checked(g, func() { g.Nodes = append(g.Nodes[:idx], g.Nodes[idx+1:]...) }); err != nil {
		return nil, err
	}
	return &RemoveFeature{ID: n.ID}, nil
}

type RemoveFeature struct {
	ID string `json:"id"`
}

func (*RemoveFeature) Type() string { return "RemoveFeature" }

func (c *RemoveFeature) Apply(g *cadir.Graph) (Command, error) {
	n, err := mustNode(g, c.ID)
	if err != nil {
		return nil, err
	}
	if deps := g.Dependents(c.ID); len(deps) > 0 {
		return nil, fmt.Errorf("cannot remove %q: %v depend on it", c.ID, deps)
	}
	idx := g.Index(c.ID)
	g.Nodes = append(g.Nodes[:idx], g.Nodes[idx+1:]...)
	return &AddFeature{Node: n, Index: idx}, nil
}

func cloneNode(n *cadir.Node) *cadir.Node {
	b, _ := json.Marshal(n)
	var c cadir.Node
	_ = json.Unmarshal(b, &c)
	return &c
}

// ---- SetParameter / SetExpression ------------------------------------------------

type SetParameter struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Value  any    `json:"value,omitempty"`
	Delete bool   `json:"delete,omitempty"`
}

func (*SetParameter) Type() string { return "SetParameter" }

func (c *SetParameter) Apply(g *cadir.Graph) (Command, error) {
	n, err := mustNode(g, c.ID)
	if err != nil {
		return nil, err
	}
	if c.Name == "" {
		return nil, fmt.Errorf("parameter name is required")
	}
	if _, has := n.Expressions[c.Name]; has && !c.Delete {
		return nil, fmt.Errorf("%s.%s is driven by an expression; clear the expression first", c.ID, c.Name)
	}
	old, existed := n.Parameters[c.Name]
	if c.Delete {
		if !existed {
			return nil, fmt.Errorf("%s has no parameter %q", c.ID, c.Name)
		}
		delete(n.Parameters, c.Name)
		return &SetParameter{ID: c.ID, Name: c.Name, Value: old}, nil
	}
	if !jsonScalarOrList(c.Value) {
		return nil, fmt.Errorf("unsupported value for %s.%s", c.ID, c.Name)
	}
	if n.Parameters == nil {
		n.Parameters = map[string]any{}
	}
	n.Parameters[c.Name] = c.Value
	if !existed {
		return &SetParameter{ID: c.ID, Name: c.Name, Delete: true}, nil
	}
	return &SetParameter{ID: c.ID, Name: c.Name, Value: old}, nil
}

// jsonScalarOrList accepts JSON values: scalars, lists and string-keyed objects of them
// (path points and profile contours are nested data).
func jsonScalarOrList(v any) bool {
	switch x := v.(type) {
	case float64, string, bool:
		return true
	case int:
		return true
	case []any:
		for _, e := range x {
			if !jsonScalarOrList(e) {
				return false
			}
		}
		return true
	case map[string]any:
		for _, e := range x {
			if !jsonScalarOrList(e) {
				return false
			}
		}
		return true
	}
	return false
}

type SetExpression struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Expr   string `json:"expr,omitempty"`
	Delete bool   `json:"delete,omitempty"`
}

func (*SetExpression) Type() string { return "SetExpression" }

func (c *SetExpression) Apply(g *cadir.Graph) (Command, error) {
	n, err := mustNode(g, c.ID)
	if err != nil {
		return nil, err
	}
	old, existed := n.Expressions[c.Name]
	if c.Delete {
		if !existed {
			return nil, fmt.Errorf("%s.%s has no expression", c.ID, c.Name)
		}
		delete(n.Expressions, c.Name)
		if len(n.Expressions) == 0 {
			n.Expressions = nil
		}
		return &SetExpression{ID: c.ID, Name: c.Name, Expr: old}, nil
	}
	if err := expr.Check(c.Expr); err != nil {
		return nil, fmt.Errorf("expression %q: %w", c.Expr, err)
	}
	if n.Expressions == nil {
		n.Expressions = map[string]string{}
	}
	n.Expressions[c.Name] = c.Expr
	revert := func() {
		if existed {
			n.Expressions[c.Name] = old
		} else {
			delete(n.Expressions, c.Name)
			if len(n.Expressions) == 0 {
				n.Expressions = nil
			}
		}
	}
	if err := checked(g, revert); err != nil {
		return nil, err
	}
	if !existed {
		return &SetExpression{ID: c.ID, Name: c.Name, Delete: true}, nil
	}
	return &SetExpression{ID: c.ID, Name: c.Name, Expr: old}, nil
}

// ---- Reorder / Suppress / Rename --------------------------------------------------

type Reorder struct {
	ID    string `json:"id"`
	Index int    `json:"index"`
}

func (*Reorder) Type() string { return "Reorder" }

func (c *Reorder) Apply(g *cadir.Graph) (Command, error) {
	from := g.Index(c.ID)
	if from < 0 {
		return nil, fmt.Errorf("feature %q does not exist", c.ID)
	}
	if c.Index < 0 || c.Index >= len(g.Nodes) {
		return nil, fmt.Errorf("index %d out of range [0,%d)", c.Index, len(g.Nodes))
	}
	n := g.Nodes[from]
	g.Nodes = append(g.Nodes[:from], g.Nodes[from+1:]...)
	g.Nodes = append(g.Nodes, nil)
	copy(g.Nodes[c.Index+1:], g.Nodes[c.Index:])
	g.Nodes[c.Index] = n
	return &Reorder{ID: c.ID, Index: from}, nil
}

type Suppress struct {
	ID         string `json:"id"`
	Suppressed bool   `json:"suppressed"`
}

func (*Suppress) Type() string { return "Suppress" }

func (c *Suppress) Apply(g *cadir.Graph) (Command, error) {
	n, err := mustNode(g, c.ID)
	if err != nil {
		return nil, err
	}
	old := n.Suppressed
	n.Suppressed = c.Suppressed
	return &Suppress{ID: c.ID, Suppressed: old}, nil
}

type Rename struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func (*Rename) Type() string { return "Rename" }

func (c *Rename) Apply(g *cadir.Graph) (Command, error) {
	n, err := mustNode(g, c.ID)
	if err != nil {
		return nil, err
	}
	old := n.Metadata.Name
	n.Metadata.Name = c.Name
	return &Rename{ID: c.ID, Name: old}, nil
}

// ---- Appearance (presentation only; never affects geometry) ------------------------

// IsAppearance reports whether a command type only edits presentation metadata (appearance
// and design-tree grouping).
func IsAppearance(t string) bool {
	switch t {
	case "SetColor", "SetOpacity", "SetVisibility", "ResetAppearance", "SetMaterial", "RemoveMaterial", "SetMaterialScale", "SetGroup",
		"SetLockProportions":
		return true
	}
	return false
}

// edit applies f to the node's appearance and normalizes an empty result back to nil.
func editAppearance(g *cadir.Graph, id string, f func(a *cadir.Appearance)) (old *cadir.Appearance, err error) {
	n, err := mustNode(g, id)
	if err != nil {
		return nil, err
	}
	old = n.Appearance.Clone()
	a := n.Appearance.Clone()
	if a == nil {
		a = &cadir.Appearance{}
	}
	f(a)
	if a.IsZero() {
		a = nil
	}
	n.Appearance = a
	return old, nil
}

// SetColor sets the color ("#RRGGBB"); an empty Color clears the override.
type SetColor struct {
	ID    string `json:"id"`
	Color string `json:"color"`
}

func (*SetColor) Type() string { return "SetColor" }

func (c *SetColor) Apply(g *cadir.Graph) (Command, error) {
	col := ""
	if c.Color != "" {
		var err error
		if col, err = cadir.NormalizeColor(c.Color); err != nil {
			return nil, err
		}
	}
	var prev string
	if _, err := editAppearance(g, c.ID, func(a *cadir.Appearance) { prev = a.Color; a.Color = col }); err != nil {
		return nil, err
	}
	return &SetColor{ID: c.ID, Color: prev}, nil
}

// SetOpacity sets opacity in [0,1]; nil clears the override.
type SetOpacity struct {
	ID      string   `json:"id"`
	Opacity *float64 `json:"opacity"`
}

func (*SetOpacity) Type() string { return "SetOpacity" }

func (c *SetOpacity) Apply(g *cadir.Graph) (Command, error) {
	if c.Opacity != nil {
		if err := cadir.ValidateOpacity(*c.Opacity); err != nil {
			return nil, err
		}
	}
	var prev *float64
	if _, err := editAppearance(g, c.ID, func(a *cadir.Appearance) {
		prev = a.Opacity
		a.Opacity = c.Opacity
	}); err != nil {
		return nil, err
	}
	return &SetOpacity{ID: c.ID, Opacity: prev}, nil
}

// SetVisibility shows or hides the object; nil clears the override.
type SetVisibility struct {
	ID      string `json:"id"`
	Visible *bool  `json:"visible"`
}

func (*SetVisibility) Type() string { return "SetVisibility" }

func (c *SetVisibility) Apply(g *cadir.Graph) (Command, error) {
	var prev *bool
	if _, err := editAppearance(g, c.ID, func(a *cadir.Appearance) {
		prev = a.Visible
		a.Visible = c.Visible
	}); err != nil {
		return nil, err
	}
	return &SetVisibility{ID: c.ID, Visible: prev}, nil
}

// SetMaterial assigns a visual material by id (empty clears it). A material change never
// touches the kernel; the library is resolved at render time, so unknown ids are allowed.
// ClearColor also drops the color override so the material's own color shows (the usual
// intent of "make it aluminum"); the inverse restores the whole previous appearance.
type SetMaterial struct {
	ID         string `json:"id"`
	MaterialID string `json:"material_id"`
	ClearColor bool   `json:"clear_color,omitempty"`
}

func (*SetMaterial) Type() string { return "SetMaterial" }

func (c *SetMaterial) Apply(g *cadir.Graph) (Command, error) {
	old, err := editAppearance(g, c.ID, func(a *cadir.Appearance) {
		a.MaterialID = c.MaterialID
		if c.ClearColor {
			a.Color = ""
		}
	})
	if err != nil {
		return nil, err
	}
	return &ResetAppearance{ID: c.ID, Restore: old}, nil
}

// RemoveMaterial clears the visual material. Its inverse is a SetMaterial restoring it.
type RemoveMaterial struct {
	ID string `json:"id"`
}

func (*RemoveMaterial) Type() string { return "RemoveMaterial" }

func (c *RemoveMaterial) Apply(g *cadir.Graph) (Command, error) {
	var prev string
	if _, err := editAppearance(g, c.ID, func(a *cadir.Appearance) { prev = a.MaterialID; a.MaterialID = "" }); err != nil {
		return nil, err
	}
	return &SetMaterial{ID: c.ID, MaterialID: prev}, nil
}

// SetMaterialScale sets the texture scale multiplier (>0); nil clears the override.
type SetMaterialScale struct {
	ID    string   `json:"id"`
	Scale *float64 `json:"scale"`
}

func (*SetMaterialScale) Type() string { return "SetMaterialScale" }

func (c *SetMaterialScale) Apply(g *cadir.Graph) (Command, error) {
	if c.Scale != nil {
		if err := cadir.ValidateTextureScale(*c.Scale); err != nil {
			return nil, err
		}
	}
	var prev *float64
	if _, err := editAppearance(g, c.ID, func(a *cadir.Appearance) { prev = a.TextureScale; a.TextureScale = c.Scale }); err != nil {
		return nil, err
	}
	return &SetMaterialScale{ID: c.ID, Scale: prev}, nil
}

// ResetAppearance replaces the whole appearance with Restore (nil = defaults). Its
// inverse carries the previous appearance, so undo restores it exactly.
type ResetAppearance struct {
	ID      string            `json:"id"`
	Restore *cadir.Appearance `json:"restore,omitempty"`
}

func (*ResetAppearance) Type() string { return "ResetAppearance" }

func (c *ResetAppearance) Apply(g *cadir.Graph) (Command, error) {
	n, err := mustNode(g, c.ID)
	if err != nil {
		return nil, err
	}
	old := n.Appearance.Clone()
	if c.Restore.IsZero() {
		n.Appearance = nil
	} else {
		n.Appearance = c.Restore.Clone()
	}
	return &ResetAppearance{ID: c.ID, Restore: old}, nil
}

// ---- SetGroup (design-tree organization; never affects geometry) -------------------

// SetGroup puts a node in a named group; an empty Group removes it from its group.
type SetGroup struct {
	ID    string `json:"id"`
	Group string `json:"group"`
}

func (*SetGroup) Type() string { return "SetGroup" }

func (c *SetGroup) Apply(g *cadir.Graph) (Command, error) {
	n, err := mustNode(g, c.ID)
	if err != nil {
		return nil, err
	}
	name, err := cadir.NormalizeGroup(c.Group)
	if err != nil {
		return nil, err
	}
	old := n.Group
	n.Group = name
	return &SetGroup{ID: c.ID, Group: old}, nil
}

// ---- RestoreSnapshot (checkpoint restore) ------------------------------------------

type RestoreSnapshot struct {
	Graph *cadir.Graph `json:"graph"`
	From  string       `json:"from,omitempty"` // checkpoint id, for explanations
}

func (*RestoreSnapshot) Type() string { return "RestoreSnapshot" }

func (c *RestoreSnapshot) Apply(g *cadir.Graph) (Command, error) {
	if c.Graph == nil {
		return nil, fmt.Errorf("snapshot is required")
	}
	old := g.Clone()
	next := c.Graph.Clone()
	if err := next.Validate(); err != nil {
		return nil, err
	}
	g.Units, g.Nodes = next.Units, next.Nodes
	return &RestoreSnapshot{Graph: old, From: c.From}, nil
}

// ---- SetLockProportions (dimension-editing preference; never affects geometry) ------

// SetLockProportions turns proportional dimension editing of a body on or off.
type SetLockProportions struct {
	ID      string `json:"id"`
	Enabled bool   `json:"enabled"`
}

func (*SetLockProportions) Type() string { return "SetLockProportions" }

func (c *SetLockProportions) Apply(g *cadir.Graph) (Command, error) {
	n, err := mustNode(g, c.ID)
	if err != nil {
		return nil, err
	}
	old := n.LockProportions
	n.LockProportions = c.Enabled
	return &SetLockProportions{ID: c.ID, Enabled: old}, nil
}
