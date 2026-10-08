package tools

import (
	"fmt"
	"sort"
	"strings"

	"github.com/accelone/simoncad/internal/commands"
	"github.com/accelone/simoncad/internal/geometry/features"
	"github.com/accelone/simoncad/internal/selectors"
)

// Deleting is always a ChangeSet of RemoveFeature commands over CAD-IR features. B-Rep
// topology (faces, edges, vertices) is derived and is never edited: a selected face/edge/
// vertex resolves to its owning feature, which the caller may then choose to remove.

// SubEntity is a selected face, edge or vertex and the feature that owns it.
type SubEntity struct {
	Selector string `json:"selector"`
	Kind     string `json:"kind"`
	Owner    string `json:"owner"`
}

// DeleteAnalysis is what a delete request would do, computed without changing anything.
type DeleteAnalysis struct {
	Targets     []string    `json:"targets"`                // features named directly (or owning a selected body)
	SubEntities []SubEntity `json:"sub_entities,omitempty"` // selected faces/edges/vertices
	Dependents  []string    `json:"dependents,omitempty"`   // other features that depend on the targets (transitively)
}

// analyzeDelete resolves the request: explicit `features`, or the current selection.
// Sub-entity owners are only added to the targets when resolveOwner is set.
func analyzeDelete(env Env, a Args) (*DeleteAnalysis, error) {
	out := &DeleteAnalysis{}
	seen := map[string]bool{}
	addTarget := func(id string) error {
		if seen[id] {
			return nil
		}
		if _, err := requireFeature(env, id); err != nil {
			return err
		}
		seen[id] = true
		out.Targets = append(out.Targets, id)
		return nil
	}
	if raw, ok := a["features"].([]any); ok {
		for _, v := range raw {
			s, _ := v.(string)
			if err := addTarget(s); err != nil {
				return nil, err
			}
		}
	} else {
		refs := env.SelectionRefs()
		if len(refs) == 0 {
			return nil, fmt.Errorf("nothing to delete: pass `features` or select objects first")
		}
		for _, ref := range refs {
			sel, err := selectors.Parse(ref)
			if err != nil {
				return nil, err
			}
			if sel.Kind == "body" {
				if err := addTarget(sel.Feature()); err != nil {
					return nil, err
				}
				continue
			}
			if _, err := requireFeature(env, sel.Feature()); err != nil {
				return nil, err
			}
			out.SubEntities = append(out.SubEntities, SubEntity{Selector: sel.String(), Kind: strings.TrimSuffix(sel.Kind, "s"), Owner: sel.Feature()})
		}
		if a.Bool("resolve_owner") {
			for _, s := range out.SubEntities {
				if err := addTarget(s.Owner); err != nil {
					return nil, err
				}
			}
		}
	}
	if len(out.Targets) == 0 && len(out.SubEntities) == 0 {
		return nil, fmt.Errorf("nothing to delete")
	}
	out.Dependents = transitiveDependents(env, out.Targets)
	return out, nil
}

