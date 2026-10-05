// Package features holds the primitive feature builders that turn resolved CAD-IR
// node parameters into kernel geometry. Builders never mutate their inputs.
package features

import (
	"fmt"
	"math"
	"sort"

	"github.com/accelone/simoncad/internal/cadir"
	"github.com/accelone/simoncad/internal/geometry/api"
	"github.com/accelone/simoncad/internal/selectors"
)

// Params are a node's parameters after expression evaluation.
type Params map[string]any

func (p Params) Num(name string) (float64, error) {
	v, ok := p[name]
	if !ok {
		return 0, fmt.Errorf("missing parameter %q", name)
	}
	f, ok := v.(float64)
	if !ok || math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, fmt.Errorf("parameter %q must be a finite number", name)
	}
	return f, nil
}

func (p Params) NumOr(name string, def float64) (float64, error) {
	if _, ok := p[name]; !ok {
		return def, nil
	}
	return p.Num(name)
}

func (p Params) Str(name string) string { s, _ := p[name].(string); return s }

func (p Params) Bool(name string) bool { b, _ := p[name].(bool); return b }

func (p Params) Positive(name string) (float64, error) {
	f, err := p.Num(name)
	if err != nil {
		return 0, err
	}
	if !(f > 0) {
		return 0, fmt.Errorf("parameter %q must be > 0 (got %g)", name, f)
	}
	return f, nil
}

// Body is a built shape with its cached properties.
type Body struct {
	Shape *api.Shape
	Props api.Props
}

func (b *Body) Bounds() selectors.Bounds {
	return selectors.Bounds{
		Min: [3]float64{b.Props.MinX, b.Props.MinY, b.Props.MinZ},
		Max: [3]float64{b.Props.MaxX, b.Props.MaxY, b.Props.MaxZ},
	}
}

// Ctx is everything a builder may read. Bodies holds every already-built node.
type Ctx struct {
	K      *api.Kernel
	Node   *cadir.Node
	P      Params
	Bodies map[string]*Body
	// ParamsOf returns the resolved parameters of an already-built node.
	ParamsOf func(id string) (Params, bool)
	NodeOf   func(id string) *cadir.Node
}

func (c *Ctx) body(id string) (*Body, error) {
	if id == "" {
		return nil, fmt.Errorf("missing node reference")
	}
	b, ok := c.Bodies[id]
	if !ok || b == nil {
		return nil, fmt.Errorf("node %q has no geometry", id)
	}
	return b, nil
}

// Feature describes one buildable node type.
type Feature struct {
	Type  string
	Build func(*Ctx) (*api.Shape, error)
	// Tool is set for features that apply a tool shape to a target (hole, extrude). Patterns
	// reuse it. subtract=false means union.
	Tool func(*Ctx) (tool *api.Shape, subtract bool, err error)
	// Target returns the node ID this feature modifies ("" for primitives); used for
	// suppression pass-through.
	Target func(Params) string
}

var registry = map[string]*Feature{}

func register(f *Feature) { registry[f.Type] = f }

func Lookup(t string) (*Feature, bool) { f, ok := registry[t]; return f, ok }

func Types() []string {
	out := make([]string, 0, len(registry))
	for t := range registry {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

func release(ss ...*api.Shape) {
	for _, s := range ss {
		s.Release()
	}
}

// applyTool combines target with tool, releasing neither input.
func applyTool(k *api.Kernel, target *api.Shape, tool *api.Shape, subtract bool) (*api.Shape, error) {
	if subtract {
		return k.Subtract(target, tool)
	}
	return k.Union(target, tool)
}

func targetOf(p Params) string { return p.Str("target") }

func degToRad(d float64) float64 { return d * math.Pi / 180 }
