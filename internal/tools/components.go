package tools

import (
	"fmt"
	"sort"
	"strings"

	"github.com/accelone/simoncad/internal/cadir"
	"github.com/accelone/simoncad/internal/commands"
	"github.com/accelone/simoncad/internal/components"
	"github.com/accelone/simoncad/internal/geometry/features"
	"github.com/accelone/simoncad/internal/selectors"
)

// Components are reusable projects. Inserting one adds a single component_instance node: the
// geometry stays in the component's own project and is resolved at rebuild time, so four
// wheels are four references, not four copies.

func componentTools() []*Tool {
	placement := props{
		"dx": number("offset along X (mm)"), "dy": number("offset along Y (mm)"), "dz": number("offset along Z (mm)"),
		"angle_deg": number("rotation about the axis (degrees)"),
		"ax":        number("rotation axis X (default 0)"), "ay": number("rotation axis Y (default 0)"),
		"az": number("rotation axis Z (default 1)"),
		"ox": number("rotation origin X"), "oy": number("rotation origin Y"), "oz": number("rotation origin Z"),
		"sx": positive("scale along X (default 1)"), "sy": positive("scale along Y (default 1)"),
		"sz": positive("scale along Z (default 1)"),
	}
	insertProps := props{
		"component": text("the component's project id, or its name when unambiguous"),
		"id":        text("feature id for the new instance"),
		"name":      text("display name for the instance (e.g. \"Wheel FL\")"),
		"intent":    text("why this instance exists"),
	}
	for k, v := range placement {
		insertProps[k] = v
	}

	return []*Tool{{
		Name:       "list_components",
		Capability: capGeometry,
		Description: "List the reusable components in the library. A component is a project saved as reusable; " +
			"insert_component places a reference to one in the current project.",
		Schema: object(props{}),
		Query:  func(env Env, a Args) (any, error) { return componentList(env.Components().List()), nil },
	}, {
		Name:        "search_components",
		Capability:  capGeometry,
		Description: "Search the component library by name, description or tag.",
		Schema:      object(props{"query": text("text to look for")}, "query"),
		Query: func(env Env, a Args) (any, error) {
			return componentList(env.Components().Search(a.Str("query"))), nil
		},
	}, {
		Name:       "open_component",
		Capability: capGeometry,
		Description: "Look up one component and return its details and project path. It does not switch projects: " +
			"opening a component for editing is something the user does in the Components panel.",
		Schema: object(props{"component": text("the component's project id or name")}, "component"),
		Query: func(env Env, a Args) (any, error) {
			c, err := env.Components().Find(a.Str("component"))
			if err != nil {
				return nil, err
			}
			return c, nil
		},
	}, {
		Name:       "create_component",
		Capability: capGeometry,
		Description: "Turn the selected features (or the ones named in `features`) into a reusable component: " +
			"they move into a new reusable project and are replaced in place by a single instance that references it. " +
			"The selection must be self-contained — nothing outside it may depend on it, and it may not depend on anything outside.",
		Schema: object(props{
			"name":        text("the component's name"),
			"description": text("what the component is for"),
			"tags":        array("tags to find the component by", map[string]any{"type": "string"}, 0, 16),
			"features":    array("feature ids to move into the component (default: the selection)", map[string]any{"type": "string"}, 1, 200),
			"id":          text("feature id for the instance left behind"),
			"intent":      text("why this component exists"),
		}, "name"),
		Plan: planCreateComponent,
	}, {
		Name:        "insert_component",
		Capability:  capGeometry,
		Description: "Place an instance of a library component in the current project. Call it once per instance.",
		Schema:      object(insertProps, "component"),
		Plan:        planInsertComponent,
	}, {
		Name:       "sync_component",
		Capability: capGeometry,
		Description: "Re-pin component instances to the current state of their component, after the component was edited. " +
			"With no arguments it syncs every instance that has fallen behind.",
		Schema: object(props{
			"instances": array("instance feature ids to sync (default: all out-of-date instances)",
				map[string]any{"type": "string"}, 1, 200),
		}),
		Plan: planSyncComponent,
	}, {
		Name:       "detach_component",
		Capability: capGeometry,
		Description: "Replace a component instance with its own local copy of the geometry. After detaching, " +
			"changes to the component no longer affect it.",
		Schema: object(props{
			"instance": text("the component instance's feature id"),
			"intent":   text("why it is being detached"),
		}, "instance"),
		Plan: planDetachComponent,
	}}
}

func componentList(cs []*components.Component) any {
	if cs == nil {
		return []*components.Component{}
	}
	return cs
}

