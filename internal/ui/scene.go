package ui

import (
	"github.com/accelone/simoncad/internal/app"
	"github.com/accelone/simoncad/internal/cadir"
	"github.com/accelone/simoncad/internal/rendering"
)

// ---- scene ----------------------------------------------------------------------------

// Scene brings a client holding `known` (body id → key) up to date.
func (s *Service) Scene(known map[string]string) (*rendering.Delta, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.app.Model()
	if err != nil {
		return nil, err
	}
	d, err := s.rend.Scene(m, known)
	if err != nil {
		return nil, err
	}
	// Appearance is read from the CAD-IR on every call: it is tiny and independent of
	// the mesh keys, so an appearance-only change never re-tessellates anything.
	g := s.app.Snapshot()
	d.Appearance = map[string]*cadir.Appearance{}
	for _, id := range d.Order {
		if n := g.Get(id); n != nil && !n.Appearance.IsZero() {
			d.Appearance[id] = n.Appearance
		}
	}
	d.Paths = rendering.PathsOf(g, m)
	return d, nil
}

// Ghost is the preview overlay of a proposed ChangeSet: the bodies it would add, and the
// live bodies it would replace.
type Ghost struct {
	ChangeSet ChangeSetCard         `json:"changeset"`
	Added     []*rendering.BodyMesh `json:"added"`
	Removed   []string              `json:"removed"`
	Triangles int                   `json:"triangles"`
	// Sketches are outlines of sketch nodes the ChangeSet would add or change.
	Sketches []*rendering.SketchLines `json:"sketches"`
}

// Preview rebuilds the model a proposed ChangeSet would produce and diffs its bodies
// against the live ones. Nothing is applied.
func (s *Service) Preview(csID string) (*Ghost, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cand, cs, err := s.app.PreviewModel(csID)
	if err != nil {
		return nil, err
	}
	live, err := s.app.Model()
	if err != nil {
		return nil, err
	}
	liveKeys := map[string]string{}
	for _, id := range live.Leaves {
		liveKeys[id] = live.Nodes[id].Key
	}
	g := &Ghost{ChangeSet: app.CardOf(cs), Added: []*rendering.BodyMesh{}, Removed: []string{}, Sketches: []*rendering.SketchLines{}}
	liveSketch := map[string]string{}
	for _, id := range live.Sketches {
		liveSketch[id] = live.Nodes[id].Key
	}
	for _, id := range cand.Sketches {
		if liveSketch[id] == cand.Nodes[id].Key {
			continue
		}
		sl, err := s.rend.Sketch(id, cand.Nodes[id])
		if err != nil {
			return nil, err
		}
		g.Sketches = append(g.Sketches, sl)
	}
	candKeys := map[string]bool{}
	for _, id := range cand.Leaves {
		candKeys[id+"|"+cand.Nodes[id].Key] = true
		if liveKeys[id] == cand.Nodes[id].Key {
			continue
		}
		bm, _, err := s.rend.Body(id, cand.Nodes[id])
		if err != nil {
			return nil, err
		}
		g.Added = append(g.Added, bm)
		g.Triangles += bm.Triangles
	}
	for _, id := range live.Leaves {
		if !candKeys[id+"|"+live.Nodes[id].Key] {
			g.Removed = append(g.Removed, id)
		}
	}
	return g, nil
}
