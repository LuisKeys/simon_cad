package ui

import (
	"errors"

	"github.com/accelone/simoncad/internal/agent/attachments"
	"github.com/accelone/simoncad/internal/agent/refine"
	"github.com/accelone/simoncad/internal/agent/runtime"
	"github.com/accelone/simoncad/internal/agent/workflow"
	"github.com/accelone/simoncad/internal/app"
	"github.com/accelone/simoncad/internal/cadir"
	"github.com/accelone/simoncad/internal/geometry/rebuild"
	"github.com/accelone/simoncad/internal/workspace"
)

// ---- state ----------------------------------------------------------------------------

// ChangeSetCard is a ChangeSet as the conversation and review UI show it.
type ChangeSetCard = app.ChangeSetCard

// Validation is one check result on a ChangeSet card.
type Validation = app.Validation

func nz(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// TreeNode is one design-tree row.
type TreeNode struct {
	ID         string            `json:"id"`
	Name       string            `json:"name"`
	Type       string            `json:"type"`
	Status     string            `json:"status"`
	Errors     []string          `json:"errors"`
	Intent     string            `json:"intent"`
	CreatedBy  string            `json:"created_by"`
	Capability string            `json:"capability,omitempty"`
	Suppressed bool              `json:"suppressed"`
	Visible    bool              `json:"visible"` // produces a body nothing else consumes (a leaf)
	Appearance *cadir.Appearance `json:"appearance,omitempty"`
	Deps       []string          `json:"deps"`
	Params     map[string]any    `json:"params"`
	Exprs      map[string]string `json:"expressions,omitempty"`
	Group      string            `json:"group,omitempty"` // design-tree group (organization only)
	// Component is the referenced project's id when this row is a component instance, which
	// is what makes instances visually identifiable in the design tree. OutOfDate marks one
	// whose component has changed on disk since the instance was pinned to it.
	Component   string `json:"component,omitempty"`
	OutOfDate   bool   `json:"out_of_date,omitempty"`
	ComponentOK bool   `json:"component_resolved,omitempty"`
}

// State is everything the panels render.
type State struct {
	Name        string           `json:"name"`
	Path        string           `json:"path"`
	Dirty       bool             `json:"dirty"`
	Units       string           `json:"units"`
	Level       string           `json:"level"`
	Busy        bool             `json:"busy"`
	Tree        []TreeNode       `json:"tree"`
	Pending     []ChangeSetCard  `json:"pending"`
	Recent      []ChangeSetCard  `json:"recent"`
	Checkpoints []CheckpointView `json:"checkpoints"`
	Chat        []ChatMessage    `json:"chat"`
	Selection   SelectionView    `json:"selection"`
	// OpenWarnings are notes about the project as it was opened (see app.App.OpenWarnings).
	OpenWarnings []string        `json:"open_warnings,omitempty"`
	CanUndo      bool            `json:"can_undo"`
	CanRedo      bool            `json:"can_redo"`
	Volume       float64         `json:"volume_mm3"`
	Failed       []string        `json:"failed"`
	Frames       int             `json:"frames"`
	Draft        []string        `json:"draft"`
	Provider     string          `json:"provider"`
	Images       []ImageInfo     `json:"images"`
	Specs        []Spec          `json:"specs"`
	Proposals    []ProposalView  `json:"proposals"`
	AgentState   *workflow.Event `json:"agent_state,omitempty"`
	RefineState  *refine.Event   `json:"refine_state,omitempty"` // while a visual refinement runs
	Features     Features        `json:"features"`
	// Reusable says the open project is itself a component; Stale counts the instances whose
	// component has moved on, which is what the "Sync components" affordance keys off.
	Reusable bool `json:"reusable"`
	Stale    int  `json:"stale_components"`
	// Usage is token consumption (last turn and session) with its cost estimate.
	Usage UsageView `json:"usage"`
	// Model is the model serving the running request, or the one that served the last;
	// Primary is the configured primary model (shown before any request).
	Model   *runtime.RouteEvent `json:"model,omitempty"`
	Primary *runtime.RouteEvent `json:"primary,omitempty"`
	// Workspace is the project's units, grid and snapping, as the viewport and panels use them.
	Workspace WorkspaceView `json:"workspace"`
}

// WorkspaceView is the project's workspace settings plus what a client needs to display
// values without its own conversion table: the size of the display unit in millimetres.
type WorkspaceView struct {
	workspace.Settings
	MMPerUnit       float64            `json:"mm_per_unit"`
	LinearUnits     map[string]float64 `json:"linear_units"`        // every unit → mm (the UnitService table)
	TranslationStep float64            `json:"translation_step_mm"` // the effective translation increment
	Warnings        []string           `json:"warnings,omitempty"`  // what could not be read from workspace.json
}

func workspaceView(a *app.App) WorkspaceView {
	ws := a.Workspace.Clone()
	return WorkspaceView{Settings: ws, MMPerUnit: workspace.MMPer(ws.Units.Linear), LinearUnits: workspace.MMPerTable(),
		TranslationStep: ws.TranslationStep(),
		Warnings:        a.WorkspaceWarnings}
}

// Features says which attachment workflows can work with the configured providers. It
// looks at every configured provider, not only the selected one: with Ollama selected,
// images still work when OpenAI or Claude is configured.
type Features struct {
	Images      bool   `json:"images"`
	ImageReason string `json:"image_reason,omitempty"`
	Documents   bool   `json:"documents"` // PDF documents
	// Refinement: visual closed-loop refinement can run (it needs a vision model).
	Refinement bool `json:"refinement"`
}

func (s *Service) features() Features {
	var prov runtime.Provider
	if p, err := s.NewProvider(); err == nil {
		prov = p
	}
	proc := &attachments.Processor{Router: s.router(prov)}
	// A remote fallback that still needs the user's authorization is available: the user is
	// asked when a request needs it.
	ok := func(k attachments.Kind) bool {
		err := proc.Available(k)
		return err == nil || errors.Is(err, runtime.ErrConsentRequired)
	}
	f := Features{Images: ok(attachments.KindImage), Documents: ok(attachments.KindDocument)}
	f.Refinement = f.Images
	if !f.Images {
		f.ImageReason = "Image features need a vision model: configure a Fallback Model (OpenAI or Anthropic) in Settings, or use a multimodal Ollama model."
		if err := proc.Available(attachments.KindImage); err != nil {
			f.ImageReason = err.Error()
		}
	}
	return f
}

// State snapshots the project for the panels.
func (s *Service) State() (*State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.app.Model()
	if err != nil {
		return nil, err
	}
	g := s.app.Snapshot()
	st := &State{Name: s.app.Meta.Name, Path: s.path, Dirty: s.dirty, Units: g.Units, Level: s.level, Busy: s.busy,
		Tree: []TreeNode{}, Pending: []ChangeSetCard{}, Recent: []ChangeSetCard{}, Checkpoints: s.checkpoints(),
		Chat: append([]ChatMessage{}, s.chat...), Selection: s.selectionView(), OpenWarnings: s.app.OpenWarnings, CanUndo: s.app.CanUndo(),
		Draft: append([]string{}, s.draft...), CanRedo: s.app.CanRedo(), Volume: m.Volume(), Failed: nz(m.Failed()), Frames: len(s.app.Committed())}
	st.Images, st.Proposals = []ImageInfo{}, s.proposals()
	st.Specs = append([]Spec{}, s.specs...)
	for _, im := range s.app.Images.All() {
		st.Images = append(st.Images, imageInfo(im))
	}
	if p, err := runtime.ProviderNameFromEnv(); err == nil {
		st.Provider = p
	}
	if p, err := s.NewProvider(); err == nil {
		prov, model := runtime.Identify(p)
		st.Primary = &runtime.RouteEvent{Provider: prov, Model: model, Local: runtime.IsLocal(p)}
	}
	s.routeMu.Lock()
	if s.model != nil {
		m := *s.model
		st.Model = &m
	}
	s.routeMu.Unlock()
	st.AgentState, st.Features = s.agentState, s.features()
	st.Usage = s.usageView()
	st.Workspace = workspaceView(s.app)
	if s.refineCancel != nil {
		st.RefineState = s.refineState
	}
	stale := s.app.ComponentsOutOfDate()
	st.Reusable, st.Stale = s.app.Meta.Reusable, len(stale)
	st.Tree = treeRows(g, m, stale)
	for _, cs := range s.app.Pending() {
		st.Pending = append(st.Pending, app.CardOf(cs))
	}
	sets := s.app.Committed()
	for i := len(sets) - 1; i >= 0 && len(st.Recent) < 8; i-- {
		st.Recent = append(st.Recent, app.CardOf(sets[i]))
	}
	return st, nil
}

// treeRows lists the design-tree rows of g, rebuilt as m. stale holds the component
// instances whose component has changed on disk.
func treeRows(g *cadir.Graph, m *rebuild.Model, stale map[string]string) []TreeNode {
	leaf := map[string]bool{}
	for _, id := range m.Leaves {
		leaf[id] = true
	}
	rows := []TreeNode{}
	for _, n := range g.Nodes {
		name := n.Metadata.Name
		if name == "" {
			name = n.ID
		}
		row := TreeNode{ID: n.ID, Name: name, Type: n.Type, Status: n.Status, Errors: nz(n.Errors),
			Intent: n.Intent, CreatedBy: n.Provenance.CreatedBy, Capability: n.Provenance.Capability,
			Suppressed: n.Suppressed, Visible: leaf[n.ID], Appearance: n.Appearance, Deps: nz(n.Dependencies), Params: n.Parameters, Exprs: n.Expressions,
			Group: n.Group}
		if c, _ := n.Parameters["component"].(string); c != "" && n.Type == "component_instance" {
			row.Component, row.ComponentOK = c, n.Status == "ok"
			_, row.OutOfDate = stale[n.ID]
		}
		rows = append(rows, row)
	}
	return rows
}
