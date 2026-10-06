// Package app is the composition root. The CLI, the agent runtime and (from Phase 2) the
// UI all drive the model through App.ApplyTool: tool → commands → ChangeSet → commit.
// There is no other mutation path.
package app

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/accelone/simoncad/internal/cadir"
	"github.com/accelone/simoncad/internal/commands"
	"github.com/accelone/simoncad/internal/events"
	"github.com/accelone/simoncad/internal/export"
	"github.com/accelone/simoncad/internal/geometry/api"
	"github.com/accelone/simoncad/internal/geometry/rebuild"
	"github.com/accelone/simoncad/internal/history"
	"github.com/accelone/simoncad/internal/materials"
	"github.com/accelone/simoncad/internal/persistence"
	"github.com/accelone/simoncad/internal/selection"
	"github.com/accelone/simoncad/internal/tools"
	"github.com/accelone/simoncad/internal/vision"
	materialsdata "github.com/accelone/simoncad/materials"
)

// Policy decides whether a valid ChangeSet commits immediately.
type Policy int

const (
	// PolicyAuto commits low-risk single-command ChangeSets and leaves the rest proposed.
	PolicyAuto Policy = iota
	// PolicyCommit commits every valid ChangeSet.
	PolicyCommit
	// PolicyPreview never commits; everything stays proposed.
	PolicyPreview
)

// Actor identifies who is driving tool calls (recorded in node provenance).
type Actor struct {
	CreatedBy  string // user | agent | cli
	Capability string
	Model      string
}

type App struct {
	Meta   persistence.Meta
	K      *api.Kernel
	Engine *rebuild.Engine
	Graph  *cadir.Graph
	Mgr    *commands.Manager
	Hist   *history.Log
	Bus    *events.Bus
	Reg    *tools.Registry
	AH     persistence.AgentHistory
	// Vision keeps image interpretations (proposals); Images the uploaded source images.
	// Neither is CAD state.
	Vision *vision.Store
	Images *vision.ImageStore
	// Materials is the visual material library (built-in plus the open project's own).
	// Presentation only; it is never an input to geometry.
	Materials *materials.Library
	matDir    string // project dir whose assets/materials feed Materials ("" = none)
	// meshDetail is the viewport mesh detail ("fine" | "normal" | "coarse"). Display only:
	// it is not CAD state, is not saved and never changes geometry.
	meshDetail string

	// Raw preserved files not interpreted in Phase 1.
	components, constraints, materials json.RawMessage

	Clock     func() time.Time
	actor     Actor
	callID    string
	promptID  string
	selection []string
	sinceCP   []string // ChangeSets committed since the last checkpoint
}

func newApp(clock func() time.Time) *App {
	if clock == nil {
		clock = time.Now
	}
	k := api.New()
	a := &App{K: k, Engine: rebuild.New(k), Graph: cadir.NewGraph(), Hist: &history.Log{}, Bus: events.NewBus(),
		Reg: tools.Default(), Clock: clock, actor: Actor{CreatedBy: cadir.CreatedByCLI},
		Vision: vision.NewStore(), Images: vision.NewImageStore(),
		Materials: materials.New(materialsdata.FS())}
	a.Bus.Now = clock
	a.Mgr = commands.NewManager(a.Graph, a, a.Bus)
	a.Mgr.Now = clock
	return a
}

// New starts an empty project.
func New(name string, clock func() time.Time) *App {
	a := newApp(clock)
	a.Meta = persistence.Meta{SchemaVersion: persistence.SchemaVersion, Name: name, Units: "mm",
		OCCTVersion: a.K.Version(), Created: a.Clock().UTC().Format(time.RFC3339)}
	return a
}

// Open loads a .scadproj directory.
func Open(dir string, clock func() time.Time) (*App, error) {
	p, err := persistence.Load(dir)
	if err != nil {
		return nil, err
	}
	a := newApp(clock)
	a.Meta, a.AH = p.Meta, p.History
	a.useProjectMaterials(dir)
	a.components, a.constraints, a.materials = p.Components, p.Constraints, p.Materials
	*a.Graph = *p.Graph
	if len(a.AH.Interpretations) > 0 {
		if err := json.Unmarshal(a.AH.Interpretations, a.Vision); err != nil {
			return nil, fmt.Errorf("agent-history.json interpretations: %w", err)
		}
	}
	for _, im := range p.Images { // retained source images; the geometry never needs them
		if put, err := a.Images.Put(im.Data); err == nil {
			_ = a.Images.Retain(put.ID, true)
		}
	}
	a.Mgr.Restore(a.AH.ChangeSets)
	a.Hist.Checkpoints = a.AH.Checkpoints
	a.Bus.Load(p.Events)
	covered := map[string]bool{}
	for _, cp := range a.Hist.Checkpoints {
		for _, id := range cp.ChangeSets {
			covered[id] = true
		}
	}
	for _, id := range a.Mgr.CommittedIDs() {
		if cs, ok := a.Mgr.Get(id); ok && !covered[id] {
			a.trackForCheckpoint(cs)
		}
	}
	return a, nil
}

