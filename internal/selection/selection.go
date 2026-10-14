// Package selection models the visual-selection context that accompanies every agent
// request: "put four M3 holes here" resolves against the selected face. Each entity is a
// semantic selector plus geometric descriptors, so the agent never sees raw topology IDs.
package selection

import (
	"encoding/json"
	"fmt"

	"github.com/accelone/simoncad/internal/selectors"
)

// Entity is one selected thing.
type Entity = selectors.Descriptor

// Context is the ordered current selection.
type Context struct {
	Entities []Entity `json:"entities"`
}

// TopologyFn looks up a feature's topology in the current model.
type TopologyFn func(featureID string) (*selectors.Topology, bool)

// Resolve parses and resolves selector strings against the model.
func Resolve(raw []string, topo TopologyFn) (Context, error) {
	var c Context
	for _, r := range raw {
		sel, err := selectors.Parse(r)
		if err != nil {
			return Context{}, err
		}
		t, ok := topo(sel.Feature())
		if !ok {
			return Context{}, fmt.Errorf("selection %s: feature %q has no geometry", sel.String(), sel.Feature())
		}
		d, err := selectors.ResolveTopo(sel, t)
		if err != nil {
			return Context{}, err
		}
		c.Entities = append(c.Entities, d)
	}
	return c, nil
}

func (c Context) Empty() bool { return len(c.Entities) == 0 }

// Selectors returns the canonical selector strings.
func (c Context) Selectors() []string {
	out := make([]string, len(c.Entities))
	for i, e := range c.Entities {
		out[i] = e.Selector
	}
	return out
}

// JSON is the deterministic serialization placed in the agent context.
func (c Context) JSON() string {
	b, _ := json.Marshal(c) // compact: this is model context, not for people
	return string(b)
}
