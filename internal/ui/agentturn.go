package ui

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/accelone/simoncad/internal/agent/attachments"
	geo "github.com/accelone/simoncad/internal/agent/capabilities/geometry"
	vis "github.com/accelone/simoncad/internal/agent/capabilities/vision"
	visualcap "github.com/accelone/simoncad/internal/agent/capabilities/visual"
	"github.com/accelone/simoncad/internal/agent/runtime"
	"github.com/accelone/simoncad/internal/agent/workflow"
	"github.com/accelone/simoncad/internal/app"
)

// Prompt starts one agent turn on a goroutine. Progress arrives on the Hub as chat
// messages; the turn ends with a "agent_done" notice.
func (s *Service) Prompt(text string, thinking bool) error {
	return s.PromptWith(text, thinking, nil)
}

// PromptWith is Prompt with uploaded images (ids from UploadImage) attached to the request.
func (s *Service) PromptWith(text string, thinking bool, images []string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return fmt.Errorf("empty prompt")
	}
	if len(images) > maxPromptImages {
		return fmt.Errorf("attach at most %d images to one request", maxPromptImages)
	}
	s.mu.Lock()
	if err := s.guard(); err != nil {
		s.mu.Unlock()
		return err
	}
	for _, id := range images {
		if _, ok := s.app.Images.Get(id); !ok {
			s.mu.Unlock()
			return fmt.Errorf("image %q was not uploaded", id)
		}
	}
	prov, err := s.NewProvider()
	if err != nil {
		s.mu.Unlock()
		return err
	}
	// Images the selected provider cannot see go to a vision-capable model first (attachment
	// processor + model router); the selected provider stays the CAD agent. The CAD agent
	// itself runs on the router's agent provider, which may hand the request over to the
	// fallback model once if the primary becomes unavailable.
	router := s.router(prov)
	// The whole request runs on the fallback (frontier) model when something is attached
	// (an image or a document) and the primary is local, when the prompt asks for it, or
	// when an earlier turn of this conversation already ran there: a conversation never
	// drops from the frontier model back to the local one (until Clear).
	// Without a usable fallback the request stays on the primary, as before.
	reason, asked := "", runtime.WantsFrontier(text)
	switch {
	case asked:
		reason = "requested in the prompt"
	case len(images) > 0:
		reason = "an image is attached"
	case len(s.specs) > 0:
		reason = "a document is attached"
	case s.frontier:
		reason = "the conversation runs on the frontier model"
	}
	agentProv, onFallback, err := router.AgentOnFallback(reason, asked)
	if err != nil {
		s.mu.Unlock()
		return err
	}
	var interp runtime.ImageInterpreter
	if !onFallback && len(images) > 0 && !runtime.CapabilitiesOf(prov).Vision {
		proc := &attachments.Processor{Router: router, NoThinking: !thinking,
			Wrap: func(p runtime.Provider) runtime.Provider { return unlocking{p, &s.mu} },
			OnRoute: func(kind attachments.Kind, used string) {
				label := used
				for _, fb := range router.Fallbacks {
					if fb.Name() == used {
						label = runtime.Label(fb)
					}
				}
				s.announceFallback(label, fmt.Sprintf("%s cannot read images; the image is interpreted by %s.", prov.Name(), used))
			}}
		if err := proc.Available(attachments.KindImage); err != nil {
			s.mu.Unlock()
			return err
		}
		interp = proc.Images()
	}
	s.keepDraft() // a new request implicitly accepts the previous batch
	s.busy = true
	s.agentState = &workflow.Event{State: workflow.StateIdle}
	s.beginRoutes()
	prior := s.history()
	s.sayWith("user", text, "", true, "", append([]string(nil), images...))
	docs := s.newSpecs() // the message shows the documents it introduced
	s.chat[len(s.chat)-1].Docs = docs
	var docIDs []string
	for _, d := range docs {
		docIDs = append(docIDs, d.ID)
	}
	s.hub.Publish(Notice{Kind: "state"})
	level := s.level
	s.mu.Unlock()
	go func() {
		defer func() {
			s.mu.Lock()
			s.busy = false
			s.hub.Publish(Notice{Kind: "agent_done"})
			s.hub.Publish(Notice{Kind: "state"})
			s.mu.Unlock()
		}()
		// The lock is held for the turn, except while waiting on the model (see unlocking).
		s.mu.Lock()
		defer s.mu.Unlock()
		caps := runtime.SelectCaps(s.app, images, geo.New(), vis.New(), visualcap.New())
		rt := &runtime.Runtime{Provider: unlocking{agentProv, &s.mu}, Caps: caps, History: prior, NoThinking: !thinking, ToolLoading: runtime.InteractiveToolLoading(),
			ImageIDs: images, DocumentIDs: docIDs, Interpreter: interp, Reference: s.specContext()}
		rt.OnStep = func(name string, args json.RawMessage, o app.Outcome) {
			cs := ""
			if o.ChangeSet != nil {
				cs = o.ChangeSet.ID
			}
			msg := o.Message
			if o.Error != "" {
				msg = o.Error
			}
			s.say("tool", msg, name, o.OK, cs)
			if o.Committed && level == LevelReview && !o.ChangeSet.AppearanceOnly() {
				s.draft = append(s.draft, cs)
			}
			s.hub.Publish(Notice{Kind: "state"})
		}
		// Review applies each step as a draft so later steps can build on earlier ones; the
		// user then keeps or discards the whole batch.
		pol := app.PolicyCommit
		if level == LevelAuto {
			pol = app.PolicyAuto
		}
		var res *runtime.Result
		var err error
		if s.Autonomous {
			ag := &workflow.Agent{Runtime: rt, OnState: s.setAgentState, OnRevert: s.dropFromDraft,
				OnPlan: func(p *workflow.Plan) { s.say("plan", "Plan:\n"+p.Text(), "", true, "") }}
			var wr *workflow.Result
			wr, err = ag.Run(context.Background(), s.app, text, pol)
			res = wr.Result
		} else {
			res, err = rt.Run(context.Background(), s.app, text, pol)
		}
		switch {
		case err != nil:
			s.say("system", "Agent error: "+err.Error(), "", false, "")
		case res.Final != "":
			s.say("agent", res.Final, "", true, "")
		}
		// Usage is reported even when the turn failed: the tokens were spent either way.
		if runtime.IsLocal(prov) && !runtime.IsLocal(agentProv) { // ran on the cloud fallback
			s.frontier = true
		}
		s.recordUsage(res.Usage)
		s.endRoutes(res.Usage)
		s.sayUsage(res.Usage)
		if len(s.draft) > 0 {
			s.say("draft", fmt.Sprintf("%d step(s) applied as a draft — keep or discard them.", len(s.draft)), "", true, "")
		} else if cp := s.app.Checkpoint(text, false); cp != nil {
			s.hub.Publish(Notice{Kind: "state"})
		}
		s.app.SetActor(app.Actor{CreatedBy: "user"})
		s.changed()
	}()
	return nil
}

// setAgentState publishes a workflow transition. Caller holds mu.
func (s *Service) setAgentState(ev workflow.Event) {
	s.agentState = &ev
	s.hub.Publish(Notice{Kind: "agent_state", State: &ev})
}

// dropFromDraft forgets draft ChangeSets that the workflow undid. Caller holds mu.
func (s *Service) dropFromDraft(ids []string) {
	gone := map[string]bool{}
	for _, id := range ids {
		gone[id] = true
	}
	kept := s.draft[:0]
	for _, id := range s.draft {
		if !gone[id] {
			kept = append(kept, id)
		}
	}
	s.draft = kept
	s.hub.Publish(Notice{Kind: "state"})
}

// maxPromptImages bounds the images of one request.
const maxPromptImages = 10