func (a *App) Close() {
	a.Engine.Close()
	a.K.Close()
}

// ProjectMaterialsDir is where a project keeps its own materials.
const ProjectMaterialsDir = "assets/materials"

// useProjectMaterials points the library at dir/assets/materials (a no-op layer when absent).
func (a *App) useProjectMaterials(dir string) {
	a.matDir = dir
	root := filepath.Join(dir, filepath.FromSlash(ProjectMaterialsDir))
	if st, err := os.Stat(root); err == nil && st.IsDir() {
		a.Materials.SetProject(os.DirFS(root))
		return
	}
	a.Materials.SetProject(nil)
}

// carryProjectMaterials copies project-specific materials when saving to a new location, so
// "Save as" never strands the assets a project's materials reference.
func (a *App) carryProjectMaterials(dir string) error {
	if a.matDir == "" || a.matDir == dir {
		return nil
	}
	src := filepath.Join(a.matDir, filepath.FromSlash(ProjectMaterialsDir))
	if st, err := os.Stat(src); err != nil || !st.IsDir() {
		return nil
	}
	dst := filepath.Join(dir, filepath.FromSlash(ProjectMaterialsDir))
	err := filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dst, rel), b, 0o644)
	})
	if err == nil {
		a.useProjectMaterials(dir)
	}
	return err
}

// Save writes the project to dir.
func (a *App) Save(dir string) error {
	a.AH.ChangeSets = a.Mgr.Log
	a.AH.Checkpoints = a.Hist.Checkpoints
	if a.AH.Prompts == nil {
		a.AH.Prompts = []persistence.Prompt{}
	}
	if a.AH.ToolCalls == nil {
		a.AH.ToolCalls = []persistence.ToolCall{}
	}
	if a.AH.ChangeSets == nil {
		a.AH.ChangeSets = []*commands.ChangeSet{}
	}
	if a.AH.Checkpoints == nil {
		a.AH.Checkpoints = []*history.Checkpoint{}
	}
	if _, err := a.Model(); err != nil { // refresh derived status before writing
		return err
	}
	a.AH.Interpretations = nil
	if len(a.Vision.List()) > 0 {
		b, err := json.Marshal(a.Vision)
		if err != nil {
			return err
		}
		a.AH.Interpretations = b
	}
	var imgs []persistence.Image
	for _, im := range a.Images.All() {
		if !im.Retained {
			continue
		}
		ext := map[string]string{"image/png": ".png", "image/jpeg": ".jpg", "image/webp": ".webp"}[im.MediaType]
		imgs = append(imgs, persistence.Image{ID: im.ID, Ext: ext, Data: im.Data})
	}
	if err := persistence.Save(dir, &persistence.Project{
		Meta: a.Meta, Graph: a.Graph, History: a.AH, Events: a.Bus.Events(),
		Components: a.components, Constraints: a.constraints, Materials: a.materials, Images: imgs,
	}); err != nil {
		return err
	}
	return a.carryProjectMaterials(dir)
}

// SetActor sets who is driving subsequent tool calls.
func (a *App) SetActor(ac Actor) { a.actor = ac }

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

// ---- tools.Env ----------------------------------------------------------------------

type toolEnv struct{ a *App }

func (e toolEnv) Graph() *cadir.Graph            { return e.a.Graph }
func (e toolEnv) Model() (*rebuild.Model, error) { return e.a.Model() }
func (e toolEnv) Now() time.Time                 { return e.a.Clock() }
func (e toolEnv) NextChangeSetID() string        { return fmt.Sprintf("cs_%04d", len(e.a.Mgr.Log)+1) }
func (e toolEnv) SelectionRefs() []string        { return append([]string(nil), e.a.selection...) }
func (e toolEnv) Checkpoints() *history.Log      { return e.a.Hist }
func (e toolEnv) Vision() *vision.Store          { return e.a.Vision }
func (e toolEnv) Images() *vision.ImageStore     { return e.a.Images }
func (e toolEnv) Materials() *materials.Library  { return e.a.Materials }
func (e toolEnv) MeshDetail() string             { return e.a.MeshDetail() }
func (e toolEnv) SetMeshDetail(level string)     { e.a.meshDetail = level }
func (e toolEnv) Provenance() cadir.Provenance {
	return cadir.Provenance{CreatedBy: e.a.actor.CreatedBy, Capability: e.a.actor.Capability,
		ToolCallID: e.a.callID, Model: e.a.actor.Model}
}

