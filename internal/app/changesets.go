package app

import (
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/accelone/simoncad/internal/cadir"
	"github.com/accelone/simoncad/internal/commands"
	"github.com/accelone/simoncad/internal/events"
	"github.com/accelone/simoncad/internal/geometry/features"
	"github.com/accelone/simoncad/internal/history"
	"github.com/accelone/simoncad/internal/persistence"
	"github.com/accelone/simoncad/internal/tools"
)

// ---- prompts, tools, ChangeSets ---------------------------------------------------------

// RecordPrompt logs a user intent and returns its id.
func (a *App) RecordPrompt(text string) string {
	p := persistence.Prompt{
		ID: fmt.Sprintf("pr_%04d", len(a.AH.Prompts)+1), Time: a.Clock().UTC().Format(time.RFC3339),
		Text: text, Selection: append([]string(nil), a.selection...),
		Capability: a.actor.Capability, Model: a.actor.Model,
	}
	a.AH.Prompts = append(a.AH.Prompts, p)
	a.promptID = p.ID
	a.Bus.Publish(events.UserIntentReceived, map[string]any{"id": p.ID, "text": text, "selection": p.Selection})
	return p.ID
}

// RecordPromptImages notes which uploaded images were attached to a prompt.
func (a *App) RecordPromptImages(promptID string, ids []string) {
	if len(ids) == 0 {
		return
	}
	for i := range a.AH.Prompts {
		if a.AH.Prompts[i].ID == promptID {
			a.AH.Prompts[i].Images = append([]string(nil), ids...)
		}
	}
}

// SetPromptSummary stores the agent's final explanation on the current prompt.
func (a *App) SetPromptSummary(s string) {
	for i := range a.AH.Prompts {
		if a.AH.Prompts[i].ID == a.promptID {
			a.AH.Prompts[i].Summary = s
		}
	}
	a.Bus.Publish(events.AgentResponseFinal, map[string]any{"prompt_id": a.promptID, "text": s})
}

// Outcome is the result of one tool call, safe to show a user or feed back to a model.
type Outcome struct {
	ToolCallID string
	Tool       string
	OK         bool
	Error      string
	Data       any
	ChangeSet  *commands.ChangeSet
	Committed  bool
	Message    string
}

// Code is the machine-readable error code of a failed call ("" when none applies): either named
// in the error itself or in an error-severity validation of its ChangeSet (a rebuild failure).
func (o Outcome) Code() string {
	if o.Error != "" {
		if c := features.CodeOf(o.Error); c != "" {
			return c
		}
	}
	if o.ChangeSet != nil {
		for _, v := range o.ChangeSet.Validation {
			if v.Severity == "error" {
				if c := features.CodeOf(v.Message); c != "" {
					return c
				}
			}
		}
	}
	return ""
}

// JSON renders the outcome as the structured tool result returned to an agent.
func (o Outcome) JSON() string {
	m := map[string]any{"tool_call_id": o.ToolCallID, "ok": o.OK}
	if o.Error != "" {
		m["error"] = o.Error
	}
	if c := o.Code(); c != "" {
		m["code"] = c
	}
	if o.Data != nil {
		m["result"] = o.Data
	}
	if o.ChangeSet != nil {
		cs := o.ChangeSet
		m["changeset"] = map[string]any{
			"id": cs.ID, "state": cs.State, "committed": o.Committed, "affected": cs.Affected,
			"explanation": cs.Explanation, "warnings": cs.Warnings,
			"volume_before_mm3": cs.Effects.VolumeBefore, "volume_after_mm3": cs.Effects.VolumeAfter,
			"added": cs.Effects.Added, "removed": cs.Effects.Removed, "modified": cs.Effects.Modified,
		}
		if !o.Committed && o.OK {
			m["note"] = "ChangeSet is proposed and awaits user approval; it has NOT been applied. Do not repeat the call."
		}
	}
	b, _ := json.Marshal(m)
	return string(b)
}

func (a *App) recordCall(name string, args []byte, o Outcome) {
	tc := persistence.ToolCall{ID: o.ToolCallID, PromptID: a.promptID, Actor: a.actor.CreatedBy, Name: name,
		Args: json.RawMessage(compactJSON(args))}
	switch {
	case o.OK:
		tc.Status = "ok"
	case o.ChangeSet != nil:
		tc.Status = "invalid"
	default:
		tc.Status = "rejected"
	}
	tc.Error = o.Error
	if o.ChangeSet != nil {
		tc.ChangeSetID = o.ChangeSet.ID
	}
	if o.Data != nil {
		if b, err := json.Marshal(o.Data); err == nil {
			tc.Result = b
		}
	}
	a.AH.ToolCalls = append(a.AH.ToolCalls, tc)
}

