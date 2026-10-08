// Package tools is the single mutation surface of Simon CAD. The agent, the CLI and
// (from Phase 2) the UI all invoke tools; a mutating tool never touches the model — it
// returns a Plan of commands that become a ChangeSet.
package tools

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/accelone/simoncad/internal/cadir"
	"github.com/accelone/simoncad/internal/commands"
	"github.com/accelone/simoncad/internal/components"
	"github.com/accelone/simoncad/internal/geometry/rebuild"
	"github.com/accelone/simoncad/internal/history"
	"github.com/accelone/simoncad/internal/materials"
	"github.com/accelone/simoncad/internal/vision"
	"github.com/accelone/simoncad/internal/visual"
	"github.com/accelone/simoncad/internal/workspace"
)

// Env is what tools may read. It is implemented by the app composition root.
type Env interface {
	Graph() *cadir.Graph
	Model() (*rebuild.Model, error)
	Now() time.Time
	// Provenance for nodes created by the current call (creator, capability, tool call id, model).
	Provenance() cadir.Provenance
	NextChangeSetID() string
	SelectionRefs() []string
	Checkpoints() *history.Log
	// Vision holds image interpretations (proposals); Images the uploaded source images.
	// Neither is CAD state: a proposal never changes the model.
	Vision() *vision.Store
	Images() *vision.ImageStore
	// Materials is the visual material library (presentation only).
	Materials() *materials.Library
	// Components is the library of reusable projects; ComponentDeps answers dependency and
	// cycle questions about them; ComponentResolver turns a reference into geometry.
	Components() *components.Library
	ComponentDeps() *components.DependencyGraph
	ComponentResolver() *components.Resolver
	// ProjectID is the open project's stable id ("" before it has one). A component instance
	// references a project by this id, never by directory name.
	ProjectID() string
	// PublishComponent writes nodes out as a new reusable project and returns its id. It is a
	// side effect outside the ChangeSet: the directory is content-addressed so that undo and
	// redo land on the same one, but undoing the ChangeSet leaves the library project behind.
	PublishComponent(name, description string, tags []string, nodes []*cadir.Node) (projectID string, err error)
	// MeshDetail / SetMeshDetail: viewport mesh detail ("fine" | "normal" | "coarse").
	// Display only; never CAD state.
	MeshDetail() string
	SetMeshDetail(level string)
	// Workspace is the project's units, grid and snapping settings; SetWorkspace replaces them
	// (validated) and WorkspaceDefaults are the global defaults new projects start from.
	// Presentation and editing aids only: never CAD state, never a ChangeSet.
	Workspace() workspace.Settings
	SetWorkspace(s workspace.Settings) error
	WorkspaceDefaults() workspace.Settings
	// Export writes the current model ("step" or "stl") and returns a summary.
	Export(format, path string, deflection float64) (map[string]any, error)
	// Visual is the visual-refinement state (not CAD state); Render draws standard views
	// offscreen into it; SilhouetteAgainstReference scores a render against its reference.
	Visual() *visual.State
	Render(views []string, w, h int, mode string) ([]*visual.Render, error)
	SilhouetteAgainstReference(r *visual.Render) (visual.SilhouetteScore, bool)
}

// Args are decoded, schema-validated tool arguments.
type Args map[string]any

func (a Args) Has(k string) bool { _, ok := a[k]; return ok }
func (a Args) Str(k string) string {
	s, _ := a[k].(string)
	return s
}
func (a Args) Num(k string) float64 { f, _ := a[k].(float64); return f }
func (a Args) NumOr(k string, d float64) float64 {
	if f, ok := a[k].(float64); ok {
		return f
	}
	return d
}
func (a Args) Bool(k string) bool { b, _ := a[k].(bool); return b }

// Plan is the result of preparing a mutating tool.
type Plan struct {
	Commands    []commands.Command
	Intent      string
	Explanation string
	// Warnings are recorded on the ChangeSet (and stop it from auto-committing).
	Warnings []string
	// RequireApproval keeps the ChangeSet proposed (never auto-committed) whatever the policy,
	// so the user always sees the preview first. Used for interpreted, possibly ambiguous input.
	RequireApproval bool
	// LowRisk marks several commands that form one non-destructive user gesture (align,
	// duplicate several bodies): under the auto policy they commit like a single command.
	LowRisk bool
}

// Tool is one registered capability-tagged operation.
type Tool struct {
	Name        string
	Description string
	Capability  string
	Schema      map[string]any
	// Plan is set for tools that edit the model.
	Plan func(env Env, a Args) (*Plan, error)
	// Query is set for read-only (or file-exporting) tools.
	Query func(env Env, a Args) (any, error)
}

func (t *Tool) Mutating() bool { return t.Plan != nil }

type Registry struct {
	tools map[string]*Tool
	order []string
}

func NewRegistry() *Registry { return &Registry{tools: map[string]*Tool{}} }

func (r *Registry) Register(t *Tool) {
	if _, dup := r.tools[t.Name]; dup {
		panic("duplicate tool " + t.Name)
	}
	if (t.Plan == nil) == (t.Query == nil) {
		panic("tool " + t.Name + " must define exactly one of Plan or Query")
	}
	r.tools[t.Name] = t
	r.order = append(r.order, t.Name)
}

func (r *Registry) Get(name string) (*Tool, bool) { t, ok := r.tools[name]; return t, ok }

// Subset returns the tools of a capability in registration order.
func (r *Registry) Subset(capability string) []*Tool {
	var out []*Tool
	for _, n := range r.order {
		if t := r.tools[n]; t.Capability == capability {
			out = append(out, t)
		}
	}
	return out
}

func (r *Registry) Names() []string {
	n := append([]string(nil), r.order...)
	sort.Strings(n)
	return n
}

// Result of invoking a tool.
type Result struct {
	Tool string
	Plan *Plan // mutating tools
	Data any   // query tools
}

// Decode strictly parses raw JSON arguments (numbers as float64, no trailing data).
func Decode(raw []byte) (Args, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return Args{}, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("arguments are not a valid JSON object: %w", err)
	}
	if dec.More() {
		return nil, fmt.Errorf("unexpected data after arguments")
	}
	if m == nil {
		m = map[string]any{}
	}
	return Args(m), nil
}

// Invoke runs the pipeline: tool lookup → JSON-schema validation → semantic validation
// (inside Plan/Query) → plan or result. Only registered tools can be called.
func (r *Registry) Invoke(env Env, name string, raw []byte) (*Result, error) {
	t, ok := r.tools[name]
	if !ok {
		return nil, fmt.Errorf("unknown tool %q (available: %s)", name, strings.Join(r.Names(), ", "))
	}
	args, err := Decode(raw)
	if err != nil {
		return nil, err
	}
	if err := ValidateSchema(t.Schema, map[string]any(args)); err != nil {
		return nil, fmt.Errorf("invalid arguments for %s: %w", name, err)
	}
	if t.Plan != nil {
		p, err := t.Plan(env, args)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		return &Result{Tool: name, Plan: p}, nil
	}
	data, err := t.Query(env, args)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	return &Result{Tool: name, Data: data}, nil
}