func (e toolEnv) Export(format, path string, deflection float64) (map[string]any, error) {
	return e.a.export(format, path, deflection)
}

func (a *App) export(format, path string, deflection float64) (map[string]any, error) {
	m, err := a.Model()
	if err != nil {
		return nil, err
	}
	if f := m.Failed(); len(f) > 0 {
		return nil, fmt.Errorf("cannot export while features are failing: %s", strings.Join(f, ", "))
	}
	shape, err := m.ResultShape(a.K)
	if err != nil {
		return nil, err
	}
	defer shape.Release()
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
	}
	valid, err := shape.IsValid()
	if err != nil || !valid {
		return nil, fmt.Errorf("model is not a valid solid; refusing to export")
	}
	props, err := shape.Props()
	if err != nil {
		return nil, err
	}
	out := map[string]any{"format": format, "path": path, "volume_mm3": props.Volume, "valid": true}
	switch format {
	case "step":
		if err := shape.ExportSTEP(path); err != nil {
			return nil, err
		}
		re, err := a.K.ImportSTEP(path)
		if err != nil {
			return nil, fmt.Errorf("STEP re-import failed: %w", err)
		}
		rp, err := re.Props()
		re.Release()
		if err != nil {
			return nil, err
		}
		rel := (rp.Volume - props.Volume) / props.Volume
		if rel < 0 {
			rel = -rel
		}
		out["reimport_volume_rel_error"] = rel
		if rel > 1e-6 {
			return nil, fmt.Errorf("STEP re-import volume differs by %.2e", rel)
		}
	case "stl":
		if deflection <= 0 {
			deflection = 0.05
		}
		if err := shape.ExportSTL(path, deflection); err != nil {
			return nil, err
		}
		rep, err := export.CheckBinarySTL(path)
		if err != nil {
			return nil, err
		}
		out["triangles"], out["watertight"] = rep.Triangles, rep.Watertight()
		if !rep.Watertight() {
			return nil, fmt.Errorf("exported STL is not watertight (open edges %d, non-manifold %d)", rep.OpenEdges, rep.NonManifold)
		}
	default:
		return nil, fmt.Errorf("unknown export format %q", format)
	}
	if st, err := os.Stat(path); err == nil {
		out["bytes"] = st.Size()
	}
	a.Bus.Publish(events.ExportCompleted, out)
	return out, nil
}

// ---- prompts, tools, ChangeSets ---------------------------------------------------------

// RecordPrompt logs a user intent and returns its id.
func (a *App) RecordPrompt(text string) string {
	p := persistence.Prompt{
		ID: fmt.Sprintf("pr_%04d", len(a.AH.Prompts)+1), Time: a.Clock().UTC().Format(time.RFC3339),
		Text: text, Selection: append([]string(nil), a.selection...),
		Capability: a.actor.Capability, Model: a.actor.Model,
	}
	a.AH.Prompts = append(a.AH.Prompts, p)
	a.promptID = p.ID
	a.Bus.Publish(events.UserIntentReceived, map[string]any{"id": p.ID, "text": text, "selection": p.Selection})
	return p.ID
}

// RecordPromptImages notes which uploaded images were attached to a prompt.
func (a *App) RecordPromptImages(promptID string, ids []string) {
	if len(ids) == 0 {
		return
	}
	for i := range a.AH.Prompts {
		if a.AH.Prompts[i].ID == promptID {
			a.AH.Prompts[i].Images = append([]string(nil), ids...)
		}
	}
}

// SetPromptSummary stores the agent's final explanation on the current prompt.
func (a *App) SetPromptSummary(s string) {
	for i := range a.AH.Prompts {
		if a.AH.Prompts[i].ID == a.promptID {
			a.AH.Prompts[i].Summary = s
		}
	}
	a.Bus.Publish(events.AgentResponseFinal, map[string]any{"prompt_id": a.promptID, "text": s})
}

