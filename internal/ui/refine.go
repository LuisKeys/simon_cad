package ui

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/accelone/simoncad/internal/agent/refine"
	"github.com/accelone/simoncad/internal/agent/runtime"
	"github.com/accelone/simoncad/internal/app"
	"github.com/accelone/simoncad/internal/visual"
)

// RefineRequest starts a visual refinement run.
type RefineRequest struct {
	Goal          string   `json:"goal"`
	Images        []string `json:"images"` // new reference images (uploaded ids)
	MaxIterations int      `json:"max_iterations"`
	Thinking      bool     `json:"thinking"`
}

// RunRefinement starts the visual closed-loop refinement on a goroutine. The selected
// provider is the CAD builder; the critic and the reference analysis use a vision-capable
// provider chosen by the model router. Progress arrives as "refine" notices.
func (s *Service) RunRefinement(req RefineRequest) error {
	s.mu.Lock()
	if err := s.guard(); err != nil {
		s.mu.Unlock()
		return err
	}
	for _, id := range req.Images {
		if _, ok := s.app.Images.Get(id); !ok {
			s.mu.Unlock()
			return fmt.Errorf("image %q was not uploaded", id)
		}
	}
	if len(req.Images) == 0 && len(s.app.Visual.References) == 0 {
		s.mu.Unlock()
		return fmt.Errorf("attach at least one reference image to start a visual refinement")
	}
	if strings.TrimSpace(req.Goal) == "" && s.app.Visual.Goal == "" {
		s.mu.Unlock()
		return fmt.Errorf("describe what to model")
	}
	prov, err := s.NewProvider()
	if err != nil {
		s.mu.Unlock()
		return err
	}
	router := s.router(prov)
	vision, visionFallback, err := router.Route(runtime.ProviderCapabilities{Vision: true})
	if err != nil {
		s.mu.Unlock()
		return err
	}
	visionReason := ""
	if visionFallback {
		visionReason = prov.Name() + " cannot read images; the visual critique runs on " + vision.Name()
	}
	vision = router.Observe(vision, visionFallback, visionReason)
	// builds the model: on the fallback from the start when the primary is local (the
	// request always carries images) or the goal asks for it; otherwise it may hand over
	// to the fallback once
	agent, _, err := router.AgentOnFallback("an image is attached", runtime.WantsFrontier(req.Goal))
	if err != nil {
		s.mu.Unlock()
		return err
	}
	s.keepDraft()
	s.beginRoutes()
	s.busy = true
	ctx, cancel := context.WithCancel(context.Background())
	s.refineCancel = cancel
	s.refineState = &refine.Event{State: refine.StateAnalyzing, Message: "Starting visual refinement…"}
	text := "Visual refinement: " + strings.TrimSpace(req.Goal)
	if strings.TrimSpace(req.Goal) == "" {
		text = "Continue visual refinement: " + s.app.Visual.Goal
	}
	s.sayWith("user", text, "", true, "", append([]string(nil), req.Images...))
	s.hub.Publish(Notice{Kind: "state"})
	s.mu.Unlock()

	go func() {
		defer func() {
			s.mu.Lock()
			s.busy, s.refineCancel = false, nil
			s.hub.Publish(Notice{Kind: "agent_done"})
			s.hub.Publish(Notice{Kind: "visual"})
			s.hub.Publish(Notice{Kind: "state"})
			s.mu.Unlock()
		}()
		s.mu.Lock()
		defer s.mu.Unlock()
		wrap := func(p runtime.Provider) runtime.Provider { return unlocking{p, &s.mu} }
		b := &refine.Builder{Provider: wrap(agent), NoThinking: !req.Thinking}
		b.OnStep = func(name string, args json.RawMessage, o app.Outcome) {
			msg := o.Message
			if o.Error != "" {
				msg = o.Error
			}
			cs := ""
			if o.ChangeSet != nil {
				cs = o.ChangeSet.ID
			}
			s.say("tool", msg, name, o.OK, cs)
		}
		if visionFallback {
			s.announceFallback(runtime.Label(vision),
				fmt.Sprintf("visual critique and reference analysis use %s; %s builds the CAD model.", vision.Name(), prov.Name()))
		}
		ctrl := &refine.Controller{App: s.app, Analyzer: &refine.Analyzer{Vision: wrap(vision)}, Planner: &refine.Planner{Provider: wrap(agent)},
			Builder: b, Critic: &refine.VisionCritic{Vision: wrap(vision)}, MaxIterations: req.MaxIterations,
			OnEvent: func(ev refine.Event) {
				s.refineState = &ev
				s.hub.Publish(Notice{Kind: "refine", Refine: &ev})
			},
			OnIteration: func(*visual.Iteration) { s.hub.Publish(Notice{Kind: "visual"}); s.changed() },
			OnRevert:    s.dropFromDraft,
		}
		res, err := ctrl.Run(ctx, req.Goal, req.Images)
		switch {
		case err != nil:
			s.refineState = &refine.Event{State: refine.StateFailed, Message: err.Error()}
			s.say("system", "Visual refinement error: "+err.Error(), "", false, "")
		default:
			s.say("agent", res.Summary, "", true, "")
		}
		u := ctrl.Usage() // also counted when the run failed or was stopped part-way
		s.recordUsage(u)
		s.endRoutes(u)
		s.sayUsage(u)
		s.app.SetActor(app.Actor{CreatedBy: "user"})
		s.changed()
	}()
	return nil
}