// cloneNode deep-copies a node through the graph's canonical encoding, so a detached or moved
// feature never shares maps or slices with the one it came from.
func cloneNode(n *cadir.Node) *cadir.Node {
	g := &cadir.Graph{Units: "mm", Nodes: []*cadir.Node{n}}
	return g.Clone().Nodes[0]
}

// ---- create -----------------------------------------------------------------------------

func planCreateComponent(env Env, a Args) (*Plan, error) {
	g := env.Graph()
	ids, err := targetFeatures(env, a)
	if err != nil {
		return nil, err
	}
	if err := checkSelfContained(g, ids); err != nil {
		return nil, err
	}
	// Move the nodes in graph order, so the component's own evaluation order is preserved.
	in := map[string]bool{}
	for _, id := range ids {
		in[id] = true
	}
	var moved []*cadir.Node
	for _, n := range g.Nodes {
		if in[n.ID] {
			moved = append(moved, cloneNode(n))
		}
	}
	name := strings.TrimSpace(a.Str("name"))
	projectID, err := env.PublishComponent(name, a.Str("description"), stringList(a, "tags"), moved)
	if err != nil {
		return nil, err
	}
	hash, err := env.ComponentResolver().Hash(projectID, "")
	if err != nil {
		return nil, err
	}

	instanceID, err := uniqueID(g, Args{"id": a.Str("id"), "name": name}, "component")
	if err != nil {
		return nil, err
	}
	params := map[string]any{features.ParamComponent: projectID, features.ParamVersion: "latest",
		features.ParamContentHash: hash}
	node := newNode(env, Args{"name": name, "intent": a.Str("intent")}, instanceID,
		features.TypeComponentInstance, params)
	// The instance stands in for the geometry it replaces, so it keeps its look and grouping.
	if last := g.Get(moved[len(moved)-1].ID); last != nil {
		node.Appearance, node.Group = last.Appearance.Clone(), last.Group
	}

	cmds := make([]commands.Command, 0, len(moved)+1)
	for i := len(moved) - 1; i >= 0; i-- { // remove dependents before their dependencies
		cmds = append(cmds, &commands.RemoveFeature{ID: moved[i].ID})
	}
	cmds = append(cmds, &commands.AddFeature{Node: node, Index: g.Index(moved[0].ID)})
	return &Plan{
		Commands: cmds,
		Intent:   fmt.Sprintf("Create the component %q from %d feature(s)", name, len(moved)),
		Explanation: fmt.Sprintf("Moved %s into the reusable project %s and replaced them with the instance %q. "+
			"The component project stays on disk even if this change is undone.",
			strings.Join(idsOfNodes(moved), ", "), projectID, instanceID),
	}, nil
}

// targetFeatures resolves the explicit `features` argument or the current selection.
func targetFeatures(env Env, a Args) ([]string, error) {
	var ids []string
	seen := map[string]bool{}
	add := func(id string) error {
		if id == "" || seen[id] {
			return nil
		}
		if _, err := requireFeature(env, id); err != nil {
			return err
		}
		seen[id] = true
		ids = append(ids, id)
		return nil
	}
	if raw, ok := a["features"].([]any); ok {
		for _, v := range raw {
			s, _ := v.(string)
			if err := add(s); err != nil {
				return nil, err
			}
		}
	} else {
		refs := env.SelectionRefs()
		if len(refs) == 0 {
			return nil, fmt.Errorf("nothing selected: select the objects to turn into a component, or pass `features`")
		}
		for _, ref := range refs {
			sel, err := selectors.Parse(ref)
			if err != nil {
				return nil, err
			}
			if err := add(sel.Feature()); err != nil {
				return nil, err
			}
		}
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("no features to put in the component")
	}
	return ids, nil
}

// checkSelfContained refuses a selection that is not closed over EffectiveDeps in both
// directions. Upward: the extracted project would fail validation and could never be reopened.
// Downward: RemoveFeature refuses while dependents exist, so the whole ChangeSet would die.
// Expressions and selector references crossing the cut are unrecoverable either way — there is
// no rewriter for an expression, and no stable name for a face across the boundary.
func checkSelfContained(g *cadir.Graph, ids []string) error {
	in := map[string]bool{}
	for _, id := range ids {
		in[id] = true
	}
	var outward, inward []string
	for _, n := range g.Nodes {
		for _, d := range cadir.EffectiveDeps(n) {
			switch {
			case in[n.ID] && !in[d]:
				outward = append(outward, fmt.Sprintf("%s → %s", n.ID, d))
			case !in[n.ID] && in[d]:
				inward = append(inward, fmt.Sprintf("%s → %s", n.ID, d))
			}
		}
	}
	sort.Strings(outward)
	sort.Strings(inward)
	if len(outward) > 0 {
		return fmt.Errorf("the selection depends on features outside it (%s): add them to the selection, "+
			"a component has to be self-contained", strings.Join(outward, ", "))
	}
	if len(inward) > 0 {
		return fmt.Errorf("features outside the selection depend on it (%s): add them to the selection or detach them first",
			strings.Join(inward, ", "))
	}
	return nil
}

