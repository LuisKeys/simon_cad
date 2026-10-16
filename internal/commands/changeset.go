package commands

import (
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/accelone/simoncad/internal/cadir"
	"github.com/accelone/simoncad/internal/events"
)

type State string

const (
	StateProposed  State = "proposed"
	StateCommitted State = "committed"
	StateRejected  State = "rejected"
	StateUndone    State = "undone"
)

// Validation is one named check on a proposed ChangeSet.
type Validation struct {
	Check    string `json:"check"` // schema|semantic|rebuild|geometry
	OK       bool   `json:"ok"`
	Severity string `json:"severity"` // error|warning|info
	Message  string `json:"message,omitempty"`
}

// Effects are the expected consequences, computed before commit.
type Effects struct {
	VolumeBefore float64  `json:"volume_before"`
	VolumeAfter  float64  `json:"volume_after"`
	Added        []string `json:"added,omitempty"`
	Removed      []string `json:"removed,omitempty"`
	Modified     []string `json:"modified,omitempty"`
}

// ChangeSet is the unit of agent action, preview, undo/redo and audit.
type ChangeSet struct {
	ID          string           `json:"id"`
	Seq         int              `json:"seq"`
	State       State            `json:"state"`
	Intent      string           `json:"intent,omitempty"`
	Explanation string           `json:"explanation,omitempty"`
	Commands    []Record         `json:"commands"`
	Inverse     []Record         `json:"rollback"` // inverses in forward order; undo applies them reversed
	Affected    []string         `json:"affected,omitempty"`
	Effects     Effects          `json:"expected_effects"`
	Warnings    []string         `json:"warnings,omitempty"`
	Validation  []Validation     `json:"validation"`
	Provenance  cadir.Provenance `json:"provenance"`
	BaseHash    string           `json:"base_hash"`
	ResultHash  string           `json:"result_hash"`
	Time        string           `json:"time"`
}

// Valid reports whether all error-severity checks passed.
func (c *ChangeSet) Valid() bool {
	for _, v := range c.Validation {
		if !v.OK && v.Severity == "error" {
			return false
		}
	}
	return true
}

// AppearanceOnly reports whether every command only edits presentation metadata. Such
// ChangeSets stay undoable but are not engineering intent, so they never form checkpoints
// or agent drafts.
func (c *ChangeSet) AppearanceOnly() bool {
	for _, r := range c.Commands {
		if !IsAppearance(r.Type) {
			return false
		}
	}
	return len(c.Commands) > 0
}

// AutoCommitEligible is the low-risk policy: a single non-destructive command, clean validation.
func (c *ChangeSet) AutoCommitEligible() bool {
	if !c.Valid() || len(c.Warnings) > 0 {
		return false
	}
	// Presentation-only edits are always safe, including multi-object ones.
	if c.AppearanceOnly() {
		return true
	}
	if len(c.Commands) != 1 {
		return false
	}
	switch c.Commands[0].Type {
	case "RemoveFeature", "RestoreSnapshot":
		return false
	}
	return true
}

// Evaluation is what the rebuild layer reports about a graph.
type Evaluation struct {
	Volume  float64
	Failed  map[string][]string // node -> error chain
	BodyCnt int
}

// Evaluator rebuilds a graph and summarizes it (implemented by the app over the rebuild engine).
type Evaluator interface {
	Evaluate(g *cadir.Graph) (Evaluation, error)
}

// Reversible is an undoable edit outside the model (the workspace settings, say). It shares
// the undo/redo stacks with the ChangeSets, so undo always reverts the latest edit of any kind.
type Reversible interface {
	Label() string
	Revert() error
	Reapply() error
}

// Step is one entry of the undo/redo stacks: a ChangeSet, or else an edit outside the model.
type Step struct {
	ChangeSet *ChangeSet
	Other     Reversible
}

// Label names the step for messages.
func (s Step) Label() string {
	if s.ChangeSet != nil {
		if s.ChangeSet.Intent != "" {
			return s.ChangeSet.ID + ": " + s.ChangeSet.Intent
		}
		return s.ChangeSet.ID
	}
	return s.Other.Label()
}

// entry is a stack element: a ChangeSet id, or an edit outside the model.
type entry struct {
	id    string
	other Reversible
}

// Manager owns the live graph and the ChangeSet log with undo/redo.
type Manager struct {
	G    *cadir.Graph
	Eval Evaluator
	Bus  *events.Bus
	Now  func() time.Time

	Log  []*ChangeSet
	undo []entry
	redo []entry
}

