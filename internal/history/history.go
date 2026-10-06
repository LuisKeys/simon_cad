// Package history records semantic checkpoints: engineering intent ("Created servo
// mount"), not raw commands. A checkpoint links its ChangeSets and a CAD-IR snapshot.
package history

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/accelone/simoncad/internal/cadir"
)

type Checkpoint struct {
	ID         string       `json:"id"`
	Seq        int          `json:"seq"`
	Label      string       `json:"label"`
	Actor      string       `json:"actor,omitempty"` // user|agent|cli
	ChangeSets []string     `json:"changesets"`
	GraphHash  string       `json:"graph_hash"`
	Time       string       `json:"time"`
	Snapshot   *cadir.Graph `json:"snapshot"`
}

type Log struct {
	Checkpoints []*Checkpoint `json:"checkpoints"`
}

// Create appends a checkpoint capturing g.
func (l *Log) Create(label, actor string, changesets []string, g *cadir.Graph, now time.Time) *Checkpoint {
	cp := &Checkpoint{
		ID: fmt.Sprintf("cp_%03d", len(l.Checkpoints)+1), Seq: len(l.Checkpoints) + 1,
		Label: label, Actor: actor, ChangeSets: append([]string{}, changesets...),
		GraphHash: g.Hash(), Time: now.UTC().Format(time.RFC3339), Snapshot: g.Clone(),
	}
	l.Checkpoints = append(l.Checkpoints, cp)
	return cp
}

// Find resolves an id ("cp_002"), a 1-based number ("2"), "previous"/"last", or a unique
// case-insensitive label substring.
func (l *Log) Find(ref string) (*Checkpoint, error) {
	ref = strings.TrimSpace(ref)
	if len(l.Checkpoints) == 0 {
		return nil, fmt.Errorf("no checkpoints yet")
	}
	for _, c := range l.Checkpoints {
		if c.ID == ref {
			return c, nil
		}
	}
	switch strings.ToLower(ref) {
	case "last", "latest":
		return l.Checkpoints[len(l.Checkpoints)-1], nil
	case "previous", "prev":
		if len(l.Checkpoints) < 2 {
			return nil, fmt.Errorf("no previous checkpoint")
		}
		return l.Checkpoints[len(l.Checkpoints)-2], nil
	}
	var n int
	if _, err := fmt.Sscanf(ref, "%d", &n); err == nil && fmt.Sprint(n) == ref && n >= 1 && n <= len(l.Checkpoints) {
		return l.Checkpoints[n-1], nil
	}
	var hits []*Checkpoint
	for _, c := range l.Checkpoints {
		if strings.Contains(strings.ToLower(c.Label), strings.ToLower(ref)) {
			hits = append(hits, c)
		}
	}
	switch len(hits) {
	case 1:
		return hits[0], nil
	case 0:
		return nil, fmt.Errorf("no checkpoint matches %q", ref)
	}
	return nil, fmt.Errorf("%q matches %d checkpoints; use an id", ref, len(hits))
}

// Recent returns up to n newest checkpoints, newest last (without snapshots).
func (l *Log) Recent(n int) []Checkpoint {
	start := len(l.Checkpoints) - n
	if start < 0 {
		start = 0
	}
	out := make([]Checkpoint, 0, n)
	for _, c := range l.Checkpoints[start:] {
		cc := *c
		cc.Snapshot = nil
		out = append(out, cc)
	}
	return out
}

type ParamChange struct {
	Feature string `json:"feature"`
	Name    string `json:"name"`
	From    any    `json:"from"`
	To      any    `json:"to"`
}

// Diff is a semantic comparison of two CAD-IR snapshots.
type Diff struct {
	Added   []string      `json:"added,omitempty"`
	Removed []string      `json:"removed,omitempty"`
	Changed []ParamChange `json:"changed,omitempty"`
}

func (d Diff) Empty() bool { return len(d.Added)+len(d.Removed)+len(d.Changed) == 0 }

// Compare reports what changed going from a to b.
func Compare(a, b *cadir.Graph) Diff {
	var d Diff
	for _, n := range b.Nodes {
		if a.Get(n.ID) == nil {
			d.Added = append(d.Added, n.ID)
		}
	}
	for _, n := range a.Nodes {
		bn := b.Get(n.ID)
		if bn == nil {
			d.Removed = append(d.Removed, n.ID)
			continue
		}
		names := map[string]bool{}
		for k := range n.Parameters {
			names[k] = true
		}
		for k := range bn.Parameters {
			names[k] = true
		}
		keys := make([]string, 0, len(names))
		for k := range names {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			from, to := n.Parameters[k], bn.Parameters[k]
			if fmt.Sprint(from) != fmt.Sprint(to) {
				d.Changed = append(d.Changed, ParamChange{n.ID, k, from, to})
			}
		}
		if n.Suppressed != bn.Suppressed {
			d.Changed = append(d.Changed, ParamChange{n.ID, "suppressed", n.Suppressed, bn.Suppressed})
		}
		for _, f := range []struct {
			name     string
			from, to string
		}{
			{"appearance.color", appearanceField(n.Appearance, "color"), appearanceField(bn.Appearance, "color")},
			{"appearance.opacity", appearanceField(n.Appearance, "opacity"), appearanceField(bn.Appearance, "opacity")},
			{"appearance.visible", appearanceField(n.Appearance, "visible"), appearanceField(bn.Appearance, "visible")},
		} {
			if f.from != f.to {
				d.Changed = append(d.Changed, ParamChange{n.ID, f.name, f.from, f.to})
			}
		}
	}
	return d
}

// appearanceField renders one appearance attribute as text ("" when it is the default).
func appearanceField(a *cadir.Appearance, field string) string {
	if a == nil {
		return ""
	}
	switch field {
	case "color":
		return a.Color
	case "opacity":
		if a.Opacity != nil {
			return fmt.Sprint(*a.Opacity)
		}
	case "visible":
		if a.Visible != nil {
			return fmt.Sprint(*a.Visible)
		}
	}
	return ""
}