// ---- insert -----------------------------------------------------------------------------

func planInsertComponent(env Env, a Args) (*Plan, error) {
	c, err := env.Components().Find(a.Str("component"))
	if err != nil {
		return nil, err
	}
	if err := env.ComponentDeps().CheckInsert(env.ProjectID(), c.ProjectID); err != nil {
		return nil, err
	}
	hash, err := env.ComponentResolver().Hash(c.ProjectID, "")
	if err != nil {
		return nil, err
	}
	g := env.Graph()
	name := a.Str("name")
	if name == "" {
		name = c.Name
	}
	id, err := uniqueID(g, Args{"id": a.Str("id"), "name": name}, "component")
	if err != nil {
		return nil, err
	}
	params := map[string]any{features.ParamComponent: c.ProjectID, features.ParamVersion: "latest",
		features.ParamContentHash: hash}
	copyOpt(params, a, "dx", "dy", "dz", "angle_deg", "ax", "ay", "az", "ox", "oy", "oz", "sx", "sy", "sz")
	node := newNode(env, Args{"name": name, "intent": a.Str("intent")}, id, features.TypeComponentInstance, params)
	return &Plan{
		Commands:    []commands.Command{&commands.AddFeature{Node: node, Index: -1}},
		Intent:      fmt.Sprintf("Place an instance of %q", c.Name),
		Explanation: fmt.Sprintf("%q references the component project %s; its geometry is not copied.", id, c.ProjectID),
		LowRisk:     true,
	}, nil
}

// ---- sync -------------------------------------------------------------------------------

