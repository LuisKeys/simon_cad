package app

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/accelone/simoncad/internal/cadir"
	"github.com/accelone/simoncad/internal/commands"
	"github.com/accelone/simoncad/internal/dimensions"
	"github.com/accelone/simoncad/internal/geometry/api"
	"github.com/accelone/simoncad/internal/geometry/rebuild"
	"github.com/accelone/simoncad/internal/persistence"
	"github.com/accelone/simoncad/internal/tools"
	"github.com/accelone/simoncad/internal/transform"
)

// Read-only views for front ends. The UI never touches App.Graph or App.Mgr directly: it
// reads these snapshots and mutates only through ApplyTool and the ChangeSet lifecycle
// (Commit, Reject, Undo, Redo, Restore).

// PrintShape returns the model's combined solid for read-only consumers such as print
// simulation, with the geometry revision (appearance excluded) it was built from. The
// shape is a fresh copy owned by the caller, who must Release it; the model is not touched.
func (a *App) PrintShape() (*api.Shape, string, error) {
	m, err := a.Model()
	if err != nil {
		return nil, "", err
	}
	if f := m.Failed(); len(f) > 0 {
		return nil, "", fmt.Errorf("features are failing: %s", strings.Join(f, ", "))
	}
	if len(m.Leaves) == 0 {
		return nil, "", errors.New("the model has no solid geometry")
	}
	shape, err := m.ResultShape(a.K)
	if err != nil {
		return nil, "", err
	}
	if ok, err := shape.IsValid(); err != nil || !ok {
		shape.Release()
		return nil, "", errors.New("the model is not a valid solid")
	}
	return shape, a.Graph.GeometryHash(), nil
}

// Snapshot returns a deep copy of the live graph; changing it has no effect on the model.
func (a *App) Snapshot() *cadir.Graph { return a.Graph.Clone() }

// ObjectDimensions measures a visible body (width, depth, height along its local axes), the
// same measurement the get_object_dimensions tool returns.
func (a *App) ObjectDimensions(id string) (*dimensions.Dimensions, error) {
	return tools.ObjectDimensions(toolEnv{a}, id)
}

// ObjectTransform reads where a visible body is and whether it can be moved now, the same
// answer the get_object_transform tool gives.
func (a *App) ObjectTransform(id string) (*transform.Object, error) {
	return tools.ObjectTransform(toolEnv{a}, id)
}

// CanUndo and CanRedo report the ChangeSet undo/redo stacks.
func (a *App) CanUndo() bool { return a.Mgr.CanUndo() }
func (a *App) CanRedo() bool { return a.Mgr.CanRedo() }

// ChangeSet returns a ChangeSet by id.
func (a *App) ChangeSet(id string) (*commands.ChangeSet, bool) { return a.Mgr.Get(id) }

// PreviewModel rebuilds the graph a proposed ChangeSet would produce, without applying it.
// The live graph and its derived status are untouched.
func (a *App) PreviewModel(id string) (*rebuild.Model, *commands.ChangeSet, error) {
	g, err := a.Mgr.Preview(id)
	if err != nil {
		return nil, nil, err
	}
	m, err := a.Engine.Rebuild(g)
	if err != nil {
		return nil, nil, err
	}
	cs, _ := a.Mgr.Get(id)
	return m, cs, nil
}

// committedInOrder lists the applied ChangeSets, oldest first.
func (a *App) committedInOrder() []*commands.ChangeSet {
	var sets []*commands.ChangeSet
	for _, cs := range a.Mgr.Log {
		if cs.State == commands.StateCommitted {
			sets = append(sets, cs)
		}
	}
	sort.Slice(sets, func(i, j int) bool { return sets[i].Seq < sets[j].Seq })
	return sets
}

// Committed returns the applied ChangeSets, oldest first (the playback script).
func (a *App) Committed() []*commands.ChangeSet { return a.committedInOrder() }

// ModelAfter rebuilds the model as it stood after the first n committed ChangeSets
// (n=0 is the empty project). It powers Instant Design playback from the event record.
func (a *App) ModelAfter(n int) (*rebuild.Model, error) {
	sets := a.committedInOrder()
	if n < 0 || n > len(sets) {
		return nil, fmt.Errorf("playback frame %d is out of range (0..%d)", n, len(sets))
	}
	g := cadir.NewGraph()
	if err := commands.ReplayOnto(g, sets[:n]); err != nil {
		return nil, err
	}
	return a.Engine.Rebuild(g)
}

