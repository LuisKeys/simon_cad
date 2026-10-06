package ui

import "testing"

func TestHistoryReplaysOnlyConversationText(t *testing.T) {
	s := &Service{}
	s.chat = []ChatMessage{
		{Role: "system", Text: "welcome"},
		{Role: "agent", Text: "orphan reply"},
		{Role: "user", Text: "make a box"},
		{Role: "tool", Text: "box created", Tool: "add_box"},
		{Role: "agent", Text: "done"},
		{Role: "user", Text: "failed turn"},
		{Role: "user", Text: "taller"},
	}
	h := s.history()
	if len(h) != 3 || h[0].Role != "user" || h[1].Role != "assistant" || h[2].Role != "user" {
		t.Fatalf("unexpected history: %+v", h)
	}
	if got := h[2].Content[0].Text; got != "failed turn\n\ntaller" {
		t.Fatalf("consecutive user messages not merged: %q", got)
	}

	s.chat = nil
	for i := 0; i < 100; i++ {
		s.chat = append(s.chat, ChatMessage{Role: "user", Text: "q"}, ChatMessage{Role: "agent", Text: "a"})
	}
	if h := s.history(); len(h) > maxHistory || h[0].Role != "user" {
		t.Fatalf("history not bounded/aligned: len=%d first=%s", len(h), h[0].Role)
	}
}
