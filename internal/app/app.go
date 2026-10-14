// Package app is the composition root. The CLI, the agent runtime and (from Phase 2) the
// UI all drive the model through App.ApplyTool: tool → commands → ChangeSet → commit.
// There is no other mutation path.
package app

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/accelone/simoncad/internal/cadir"
	"github.com/accelone/simoncad/internal/commands"
	"github.com/accelone/simoncad/internal/components"
	"github.com/accelone/simoncad/internal/config"
	"github.com/accelone/simoncad/internal/events"
	"github.com/accelone/simoncad/internal/geometry/api"
	"github.com/accelone/simoncad/internal/geometry/rebuild"
	"github.com/accelone/simoncad/internal/history"
	"github.com/accelone/simoncad/internal/materials"
	"github.com/accelone/simoncad/internal/persistence"
	"github.com/accelone/simoncad/internal/rendering"
	"github.com/accelone/simoncad/internal/tools"
	"github.com/accelone/simoncad/internal/vision"
	"github.com/accelone/simoncad/internal/visual"
	"github.com/accelone/simoncad/internal/workspace"
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
	// Visual is the visual-refinement state (references, regions and locks, iteration
	// history, offscreen renders). Not CAD state; region locks are enforced in ApplyTool.
	Visual     *visual.State
	offscreenR *rendering.Renderer
	// meshDetail is the viewport mesh detail ("fine" | "normal" | "coarse"). Display only:
	// it is not CAD state, is not saved and never changes geometry.
	meshDetail string
	// Workspace is the project's units, grid and snapping (workspace.json): presentation and
	// editing aids, never an input to geometry. WorkspaceWarnings say what could not be read
	// from the project's file and was replaced by defaults.
	Workspace         workspace.Settings
	WorkspaceWarnings []string
	// OpenWarnings are things worth telling the user about the project that was just opened
	// (for example that it was saved with another OpenCASCADE version). They never block it.
	OpenWarnings []string

	// Raw preserved files not interpreted here. components.json is derived on save from the
	// graph's component instances, so it is no longer carried through.
	constraints, materials json.RawMessage
	// Lib is the component library (which reusable projects exist and where); Resolver turns
	// a component instance into geometry. Both are kept out of rendering and UI logic.
	Lib      *components.Library
	Resolver *components.Resolver
	Deps     *components.DependencyGraph
	dir      string // the project's directory, "" until saved or opened; anchors the library
	// savedHash is the graph hash as of the last save; it decides whether a reusable project's
	// component_version advances, so save → load → save stays byte-identical.
	savedHash string

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
	lib := components.NewLibrary()
	a := &App{K: k, Engine: rebuild.New(k), Graph: cadir.NewGraph(), Hist: &history.Log{}, Bus: events.NewBus(),
		Reg: tools.Default(), Clock: clock, actor: Actor{CreatedBy: cadir.CreatedByCLI},
		Vision: vision.NewStore(), Images: vision.NewImageStore(), Visual: visual.NewState(),
		Materials: materials.New(materialsdata.FS()),
		Lib:       lib, Resolver: components.NewResolver(lib, k), Deps: components.NewDependencyGraph(lib)}
	a.Engine.Components = a.Resolver
	a.useLibraryRoots("")
	a.Bus.Now = clock
	a.Mgr = commands.NewManager(a.Graph, a, a.Bus)
	a.Mgr.Now = clock
	return a
}

