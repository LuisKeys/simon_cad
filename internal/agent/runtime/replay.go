package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
)

// Recording is a sequence of provider responses captured from (or scripted for) one run.
type Recording struct {
	Source    string     `json:"source"` // "live:<model>" for captured runs, "scripted" for hand-written ones
	Responses []Response `json:"responses"`
}

// Replay returns recorded responses in order; used by CI evals so they need no network.
type Replay struct {
	Rec  Recording
	mu   sync.Mutex
	next int
}

func (r *Replay) Name() string { return "replay:" + r.Rec.Source }

func (r *Replay) Complete(_ context.Context, _ Request) (*Response, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.next >= len(r.Rec.Responses) {
		return nil, fmt.Errorf("recording exhausted after %d responses (the agent asked for more turns than were recorded)", r.next)
	}
	resp := r.Rec.Responses[r.next]
	r.next++
	return &resp, nil
}

// Recorder wraps a live provider and keeps every response so the run can be saved and replayed.
type Recorder struct {
	Inner Provider
	mu    sync.Mutex
	rec   Recording
}

func NewRecorder(p Provider) *Recorder {
	return &Recorder{Inner: p, rec: Recording{Source: "live:" + p.Name()}}
}

func (r *Recorder) Name() string { return r.Inner.Name() }

func (r *Recorder) Complete(ctx context.Context, req Request) (*Response, error) {
	resp, err := r.Inner.Complete(ctx, req)
	if err == nil {
		r.mu.Lock()
		r.rec.Responses = append(r.rec.Responses, *resp)
		r.mu.Unlock()
	}
	return resp, err
}

func (r *Recorder) Recording() Recording {
	r.mu.Lock()
	defer r.mu.Unlock()
	return Recording{Source: r.rec.Source, Responses: append([]Response(nil), r.rec.Responses...)}
}

func (r Recording) Save(path string) error {
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}