func NewManager(g *cadir.Graph, ev Evaluator, bus *events.Bus) *Manager {
	return &Manager{G: g, Eval: ev, Bus: bus, Now: time.Now}
}

// Restore re-seeds the log when loading a project and rebuilds the undo stack from the
// committed ChangeSets (oldest first). The redo stack is not persisted across sessions.
func (m *Manager) Restore(log []*ChangeSet) {
	m.Log = log
	m.undo, m.redo = nil, nil
	var committed []*ChangeSet
	for _, cs := range log {
		if cs.State == StateCommitted {
			committed = append(committed, cs)
		}
	}
	sort.Slice(committed, func(i, j int) bool { return committed[i].Seq < committed[j].Seq })
	for _, cs := range committed {
		m.undo = append(m.undo, entry{id: cs.ID})
	}
}

func (m *Manager) find(id string) *ChangeSet {
	for _, c := range m.Log {
		if c.ID == id {
			return c
		}
	}
	return nil
}

func (m *Manager) Get(id string) (*ChangeSet, bool) {
	c := m.find(id)
	return c, c != nil
}

func (m *Manager) CanUndo() bool { return len(m.undo) > 0 }
func (m *Manager) CanRedo() bool { return len(m.redo) > 0 }

func (m *Manager) publish(typ string, payload any) {
	if m.Bus != nil {
		m.Bus.Publish(typ, payload)
	}
}

// Propose applies the commands to a clone of the live graph, validates, and records a
// proposed ChangeSet. The live graph is untouched.
func (m *Manager) Propose(intent, explanation string, prov cadir.Provenance, cmds []Command) (*ChangeSet, error) {
	if len(cmds) == 0 {
		return nil, fmt.Errorf("a ChangeSet needs at least one command")
	}
	cs := &ChangeSet{
		ID: fmt.Sprintf("cs_%04d", len(m.Log)+1), Seq: len(m.Log) + 1, State: StateProposed,
		Intent: intent, Explanation: explanation, Provenance: prov,
		BaseHash: m.G.Hash(), Time: m.Now().UTC().Format(time.RFC3339),
	}
	cs.Provenance.ChangeSet = cs.ID
	work := m.G.Clone()
	var inverses []Command
	for i, c := range cmds {
		inv, err := c.Apply(work)
		if err != nil {
			return nil, fmt.Errorf("command %d (%s): %w", i+1, c.Type(), err)
		}
		inverses = append(inverses, inv)
		cs.Commands = append(cs.Commands, Encode(c))
		cs.Inverse = append(cs.Inverse, Encode(inv))
	}
	cs.Validation = append(cs.Validation,
		Validation{Check: "schema", OK: true, Severity: "info"},
		Validation{Check: "semantic", OK: true, Severity: "info"})
	cs.ResultHash = work.Hash()
	cs.Effects, cs.Affected = summarize(m.G, work, cmds)

	if m.Eval != nil {
		before, err := m.Eval.Evaluate(m.G)
		if err != nil {
			return nil, fmt.Errorf("evaluate base model: %w", err)
		}
		after, err := m.Eval.Evaluate(work)
		if err != nil {
			cs.Validation = append(cs.Validation, Validation{Check: "rebuild", OK: false, Severity: "error", Message: err.Error()})
		} else {
			cs.Effects.VolumeBefore, cs.Effects.VolumeAfter = before.Volume, after.Volume
			cs.Validation = append(cs.Validation, rebuildChecks(before, after)...)
			for id, errs := range after.Failed {
				if _, was := before.Failed[id]; was {
					cs.Warnings = append(cs.Warnings, fmt.Sprintf("feature %s was already failing: %s", id, firstOr(errs)))
				}
			}
		}
	}
	sort.Strings(cs.Warnings)
	m.Log = append(m.Log, cs)
	m.publish(events.ChangeSetProposed, map[string]any{"id": cs.ID, "intent": cs.Intent, "valid": cs.Valid(), "commands": len(cs.Commands)})
	return cs, nil
}

func firstOr(s []string) string {
	if len(s) == 0 {
		return ""
	}
	return s[0]
}

