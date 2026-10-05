package ui

import "sync"

// Notice tells a front end that something changed; it carries no model data, so a client
// refetches the state or scene it needs.
type Notice struct {
	Kind string       `json:"kind"` // state | selection | chat | event | agent_done
	Type string       `json:"type,omitempty"`
	Seq  int          `json:"seq,omitempty"`
	Chat *ChatMessage `json:"chat,omitempty"`
}

// Hub fans notices out to subscribers without ever blocking the publisher.
type Hub struct {
	mu   sync.Mutex
	subs map[chan Notice]struct{}
}

func NewHub() *Hub { return &Hub{subs: map[chan Notice]struct{}{}} }

// Subscribe returns a notice channel and its cancel function.
func (h *Hub) Subscribe() (<-chan Notice, func()) {
	ch := make(chan Notice, 64)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		delete(h.subs, ch)
		h.mu.Unlock()
	}
}

// Publish drops a notice for any subscriber whose buffer is full: they will catch up on
// the next one, and state is always refetched rather than reconstructed from notices.
func (h *Hub) Publish(n Notice) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs {
		select {
		case ch <- n:
		default:
		}
	}
}