func planSyncComponent(env Env, a Args) (*Plan, error) {
	g := env.Graph()
	stale := env.ComponentResolver().OutOfDate(g)
	wanted := stringList(a, "instances")
	if len(wanted) > 0 {
		only := map[string]bool{}
		for _, id := range wanted {
			if _, err := requireFeature(env, id); err != nil {
				return nil, err
			}
			only[id] = true
		}
		for id := range stale {
			if !only[id] {
				delete(stale, id)
			}
		}
		for _, id := range wanted {
			if _, ok := stale[id]; !ok {
				return nil, fmt.Errorf("%q is already up to date with its component", id)
			}
		}
	}
	if len(stale) == 0 {
		return nil, fmt.Errorf("every component instance is already up to date")
	}
	ids := make([]string, 0, len(stale))
	for id := range stale {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	cmds := make([]commands.Command, 0, len(ids))
	for _, id := range ids {
		cmds = append(cmds, &commands.SetParameter{ID: id, Name: features.ParamContentHash, Value: stale[id]})
	}
	return &Plan{
		Commands:    cmds,
		Intent:      fmt.Sprintf("Update %d component instance(s) to the latest component", len(ids)),
		Explanation: "Re-pinned " + strings.Join(ids, ", ") + " to the current content of their components.",
		LowRisk:     true,
	}, nil
}

// ---- detach -----------------------------------------------------------------------------

func planDetachComponent(env Env, a Args) (*Plan, error) {
	g := env.Graph()
	id := a.Str("instance")
	inst, err := requireFeature(env, id)
	if err != nil {
		return nil, err
	}
	if inst.Type != features.TypeComponentInstance {
		return nil, fmt.Errorf("%q is not a component instance", id)
	}
	if dep := g.Dependents(id); len(dep) > 0 {
		return nil, fmt.Errorf("%s depend(s) on %q: detaching would have to rebuild them too. "+
			"Remove or retarget them first", strings.Join(dep, ", "), id)
	}
	projectID, _ := inst.Parameters[features.ParamComponent].(string)
	dir, err := env.Components().Path(projectID)
	if err != nil {
		return nil, err
	}
	_, src, _, err := env.Components().LoadGraph(dir)
	if err != nil {
		return nil, err
	}
	order, err := src.TopoOrder()
	if err != nil {
		return nil, err
	}
	if len(order) == 0 {
		return nil, fmt.Errorf("component %q has no features to detach", projectID)
	}
	rename, err := detachIDs(g, id, order)
	if err != nil {
		return nil, err
	}

	at := g.Index(id)
	cmds := []commands.Command{&commands.RemoveFeature{ID: id}}
	for i, n := range order {
		c := cloneNode(n)
		rewriteRefs(c, rename)
		c.ID = rename[n.ID]
		c.Provenance = env.Provenance()
		c.Provenance.ChangeSet = env.NextChangeSetID()
		c.Group = inst.Group
		c.Status, c.Errors = "", nil
		if i == len(order)-1 { // the body that replaces the instance keeps its look
			c.Appearance = inst.Appearance.Clone()
		} else {
			c.Appearance = nil
		}
		cmds = append(cmds, &commands.AddFeature{Node: c, Index: at + i})
	}
	return &Plan{
		Commands: cmds,
		Intent:   fmt.Sprintf("Detach %q from its component", id),
		Explanation: fmt.Sprintf("Replaced the reference with %d local feature(s); changes to the component "+
			"no longer affect them.", len(order)),
	}, nil
}

// detachIDs maps the component's node ids onto free, valid ids in the host graph. Ids are
// bounded by the CAD-IR id pattern, so the prefix is kept short and the original is truncated.
func detachIDs(g *cadir.Graph, instanceID string, nodes []*cadir.Node) (map[string]string, error) {
	const maxID = 48
	prefix := instanceID
	if len(prefix) > 16 {
		prefix = prefix[:16]
	}
	taken := map[string]bool{}
	for _, n := range g.Nodes {
		taken[n.ID] = true
	}
	out := map[string]string{}
	for _, n := range nodes {
		base := prefix + "_" + n.ID
		if len(base) > maxID {
			base = base[:maxID]
		}
		base = strings.Trim(base, "_")
		if !commands.ValidID(base) {
			base = prefix
		}
		id := base
		for i := 2; taken[id]; i++ {
			suffix := fmt.Sprintf("_%d", i)
			trunc := base
			if len(trunc)+len(suffix) > maxID {
				trunc = trunc[:maxID-len(suffix)]
			}
			id = trunc + suffix
		}
		if !commands.ValidID(id) {
			return nil, fmt.Errorf("cannot derive a valid feature id for %q; rename the instance to something shorter", n.ID)
		}
		taken[id] = true
		out[n.ID] = id
	}
	return out, nil
}

// rewriteRefs points every reference a detached node makes at its renamed copy: dependencies,
// the target/source parameters, expression variables, selector references and selection refs.
func rewriteRefs(n *cadir.Node, rename map[string]string) {
	for i, d := range n.Dependencies {
		if to, ok := rename[d]; ok {
			n.Dependencies[i] = to
		}
	}
	for _, k := range []string{"target", "source"} {
		if s, _ := n.Parameters[k].(string); s != "" {
			if to, ok := rename[s]; ok {
				n.Parameters[k] = to
			}
		}
	}
	for k, v := range n.Expressions {
		n.Expressions[k] = renameInText(v, rename)
	}
	for k, v := range n.References {
		n.References[k] = renameInText(v, rename)
	}
	for i, v := range n.SelectionRefs {
		n.SelectionRefs[i] = renameInText(v, rename)
	}
}

// renameInText substitutes whole-word feature ids inside an expression or selector string.
// Longer ids are replaced first so that "hub" never clobbers part of "hub_ring".
func renameInText(s string, rename map[string]string) string {
	keys := make([]string, 0, len(rename))
	for k := range rename {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return len(keys[i]) > len(keys[j]) })
	var b strings.Builder
	for i := 0; i < len(s); {
		matched := false
		if !isIDChar(byteAt(s, i-1)) {
			for _, k := range keys {
				if strings.HasPrefix(s[i:], k) && !isIDChar(byteAt(s, i+len(k))) {
					b.WriteString(rename[k])
					i += len(k)
					matched = true
					break
				}
			}
		}
		if !matched {
			b.WriteByte(s[i])
			i++
		}
	}
	return b.String()
}

func byteAt(s string, i int) byte {
	if i < 0 || i >= len(s) {
		return 0
	}
	return s[i]
}

func isIDChar(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

func idsOfNodes(nodes []*cadir.Node) []string {
	out := make([]string, len(nodes))
	for i, n := range nodes {
		out[i] = n.ID
	}
	return out
}

func stringList(a Args, key string) []string {
	raw, ok := a[key].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		if s, _ := v.(string); s != "" {
			out = append(out, s)
		}
	}
	return out
}