func rebuildChecks(before, after Evaluation) []Validation {
	var newlyFailed []string
	for id := range after.Failed {
		if _, was := before.Failed[id]; !was {
			newlyFailed = append(newlyFailed, id)
		}
	}
	sort.Strings(newlyFailed)
	if len(newlyFailed) > 0 {
		id := newlyFailed[0]
		return []Validation{{Check: "rebuild", OK: false, Severity: "error",
			Message: fmt.Sprintf("feature %s would fail: %s", id, firstOr(after.Failed[id]))}}
	}
	out := []Validation{{Check: "rebuild", OK: true, Severity: "info"}}
	geo := Validation{Check: "geometry", OK: true, Severity: "info"}
	if after.BodyCnt == 0 {
		geo = Validation{Check: "geometry", OK: false, Severity: "warning", Message: "model has no solid geometry"}
	}
	return append(out, geo)
}

func sameAppearance(a, b *cadir.Appearance) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

func summarize(before, after *cadir.Graph, cmds []Command) (Effects, []string) {
	var e Effects
	aff := map[string]bool{}
	for _, n := range after.Nodes {
		if before.Get(n.ID) == nil {
			e.Added = append(e.Added, n.ID)
			aff[n.ID] = true
		}
	}
	for _, n := range before.Nodes {
		if after.Get(n.ID) == nil {
			e.Removed = append(e.Removed, n.ID)
			aff[n.ID] = true
		}
	}
	for _, n := range after.Nodes {
		if b := before.Get(n.ID); b != nil && (b.ContentKey() != n.ContentKey() || !sameAppearance(b.Appearance, n.Appearance)) {
			e.Modified = append(e.Modified, n.ID)
			aff[n.ID] = true
		}
	}
	out := make([]string, 0, len(aff))
	for id := range aff {
		out = append(out, id)
	}
	sort.Strings(out)
	return e, out
}

// Preview returns the graph that committing a proposed ChangeSet would produce, without
// touching the live graph. It fails if the ChangeSet is not proposed or has gone stale.
func (m *Manager) Preview(id string) (*cadir.Graph, error) {
	cs := m.find(id)
	if cs == nil {
		return nil, fmt.Errorf("unknown ChangeSet %q", id)
	}
	if cs.State != StateProposed {
		return nil, fmt.Errorf("ChangeSet %s is %s, not proposed", id, cs.State)
	}
	if cs.BaseHash != m.G.Hash() {
		return nil, fmt.Errorf("ChangeSet %s is stale: the model changed after it was proposed", id)
	}
	work := m.G.Clone()
	for i, r := range cs.Commands {
		c, err := Decode(r)
		if err != nil {
			return nil, fmt.Errorf("command %d: %w", i+1, err)
		}
		if _, err := c.Apply(work); err != nil {
			return nil, fmt.Errorf("command %d (%s): %w", i+1, c.Type(), err)
		}
	}
	return work, nil
}

func (m *Manager) applyRecords(recs []Record, reverse bool) error {
	work := m.G.Clone()
	order := make([]int, len(recs))
	for i := range recs {
		order[i] = i
		if reverse {
			order[i] = len(recs) - 1 - i
		}
	}
	for _, i := range order {
		c, err := Decode(recs[i])
		if err != nil {
			return err
		}
		if _, err := c.Apply(work); err != nil {
			return fmt.Errorf("%s: %w", recs[i].Type, err)
		}
	}
	m.G.Units, m.G.Nodes = work.Units, work.Nodes
	return nil
}

// Commit applies a proposed, valid ChangeSet to the live graph.
func (m *Manager) Commit(id string) (*ChangeSet, error) {
	cs := m.find(id)
	if cs == nil {
		return nil, fmt.Errorf("unknown ChangeSet %q", id)
	}
	if cs.State != StateProposed {
		return nil, fmt.Errorf("ChangeSet %s is %s, not proposed", id, cs.State)
	}
	if !cs.Valid() {
		return nil, fmt.Errorf("ChangeSet %s failed validation and cannot be committed", id)
	}
	if cs.BaseHash != m.G.Hash() {
		return nil, fmt.Errorf("ChangeSet %s is stale: the model changed since it was proposed", id)
	}
	if err := m.applyRecords(cs.Commands, false); err != nil {
		return nil, err
	}
	cs.State = StateCommitted
	m.undo = append(m.undo, entry{id: cs.ID})
	m.redo = nil
	m.publish(events.ChangeSetCommitted, map[string]any{"id": cs.ID, "affected": cs.Affected, "result_hash": cs.ResultHash})
	m.publish(events.FeatureGraphUpdated, map[string]any{"hash": m.G.Hash(), "nodes": len(m.G.Nodes)})
	return cs, nil
}