// ChatLine is one conversation row replayed alongside playback, shaped like a chat message.
type ChatLine struct {
	Role   string   `json:"role"` // user | agent | tool | system
	Text   string   `json:"text"`
	Tool   string   `json:"tool,omitempty"`
	OK     bool     `json:"ok"`
	Images []string `json:"images,omitempty"`
}

// StepChat is the conversation around one playback frame: Before plays out ahead of the
// geometry (the prompt that started it, the agent's tool calls up to and including the one
// that produced it), After follows it (trailing calls and the agent's answer, on the last
// frame of its prompt).
type StepChat struct {
	Before []ChatLine `json:"before"`
	After  []ChatLine `json:"after,omitempty"`
}

// PlaybackScript reconstructs, from the agent history, the conversation behind each committed
// ChangeSet (one StepChat per playback frame, oldest first). A ChangeSet the agent did not
// make is a manual edit. Prompts that committed nothing are skipped.
func (a *App) PlaybackScript() []StepChat {
	sets := a.committedInOrder()
	calls := a.AH.ToolCalls
	callAt := make(map[string]int, len(calls))
	for i, tc := range calls {
		callAt[tc.ID] = i
	}
	committed := make(map[string]bool, len(sets))
	for _, cs := range sets {
		committed[cs.ID] = true
	}
	prompts := make(map[string]persistence.Prompt, len(a.AH.Prompts))
	for _, p := range a.AH.Prompts {
		prompts[p.ID] = p
	}
	agentCall := func(cs *commands.ChangeSet) (int, bool) {
		i, ok := callAt[cs.Provenance.ToolCallID]
		return i, ok && calls[i].Actor == cadir.CreatedByAgent && calls[i].PromptID != ""
	}
	// line renders a tool call the way the live chat showed it: the ChangeSet's explanation, or the error.
	line := func(tc persistence.ToolCall) ChatLine {
		l := ChatLine{Role: "tool", Tool: tc.Name, OK: tc.Status == "ok", Text: tc.Error}
		if tc.Error == "" && tc.ChangeSetID != "" {
			if cs, ok := a.Mgr.Get(tc.ChangeSetID); ok {
				l.Text = cs.Explanation
			}
		}
		return l
	}
	// between lists the prompt's other agent calls in (from, to): failures, queries, uncommitted proposals.
	between := func(prompt string, from, to int) []ChatLine {
		var out []ChatLine
		for i := from + 1; i < to; i++ {
			tc := calls[i]
			if tc.PromptID == prompt && tc.Actor == cadir.CreatedByAgent && !committed[tc.ChangeSetID] {
				out = append(out, line(tc))
			}
		}
		return out
	}

	script := make([]StepChat, len(sets))
	prev, last := "", -1 // the prompt being played and its last call shown
	for f, cs := range sets {
		i, ok := agentCall(cs)
		if !ok {
			script[f].Before = []ChatLine{{Role: "system", Text: "Manual edit — " + cs.Intent, OK: true}}
			continue
		}
		tc := calls[i]
		if tc.PromptID != prev {
			p := prompts[tc.PromptID]
			script[f].Before = append(script[f].Before, ChatLine{Role: "user", Text: p.Text, OK: true, Images: p.Images})
			prev, last = tc.PromptID, -1
			for j := range calls { // replay from the prompt's first call
				if calls[j].PromptID == prev {
					last = j - 1
					break
				}
			}
		}
		script[f].Before = append(script[f].Before, between(prev, last, i)...)
		script[f].Before = append(script[f].Before, line(tc))
		last = i
		// the last frame of the prompt closes its turn
		more := false
		for _, n := range sets[f+1:] {
			if j, ok := agentCall(n); ok && calls[j].PromptID == prev {
				more = true
				break
			}
		}
		if !more {
			script[f].After = between(prev, i, len(calls))
			if s := prompts[prev].Summary; s != "" {
				script[f].After = append(script[f].After, ChatLine{Role: "agent", Text: s, OK: true})
			}
		}
	}
	return script
}
