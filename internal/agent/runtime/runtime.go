package runtime

import (
	"context"
	"encoding/base64"
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

// Conditional is implemented by a capability that is only worth loading in some projects.
// Tool definitions are resent on every step of the tool loop and dominate the input of a
// turn, so a capability with nothing to contribute is paid for on every single call.
type Conditional interface {
	// Relevant reports whether this capability has anything to contribute to a turn that
	// attaches images (ids into App.Images; empty when none are attached).
	Relevant(a *app.App, images []string) bool
}

// SelectCaps keeps the capabilities worth sending for this turn. A capability that does not
// implement Conditional is always kept, so adding one is opt-in.
func SelectCaps(a *app.App, images []string, caps ...Capability) []Capability {
	out := make([]Capability, 0, len(caps))
	for _, c := range caps {
		if cond, ok := c.(Conditional); ok && !cond.Relevant(a, images) {
			continue
		}
		out = append(out, c)
	}
	return out
}

// ImageInterpreter turns attached images into text for a provider without vision: the
// attachment processor routes them to a vision-capable model, whose structured result the
// main agent then works from.
type ImageInterpreter interface {
	// Available is nil when images can be interpreted, otherwise the reason they cannot.
	Available() error
	Interpret(ctx context.Context, a *app.App, intent string, imageIDs []string, policy app.Policy) (*Interpretation, error)
}

// Interpretation is what an ImageInterpreter learned from the images.
type Interpretation struct {
	Text     string // structured description for the main agent
	Provider string // the model that looked at the images
	Proposed bool   // it left an interpretation for the user to review
	Usage    Usage
}

type Runtime struct {
	Provider Provider
	Caps     []Capability
	MaxSteps int
	// NoThinking disables model thinking/reasoning (instant answers). Default: thinking on.
	NoThinking bool
	// History is the prior conversation (plain user/assistant text, oldest first). It is
	// prepended to the turn; the live model context is only attached to the newest request.
	History []Message
	// ImageIDs are uploaded images (App.Images) attached to this turn's user message.
	ImageIDs []string
	// Interpreter handles ImageIDs when Provider has no vision. Nil: such a turn is refused.
	Interpreter ImageInterpreter
	// Reference is user-attached document text (specifications) shown to the model every turn.
	Reference string
	// OnStep, if set, observes each tool call (for CLI progress output).
	OnStep func(name string, args json.RawMessage, o app.Outcome)
	// MaxStructural bounds the structural (geometry-changing) tool calls of each Send; 0 is
	// unlimited. Visual refinement uses it to keep each iteration's feedback causal.
	MaxStructural int
	// Subtask marks a helper run inside another agent turn (e.g. image interpretation): it
	// records no prompt of its own; its tool calls belong to the enclosing prompt.
	Subtask bool
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

// SystemPrompt joins the capability prompts.
func (rt *Runtime) SystemPrompt() string {
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

// BuildContext renders every capability's context (the live model state) for the model.
func (rt *Runtime) BuildContext(a *app.App) (string, error) {
	var sb strings.Builder
	for _, c := range rt.Caps {
		ctxText, err := c.BuildContext(a)
		if err != nil {
			return "", fmt.Errorf("context builder (%s): %w", c.Name(), err)
		}
		sb.WriteString(ctxText)
		sb.WriteString("\n")
	}
	return sb.String(), nil
}

// Run handles one user intent. The model can only act through registered tools; every
// tool call goes through App.ApplyTool (validation → ChangeSet → policy). Free-form text
// never mutates state.
func (rt *Runtime) Run(ctx context.Context, a *app.App, intent string, policy app.Policy) (*Result, error) {
	c, err := rt.Begin(ctx, a, intent, policy)
	if err != nil {
		return c.Result(), err
	}
	turn, err := c.Send(ctx, "")
	if err != nil {
		return c.Result(), err
	}
	c.Res.Final = turn.Final
	return c.Finish(), nil
}

// Conversation is one agent turn in progress: the opening request (context, images, the
// user's intent) and then any number of Sends, each running the tool-call loop until the
// model answers in text. The planner/executor drive it one plan step at a time.
type Conversation struct {
	rt      *Runtime
	app     *app.App
	policy  app.Policy
	allowed map[string]bool
	defs    []ToolDef
	system  string
	context string  // capability context + attachments + references (no request)
	opening []Block // image blocks for the first message
	intent  string
	started bool
	review  bool // an interpreter left an image interpretation for the user to review
	Msgs    []Message
	Res     *Result
}

// TurnCall is one tool call made during a Send.
type TurnCall struct {
	ID      string
	Name    string
	Args    json.RawMessage
	Outcome app.Outcome
	Refused bool // not a tool of this agent; never reached the app
}

// Turn is the result of one Send.
type Turn struct {
	Final     string
	Calls     []TurnCall
	Exhausted bool // stopped at MaxSteps without a text answer
	// AwaitingReview: the turn stopped because an image interpretation waits for the user
	// to review, adjust and approve it (the agent does not go on until then).
	AwaitingReview bool
}

// reviewNotice is what a turn that stops for an image interpretation tells the user.
const reviewNotice = "I read the image. Review the interpretation (Review interpretation), adjust the values and press Create & preview; I'll wait for your approval before going on."

// Begin records the intent and prepares the opening request. Nothing is sent to the model
// and nothing is recorded if it fails.
func (rt *Runtime) Begin(ctx context.Context, a *app.App, intent string, policy app.Policy) (*Conversation, error) {
	if rt.MaxSteps == 0 {
		rt.MaxSteps = 20
	}
	c := &Conversation{rt: rt, app: a, policy: policy, intent: intent, Res: &Result{}}
	capNames := make([]string, len(rt.Caps))
	for i, cp := range rt.Caps {
		capNames[i] = cp.Name()
	}
	actor := app.Actor{CreatedBy: "agent", Capability: strings.Join(capNames, ","), Model: rt.Provider.Name()}
	a.SetActor(actor)
	vision := CapabilitiesOf(rt.Provider).Vision
	var attached []string
	for _, id := range rt.ImageIDs {
		im, ok := a.Images.Get(id)
		if !ok {
			return c, fmt.Errorf("image %q is not available (upload it first)", id)
		}
		if !vision {
			if rt.Interpreter == nil {
				return c, fmt.Errorf("provider %s does not support image input; switch SIMONCAD_PROVIDER to claude or openai", rt.Provider.Name())
			}
			if err := rt.Interpreter.Available(); err != nil {
				return c, err
			}
			continue
		}
		c.opening = append(c.opening, Block{Type: "image", Source: &ImageSource{Type: "base64", MediaType: im.MediaType, Data: base64.StdEncoding.EncodeToString(im.Data)}})
		attached = append(attached, fmt.Sprintf("Attached image id=%s (%dx%d px)", im.ID, im.Width, im.Height))
	}
	if !rt.Subtask {
		c.Res.PromptID = a.RecordPrompt(intent)
		a.RecordPromptImages(c.Res.PromptID, rt.ImageIDs)
	}
	if !vision && len(rt.ImageIDs) > 0 {
		in, err := rt.Interpreter.Interpret(ctx, a, intent, rt.ImageIDs, policy)
		a.SetActor(actor) // the interpreter acted as its own model
		if err != nil {
			return c, fmt.Errorf("image interpretation: %w", err)
		}
		c.Res.Usage.Add(in.Usage)
		c.review = in.Proposed
		for _, id := range rt.ImageIDs {
			im, _ := a.Images.Get(id)
			attached = append(attached, fmt.Sprintf("Attached image id=%s (%dx%d px) — you cannot see it; %s interpreted it for you (see below).", im.ID, im.Width, im.Height, in.Provider))
		}
		attached = append(attached, "## Image interpretation (from the vision model; work from it and from the image interpretations above)\n"+in.Text)
	}
	c.allowed, c.defs = rt.toolset(a)
	c.system = rt.SystemPrompt()
	ctxText, err := rt.BuildContext(a)
	if err != nil {
		return c, err
	}
	var sb strings.Builder
	sb.WriteString(ctxText)
	for _, line := range attached {
		sb.WriteString(line + "\n")
	}
	if rt.Reference != "" {
		sb.WriteString(rt.Reference + "\n")
	}
	c.context = sb.String()
	c.Msgs = append([]Message(nil), rt.History...)
	return c, nil
}

// Context is the opening context (model state, attachments, references) without the request.
func (c *Conversation) Context() string { return c.context }

// App is the project the conversation acts on.
func (c *Conversation) App() *app.App { return c.app }

// Runtime is the runtime the conversation belongs to.
func (c *Conversation) Runtime() *Runtime { return c.rt }

// Result is the running summary (never nil).
func (c *Conversation) Result() *Result {
	if c == nil || c.Res == nil {
		return &Result{}
	}
	return c.Res
}

// Send runs the tool loop once. The first Send carries the opening request, with directive
// (if any) appended after it; later Sends add directive as a new user message.
func (c *Conversation) Send(ctx context.Context, directive string) (*Turn, error) {
	if !c.started {
		c.started = true
		text := c.context + "## User request\n" + c.intent
		if directive != "" {
			text += "\n\n" + directive
		}
		c.Msgs = append(c.Msgs, Message{Role: "user", Content: append(c.opening, Block{Type: "text", Text: text})})
	} else {
		c.Msgs = append(c.Msgs, Message{Role: "user", Content: []Block{{Type: "text", Text: directive}}})
	}
	rt, res, turn := c.rt, c.Res, &Turn{}
	if c.review {
		// an interpreter already read the image: the proposal waits for the user
		c.review = false
		turn.Final, turn.AwaitingReview = reviewNotice, true
		c.Msgs = append(c.Msgs, Message{Role: "assistant", Content: []Block{{Type: "text", Text: turn.Final}}})
		return turn, nil
	}
	structural := 0
	for step := 0; step < rt.MaxSteps; step++ {
		resp, err := rt.Provider.Complete(ctx, Request{System: c.system, Messages: c.Msgs, Tools: c.defs, NoThinking: rt.NoThinking})
		if err != nil {
			return turn, err
		}
		res.Steps++
		res.Usage.Add(resp.Usage)
		c.Msgs = append(c.Msgs, Message{Role: "assistant", Content: resp.Content})
		uses := resp.ToolUses()
		if len(uses) == 0 {
			turn.Final = resp.Text()
			return turn, nil
		}
		var results []Block
		for _, u := range uses {
			var o app.Outcome
			call := TurnCall{ID: u.ID, Name: u.Name, Args: u.Input}
			if !c.allowed[u.Name] {
				o = app.Outcome{Tool: u.Name, Error: fmt.Sprintf("tool %q is not available to this agent", u.Name)}
				call.Refused = true
			} else if rt.MaxStructural > 0 && structural >= rt.MaxStructural && c.app.Reg.Structural(u.Name) {
				o = app.Outcome{Tool: u.Name, Error: fmt.Sprintf("limit reached: at most %d structural changes per iteration; stop now and report what you changed (the model is re-rendered and measured next)", rt.MaxStructural)}
				call.Refused = true
			} else {
				o = c.app.ApplyTool(u.Name, u.Input, c.policy)
				res.ToolCalls++
				if o.OK && o.ChangeSet != nil && !o.ChangeSet.AppearanceOnly() {
					structural++
				}
				if o.Committed {
					res.Committed = append(res.Committed, o.ChangeSet.ID)
				} else if o.OK && o.ChangeSet != nil {
					res.Pending = append(res.Pending, o.ChangeSet.ID)
				}
			}
			call.Outcome = o
			turn.Calls = append(turn.Calls, call)
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
		c.Msgs = append(c.Msgs, Message{Role: "user", Content: results})
		if proposed(turn.Calls[len(turn.Calls)-len(uses):]) {
			// A fresh interpretation is the user's to review and approve: stop here instead of
			// letting the agent build on values the user has not seen.
			turn.Final, turn.AwaitingReview = reviewNotice, true
			if t := strings.TrimSpace(resp.Text()); t != "" {
				turn.Final = t + "\n\n" + reviewNotice
			}
			c.Msgs = append(c.Msgs, Message{Role: "assistant", Content: []Block{{Type: "text", Text: turn.Final}}})
			return turn, nil
		}
	}
	turn.Exhausted = true
	turn.Final = fmt.Sprintf("(stopped after %d steps without a final answer)", rt.MaxSteps)
	return turn, nil
}

// proposed reports whether calls produced an image interpretation proposal.
func proposed(calls []TurnCall) bool {
	for _, c := range calls {
		if c.Name == "analyze_image" && c.Outcome.OK && !c.Refused {
			return true
		}
	}
	return false
}

// Finish closes the turn: the transcript and the final summary are recorded.
func (c *Conversation) Finish() *Result {
	c.Res.Transcript = c.Msgs
	c.app.SetPromptSummary(c.Res.Final)
	return c.Res
}
