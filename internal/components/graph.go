package components

import (
	"fmt"
	"strings"

	"github.com/accelone/simoncad/internal/cadir"
	"github.com/accelone/simoncad/internal/geometry/features"
)

// DependencyGraph answers questions about how projects compose, reading only project.json and
// model.json parameters: it never builds geometry, so a cycle check costs no kernel time.
type DependencyGraph struct{ lib *Library }

func NewDependencyGraph(lib *Library) *DependencyGraph { return &DependencyGraph{lib: lib} }

// DirectDeps lists the project ids a project references directly.
func (d *DependencyGraph) DirectDeps(projectID string) ([]string, error) {
	dir, err := d.lib.Path(projectID)
	if err != nil {
		return nil, err
	}
	_, g, _, err := d.lib.LoadGraph(dir)
	if err != nil {
		return nil, err
	}
	return directDeps(g), nil
}

func directDeps(g *cadir.Graph) []string {
	seen := map[string]bool{}
	var out []string
	for _, n := range g.Nodes {
		if n.Type != features.TypeComponentInstance {
			continue
		}
		if id, _ := n.Parameters[features.ParamComponent].(string); id != "" && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

// Walk visits the transitive component dependencies of a project, depth first, each id once.
// A cycle stops the walk with the offending chain.
func (d *DependencyGraph) Walk(projectID string, visit func(id string, depth int) error) error {
	seen := map[string]bool{}
	var rec func(id string, depth int, path []string) error
	rec = func(id string, depth int, path []string) error {
		for _, p := range path {
			if p == id {
				return cycleError(append(path, id))
			}
		}
		if seen[id] {
			return nil
		}
		seen[id] = true
		if depth > 0 {
			if err := visit(id, depth); err != nil {
				return err
			}
		}
		deps, err := d.DirectDeps(id)
		if err != nil {
			if depth == 0 {
				return err
			}
			return nil // a missing nested component is reported when it is resolved, not here
		}
		for _, dep := range deps {
			if err := rec(dep, depth+1, append(path, id)); err != nil {
				return err
			}
		}
		return nil
	}
	return rec(projectID, 0, nil)
}

// CheckInsert reports whether inserting componentID into the project parentID would create a
// circular dependency. There is deliberately no nesting depth limit: only cycles are refused.
// parentID may be empty for a project that has not been saved yet — it then cannot be the
// target of any reference, so only the component's own subtree is checked.
func (d *DependencyGraph) CheckInsert(parentID, componentID string) error {
	if componentID == "" {
		return fmt.Errorf("no component given")
	}
	if parentID != "" && parentID == componentID {
		return cycleError([]string{parentID, componentID})
	}
	if parentID == "" {
		return d.Walk(componentID, func(string, int) error { return nil })
	}
	// Reaching parentID from componentID is exactly the cycle the new edge would close.
	var found bool
	err := d.Walk(componentID, func(id string, _ int) error {
		if id == parentID {
			found = true
		}
		return nil
	})
	if err != nil {
		return err
	}
	if found || componentID == parentID {
		return cycleError([]string{parentID, componentID, parentID})
	}
	return nil
}

func cycleError(path []string) error {
	return fmt.Errorf("circular component reference: %s", strings.Join(path, " → "))
}
