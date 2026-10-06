package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/accelone/simoncad/internal/app"
	"github.com/accelone/simoncad/internal/tools"
)

// Capability is a pluggable skill of the one agent: its own prompt, tool subset and
// domain context. Promotion to an independent agent later needs no change to tools,
// ChangeSets or the CAD-IR.
type Capability interface {
	Name() string
	SystemPrompt() string
	ToolSubset(reg *tools.Registry) []*tools.Tool
	// BuildContext deterministically renders what the model needs to know right now.
	BuildContext(a *app.App) (string, error)
}

type Runtime struct {
	Provider Provider
	Caps     []Capability
	MaxSteps int
	// History is the prior conversation (plain user/assistant text, oldest first). It is
	// prepended to the turn; the live model context is only attached to the newest request.
	History []Message
	// OnStep, if set, observes each tool call (for CLI progress output).
	OnStep func(name string, args json.RawMessage, o app.Outcome)
}

// Result summarizes one agent turn.
type Result struct {
	PromptID   string
	Final      string
	ToolCalls  int
	Committed  []string
	Pending    []string
	Steps      int
	Usage      Usage
	Transcript []Message
}

func (rt *Runtime) systemPrompt() string {
	var parts []string
	for _, c := range rt.Caps {
		parts = append(parts, c.SystemPrompt())
	}
	return strings.Join(parts, "\n\n")
}

func (rt *Runtime) toolset(a *app.App) (map[string]bool, []ToolDef) {
	allowed := map[string]bool{}
	var defs []ToolDef
	for _, c := range rt.Caps {
		for _, t := range c.ToolSubset(a.Reg) {
			if !allowed[t.Name] {
				allowed[t.Name] = true
				defs = append(defs, ToolDef{Name: t.Name, Description: t.Description, InputSchema: t.Schema})
			}
		}
	}
	return allowed, defs
}

// Run handles one user intent. The model can only act through registered tools; every
// tool call goes through App.ApplyTool (validation → ChangeSet → policy). Free-form text
// never mutates state.
func (rt *Runtime) Run(ctx context.Context, a *app.App, intent string, policy app.Policy) (*Result, error) {
	if rt.MaxSteps == 0 {
		rt.MaxSteps = 12
	}
	capNames := make([]string, len(rt.Caps))
	for i, c := range rt.Caps {
		capNames[i] = c.Name()
	}
	a.SetActor(app.Actor{CreatedBy: "agent", Capability: strings.Join(capNames, ","), Model: rt.Provider.Name()})
	res := &Result{PromptID: a.RecordPrompt(intent)}
	allowed, defs := rt.toolset(a)

	var sb strings.Builder
	for _, c := range rt.Caps {
		ctxText, err := c.BuildContext(a)
		if err != nil {
			return res, fmt.Errorf("context builder (%s): %w", c.Name(), err)
		}
		sb.WriteString(ctxText)
		sb.WriteString("\n")
	}
	sb.WriteString("## User request\n")
	sb.WriteString(intent)
	msgs := append(append([]Message(nil), rt.History...),
		Message{Role: "user", Content: []Block{{Type: "text", Text: sb.String()}}})

	for step := 0; step < rt.MaxSteps; step++ {
		resp, err := rt.Provider.Complete(ctx, Request{System: rt.systemPrompt(), Messages: msgs, Tools: defs})
		if err != nil {
			return res, err
		}
		res.Steps++
		res.Usage.InputTokens += resp.Usage.InputTokens
		res.Usage.OutputTokens += resp.Usage.OutputTokens
		msgs = append(msgs, Message{Role: "assistant", Content: resp.Content})
		uses := resp.ToolUses()
		if len(uses) == 0 {
			res.Final = resp.Text()
			break
		}
		var results []Block
		for _, u := range uses {
			var o app.Outcome
			if !allowed[u.Name] {
				o = app.Outcome{Tool: u.Name, Error: fmt.Sprintf("tool %q is not available to this agent", u.Name)}
			} else {
				o = a.ApplyTool(u.Name, u.Input, policy)
				res.ToolCalls++
				if o.Committed {
					res.Committed = append(res.Committed, o.ChangeSet.ID)
				} else if o.OK && o.ChangeSet != nil {
					res.Pending = append(res.Pending, o.ChangeSet.ID)
				}
			}
			if rt.OnStep != nil {
				rt.OnStep(u.Name, u.Input, o)
			}
			content := o.JSON()
			if o.ToolCallID == "" { // rejected before reaching the app
				b, _ := json.Marshal(map[string]any{"ok": false, "error": o.Error})
				content = string(b)
			}
			results = append(results, Block{Type: "tool_result", ToolUseID: u.ID, Content: content, IsError: !o.OK})
		}
		msgs = append(msgs, Message{Role: "user", Content: results})
	}
	if res.Final == "" && res.Steps >= rt.MaxSteps {
		res.Final = fmt.Sprintf("(stopped after %d steps without a final answer)", rt.MaxSteps)
	}
	res.Transcript = msgs
	a.SetPromptSummary(res.Final)
	return res, nil
}