// Outcome is the result of one tool call, safe to show a user or feed back to a model.
type Outcome struct {
	ToolCallID string
	Tool       string
	OK         bool
	Error      string
	Data       any
	ChangeSet  *commands.ChangeSet
	Committed  bool
	Message    string
}

// JSON renders the outcome as the structured tool result returned to an agent.
func (o Outcome) JSON() string {
	m := map[string]any{"tool_call_id": o.ToolCallID, "ok": o.OK}
	if o.Error != "" {
		m["error"] = o.Error
	}
	if o.Data != nil {
		m["result"] = o.Data
	}
	if o.ChangeSet != nil {
		cs := o.ChangeSet
		m["changeset"] = map[string]any{
			"id": cs.ID, "state": cs.State, "committed": o.Committed, "affected": cs.Affected,
			"explanation": cs.Explanation, "warnings": cs.Warnings,
			"volume_before_mm3": cs.Effects.VolumeBefore, "volume_after_mm3": cs.Effects.VolumeAfter,
			"added": cs.Effects.Added, "removed": cs.Effects.Removed, "modified": cs.Effects.Modified,
		}
		if !o.Committed && o.OK {
			m["note"] = "ChangeSet is proposed and awaits user approval; it has NOT been applied. Do not repeat the call."
		}
	}
	b, _ := json.Marshal(m)
	return string(b)
}

func (a *App) recordCall(name string, args []byte, o Outcome) {
	tc := persistence.ToolCall{ID: o.ToolCallID, PromptID: a.promptID, Actor: a.actor.CreatedBy, Name: name,
		Args: json.RawMessage(compactJSON(args))}
	switch {
	case o.OK:
		tc.Status = "ok"
	case o.ChangeSet != nil:
		tc.Status = "invalid"
	default:
		tc.Status = "rejected"
	}
	tc.Error = o.Error
	if o.ChangeSet != nil {
		tc.ChangeSetID = o.ChangeSet.ID
	}
	if o.Data != nil {
		if b, err := json.Marshal(o.Data); err == nil {
			tc.Result = b
		}
	}
	a.AH.ToolCalls = append(a.AH.ToolCalls, tc)
}

func compactJSON(b []byte) []byte {
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		q, _ := json.Marshal(string(b))
		return q
	}
	out, _ := json.Marshal(v)
	return out
}

// ApplyTool is THE mutation entry point: schema validation → semantic validation →
// Commands → proposed ChangeSet → validation → (policy) commit.
func (a *App) ApplyTool(name string, args []byte, policy Policy) Outcome {
	a.callID = fmt.Sprintf("tc_%04d", len(a.AH.ToolCalls)+1)
	out := Outcome{ToolCallID: a.callID, Tool: name}
	a.Bus.Publish(events.AgentToolRequested, map[string]any{"id": a.callID, "tool": name, "args": json.RawMessage(compactJSON(args))})
	defer func() {
		a.recordCall(name, args, out)
		a.Bus.Publish(events.AgentToolResult, map[string]any{"id": out.ToolCallID, "ok": out.OK, "error": out.Error})
	}()

	res, err := a.Reg.Invoke(toolEnv{a}, name, args)
	if err != nil {
		out.Error = err.Error()
		return out
	}
	if res.Plan == nil { // query tool
		out.OK, out.Data = true, res.Data
		return out
	}
	cs, err := a.Mgr.Propose(res.Plan.Intent, res.Plan.Explanation, toolEnv{a}.Provenance(), res.Plan.Commands)
	if err != nil {
		out.Error = err.Error()
		return out
	}
	cs.Provenance.ToolCallID = a.callID
	cs.Warnings = append(cs.Warnings, res.Plan.Warnings...)
	out.ChangeSet, out.Message = cs, cs.Explanation
	if !cs.Valid() {
		for _, v := range cs.Validation {
			if !v.OK && v.Severity == "error" {
				out.Error = fmt.Sprintf("%s check failed: %s", v.Check, v.Message)
				break
			}
		}
		_ = a.Mgr.Reject(cs.ID)
		return out
	}
	out.OK = true
	if res.Plan.RequireApproval {
		policy = PolicyPreview
	}
	commit := policy == PolicyCommit || policy == PolicyAuto && cs.AutoCommitEligible()
	if commit {
		if err := a.commit(cs); err != nil {
			out.OK, out.Error = false, err.Error()
			return out
		}
		out.Committed = true
	}
	return out
}