// StopRefinement asks a running refinement to stop after its current step.
func (s *Service) StopRefinement() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.refineCancel == nil {
		return fmt.Errorf("no visual refinement is running")
	}
	s.refineCancel()
	return nil
}

// VisualView is the Visual Comparison panel's data.
type VisualView struct {
	Goal       string              `json:"goal"`
	Stage      string              `json:"stage"`
	Stages     []visual.Stage      `json:"stages"`
	Running    bool                `json:"running"`
	State      *refine.Event       `json:"state,omitempty"`
	References []*visual.Reference `json:"references"`
	HasMask    map[string]bool     `json:"has_mask"`
	Geometry   map[string]float64  `json:"reference_geometry,omitempty"`
	Regions    []*visual.Region    `json:"regions"`
	History    []*visual.Iteration `json:"history"`
	Failed     []visual.Correction `json:"failed_corrections"`
	Available  map[string]bool     `json:"renders"` // render ids still in memory
	Parameters map[string]float64  `json:"parameters"`
}

// Visual returns the panel's view of the visual-refinement state.
func (s *Service) Visual() VisualView {
	s.mu.Lock()
	defer s.mu.Unlock()
	vs := s.app.Visual
	v := VisualView{Goal: vs.Goal, Stages: visual.Stages, Running: s.refineCancel != nil, State: s.refineState,
		References: vs.References, HasMask: map[string]bool{}, Geometry: vs.ReferenceGeometry, Regions: vs.Regions,
		History: vs.History, Failed: vs.Failed, Available: map[string]bool{}, Parameters: s.parameters()}
	if vs.Stage < len(visual.Stages) {
		v.Stage = visual.Stages[vs.Stage].Name
	}
	for _, r := range vs.References {
		v.HasMask[r.ImageID] = r.Mask() != nil
	}
	for _, it := range vs.History {
		for _, id := range it.Renders {
			if _, ok := vs.Render(id); ok {
				v.Available[id] = true
			}
		}
	}
	if v.Failed == nil {
		v.Failed = []visual.Correction{}
	}
	return v
}

func (s *Service) parameters() map[string]float64 {
	out := map[string]float64{}
	for _, n := range s.app.Snapshot().Nodes {
		if n.Type != "parameters" {
			continue
		}
		for k, val := range n.Parameters {
			if f, ok := val.(float64); ok {
				out[n.ID+"."+k] = f
			}
		}
	}
	return out
}

// RenderPNG returns a stored render.
func (s *Service) RenderPNG(id string) ([]byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.app.Visual.Render(id)
	if !ok {
		return nil, false
	}
	return r.PNG, true
}

