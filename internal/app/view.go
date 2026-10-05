package app

import (
	"fmt"
	"sort"

	"github.com/accelone/simoncad/internal/cadir"
	"github.com/accelone/simoncad/internal/commands"
	"github.com/accelone/simoncad/internal/geometry/rebuild"
)

// Read-only views for front ends. The UI never touches App.Graph or App.Mgr directly: it
// reads these snapshots and mutates only through ApplyTool and the ChangeSet lifecycle
// (Commit, Reject, Undo, Redo, Restore).

// Snapshot returns a deep copy of the live graph; changing it has no effect on the model.
func (a *App) Snapshot() *cadir.Graph { return a.Graph.Clone() }

// CanUndo and CanRedo report the ChangeSet undo/redo stacks.
func (a *App) CanUndo() bool { return a.Mgr.CanUndo() }
func (a *App) CanRedo() bool { return a.Mgr.CanRedo() }

// ChangeSet returns a ChangeSet by id.
func (a *App) ChangeSet(id string) (*commands.ChangeSet, bool) { return a.Mgr.Get(id) }

// PreviewModel rebuilds the graph a proposed ChangeSet would produce, without applying it.
// The live graph and its derived status are untouched.
func (a *App) PreviewModel(id string) (*rebuild.Model, *commands.ChangeSet, error) {
	g, err := a.Mgr.Preview(id)
	if err != nil {
		return nil, nil, err
	}
	m, err := a.Engine.Rebuild(g)
	if err != nil {
		return nil, nil, err
	}
	cs, _ := a.Mgr.Get(id)
	return m, cs, nil
}

// committedInOrder lists the applied ChangeSets, oldest first.
func (a *App) committedInOrder() []*commands.ChangeSet {
	var sets []*commands.ChangeSet
	for _, cs := range a.Mgr.Log {
		if cs.State == commands.StateCommitted {
			sets = append(sets, cs)
		}
	}
	sort.Slice(sets, func(i, j int) bool { return sets[i].Seq < sets[j].Seq })
	return sets
}

// Committed returns the applied ChangeSets, oldest first (the playback script).
func (a *App) Committed() []*commands.ChangeSet { return a.committedInOrder() }

// ModelAfter rebuilds the model as it stood after the first n committed ChangeSets
// (n=0 is the empty project). It powers Instant Design playback from the event record.
func (a *App) ModelAfter(n int) (*rebuild.Model, error) {
	sets := a.committedInOrder()
	if n < 0 || n > len(sets) {
		return nil, fmt.Errorf("playback frame %d is out of range (0..%d)", n, len(sets))
	}
	g := cadir.NewGraph()
	if err := commands.ReplayOnto(g, sets[:n]); err != nil {
		return nil, err
	}
	return a.Engine.Rebuild(g)
}