func compactJSON(b []byte) []byte {
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		q, _ := json.Marshal(string(b))
		return q
	}
	out, _ := json.Marshal(v)
	return out
}

// Query runs a read-only tool (one that never edits the model) without recording a tool call:
// for UI previews that run on every slider move. A tool that edits the model is refused.
func (a *App) Query(name string, args []byte) (any, error) {
	t, ok := a.Reg.Get(name)
	if !ok {
		return nil, fmt.Errorf("unknown tool %q", name)
	}
	if t.Mutating() {
		return nil, fmt.Errorf("%s edits the model and cannot be used as a query", name)
	}
	res, err := a.Reg.Invoke(toolEnv{a}, name, args)
	if err != nil {
		return nil, err
	}
	return res.Data, nil
}

// ApplyTool is THE mutation entry point: schema validation → semantic validation →
// Commands → proposed ChangeSet → validation → (policy) commit.
func (a *App) ApplyTool(name string, args []byte, policy Policy) Outcome {
	a.callID = fmt.Sprintf("tc_%04d", len(a.AH.ToolCalls)+1)
	out := Outcome{ToolCallID: a.callID, Tool: name}
	a.Bus.Publish(events.AgentToolRequested, map[string]any{"id": a.callID, "tool": name, "args": json.RawMessage(compactJSON(args))})
	defer func() {
		a.recordCall(name, args, out)
		a.Bus.Publish(events.AgentToolResult, map[string]any{"id": out.ToolCallID, "ok": out.OK, "error": out.Error})
	}()

	res, err := a.Reg.Invoke(toolEnv{a}, name, args)
	if err != nil {
		out.Error = err.Error()
		return out
	}
	if res.Plan == nil { // query tool
		out.OK, out.Data = true, res.Data
		return out
	}
	cs, err := a.Mgr.Propose(res.Plan.Intent, res.Plan.Explanation, toolEnv{a}.Provenance(), res.Plan.Commands)
	if err != nil {
		out.Error = err.Error()
		return out
	}
	cs.Provenance.ToolCallID = a.callID
	cs.Warnings = append(cs.Warnings, res.Plan.Warnings...)
	out.ChangeSet, out.Message = cs, cs.Explanation
	if !cs.Valid() {
		for _, v := range cs.Validation {
			if !v.OK && v.Severity == "error" {
				out.Error = fmt.Sprintf("%s check failed: %s", v.Check, v.Message)
				break
			}
		}
		_ = a.Mgr.Reject(cs.ID)
		return out
	}
	if reason := a.lockViolation(cs, res.Plan.Commands); reason != "" {
		out.Error = reason
		_ = a.Mgr.Reject(cs.ID)
		return out
	}
	out.OK = true
	if res.Plan.RequireApproval {
		policy = PolicyPreview
	}
	lowRisk := res.Plan.LowRisk && len(cs.Warnings) == 0
	commit := policy == PolicyCommit || policy == PolicyAuto && (cs.AutoCommitEligible() || lowRisk)
	if commit {
		if err := a.commit(cs); err != nil {
			out.OK, out.Error = false, err.Error()
			return out
		}
		out.Committed = true
		if tools.IsTransformTool(name) {
			a.Bus.Publish(events.ObjectTransformed, map[string]any{"tool": name, "changeset": cs.ID,
				"objects": cs.Affected, "intent": cs.Intent})
		}
	}
	return out
}

func (a *App) commit(cs *commands.ChangeSet) error {
	if _, err := a.Mgr.Commit(cs.ID); err != nil {
		return err
	}
	a.trackForCheckpoint(cs)
	a.noteInterpretations(cs)
	_, err := a.refresh()
	return err
}

// noteInterpretations records which nodes an image interpretation produced.
func (a *App) noteInterpretations(cs *commands.ChangeSet) {
	byInt := map[string][]string{}
	for _, id := range cs.Affected {
		if n := a.Graph.Get(id); n != nil && n.Provenance.InterpretationID != "" {
			byInt[n.Provenance.InterpretationID] = append(byInt[n.Provenance.InterpretationID], id)
		}
	}
	for id, nodes := range byInt {
		a.Vision.MarkUsed(id, nodes)
	}
	// The model was built another way (by hand, or by the agent from the user's words): an
	// interpretation nobody used stops being a pending task.
	if len(byInt) == 0 && len(cs.Affected) > 0 && !cs.AppearanceOnly() {
		a.Vision.CloseOpen()
	}
}