// VisualDiff draws the silhouette difference of an iteration's render for one reference
// (reference only = red, model only = blue), or — with previous — between this iteration
// and the one before it (previous only = red, current only = blue).
func (s *Service) VisualDiff(n int, refID string, previous bool) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	vs := s.app.Visual
	it, ok := vs.Iteration(n)
	if !ok {
		return nil, fmt.Errorf("no iteration %d", n)
	}
	key := "ref:" + refID
	if _, ok := it.Renders[key]; !ok {
		key = refID // a standard view name
	}
	cur, ok := vs.Render(it.Renders[key])
	if !ok || cur.Mask == nil {
		return nil, fmt.Errorf("the render of iteration %d is no longer in memory", n)
	}
	if previous {
		if n < 2 {
			return nil, fmt.Errorf("iteration %d has no previous iteration", n)
		}
		prevIt, _ := vs.Iteration(n - 1)
		prev, ok := vs.Render(prevIt.Renders[key])
		if !ok || prev.Mask == nil {
			return nil, fmt.Errorf("the render of iteration %d is no longer in memory", n-1)
		}
		return visual.DifferencePNG(prev.Mask, cur.Mask, false)
	}
	ref := vs.Reference(refID)
	if ref == nil || ref.Mask() == nil {
		return nil, fmt.Errorf("reference %s has no silhouette", refID)
	}
	return visual.DifferencePNG(ref.Mask(), cur.Mask, it.Silhouettes[key].Mirrored)
}

// RevertIteration returns the model to its state before iteration n (the checkpoint of the
// latest kept iteration before it) and marks n and later iterations reverted.
func (s *Service) RevertIteration(n int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.guard(); err != nil {
		return err
	}
	vs := s.app.Visual
	if _, ok := vs.Iteration(n); !ok || n < 2 {
		return fmt.Errorf("iteration %d cannot be reverted (the first iteration is the baseline)", n)
	}
	target, stage := "", ""
	for i := n - 1; i >= 1 && target == ""; i-- {
		if it, _ := vs.Iteration(i); !it.Reverted && it.Checkpoint != "" {
			target, stage = it.Checkpoint, it.Stage
		}
	}
	if target == "" {
		return fmt.Errorf("no earlier iteration to return to")
	}
	if err := s.restoreTo(target); err != nil {
		return err
	}
	if i := visual.StageIndex(stage); i >= 0 { // the stage goes back with the model
		vs.Stage = i
	}
	for _, it := range vs.History[n-1:] {
		it.Reverted, it.Accepted = true, false
		if it.Note == "" {
			it.Note = "reverted by the user"
		}
	}
	s.say("system", fmt.Sprintf("Reverted visual iteration %d and later.", n), "", true, "")
	s.dirty = true
	s.changed()
	s.hub.Publish(Notice{Kind: "visual"})
	return nil
}

// AcceptIteration keeps iteration n: the model returns to its state (if needed) and later
// iterations are discarded.
func (s *Service) AcceptIteration(n int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.guard(); err != nil {
		return err
	}
	vs := s.app.Visual
	it, ok := vs.Iteration(n)
	if !ok || it.Checkpoint == "" {
		return fmt.Errorf("no iteration %d to accept", n)
	}
	if it.Reverted {
		return fmt.Errorf("iteration %d was reverted", n)
	}
	if err := s.restoreTo(it.Checkpoint); err != nil {
		return err
	}
	for _, later := range vs.History[n:] {
		if !later.Reverted {
			later.Reverted, later.Note = true, "discarded: an earlier iteration was accepted"
		}
	}
	it.Accepted = true
	if i := visual.StageIndex(it.Stage); i >= 0 {
		vs.Stage = i
	}
	s.say("system", fmt.Sprintf("Accepted visual iteration %d.", n), "", true, "")
	s.dirty = true
	s.changed()
	s.hub.Publish(Notice{Kind: "visual"})
	return nil
}

// restoreTo restores a checkpoint unless the model already matches it. Caller holds mu.
func (s *Service) restoreTo(cp string) error {
	o := s.app.Restore(cp, app.PolicyCommit)
	if !o.OK && !strings.Contains(o.Error, "already matches") {
		return fmt.Errorf("%s", o.Error)
	}
	return nil
}

// SetRegionLock locks or unlocks a region through the tool path (recorded like any call).
func (s *Service) SetRegionLock(name string, locked bool) error {
	tool := "unlock_region"
	if locked {
		tool = "lock_region"
	}
	args, _ := json.Marshal(map[string]string{"name": name})
	o, err := s.CallTool(tool, args)
	if err != nil {
		return err
	}
	if !o.OK {
		return fmt.Errorf("%s", o.Error)
	}
	s.hub.Publish(Notice{Kind: "visual"})
	return nil
}