// transitiveDependents lists features outside targets that depend on any target, directly
// or through other dependents, in graph order.
func transitiveDependents(env Env, targets []string) []string {
	g := env.Graph()
	in := map[string]bool{}
	for _, t := range targets {
		in[t] = true
	}
	var out []string
	for changed := true; changed; {
		changed = false
		for _, n := range g.Nodes {
			if in[n.ID] {
				continue
			}
			for id := range in {
				if contains(g.Dependents(id), n.ID) {
					in[n.ID] = true
					out = append(out, n.ID)
					changed = true
					break
				}
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return g.Index(out[i]) < g.Index(out[j]) })
	return out
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// removalOrder orders ids so that every feature is removed before anything it depends on
// (RemoveFeature refuses to remove a feature that still has dependents).
func removalOrder(env Env, ids []string) []string {
	g := env.Graph()
	left := map[string]bool{}
	for _, id := range ids {
		left[id] = true
	}
	var out []string
	for len(left) > 0 {
		var pick string
		for id := range left { // deterministic: highest graph index among the ready ones
			ready := true
			for _, d := range g.Dependents(id) {
				if left[d] {
					ready = false
					break
				}
			}
			if ready && (pick == "" || g.Index(id) > g.Index(pick)) {
				pick = id
			}
		}
		if pick == "" { // a cycle cannot exist in a valid graph; fall back to graph order
			for id := range left {
				if pick == "" || g.Index(id) > g.Index(pick) {
					pick = id
				}
			}
		}
		out = append(out, pick)
		delete(left, pick)
	}
	return out
}

func quoteList(ids []string) string { return strings.Join(ids, ", ") }

func deleteTools() []*Tool {
	targetProps := props{
		"features": array("Feature ids to delete; omit to delete the current selection", text("Feature id"), 1, 64),
		"resolve_owner": boolean("When faces/edges/vertices are selected: true deletes the feature that owns them. " +
			"Topology itself is never edited; without this they are rejected with an explanation."),
		"cascade": boolean("true also deletes every feature that depends on the targets; without it the call fails when dependents exist"),
	}
	return []*Tool{
		{
			Name: "delete_objects",
			Description: "Delete the selected objects ('delete this', 'remove the selected part', 'delete these objects') or the listed features, as ONE ChangeSet that can be undone. " +
				"Faces, edges and vertices cannot be deleted: they belong to a feature, so the call is rejected and names the owning feature — ask the user whether to delete that feature (resolve_owner=true) or modify it instead. " +
				"If other features depend on the targets the call fails and lists them; retry with cascade=true only when the user accepts deleting them too. " +
				"Deleting a feature that is an input of another (e.g. a box inside a union) is refused unless cascaded.",
			Capability: capGeometry,
			Schema:     object(withIntent(targetProps)),
			Plan: func(env Env, a Args) (*Plan, error) {
				an, err := analyzeDelete(env, a)
				if err != nil {
					return nil, err
				}
				if len(an.SubEntities) > 0 && !a.Bool("resolve_owner") {
					var owners []string
					for _, s := range an.SubEntities {
						if !contains(owners, s.Owner) {
							owners = append(owners, s.Owner)
						}
					}
					return nil, fmt.Errorf("a %s cannot be deleted directly: B-Rep topology is derived from CAD-IR features. %s belongs to feature %s. "+
						"Offer the user to delete feature %s (delete_objects with features=[%q] or resolve_owner=true) or to modify it (modify_parameter, hole, fillet, ...) instead",
						an.SubEntities[0].Kind, an.SubEntities[0].Selector, quoteList(owners), quoteList(owners), owners[0])
				}
				if len(an.Dependents) > 0 && !a.Bool("cascade") {
					return nil, fmt.Errorf("%s is used by %s; delete those too with cascade=true, or remove/re-point them first", quoteList(an.Targets), quoteList(an.Dependents))
				}
				ids := append(append([]string(nil), an.Targets...), an.Dependents...)
				p := &Plan{
					Intent:      intentOr(a, "Delete "+quoteList(ids)),
					Explanation: fmt.Sprintf("Removes %s.", quoteList(ids)),
				}
				for _, id := range removalOrder(env, ids) {
					p.Commands = append(p.Commands, &commands.RemoveFeature{ID: id})
				}
				if len(an.Dependents) > 0 {
					p.Warnings = append(p.Warnings, fmt.Sprintf("also removes %s, which depend on %s", quoteList(an.Dependents), quoteList(an.Targets)))
				}
				if g := env.Graph(); len(an.Targets) > 0 {
					// A consumed input reappears as a body once its consumer is gone.
					var freed []string
					for _, id := range ids {
						if !features.Consumes(g.Get(id).Type) {
							continue // a copy never hid its source
						}
						for _, d := range g.Get(id).Dependencies {
							if g.Get(d) != nil && !contains(ids, d) && !contains(freed, d) {
								freed = append(freed, d)
							}
						}
					}
					sort.Strings(freed)
					if len(freed) > 0 {
						p.Warnings = append(p.Warnings, fmt.Sprintf("inputs %s are no longer consumed and become standalone bodies", quoteList(freed)))
					}
				}
				return p, nil
			},
		},
		{
			Name:        "check_delete",
			Description: "Dry run of delete_objects: reports which features would be deleted, which selected faces/edges/vertices resolve to which owning feature, and which other features depend on the targets. Changes nothing.",
			Capability:  capGeometry,
			Schema:      object(targetProps),
			Query: func(env Env, a Args) (any, error) {
				an, err := analyzeDelete(env, a)
				if err != nil {
					return nil, err
				}
				return an, nil
			},
		},
	}
}

func withIntent(p props) props {
	p["intent"] = text("Why")
	return p
}
