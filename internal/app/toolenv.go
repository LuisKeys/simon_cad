package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/accelone/simoncad/internal/cadir"
	"github.com/accelone/simoncad/internal/components"
	"github.com/accelone/simoncad/internal/events"
	"github.com/accelone/simoncad/internal/export"
	"github.com/accelone/simoncad/internal/geometry/rebuild"
	"github.com/accelone/simoncad/internal/history"
	"github.com/accelone/simoncad/internal/materials"
	"github.com/accelone/simoncad/internal/meshimport"
	"github.com/accelone/simoncad/internal/stepimport"
	"github.com/accelone/simoncad/internal/vision"
	"github.com/accelone/simoncad/internal/visual"
	"github.com/accelone/simoncad/internal/workspace"
)

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

func (e toolEnv) Components() *components.Library            { return e.a.Lib }
func (e toolEnv) ComponentDeps() *components.DependencyGraph { return e.a.Deps }
func (e toolEnv) ComponentResolver() *components.Resolver    { return e.a.Resolver }
func (e toolEnv) ProjectID() string                          { return e.a.Meta.ID }
func (e toolEnv) PublishComponent(name, description string, tags []string, nodes []*cadir.Node) (string, error) {
	return e.a.PublishComponent(name, description, tags, nodes)
}
func (e toolEnv) MeshDetail() string                      { return e.a.MeshDetail() }
func (e toolEnv) SetMeshDetail(level string)              { e.a.meshDetail = level }
func (e toolEnv) Workspace() workspace.Settings           { return e.a.Workspace.Clone() }
func (e toolEnv) SetWorkspace(s workspace.Settings) error { return e.a.SetWorkspace(s) }
func (e toolEnv) WorkspaceDefaults() workspace.Settings   { return configWorkspaceDefaults() }
func (e toolEnv) Visual() *visual.State                   { return e.a.Visual }
func (e toolEnv) Render(views []string, w, h int, mode string) ([]*visual.Render, error) {
	return e.a.Render(views, w, h, mode)
}
func (e toolEnv) SilhouetteAgainstReference(r *visual.Render) (visual.SilhouetteScore, bool) {
	return e.a.SilhouetteAgainstReference(r)
}
func (e toolEnv) Provenance() cadir.Provenance {
	return cadir.Provenance{CreatedBy: e.a.actor.CreatedBy, Capability: e.a.actor.Capability,
		ToolCallID: e.a.callID, Model: e.a.actor.Model}
}

func (e toolEnv) Export(format, path string, deflection float64) (map[string]any, error) {
	return e.a.export(format, path, deflection)
}

func (e toolEnv) StoreMesh(data []byte, opt meshimport.Options) (*meshimport.Prepared, error) {
	return e.a.StoreMesh(data, opt)
}
func (e toolEnv) ReduceMesh(id string, target int) (*meshimport.Prepared, error) {
	return e.a.ReduceMesh(id, target)
}
func (e toolEnv) MeshInfo(id string) (meshimport.Report, bool) { return e.a.MeshInfo(id) }

func (e toolEnv) StoreStep(data []byte) (*stepimport.Prepared, error) { return e.a.StoreStep(data) }
func (e toolEnv) StepInfo(id string) (stepimport.Report, bool)        { return e.a.StepInfo(id) }

func (e toolEnv) ResolveIOPath(path string, write bool) (string, error) {
	return e.a.resolveIOPath(path, write)
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
