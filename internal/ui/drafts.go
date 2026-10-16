package ui

import (
	"fmt"
)

// keepDraft accepts the open draft batch and records its checkpoint. Caller holds mu.
func (s *Service) keepDraft() {
	if len(s.draft) == 0 {
		return
	}
	s.draft = nil
	s.app.Checkpoint(s.lastIntent(), false)
}

func (s *Service) lastIntent() string {
	if p := s.app.AH.Prompts; len(p) > 0 {
		return p[len(p)-1].Text
	}
	return "Agent changes"
}

// KeepDraft accepts the agent's draft batch.
func (s *Service) KeepDraft() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.guard(); err != nil {
		return err
	}
	if len(s.draft) == 0 {
		return fmt.Errorf("no draft to keep")
	}
	s.keepDraft()
	s.say("system", "Draft kept.", "", true, "")
	s.hub.Publish(Notice{Kind: "state"})
	return nil
}

// DiscardDraft undoes the agent's draft batch, newest step first.
func (s *Service) DiscardDraft() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.guard(); err != nil {
		return err
	}
	if len(s.draft) == 0 {
		return fmt.Errorf("no draft to discard")
	}
	for i := len(s.draft) - 1; i >= 0; i-- {
		cs, err := s.app.Undo()
		if err != nil {
			s.draft = nil
			s.changed()
			return err
		}
		if cs.ID != s.draft[i] {
			s.draft = nil
			s.changed()
			return fmt.Errorf("the model changed after the draft; use Undo instead")
		}
	}
	s.draft = nil
	s.say("system", "Draft discarded.", "", true, "")
	s.changed()
	return nil
}

// ClearChat empties the conversation (and so the history replayed to the model). The
// model itself is untouched.
func (s *Service) ClearChat() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.guard(); err != nil {
		return err
	}
	s.chat = nil
	s.frontier = false
	s.detachSpecs()
	s.hub.Publish(Notice{Kind: "state"})
	return nil
}
