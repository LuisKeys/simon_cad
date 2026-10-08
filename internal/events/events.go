// Package events is the typed in-process event bus plus an append-only recorder that
// powers replay, audit and (later) time-lapse playback.
package events

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

// Event types.
const (
	UserIntentReceived  = "UserIntentReceived"
	AgentToolRequested  = "AgentToolRequested"
	AgentToolResult     = "AgentToolResult"
	ChangeSetProposed   = "ChangeSetProposed"
	ChangeSetCommitted  = "ChangeSetCommitted"
	ChangeSetRejected   = "ChangeSetRejected"
	ChangeSetUndone     = "ChangeSetUndone"
	ChangeSetRedone     = "ChangeSetRedone"
	FeatureGraphUpdated = "FeatureGraphUpdated"
	ModelRebuilt        = "ModelRebuilt"
	CheckpointCreated   = "CheckpointCreated"
	CheckpointRestored  = "CheckpointRestored"
	ProjectSaved        = "ProjectSaved"
	ExportCompleted     = "ExportCompleted"
	AgentResponseFinal  = "AgentResponseFinal"
	// WorkspaceChanged records a change of the project's units, grid or snapping settings
	// (not CAD state, so not a ChangeSet).
	WorkspaceChanged = "WorkspaceChanged"
	// Component lifecycle. These are the only operations that touch a project other than the
	// open one, so the trace would be blind to them without their own event types.
	ComponentCreated  = "ComponentCreated"
	ComponentInserted = "ComponentInserted"
	ComponentDetached = "ComponentDetached"
	ComponentSynced   = "ComponentSynced"
)

type Event struct {
	Seq  int             `json:"seq"`
	Type string          `json:"type"`
	Time string          `json:"time"`
	Data json.RawMessage `json:"data,omitempty"`
}

type Bus struct {
	mu   sync.Mutex
	seq  int
	log  []Event
	subs []func(Event)
	Now  func() time.Time
}

func NewBus() *Bus { return &Bus{Now: time.Now} }

// Load seeds the recorder with previously persisted events and continues numbering.
func (b *Bus) Load(evs []Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.log = append([]Event(nil), evs...)
	if n := len(evs); n > 0 {
		b.seq = evs[n-1].Seq
	}
}

func (b *Bus) Subscribe(fn func(Event)) {
	b.mu.Lock()
	b.subs = append(b.subs, fn)
	b.mu.Unlock()
}

// Publish records the event and notifies subscribers synchronously, in order.
func (b *Bus) Publish(typ string, payload any) Event {
	data, err := json.Marshal(payload)
	if err != nil {
		data = []byte(fmt.Sprintf("%q", err.Error()))
	}
	b.mu.Lock()
	b.seq++
	ev := Event{Seq: b.seq, Type: typ, Time: b.Now().UTC().Format(time.RFC3339), Data: data}
	b.log = append(b.log, ev)
	subs := append([]func(Event){}, b.subs...)
	b.mu.Unlock()
	for _, s := range subs {
		s(ev)
	}
	return ev
}

func (b *Bus) Events() []Event {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]Event(nil), b.log...)
}

// JSONL encodes the recorder as one canonical JSON object per line.
func (b *Bus) JSONL() []byte {
	var buf bytes.Buffer
	for _, e := range b.Events() {
		line, _ := json.Marshal(e)
		buf.Write(line)
		buf.WriteByte('\n')
	}
	return buf.Bytes()
}

// ParseJSONL decodes a recorder file.
func ParseJSONL(data []byte) ([]Event, error) {
	var out []Event
	for i, line := range bytes.Split(data, []byte{'\n'}) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var e Event
		if err := json.Unmarshal(line, &e); err != nil {
			return nil, fmt.Errorf("events line %d: %w", i+1, err)
		}
		out = append(out, e)
	}
	return out, nil
}