// Commit approves a proposed ChangeSet.
func (a *App) Commit(id string) (*commands.ChangeSet, error) {
	cs, ok := a.Mgr.Get(id)
	if !ok {
		return nil, fmt.Errorf("unknown ChangeSet %q", id)
	}
	if err := a.commit(cs); err != nil {
		return nil, err
	}
	return cs, nil
}

func (a *App) Reject(id string) error { return a.Mgr.Reject(id) }

func (a *App) Undo() (*commands.ChangeSet, error) {
	cs, err := a.Mgr.Undo()
	if err != nil {
		return nil, err
	}
	a.dropFromSinceCP(cs.ID)
	_, err = a.refresh()
	return cs, err
}

func (a *App) Redo() (*commands.ChangeSet, error) {
	cs, err := a.Mgr.Redo()
	if err != nil {
		return nil, err
	}
	a.trackForCheckpoint(cs)
	_, err = a.refresh()
	return cs, err
}

// trackForCheckpoint queues a committed ChangeSet for the next checkpoint. Appearance-only
// edits are skipped: they are undoable but are not engineering intent.
func (a *App) trackForCheckpoint(cs *commands.ChangeSet) {
	if !cs.AppearanceOnly() {
		a.sinceCP = append(a.sinceCP, cs.ID)
	}
}

func (a *App) dropFromSinceCP(id string) {
	for i, x := range a.sinceCP {
		if x == id {
			a.sinceCP = append(a.sinceCP[:i], a.sinceCP[i+1:]...)
			return
		}
	}
}

// Pending lists proposed (uncommitted, unrejected) ChangeSets.
func (a *App) Pending() []*commands.ChangeSet {
	var out []*commands.ChangeSet
	for _, cs := range a.Mgr.Log {
		if cs.State == commands.StateProposed {
			out = append(out, cs)
		}
	}
	return out
}

// Checkpoint records a semantic checkpoint if anything was committed since the last one
// (or force is set). It returns nil when there is nothing to record.
func (a *App) Checkpoint(label string, force bool) *history.Checkpoint {
	if len(a.sinceCP) == 0 && !force {
		return nil
	}
	if label == "" {
		label = "Checkpoint"
	}
	if len(label) > 120 {
		label = label[:120]
	}
	cp := a.Hist.Create(label, a.actor.CreatedBy, a.sinceCP, a.Graph, a.Clock())
	a.sinceCP = nil
	a.Bus.Publish(events.CheckpointCreated, map[string]any{"id": cp.ID, "label": cp.Label, "hash": cp.GraphHash})
	return cp
}

// Replay re-derives the model from the committed ChangeSets alone and reports whether it
// reproduces the live graph exactly.
func (a *App) Replay() (replayed *cadir.Graph, match bool, applied int, err error) {
	var sets []*commands.ChangeSet
	for _, cs := range a.Mgr.Log {
		if cs.State == commands.StateCommitted {
			sets = append(sets, cs)
		}
	}
	sort.Slice(sets, func(i, j int) bool { return sets[i].Seq < sets[j].Seq })
	g := cadir.NewGraph()
	if err = commands.ReplayOnto(g, sets); err != nil {
		return nil, false, len(sets), err
	}
	return g, g.Hash() == a.Graph.Hash(), len(sets), nil
}

// Restore proposes (and per policy commits) a return to a checkpoint through the tool path.
func (a *App) Restore(ref string, policy Policy) Outcome {
	args, _ := json.Marshal(map[string]any{"checkpoint": ref})
	return a.ApplyTool("restore_checkpoint", args, policy)
}

// EnvFor exposes the read-only tool environment (for context builders).
func EnvFor(a *App) tools.Env { return toolEnv{a} }

// RevertLatest undoes the given committed ChangeSets, newest first, as long as each is the
// latest one on the undo stack. It stops at the first one that is not (the model changed in
// the meantime), leaving that one applied, and returns the IDs it did undo. The agent's
// correction and refinement loops use it to drop a change that made things worse.
func (a *App) RevertLatest(ids []string) ([]string, error) {
	var done []string
	for i := len(ids) - 1; i >= 0; i-- {
		cs, err := a.Undo()
		if err != nil {
			return done, err
		}
		if cs.ID != ids[i] {
			if _, rerr := a.Redo(); rerr != nil {
				return done, rerr
			}
			return done, fmt.Errorf("the model changed in the meantime; could not revert %s", ids[i])
		}
		done = append(done, cs.ID)
	}
	return done, nil
}