// Reject discards a proposed ChangeSet.
func (m *Manager) Reject(id string) error {
	cs := m.find(id)
	if cs == nil || cs.State != StateProposed {
		return fmt.Errorf("no proposed ChangeSet %q", id)
	}
	cs.State = StateRejected
	m.publish(events.ChangeSetRejected, map[string]any{"id": id})
	return nil
}

// Push records an edit outside the model that was just applied, as the newest undo step.
func (m *Manager) Push(r Reversible) {
	m.undo = append(m.undo, entry{other: r})
	m.redo = nil
}

// Undo reverts the most recently committed ChangeSet. It fails when the newest undo step is
// an edit outside the model; UndoStep reverts whatever the newest step is.
func (m *Manager) Undo() (*ChangeSet, error) {
	if len(m.undo) > 0 && m.undo[len(m.undo)-1].other != nil {
		return nil, fmt.Errorf("the latest change (%s) is not a model change", m.undo[len(m.undo)-1].other.Label())
	}
	st, err := m.UndoStep()
	return st.ChangeSet, err
}

// Redo re-applies the most recently undone ChangeSet. It fails when the newest redo step is
// an edit outside the model; RedoStep re-applies whatever the newest step is.
func (m *Manager) Redo() (*ChangeSet, error) {
	if len(m.redo) > 0 && m.redo[len(m.redo)-1].other != nil {
		return nil, fmt.Errorf("the next change to redo (%s) is not a model change", m.redo[len(m.redo)-1].other.Label())
	}
	st, err := m.RedoStep()
	return st.ChangeSet, err
}

// UndoStep reverts the newest undo step, a ChangeSet or an edit outside the model.
func (m *Manager) UndoStep() (Step, error) {
	if len(m.undo) == 0 {
		return Step{}, fmt.Errorf("nothing to undo")
	}
	e := m.undo[len(m.undo)-1]
	if e.other != nil {
		if err := e.other.Revert(); err != nil {
			return Step{}, err
		}
		m.undo = m.undo[:len(m.undo)-1]
		m.redo = append(m.redo, e)
		return Step{Other: e.other}, nil
	}
	cs := m.find(e.id)
	if err := m.applyRecords(cs.Inverse, true); err != nil {
		return Step{}, err
	}
	m.undo = m.undo[:len(m.undo)-1]
	m.redo = append(m.redo, e)
	cs.State = StateUndone
	m.publish(events.ChangeSetUndone, map[string]any{"id": e.id})
	m.publish(events.FeatureGraphUpdated, map[string]any{"hash": m.G.Hash(), "nodes": len(m.G.Nodes)})
	return Step{ChangeSet: cs}, nil
}

// RedoStep re-applies the newest redo step, a ChangeSet or an edit outside the model.
func (m *Manager) RedoStep() (Step, error) {
	if len(m.redo) == 0 {
		return Step{}, fmt.Errorf("nothing to redo")
	}
	e := m.redo[len(m.redo)-1]
	if e.other != nil {
		if err := e.other.Reapply(); err != nil {
			return Step{}, err
		}
		m.redo = m.redo[:len(m.redo)-1]
		m.undo = append(m.undo, e)
		return Step{Other: e.other}, nil
	}
	cs := m.find(e.id)
	if err := m.applyRecords(cs.Commands, false); err != nil {
		return Step{}, err
	}
	m.redo = m.redo[:len(m.redo)-1]
	m.undo = append(m.undo, e)
	cs.State = StateCommitted
	m.publish(events.ChangeSetRedone, map[string]any{"id": e.id})
	m.publish(events.FeatureGraphUpdated, map[string]any{"hash": m.G.Hash(), "nodes": len(m.G.Nodes)})
	return Step{ChangeSet: cs}, nil
}

// CommittedIDs lists the ChangeSets currently applied, oldest first.
func (m *Manager) CommittedIDs() []string {
	var out []string
	for _, e := range m.undo {
		if e.other == nil {
			out = append(out, e.id)
		}
	}
	return out
}

// ReplayOnto rebuilds a graph by applying the given committed ChangeSets in order to g.
func ReplayOnto(g *cadir.Graph, sets []*ChangeSet) error {
	for _, cs := range sets {
		for _, r := range cs.Commands {
			c, err := Decode(r)
			if err != nil {
				return err
			}
			if _, err := c.Apply(g); err != nil {
				return fmt.Errorf("replay %s: %w", cs.ID, err)
			}
		}
	}
	return nil
}
