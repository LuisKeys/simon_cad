package ui

import (
	"unicode/utf8"

	"github.com/accelone/simoncad/internal/agent/runtime"
)

// maxHistory bounds how many prior chat messages are replayed to the model each turn, and
// maxHistoryChars their total size (about 4 characters per token, so roughly 8k tokens):
// a few long agent summaries must not crowd out the live model state.
const (
	maxHistory      = 30
	maxHistoryChars = 32000
)

// history renders the chat panel as model conversation: only user and agent text, with
// tool chatter dropped (the current model state is re-injected every turn). Consecutive
// messages of one role are merged and the result always starts with a user message.
// The caller holds s.mu.
func (s *Service) history() []runtime.Message {
	var out []runtime.Message
	for _, m := range s.chat {
		role := ""
		switch m.Role {
		case "user":
			role = "user"
		case "agent":
			role = "assistant"
		default:
			continue
		}
		if n := len(out); n > 0 && out[n-1].Role == role {
			out[n-1].Content[0].Text += "\n\n" + m.Text
			continue
		}
		out = append(out, runtime.Message{Role: role, Content: []runtime.Block{{Type: "text", Text: m.Text}}})
	}
	return trimHistory(out, maxHistory, maxHistoryChars)
}

// trimHistory keeps the newest messages that fit both limits (the newest message is always
// kept, truncated if it alone exceeds the size budget) and makes sure the result starts
// with a user message.
func trimHistory(out []runtime.Message, maxMsgs, maxChars int) []runtime.Message {
	if len(out) > maxMsgs {
		out = out[len(out)-maxMsgs:]
	}
	size := func(m runtime.Message) int {
		n := 0
		for _, b := range m.Content {
			n += len(b.Text)
		}
		return n
	}
	total := 0
	for _, m := range out {
		total += size(m)
	}
	for len(out) > 1 && total > maxChars {
		total -= size(out[0])
		out = out[1:]
	}
	if len(out) == 1 && total > maxChars {
		b := out[0].Content[0]
		cut := b.Text[len(b.Text)-maxChars:]
		for !utf8.ValidString(cut) {
			cut = cut[1:]
		}
		out[0] = runtime.Message{Role: out[0].Role, Content: []runtime.Block{{Type: b.Type, Text: "…" + cut}}}
	}
	for len(out) > 0 && out[0].Role != "user" {
		out = out[1:]
	}
	return out
}
