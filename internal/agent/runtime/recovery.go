package runtime

import (
	"errors"
	"fmt"
	"strings"
)

// FailureKind says why a tool call failed. A syntactically valid model response is not a
// successful CAD operation: the CAD engine's own result is the signal, and the kind decides
// whether a more capable model could do better.
type FailureKind string

const (
	FailInvalidTool    FailureKind = "invalid_tool_call"     // unknown, unavailable or unloaded tool
	FailSchema         FailureKind = "schema_validation"     // arguments the tool's schema rejects
	FailExecution      FailureKind = "cad_execution"         // the CAD engine could not do it
	FailUnsupported    FailureKind = "unsupported_operation" // no tool or feature does this
	FailMissingContext FailureKind = "missing_context"       // refers to something that is not there
	FailTruncated      FailureKind = "truncated_output"      // the model ran out of output tokens
	FailNone           FailureKind = ""                      // not a model failure
)

// Escalates reports whether a failure of this kind may be resolved by a more capable model.
// Unsupported operations and missing context are the user's to resolve, so the request is not
// sent to a bigger (and paid) model for them.
func (k FailureKind) Escalates() bool {
	switch k {
	case FailInvalidTool, FailSchema, FailExecution, FailTruncated:
		return true
	}
	return false
}

// failureKind classifies one tool call of a turn.
func failureKind(c TurnCall) FailureKind {
	o := c.Outcome
	if o.OK {
		return FailNone
	}
	msg := strings.ToLower(o.Error)
	switch {
	case strings.Contains(msg, "limit reached"), strings.Contains(msg, "which was not loaded"):
		return FailNone // the harness's own limit and tool loading: the model did nothing wrong
	case c.Refused, strings.HasPrefix(msg, "unknown tool"):
		return FailInvalidTool
	case strings.HasPrefix(msg, "invalid arguments"), strings.Contains(msg, "invalid json"), strings.Contains(msg, "decode"):
		return FailSchema
	case strings.Contains(msg, "unsupported"), strings.Contains(msg, "not supported"), strings.Contains(msg, "not implemented"):
		return FailUnsupported
	case strings.Contains(msg, "not found"), strings.Contains(msg, "no such"), strings.Contains(msg, "does not exist"), strings.Contains(msg, "no selection"):
		return FailMissingContext
	}
	return FailExecution
}

// worstFailure is the failure of a round of calls that decides what happens next: the first
// that a more capable model might fix, else the first failure of any kind.
func worstFailure(calls []TurnCall) (kind FailureKind, escalates bool) {
	for _, c := range calls {
		k := failureKind(c)
		if k.Escalates() {
			return k, true
		}
		if kind == FailNone {
			kind = k
		}
	}
	return kind, false
}

// escalationNote tells the model that takes over what it must not redo.
func (c *Conversation) escalationNote(e Escalation) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "[router] The previous model could not finish this request (%s) and you are taking over. ", e.Reason)
	if done := append(append([]string(nil), c.Res.Committed...), c.Res.Pending...); len(done) > 0 {
		fmt.Fprintf(&sb, "These changes were already applied and must not be repeated: %s. ", strings.Join(done, ", "))
	}
	sb.WriteString("Check the results above, then continue from the current state of the model and finish the request.")
	return sb.String()
}

// appendUserNote adds text for the model to the pending user message (after the tool results
// of the last round), or starts one when the last message is the model's.
func (c *Conversation) appendUserNote(text string) {
	b := Block{Type: "text", Text: text}
	if n := len(c.Msgs); n > 0 && c.Msgs[n-1].Role == "user" {
		c.Msgs[n-1].Content = append(c.Msgs[n-1].Content, b)
		return
	}
	c.Msgs = append(c.Msgs, Message{Role: "user", Content: []Block{b}})
}

// escalate moves the rest of the request to the next tier for reason. moved reports that it
// did. When the router refuses (cloud disabled or not authorized), the turn ends with the
// reason in turn.EscalationBlocked and turn.Final, and done is true; with no more capable model
// the request goes on where it is and further escalation is not tried again.
func (c *Conversation) escalate(turn *Turn, reason string) (moved, done bool) {
	esc := c.rt.Escalator
	if esc == nil || c.noEscalation {
		return false, false
	}
	e, err := esc.Escalate(reason)
	switch {
	case err == nil:
		c.fails = 0
		c.Res.Escalations = append(c.Res.Escalations, e)
		c.actor.Model = c.rt.Provider.Name()
		c.app.SetActor(c.actor)
		c.appendUserNote(c.escalationNote(e))
		if c.rt.OnEscalate != nil {
			c.rt.OnEscalate(e)
		}
		return true, false
	case errors.Is(err, ErrNoHigherTier):
		c.noEscalation = true
		return false, false
	}
	c.noEscalation = true
	c.Res.Blocked = err
	turn.EscalationBlocked = err
	turn.Final = fmt.Sprintf("I could not finish this request: the model could not finish it (%s) and %s. Changes already applied are kept; you can undo them.", reason, blockedWhy(err))
	return false, true
}

// blockedWhy phrases why an escalation was refused.
func blockedWhy(err error) string {
	var ce *ConsentError
	switch {
	case errors.Is(err, ErrLocalOnly):
		return "the routing mode is Local only, so no cloud model was used"
	case errors.As(err, &ce) && ce.Declined:
		return "sending it to the cloud model (" + ce.Fallback.Label() + ") was declined in Settings"
	case errors.As(err, &ce):
		return "it needs the cloud model " + ce.Fallback.Label() + ", which has not been authorized"
	}
	return err.Error()
}

// retries is how many consecutive failed rounds a tier gets before the request escalates.
func (rt *Runtime) retries(t Tier) int {
	switch t {
	case TierLocal:
		return rt.LocalRetries
	case TierStandard:
		return rt.StandardRetries
	}
	return -1 // the top tier has nowhere to go
}

// recover looks at one round of tool calls: a clean round resets the failure count, a failed
// round that a more capable model might fix counts, and the request escalates once the tier
// has used up its retries. done reports that the turn must end (see escalate).
func (c *Conversation) recover(turn *Turn, calls []TurnCall) (done bool) {
	kind, escalates := worstFailure(calls)
	if kind == FailNone {
		for _, call := range calls {
			if call.Outcome.OK {
				c.fails, c.lastKind, c.Res.Unresolved = 0, FailNone, false
				break
			}
		}
		return false
	}
	c.Res.Unresolved = true
	if !escalates || c.rt.Escalator == nil {
		return false
	}
	c.fails++
	c.lastKind = kind
	c.Res.Failures++
	if limit := c.rt.retries(c.rt.Escalator.Tier()); limit >= 0 && c.fails > limit {
		_, done = c.escalate(turn, "repeated_"+string(kind)+"_failure")
	}
	return done
}