// New starts an empty project. Its workspace settings are a copy of the global defaults:
// later changes to the defaults do not follow it.
func New(name string, clock func() time.Time) *App {
	a := newApp(clock)
	a.Meta = persistence.Meta{SchemaVersion: persistence.SchemaVersion, ID: persistence.NewID(),
		Name: name, Units: "mm", OCCTVersion: a.K.Version(), Created: a.Clock().UTC().Format(time.RFC3339)}
	a.Workspace = configWorkspaceDefaults().Clone()
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
	// The same model must rebuild to the same geometry; that contract is only pinned for the
	// kernel version that wrote the project, so say so when the running one differs.
	if saved, running := p.Meta.OCCTVersion, a.K.Version(); saved != "" && saved != running {
		a.OpenWarnings = append(a.OpenWarnings, fmt.Sprintf(
			"This project was saved with OpenCASCADE %s and is open with %s: its geometry may differ slightly from what it was designed with.", saved, running))
	}
	// A project keeps its own workspace settings. One saved before they existed gets the
	// factory defaults (what it was designed with), never the user's current global
	// defaults: those must not silently change an existing project.
	a.Workspace, a.WorkspaceWarnings = workspace.Defaults(), p.WorkspaceWarnings
	if p.Workspace != nil {
		a.Workspace = *p.Workspace
	}
	a.useProjectMaterials(dir)
	a.useLibraryRoots(dir)
	a.constraints, a.materials = p.Constraints, p.Materials
	*a.Graph = *p.Graph
	if len(a.AH.Interpretations) > 0 {
		if err := json.Unmarshal(a.AH.Interpretations, a.Vision); err != nil {
			return nil, fmt.Errorf("agent-history.json interpretations: %w", err)
		}
	}
	if len(a.AH.Visual) > 0 {
		if err := json.Unmarshal(a.AH.Visual, a.Visual); err != nil {
			return nil, fmt.Errorf("agent-history.json visual: %w", err)
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
	a.savedHash = a.Graph.Hash()
	return a, nil
}

func (a *App) Close() {
	a.Engine.Close()
	a.K.Close()
}

// useLibraryRoots points the component library at the project's own neighbourhood plus the
// configured library root. The roots are never derived from the process working directory: a
// component resolved by where the binary happens to run would make the same .scadproj rebuild
// to different geometry from a different shell.
func (a *App) useLibraryRoots(dir string) {
	a.dir = dir
	a.Lib.SetRoots(components.RootsFor(dir, configComponentLibrary())...)
}

// configComponentLibrary is the configured library root, indirected so tests can point the
// library somewhere else without touching the user's settings.
var configComponentLibrary = config.ComponentLibrary

// configWorkspaceDefaults are the global workspace defaults, indirected like the library root.
var configWorkspaceDefaults = config.WorkspaceDefaults

// Dir is the project's directory ("" until it is saved or opened).
func (a *App) Dir() string { return a.dir }

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
	if h := a.Graph.Hash(); a.Meta.Reusable && h != a.savedHash {
		a.Meta.Version++ // a reusable project's version advances only when its content changed
	}
	a.AH.Interpretations = nil
	if len(a.Vision.List()) > 0 {
		b, err := json.Marshal(a.Vision)
		if err != nil {
			return err
		}
		a.AH.Interpretations = b
	}
	a.AH.Visual = nil
	if !a.Visual.Empty() {
		b, err := json.Marshal(a.Visual)
		if err != nil {
			return err
		}
		a.AH.Visual = b
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
		Components: components.Manifest(a.Graph), Constraints: a.constraints, Materials: a.materials, Images: imgs,
		Workspace: &a.Workspace,
	}); err != nil {
		return err
	}
	a.savedHash = a.Graph.Hash()
	if err := a.carryProjectMaterials(dir); err != nil {
		return err
	}
	a.useLibraryRoots(dir)
	return nil
}

// SetActor sets who is driving subsequent tool calls.
func (a *App) SetActor(ac Actor) { a.actor = ac }

// ReIdentify mints a new stable project id, recording the one it was copied from. "Save as"
// calls it so two directories never claim the same id and component references stay unambiguous.
// A plain Save never does: copying a project directory by hand keeps it the same project.
func (a *App) ReIdentify() {
	a.Meta.DerivedFrom, a.Meta.ID = a.Meta.ID, persistence.NewID()
}

// SetReusable marks the project as a reusable component (or stops it being one) and sets its
// library description and tags.
func (a *App) SetReusable(reusable bool, description string, tags []string) {
	a.Meta.Reusable, a.Meta.Description, a.Meta.Tags = reusable, description, tags
	if !reusable {
		a.Meta.Description, a.Meta.Tags, a.Meta.Version = "", nil, 0
	}
}
