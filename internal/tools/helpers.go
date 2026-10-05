package tools

import (
	"fmt"
	"math"
	"regexp"
	"strings"

	"github.com/accelone/simoncad/internal/cadir"
	"github.com/accelone/simoncad/internal/commands"
	"github.com/accelone/simoncad/internal/selectors"
)

// CapGeometry tags tools that belong to the Geometry capability.
const CapGeometry = "geometry"

const capGeometry = CapGeometry

var nonID = regexp.MustCompile(`[^a-z0-9]+`)

// uniqueID picks a deterministic free feature id from args.id, args.name or the type.
func uniqueID(g *cadir.Graph, a Args, typ string) (string, error) {
	base := a.Str("id")
	if base != "" {
		if !commands.ValidID(base) {
			return "", fmt.Errorf("id %q is invalid: use lowercase letters, digits and underscores, starting with a letter", base)
		}
		if g.Get(base) != nil {
			return "", fmt.Errorf("feature %q already exists", base)
		}
		return base, nil
	}
	base = strings.Trim(nonID.ReplaceAllString(strings.ToLower(a.Str("name")), "_"), "_")
	if base == "" || base[0] < 'a' || base[0] > 'z' {
		base = typ
	}
	if len(base) > 40 {
		base = base[:40]
	}
	if g.Get(base) == nil {
		return base, nil
	}
	for i := 2; ; i++ {
		id := fmt.Sprintf("%s_%d", base, i)
		if g.Get(id) == nil {
			return id, nil
		}
	}
}

func newNode(env Env, a Args, id, typ string, params map[string]any, deps ...string) *cadir.Node {
	prov := env.Provenance()
	prov.ChangeSet = env.NextChangeSetID()
	stamp := env.Now().UTC().Format("2006-01-02T15:04:05Z")
	name := a.Str("name")
	if name == "" {
		name = id
	}
	n := &cadir.Node{
		ID: id, Type: typ, Parameters: params, Dependencies: deps,
		Intent: a.Str("intent"), Provenance: prov,
		Metadata:      cadir.Metadata{Name: name, Created: stamp, Modified: stamp},
		SelectionRefs: env.SelectionRefs(),
	}
	return n
}

// copyOpt copies the listed optional numeric/string/bool args into params.
func copyOpt(params map[string]any, a Args, names ...string) {
	for _, k := range names {
		if v, ok := a[k]; ok {
			params[k] = v
		}
	}
}

func requireFeature(env Env, id string) (*cadir.Node, error) {
	n := env.Graph().Get(id)
	if n == nil {
		var ids []string
		for _, x := range env.Graph().Nodes {
			ids = append(ids, x.ID)
		}
		return nil, fmt.Errorf("feature %q does not exist (existing: %s)", id, strings.Join(ids, ", "))
	}
	return n, nil
}

// requireBounds returns the bounds of a feature that currently has geometry.
func requireBounds(env Env, id string) (selectors.Bounds, error) {
	if _, err := requireFeature(env, id); err != nil {
		return selectors.Bounds{}, err
	}
	m, err := env.Model()
	if err != nil {
		return selectors.Bounds{}, err
	}
	b, ok := m.Bounds(id)
	if !ok {
		r := m.Nodes[id]
		reason := "it has no geometry"
		if r != nil && len(r.Errors) > 0 {
			reason = r.Errors[0]
		}
		return selectors.Bounds{}, fmt.Errorf("feature %q cannot be used: %s", id, reason)
	}
	return b, nil
}

func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

func addPlan(intent, explanation string, nodes ...*cadir.Node) *Plan {
	p := &Plan{Intent: intent, Explanation: explanation}
	for _, n := range nodes {
		p.Commands = append(p.Commands, &commands.AddFeature{Node: n, Index: -1})
	}
	return p
}

func fmtMM(v float64) string {
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.3f", v), "0"), ".")
}

// intentOr returns the caller's stated intent or a generated default.
func intentOr(a Args, def string) string {
	if s := a.Str("intent"); s != "" {
		return s
	}
	return def
}

// base properties shared by every feature-creating tool.
func withMeta(p props) props {
	p["id"] = text("Optional feature id (lowercase letters, digits, underscores). Generated if omitted.")
	p["name"] = text("Optional human-readable name.")
	p["intent"] = text("The engineering intent in natural language, e.g. 'Mount plate for the shoulder servo'.")
	return p
}
