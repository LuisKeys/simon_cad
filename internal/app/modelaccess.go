package app

import (
	"github.com/accelone/simoncad/internal/cadir"
	"github.com/accelone/simoncad/internal/commands"
	"github.com/accelone/simoncad/internal/events"
	"github.com/accelone/simoncad/internal/geometry/rebuild"
	"github.com/accelone/simoncad/internal/selection"
	"github.com/accelone/simoncad/internal/workspace"
)

// ---- model access -----------------------------------------------------------------

// Model rebuilds the live graph (incrementally) and writes derived status onto the nodes.
func (a *App) Model() (*rebuild.Model, error) {
	m, err := a.Engine.Rebuild(a.Graph)
	if err != nil {
		return nil, err
	}
	m.Apply(a.Graph)
	return m, nil
}

// Evaluate implements commands.Evaluator over the rebuild engine.
func (a *App) Evaluate(g *cadir.Graph) (commands.Evaluation, error) {
	m, err := a.Engine.Rebuild(g)
	if err != nil {
		return commands.Evaluation{}, err
	}
	ev := commands.Evaluation{Volume: m.Volume(), Failed: map[string][]string{}, BodyCnt: len(m.Leaves)}
	for _, id := range m.Failed() {
		ev.Failed[id] = m.Nodes[id].Errors
	}
	return ev, nil
}

func (a *App) refresh() (*rebuild.Model, error) {
	m, err := a.Model()
	if err != nil {
		return nil, err
	}
	a.Bus.Publish(events.ModelRebuilt, map[string]any{
		"hash": a.Graph.Hash(), "volume_mm3": m.Volume(), "failed": m.Failed(),
		"cache_hits": a.Engine.Stats.Hits, "cache_misses": a.Engine.Stats.Misses,
	})
	return m, nil
}

// ---- selection --------------------------------------------------------------------

// SetSelection validates and stores the selection context given as selector strings.
func (a *App) SetSelection(raw []string) error {
	if len(raw) == 0 {
		a.selection = nil
		return nil
	}
	ctx, err := a.SelectionContextFor(raw)
	if err != nil {
		return err
	}
	a.selection = ctx.Selectors()
	return nil
}

func (a *App) SelectionContextFor(raw []string) (selection.Context, error) {
	m, err := a.Model()
	if err != nil {
		return selection.Context{}, err
	}
	return selection.Resolve(raw, m.Topology)
}

// SelectionContext resolves the current selection.
func (a *App) SelectionContext() selection.Context {
	if len(a.selection) == 0 {
		return selection.Context{}
	}
	c, err := a.SelectionContextFor(a.selection)
	if err != nil {
		return selection.Context{}
	}
	return c
}

// MeshDetail is the viewport mesh detail level (default "fine").
func (a *App) MeshDetail() string {
	if a.meshDetail == "" {
		return "fine"
	}
	return a.meshDetail
}

// SetWorkspace validates and stores the project's workspace settings. It is what the
// workspace tools call; geometry is never touched. The change is an undo step of its own.
func (a *App) SetWorkspace(s workspace.Settings) error {
	s = s.Normalized()
	if err := s.Validate(); err != nil {
		return err
	}
	if s.Equal(a.Workspace) {
		return nil
	}
	before := a.Workspace.Clone()
	a.setWorkspace(s)
	a.Mgr.Push(&workspaceEdit{a: a, before: before, after: s.Clone()})
	return nil
}

func (a *App) setWorkspace(s workspace.Settings) {
	a.Workspace = s.Clone()
	a.Bus.Publish(events.WorkspaceChanged, a.Workspace)
}

// workspaceEdit is a change of the workspace settings on the undo/redo stacks.
type workspaceEdit struct {
	a             *App
	before, after workspace.Settings
}

func (w *workspaceEdit) Label() string  { return "workspace settings" }
func (w *workspaceEdit) Revert() error  { w.a.setWorkspace(w.before); return nil }
func (w *workspaceEdit) Reapply() error { w.a.setWorkspace(w.after); return nil }
