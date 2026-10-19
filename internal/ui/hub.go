package ui

import (
	"sync"

	"github.com/accelone/simoncad/internal/agent/refine"
	"github.com/accelone/simoncad/internal/agent/runtime"
	"github.com/accelone/simoncad/internal/agent/workflow"
	"github.com/accelone/simoncad/internal/printsim/job"
)

// Notice tells a front end that something changed; it carries no model data, so a client
// refetches the state or scene it needs.
type Notice struct {
	Kind string       `json:"kind"` // state | selection | chat | event | agent_state | agent_done | refine | visual | model | printsim | progress
	Type string       `json:"type,omitempty"`
	Seq  int          `json:"seq,omitempty"`
	Chat *ChatMessage `json:"chat,omitempty"`
	// Selection is the new selection (kind "selection"), so a client can apply it without
	// refetching the whole state.
	Selection *SelectionView  `json:"selection,omitempty"`
	State     *workflow.Event `json:"state,omitempty"` // agent_state: the workflow transition
	// Refine is a visual-refinement progress event (kind "refine"); kind "visual" means the
	// visual state (iterations, regions, references) changed.
	Refine *refine.Event `json:"refine,omitempty"`
	// Model is the model now serving the request (kind "model").
	Model *runtime.RouteEvent `json:"model,omitempty"`
	// PrintSim is a slicing-job progress event (kind "printsim").
	PrintSim *job.Event `json:"printsim,omitempty"`
	// Progress is the current stage of a long operation the user is waiting on (kind "progress").
	Progress *Progress `json:"progress,omitempty"`
}

// Progress is one stage of a long operation. ETAMS, when set, is a rough estimate of how long
// the stage takes, so the client can draw a bar; without it the stage is of unknown length.
type Progress struct {
	Op    string `json:"op"` // e.g. "import"
	Stage string `json:"stage"`
	ETAMS int    `json:"eta_ms,omitempty"`
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
