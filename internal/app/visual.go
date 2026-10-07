package app

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/accelone/simoncad/internal/cadir"
	"github.com/accelone/simoncad/internal/commands"
	"github.com/accelone/simoncad/internal/rendering"
	"github.com/accelone/simoncad/internal/visual"
)

// offscreen returns the renderer used for offscreen views (its own mesh cache, always at
// fine detail so renders do not depend on the viewport's display setting).
func (a *App) offscreen() *rendering.Renderer {
	if a.offscreenR == nil {
		a.offscreenR = rendering.New()
	}
	return a.offscreenR
}

// visibleMeshes collects the visible bodies with their display colors.
func (a *App) visibleMeshes() ([]visual.Mesh, [3]float64, [3]float64, error) {
	m, err := a.Model()
	if err != nil {
		return nil, [3]float64{}, [3]float64{}, err
	}
	var out []visual.Mesh
	lo := [3]float64{math.Inf(1), math.Inf(1), math.Inf(1)}
	hi := [3]float64{math.Inf(-1), math.Inf(-1), math.Inf(-1)}
	for _, id := range m.Leaves {
		n := a.Graph.Get(id)
		if n != nil && n.Appearance != nil && n.Appearance.Visible != nil && !*n.Appearance.Visible {
			continue
		}
		bm, _, err := a.offscreen().Body(id, m.Nodes[id])
		if err != nil {
			return nil, lo, hi, err
		}
		col := ""
		if n != nil && n.Appearance != nil {
			col = n.Appearance.Color
			if col == "" && n.Appearance.MaterialID != "" {
				if mat, ok := a.Materials.Get(n.Appearance.MaterialID); ok {
					col = mat.BaseColor
				}
			}
		}
		out = append(out, visual.Mesh{ID: id, Positions: bm.Positions, Normals: bm.Normals, Indices: bm.Indices, Color: col})
		for i := 0; i < 3; i++ {
			lo[i] = math.Min(lo[i], bm.Bounds.Min[i])
			hi[i] = math.Max(hi[i], bm.Bounds.Max[i])
		}
	}
	return out, lo, hi, nil
}

// RenderCamera renders the model from one camera into the visual render store. The frame
// stays fixed across renders (and so across iterations) unless the model outgrows it.
func (a *App) RenderCamera(view string, cam visual.Camera, w, h int, mode string) (*visual.Render, error) {
	meshes, lo, hi, err := a.visibleMeshes()
	if err != nil {
		return nil, err
	}
	if len(meshes) == 0 {
		return nil, fmt.Errorf("the model has no visible geometry to render")
	}
	vs := a.Visual
	vs.Lock()
	if vs.Frame == nil || !vs.Frame.Contains(lo, hi) {
		f := visual.FrameFor(lo, hi)
		vs.Frame = &f
	}
	frame := *vs.Frame
	vs.Unlock()
	if mode == "" {
		mode = visual.ModeShaded
	}
	im, err := visual.Rasterize(meshes, cam, frame, visual.Options{Width: w, Height: h, Mode: mode})
	if err != nil {
		return nil, err
	}
	png, err := im.PNG()
	if err != nil {
		return nil, err
	}
	vs.Lock()
	defer vs.Unlock()
	return vs.PutRender(&visual.Render{View: view, Mode: mode, Camera: cam, Width: w, Height: h, PNG: png, Mask: im.Mask, Iteration: len(vs.History)}), nil
}

// Render renders standard views (default: left, front, top, front_3q).
func (a *App) Render(views []string, w, h int, mode string) ([]*visual.Render, error) {
	if len(views) == 0 {
		views = visual.DefaultViews
	}
	if w == 0 || h == 0 {
		w, h = 1024, 1024
	}
	var out []*visual.Render
	for _, v := range views {
		cam, err := visual.StandardCamera(v)
		if err != nil {
			return nil, err
		}
		r, err := a.RenderCamera(v, cam, w, h, mode)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}

// SilhouetteAgainstReference scores a render against the segmented reference of the same
// view, if there is one.
func (a *App) SilhouetteAgainstReference(r *visual.Render) (visual.SilhouetteScore, bool) {
	vs := a.Visual
	vs.Lock()
	defer vs.Unlock()
	for _, ref := range vs.References {
		if ref.Mask() == nil || ref.Calibration == nil || r.Mask == nil {
			continue
		}
		if ref.Calibration.View == r.View || sideView(ref.Calibration.View) && sideView(r.View) {
			return visual.CompareSilhouettes(ref.Mask(), r.Mask, sideView(r.View) || r.View == "top"), true
		}
	}
	return visual.SilhouetteScore{}, false
}

func sideView(v string) bool { return v == "left" || v == "right" }

// lockViolation explains why a proposed ChangeSet would modify a locked region ("" if it
// does not). Appearance-only edits are allowed: locks protect geometry.
func (a *App) lockViolation(cs *commands.ChangeSet, cmds []commands.Command) string {
	if cs.AppearanceOnly() || len(a.Visual.Regions) == 0 {
		return ""
	}
	a.Visual.Lock()
	defer a.Visual.Unlock()
	anyLocked := false
	for _, r := range a.Visual.Regions {
		anyLocked = anyLocked || r.Locked
	}
	if !anyLocked {
		return ""
	}
	work := a.Graph.Clone()
	for _, c := range cmds {
		if _, err := c.Apply(work); err != nil {
			return "" // the ChangeSet's own validation reports invalid commands
		}
	}
	var problems []string
	for _, old := range a.Graph.Nodes {
		nw := work.Get(old.ID)
		if old.Type == "parameters" {
			if nw == nil {
				for name := range old.Parameters {
					if r := a.Visual.LockedParameter(old.ID, name); r != "" {
						problems = append(problems, fmt.Sprintf("removes parameter set %s of locked region %q", old.ID, r))
						break
					}
				}
				continue
			}
			for name, v := range old.Parameters {
				if r := a.Visual.LockedParameter(old.ID, name); r != "" && (fmt.Sprint(nw.Parameters[name]) != fmt.Sprint(v) || nw.Expressions[name] != old.Expressions[name]) {
					problems = append(problems, fmt.Sprintf("changes %s.%s of locked region %q", old.ID, name, r))
				}
			}
			continue
		}
		r := a.Visual.LockedObject(old.ID)
		if r == "" {
			continue
		}
		switch {
		case nw == nil:
			problems = append(problems, fmt.Sprintf("removes %s of locked region %q", old.ID, r))
		case nw.ContentKey() != old.ContentKey():
			problems = append(problems, fmt.Sprintf("modifies %s of locked region %q", old.ID, r))
		}
	}
	for _, n := range work.Nodes { // new features that consume a locked object reshape it
		if a.Graph.Get(n.ID) != nil {
			continue
		}
		for _, d := range cadir.EffectiveDeps(n) {
			if dep := work.Get(d); dep != nil && dep.Type != "parameters" {
				if r := a.Visual.LockedObject(d); r != "" {
					problems = append(problems, fmt.Sprintf("%s would reshape %s of locked region %q", n.ID, d, r))
				}
			}
		}
	}
	if len(problems) == 0 {
		return ""
	}
	sort.Strings(problems)
	return "region locked: this change " + strings.Join(problems, "; ") + ". Unlock the region first (only if the user asks)."
}
