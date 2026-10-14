package ui

import (
	"fmt"
	"strings"

	"github.com/accelone/simoncad/internal/dimensions"
	"github.com/accelone/simoncad/internal/rendering"
	"github.com/accelone/simoncad/internal/selection"
	"github.com/accelone/simoncad/internal/transform"
)

// ---- selection ------------------------------------------------------------------------

// PickRequest is a viewport click, already resolved to a body and a kind.
type PickRequest struct {
	Body     string `json:"body"`
	Kind     string `json:"kind"`     // body | face | edge | vertex
	Index    int    `json:"index"`    // kernel index of the face/edge/vertex
	Triangle int    `json:"triangle"` // for faces: the clicked triangle, when Index is unknown (-1 otherwise)
	Mode     string `json:"mode"`     // replace (default) | add | toggle
}

// Pick turns a click into a semantic selector and updates the selection context.
func (s *Service) Pick(p PickRequest) (SelectionView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p.Kind == "" && p.Body == "" { // click on empty space
		if p.Mode != "add" && p.Mode != "toggle" {
			return s.setSelection(nil)
		}
		return s.selectionView(), nil
	}
	m, err := s.app.Model()
	if err != nil {
		return SelectionView{}, err
	}
	bm, _, err := s.rend.Body(p.Body, m.Nodes[p.Body])
	if err != nil {
		return SelectionView{}, err
	}
	idx := p.Index
	if p.Kind == rendering.PickFace && p.Triangle >= 0 {
		f, ok := bm.FaceOfTriangle(p.Triangle)
		if !ok {
			return SelectionView{}, fmt.Errorf("triangle %d is not on body %s", p.Triangle, p.Body)
		}
		idx = f
	}
	sel, err := bm.SelectorFor(p.Kind, idx)
	if err != nil {
		return SelectionView{}, err
	}
	next := []string{sel}
	switch p.Mode {
	case "add":
		next = appendUnique(s.sel, sel)
	case "toggle":
		next = toggle(s.sel, sel)
	}
	return s.setSelection(next)
}

// SetSelection replaces the selection with selector strings (chips, agent suggestions).
func (s *Service) SetSelection(raw []string) (SelectionView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.guard(); err != nil {
		return SelectionView{}, err
	}
	return s.setSelection(raw)
}

func (s *Service) setSelection(raw []string) (SelectionView, error) {
	if err := s.app.SetSelection(raw); err != nil {
		return SelectionView{}, err
	}
	s.sel = append([]string(nil), raw...)
	s.selAt = s.app.Snapshot().GeometryHash()
	v := s.selectionView()
	s.hub.Publish(Notice{Kind: "selection", Selection: &v})
	return v, nil
}

func appendUnique(list []string, v string) []string {
	for _, x := range list {
		if x == v {
			return append([]string(nil), list...)
		}
	}
	return append(append([]string(nil), list...), v)
}

func toggle(list []string, v string) []string {
	var out []string
	found := false
	for _, x := range list {
		if x == v {
			found = true
			continue
		}
		out = append(out, x)
	}
	if !found {
		out = append(out, v)
	}
	return out
}

// SelectionView is the selection as the agent sees it, plus what the viewport must highlight.
type SelectionView struct {
	Entities   []selection.Entity `json:"entities"`
	Selectors  []string           `json:"selectors"`
	Highlights []rendering.Pick   `json:"highlights"`
	// Dimensions of the selected objects (the visible bodies the selection is on), for the
	// viewport's editable dimensions and the Dimensions section of the properties.
	Dimensions []*dimensions.Dimensions `json:"dimensions"`
	// Transforms of the selected objects (origin, center, orientation, whether they can be
	// moved): what the viewport's interactive Move/Rotate start from.
	Transforms []*transform.Object `json:"transforms"`
}

// maxDimensioned bounds how many selected objects get dimensions (beyond that they clutter).
const maxDimensioned = 8

func (s *Service) selectionView() SelectionView {
	v := SelectionView{Entities: []selection.Entity{}, Selectors: []string{}, Highlights: []rendering.Pick{},
		Dimensions: []*dimensions.Dimensions{}, Transforms: []*transform.Object{}}
	ctx := s.app.SelectionContext()
	if ctx.Empty() {
		return v
	}
	v.Entities, v.Selectors = ctx.Entities, ctx.Selectors()
	m, err := s.app.Model()
	if err != nil {
		return v
	}
	for _, raw := range v.Selectors {
		id := selectorFeature(raw)
		bm, _, err := s.rend.Body(id, m.Nodes[id])
		if err != nil {
			continue
		}
		if p, err := bm.PickFor(raw); err == nil {
			v.Highlights = append(v.Highlights, p)
		}
	}
	seen := map[string]bool{}
	for _, raw := range v.Selectors {
		id := selectorFeature(raw)
		if seen[id] || len(v.Dimensions) >= maxDimensioned {
			continue
		}
		seen[id] = true
		if d, err := s.app.ObjectDimensions(id); err == nil { // only visible solid bodies
			v.Dimensions = append(v.Dimensions, d)
		}
		if t, err := s.app.ObjectTransform(id); err == nil {
			v.Transforms = append(v.Transforms, t)
		}
	}
	return v
}

func selectorFeature(raw string) string {
	i := strings.Index(raw, "feature=")
	if i < 0 {
		return ""
	}
	rest := raw[i+len("feature="):]
	if j := strings.IndexAny(rest, ",)"); j >= 0 {
		rest = rest[:j]
	}
	return strings.TrimSpace(rest)
}

// Selection returns the current selection view.
func (s *Service) Selection() SelectionView {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.selectionView()
}
