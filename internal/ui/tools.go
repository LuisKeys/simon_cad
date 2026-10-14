package ui

import (
	"cmp"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/accelone/simoncad/internal/app"
	"github.com/accelone/simoncad/internal/tools"
)

// ---- ChangeSets and tools -------------------------------------------------------------

// Outcome is a tool result as the UI shows it.
type Outcome struct {
	OK        bool           `json:"ok"`
	Error     string         `json:"error,omitempty"`
	Code      string         `json:"code,omitempty"` // machine-readable error code, e.g. SWEEP_SELF_INTERSECTION
	Data      any            `json:"data,omitempty"`
	ChangeSet *ChangeSetCard `json:"changeset,omitempty"`
	Committed bool           `json:"committed"`
}

// QueryTool runs a read-only tool for a live preview (the trace panel's sliders). It records
// nothing and cannot edit the model: a mutating tool is refused.
func (s *Service) QueryTool(name string, args json.RawMessage) (any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.guard(); err != nil {
		return nil, err
	}
	return s.app.Query(name, args)
}

// CallTool runs one registered tool directly (palette, inline parameter edits). It is the
// same path the agent and the CLI use.
func (s *Service) CallTool(name string, args json.RawMessage) (Outcome, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.guard(); err != nil {
		return Outcome{}, err
	}
	s.app.SetActor(app.Actor{CreatedBy: "user"})
	pol := s.policy()
	direct := tools.IsDimensionEditTool(name) || tools.IsDirectTransformTool(name)
	if tools.IsAppearanceTool(name) || direct {
		// Presentation edits never wait for approval, even in Review; neither does a typed
		// dimension or a viewport move/rotate (direct manipulation: applied at once, undoable).
		pol = app.PolicyAuto
	}
	before, objects := s.leafSet(), s.dimensionTargets(args)
	o := s.app.ApplyTool(name, args, pol)
	out := s.finishTool(name, o)
	if o.Committed && direct {
		s.keepObjectSelected(before, objects)
	}
	return out, nil
}

