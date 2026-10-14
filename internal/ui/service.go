// Package ui holds the thin adapters between a front end (the Wails desktop shell, or the
// local web server used for development and tests) and the same app services the CLI uses.
//
// The UI has no mutation path of its own. Geometry changes go through App.ApplyTool (tool
// registry → Commands → ChangeSet), and the only other writes are the ChangeSet lifecycle
// (commit, reject, undo, redo, restore). nomutation_test.go enforces this statically.
package ui

import (
	"context"
	"fmt"
	"github.com/accelone/simoncad/internal/agent/refine"
	"github.com/accelone/simoncad/internal/agent/runtime"
	"github.com/accelone/simoncad/internal/agent/workflow"
	"github.com/accelone/simoncad/internal/app"
	"github.com/accelone/simoncad/internal/events"
	"github.com/accelone/simoncad/internal/printsim/job"
	"github.com/accelone/simoncad/internal/rendering"
	"sync"
	"time"
)

// ErrBusy is returned for edits attempted while the agent is working.
var ErrBusy = fmt.Errorf("the agent is working; wait for it to finish")

// Interaction levels decide whether valid ChangeSets commit without review.
const (
	LevelReview = "review" // every ChangeSet is previewed first (default)
	LevelAuto   = "auto"   // low-risk single-feature ChangeSets commit immediately
)

// ProviderFactory creates the LLM backend for one agent turn.
type ProviderFactory func() (runtime.Provider, error)

// Service is the UI-facing facade over one open project.
type Service struct {
	// NewProvider defaults to the environment-configured provider.
	NewProvider ProviderFactory
	// NewRouter builds the model router (fallback providers for attachments the selected
	// provider cannot handle). Defaults to the environment configuration.
	NewRouter func() *runtime.Router
	// Autonomous enables the plan → execute → validate → correct workflow for complex
	// requests (default on; SIMONCAD_AUTONOMOUS=off disables it).
	Autonomous bool
	Clock      func() time.Time

	mu    sync.Mutex
	app   *app.App
	path  string
	dirty bool
	level string
	chat  []ChatMessage
	specs []Spec // attached reference documents: short-term memory until Clear
	sel   []string
	selAt string // geometry hash (appearance excluded) the selection was made against

	rend *rendering.Renderer
	hub  *Hub

	busy bool
	// agentState is the workflow state of the running (or last) agent turn.
	agentState *workflow.Event
	// refineCancel stops the running visual refinement; refineState is its latest event.
	refineCancel context.CancelFunc
	refineState  *refine.Event
	// printsim runs print-simulation slicing jobs (nil until first used).
	printsim *job.Manager
	// draft holds the ChangeSets an agent turn committed at the Review level; the user keeps
	// or discards them as one batch.
	draft []string
	// turnUsage is the last turn's token consumption; sessionUsage accumulates every turn
	// since the service started (it outlives Clear and reopening a project, like a meter).
	turnUsage    runtime.Usage
	sessionUsage runtime.Usage
	turns        int
	// sessionConsent is a remote-fallback authorization given for this session only.
	sessionConsent string
	// announced holds the fallback models already named in the conversation this turn.
	announced map[string]bool

	// routeMu guards the routing record, which the router reports from model calls made
	// while mu is released (see unlocking).
	routeMu sync.Mutex
	// model is the model serving the running request, or the one that served the last.
	model *runtime.RouteEvent
	// fallbacks are the fallback routes of the running request, for the record.
	fallbacks []runtime.RouteEvent
}

// ChatMessage is one entry of the conversation panel.
type ChatMessage struct {
	Seq       int      `json:"seq"`
	Role      string   `json:"role"` // user | agent | tool | system
	Text      string   `json:"text"`
	Tool      string   `json:"tool,omitempty"`
	OK        bool     `json:"ok,omitempty"`
	ChangeSet string   `json:"changeset,omitempty"`
	Images    []string `json:"images,omitempty"` // ids of images attached to a user message
	Docs      []string `json:"docs,omitempty"`   // documents first sent with this user message
	Time      string   `json:"time"`
}

func NewService(a *app.App, path string) *Service {
	s := &Service{NewProvider: runtime.NewProviderFromEnv, NewRouter: runtime.NewRouterFromEnv, Clock: time.Now, level: LevelReview,
		Autonomous: autonomousFromEnv(), rend: rendering.New(), hub: NewHub()}
	s.rend.DetailFn = func() rendering.Detail { d, _ := rendering.ParseDetail(s.app.MeshDetail()); return d }
	s.attach(a, path)
	return s
}

// Hub streams service events to subscribers (SSE in the web server, Wails events later).
func (s *Service) Hub() *Hub { return s.hub }

func (s *Service) attach(a *app.App, path string) {
	if s.app != nil {
		s.stopPrintSim()
		s.app.Close()
	}
	s.app, s.path, s.dirty, s.sel, s.chat, s.draft, s.specs = a, path, false, nil, nil, nil, nil
	a.SetActor(app.Actor{CreatedBy: "user"})
	a.Bus.Subscribe(func(ev events.Event) { s.hub.Publish(Notice{Kind: "event", Type: ev.Type, Seq: ev.Seq}) })
	for _, p := range a.AH.Prompts { // reopen: rebuild the conversation from the project record
		s.say("user", p.Text, "", true, "")
		if p.Summary != "" {
			s.say("agent", p.Summary, "", true, "")
		}
	}
}

// Close releases the project and the kernel.
func (s *Service) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopPrintSim()
	s.app.Close()
}

func (s *Service) say(role, text, tool string, ok bool, cs string) ChatMessage {
	return s.sayWith(role, text, tool, ok, cs, nil)
}

func (s *Service) sayWith(role, text, tool string, ok bool, cs string, images []string) ChatMessage {
	m := ChatMessage{Seq: len(s.chat) + 1, Role: role, Text: text, Tool: tool, OK: ok, ChangeSet: cs, Images: images,
		Time: s.Clock().UTC().Format(time.RFC3339)}
	s.chat = append(s.chat, m)
	s.hub.Publish(Notice{Kind: "chat", Chat: &m})
	return m
}

func (s *Service) policy() app.Policy {
	if s.level == LevelAuto {
		return app.PolicyAuto
	}
	return app.PolicyPreview
}

// SetLevel switches the interaction level.
func (s *Service) SetLevel(level string) error {
	if level != LevelReview && level != LevelAuto {
		return fmt.Errorf("unknown interaction level %q (use %s or %s)", level, LevelReview, LevelAuto)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.guard(); err != nil {
		return err
	}
	s.level = level
	s.hub.Publish(Notice{Kind: "state"})
	return nil
}

// Busy reports whether an agent turn is running.
func (s *Service) Busy() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.busy
}

func (s *Service) guard() error {
	if s.busy {
		return ErrBusy
	}
	return nil
}

// changed reconciles derived UI state after the model may have changed.
func (s *Service) changed() {
	s.dirty = true
	// Face/edge indices are only meaningful for the model they were picked on, so a
	// selection never outlives a change to the model.
	if len(s.sel) > 0 && s.app.Snapshot().GeometryHash() != s.selAt {
		s.sel = nil
		_ = s.app.SetSelection(nil)
	}
	s.hub.Publish(Notice{Kind: "state"})
}
