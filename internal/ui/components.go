package ui

import (
	"fmt"

	"github.com/accelone/simoncad/internal/components"
)

// Component is one library entry as the Components panel shows it.
type Component = components.Component

// Components lists the reusable projects in the library, filtered by an optional query.
func (s *Service) Components(query string) []*Component {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.app.ListComponents(query)
	if out == nil {
		out = []*Component{}
	}
	return out
}

// OpenComponent opens a component's project for editing. Opening a component is just opening
// a project: there is no separate component editor.
func (s *Service) OpenComponent(projectID string) (string, error) {
	s.mu.Lock()
	if err := s.guard(); err != nil {
		s.mu.Unlock()
		return "", err
	}
	if s.dirty {
		s.mu.Unlock()
		return "", fmt.Errorf("save this project first: opening the component would discard unsaved changes")
	}
	if len(s.app.Pending()) > 0 {
		s.mu.Unlock()
		return "", fmt.Errorf("review the pending change first")
	}
	dir, err := s.app.ComponentPath(projectID)
	s.mu.Unlock()
	if err != nil {
		return "", err
	}
	if err := s.Open(dir); err != nil {
		return "", err
	}
	return dir, nil
}

// RenameComponent updates a component's library metadata (its own project.json).
func (s *Service) RenameComponent(projectID, name, description string, tags []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.guard(); err != nil {
		return err
	}
	if err := s.app.RenameComponent(projectID, name, description, tags); err != nil {
		return err
	}
	s.hub.Publish(Notice{Kind: "state"})
	return nil
}

// DeleteComponent removes a component's project from disk.
func (s *Service) DeleteComponent(projectID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.guard(); err != nil {
		return err
	}
	if err := s.app.DeleteComponent(projectID); err != nil {
		return err
	}
	s.hub.Publish(Notice{Kind: "state"})
	return nil
}

// PublishProject marks the open project as a reusable component (or stops it being one) so it
// appears in the library. A component is a reusable project — there is nothing else to it.
func (s *Service) PublishProject(reusable bool, description string, tags []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.guard(); err != nil {
		return err
	}
	s.app.SetReusable(reusable, description, tags)
	s.dirty = true
	s.hub.Publish(Notice{Kind: "state"})
	return nil
}