// dimensionTargets is the object a dimension tool call is about: its object_id, else the
// features of the selection (which the tool falls back to).
func (s *Service) dimensionTargets(args json.RawMessage) []string {
	var a struct {
		ObjectID string `json:"object_id"`
	}
	if json.Unmarshal(args, &a) == nil && a.ObjectID != "" {
		return []string{a.ObjectID}
	}
	var out []string
	for _, raw := range s.sel {
		if id := selectorFeature(raw); id != "" && !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out
}

func (s *Service) leafSet() map[string]bool {
	out := map[string]bool{}
	if m, err := s.app.Model(); err == nil {
		for _, id := range m.Leaves {
			out[id] = true
		}
	}
	return out
}

// keepObjectSelected re-selects the object after a dimension edit, so its dimensions stay on
// screen for the next edit. The edit may replace the visible body (a new resize feature, or the
// plain body again when a resize is cancelled out): then the object is the body that appeared.
// Otherwise (an edit folded into the object's resize feature) it is still the same body.
func (s *Service) keepObjectSelected(before map[string]bool, objects []string) {
	now := s.leafSet()
	var added []string
	for id := range now {
		if !before[id] {
			added = append(added, id)
		}
	}
	switch {
	case len(added) == 1:
		_, _ = s.setSelection([]string{"body(feature=" + added[0] + ")"})
	case len(added) == 0 && len(objects) == 1 && now[objects[0]]:
		_, _ = s.setSelection([]string{"body(feature=" + objects[0] + ")"})
	}
}

func (s *Service) finishTool(name string, o app.Outcome) Outcome {
	out := Outcome{OK: o.OK, Error: o.Error, Code: o.Code(), Data: o.Data, Committed: o.Committed}
	if o.ChangeSet != nil {
		c := app.CardOf(o.ChangeSet)
		out.ChangeSet = &c
	}
	if o.Committed && !o.ChangeSet.AppearanceOnly() {
		s.app.Checkpoint(cmp.Or(o.ChangeSet.Intent, name), false)
	}
	s.say("tool", cmp.Or(o.Error, o.Message, workspaceMessage(o.Data), name), name, o.OK, func() string {
		if o.ChangeSet != nil {
			return o.ChangeSet.ID
		}
		return ""
	}())
	s.changed()
	return out
}

// workspaceMessage describes what a workspace tool changed ("" for any other result).
func workspaceMessage(data any) string {
	m, _ := data.(map[string]any)
	c, ok := m["changed"].([]string)
	if !ok {
		return ""
	}
	if len(c) == 0 {
		return "Workspace settings unchanged"
	}
	return "Workspace: " + strings.Join(c, ", ")
}

// DeleteRequest is a UI delete (Delete/Backspace, context menu). Features empty = the
// current selection. ResolveOwner and Cascade are the user's answers to the two questions
// Delete may come back with.
type DeleteRequest struct {
	Features     []string `json:"features,omitempty"`
	ResolveOwner bool     `json:"resolve_owner,omitempty"`
	Cascade      bool     `json:"cascade,omitempty"`
}

// DeleteResult is an Outcome, or a question for the user. NeedsConfirm is "owner" (faces,
// edges or vertices were selected: topology cannot be deleted, offer the owning feature) or
// "dependents" (other features depend on the targets); nothing has changed in either case.
type DeleteResult struct {
	Outcome
	NeedsConfirm string                `json:"needs_confirm,omitempty"`
	Analysis     *tools.DeleteAnalysis `json:"analysis,omitempty"`
}

// Delete removes the selected objects as one ChangeSet through delete_objects. It first asks
// check_delete what would happen, so the user is warned before anything is committed.
func (s *Service) Delete(req DeleteRequest) (DeleteResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.guard(); err != nil {
		return DeleteResult{}, err
	}
	s.app.SetActor(app.Actor{CreatedBy: "user"})
	args := map[string]any{}
	if len(req.Features) > 0 {
		args["features"] = req.Features
	}
	if req.ResolveOwner {
		args["resolve_owner"] = true
	}
	raw, _ := json.Marshal(args)
	chk := s.app.ApplyTool("check_delete", raw, app.PolicyPreview)
	if !chk.OK {
		return DeleteResult{Outcome: Outcome{Error: chk.Error}}, nil
	}
	an, _ := chk.Data.(*tools.DeleteAnalysis)
	if an == nil {
		return DeleteResult{}, fmt.Errorf("check_delete returned no analysis")
	}
	switch {
	case len(an.SubEntities) > 0 && !req.ResolveOwner:
		return DeleteResult{NeedsConfirm: "owner", Analysis: an}, nil
	case len(an.Dependents) > 0 && !req.Cascade:
		return DeleteResult{NeedsConfirm: "dependents", Analysis: an}, nil
	}
	if req.Cascade {
		args["cascade"] = true
	}
	ids := append(append([]string(nil), an.Targets...), an.Dependents...)
	args["intent"] = "Delete " + strings.Join(ids, ", ")
	raw, _ = json.Marshal(args)
	// A deliberate delete is committed at once (it stays undoable); the ChangeSet keeps the
	// dependency warnings for the record.
	return DeleteResult{Outcome: s.finishTool("delete_objects", s.app.ApplyTool("delete_objects", raw, app.PolicyCommit)), Analysis: an}, nil
}

// SetParameter is an inline design-tree edit: modify_parameter, as a user ChangeSet. Single
// parameter edits are low-risk, so they commit immediately regardless of the level.
func (s *Service) SetParameter(feature, parameter string, value any) (Outcome, error) {
	args, err := json.Marshal(map[string]any{"feature": feature, "parameter": parameter, "value": value,
		"intent": fmt.Sprintf("Set %s.%s to %v", feature, parameter, value)})
	if err != nil {
		return Outcome{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.guard(); err != nil {
		return Outcome{}, err
	}
	s.app.SetActor(app.Actor{CreatedBy: "user"})
	return s.finishTool("modify_parameter", s.app.ApplyTool("modify_parameter", args, app.PolicyAuto)), nil
}

// Commit approves a proposed ChangeSet and records a semantic checkpoint.
func (s *Service) Commit(id string) (ChangeSetCard, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.guard(); err != nil {
		return ChangeSetCard{}, err
	}
	cs, err := s.app.Commit(id)
	if err != nil {
		return ChangeSetCard{}, err
	}
	s.app.Checkpoint(cmp.Or(cs.Intent, "Committed "+cs.ID), false)
	s.say("system", "Committed "+cs.ID+": "+cmp.Or(cs.Intent, cs.Explanation), "", true, cs.ID)
	s.changed()
	return app.CardOf(cs), nil
}

// Reject discards a proposed ChangeSet.
func (s *Service) Reject(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.guard(); err != nil {
		return err
	}
	if err := s.app.Reject(id); err != nil {
		return err
	}
	s.say("system", "Rejected "+id, "", true, id)
	s.hub.Publish(Notice{Kind: "state"})
	return nil
}

func (s *Service) Undo() (ChangeSetCard, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.guard(); err != nil {
		return ChangeSetCard{}, err
	}
	s.keepDraft()
	cs, err := s.app.Undo()
	if err != nil {
		return ChangeSetCard{}, err
	}
	s.say("system", "Undid "+cs.ID+": "+cs.Intent, "", true, cs.ID)
	s.changed()
	return app.CardOf(cs), nil
}

func (s *Service) Redo() (ChangeSetCard, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.guard(); err != nil {
		return ChangeSetCard{}, err
	}
	cs, err := s.app.Redo()
	if err != nil {
		return ChangeSetCard{}, err
	}
	s.say("system", "Redid "+cs.ID+": "+cs.Intent, "", true, cs.ID)
	s.changed()
	return app.CardOf(cs), nil
}