func (a *App) commit(cs *commands.ChangeSet) error {
	if _, err := a.Mgr.Commit(cs.ID); err != nil {
		return err
	}
	a.trackForCheckpoint(cs)
	a.noteInterpretations(cs)
	_, err := a.refresh()
	return err
}

// noteInterpretations records which nodes an image interpretation produced.
func (a *App) noteInterpretations(cs *commands.ChangeSet) {
	byInt := map[string][]string{}
	for _, id := range cs.Affected {
		if n := a.Graph.Get(id); n != nil && n.Provenance.InterpretationID != "" {
			byInt[n.Provenance.InterpretationID] = append(byInt[n.Provenance.InterpretationID], id)
		}
	}
	for id, nodes := range byInt {
		a.Vision.MarkUsed(id, nodes)
	}
}

// Commit approves a proposed ChangeSet.
func (a *App) Commit(id string) (*commands.ChangeSet, error) {
	cs, ok := a.Mgr.Get(id)
	if !ok {
		return nil, fmt.Errorf("unknown ChangeSet %q", id)
	}
	if err := a.commit(cs); err != nil {
		return nil, err
	}
	return cs, nil
}

func (a *App) Reject(id string) error { return a.Mgr.Reject(id) }

func (a *App) Undo() (*commands.ChangeSet, error) {
	cs, err := a.Mgr.Undo()
	if err != nil {
		return nil, err
	}
	a.dropFromSinceCP(cs.ID)
	_, err = a.refresh()
	return cs, err
}

func (a *App) Redo() (*commands.ChangeSet, error) {
	cs, err := a.Mgr.Redo()
	if err != nil {
		return nil, err
	}
	a.trackForCheckpoint(cs)
	_, err = a.refresh()
	return cs, err
}

// trackForCheckpoint queues a committed ChangeSet for the next checkpoint. Appearance-only
// edits are skipped: they are undoable but are not engineering intent.
func (a *App) trackForCheckpoint(cs *commands.ChangeSet) {
	if !cs.AppearanceOnly() {
		a.sinceCP = append(a.sinceCP, cs.ID)
	}
}

func (a *App) dropFromSinceCP(id string) {
	for i, x := range a.sinceCP {
		if x == id {
			a.sinceCP = append(a.sinceCP[:i], a.sinceCP[i+1:]...)
			return
		}
	}
}

// Pending lists proposed (uncommitted, unrejected) ChangeSets.
func (a *App) Pending() []*commands.ChangeSet {
	var out []*commands.ChangeSet
	for _, cs := range a.Mgr.Log {
		if cs.State == commands.StateProposed {
			out = append(out, cs)
		}
	}
	return out
}

// Checkpoint records a semantic checkpoint if anything was committed since the last one
// (or force is set). It returns nil when there is nothing to record.
func (a *App) Checkpoint(label string, force bool) *history.Checkpoint {
	if len(a.sinceCP) == 0 && !force {
		return nil
	}
	if label == "" {
		label = "Checkpoint"
	}
	if len(label) > 120 {
		label = label[:120]
	}
	cp := a.Hist.Create(label, a.actor.CreatedBy, a.sinceCP, a.Graph, a.Clock())
	a.sinceCP = nil
	a.Bus.Publish(events.CheckpointCreated, map[string]any{"id": cp.ID, "label": cp.Label, "hash": cp.GraphHash})
	return cp
}

// Replay re-derives the model from the committed ChangeSets alone and reports whether it
// reproduces the live graph exactly.
func (a *App) Replay() (replayed *cadir.Graph, match bool, applied int, err error) {
	var sets []*commands.ChangeSet
	for _, cs := range a.Mgr.Log {
		if cs.State == commands.StateCommitted {
			sets = append(sets, cs)
		}
	}
	sort.Slice(sets, func(i, j int) bool { return sets[i].Seq < sets[j].Seq })
	g := cadir.NewGraph()
	if err = commands.ReplayOnto(g, sets); err != nil {
		return nil, false, len(sets), err
	}
	return g, g.Hash() == a.Graph.Hash(), len(sets), nil
}

// Restore proposes (and per policy commits) a return to a checkpoint through the tool path.
func (a *App) Restore(ref string, policy Policy) Outcome {
	args, _ := json.Marshal(map[string]any{"checkpoint": ref})
	return a.ApplyTool("restore_checkpoint", args, policy)
}

// EnvFor exposes the read-only tool environment (for context builders).
func EnvFor(a *App) tools.Env { return toolEnv{a} }
