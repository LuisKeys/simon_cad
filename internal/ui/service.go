// Package ui holds the thin adapters between a front end (the Wails desktop shell, or the
// local web server used for development and tests) and the same app services the CLI uses.
//
// The UI has no mutation path of its own. Geometry changes go through App.ApplyTool (tool
// registry → Commands → ChangeSet), and the only other writes are the ChangeSet lifecycle
// (commit, reject, undo, redo, restore). nomutation_test.go enforces this statically.
package ui

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	geo "github.com/accelone/simoncad/internal/agent/capabilities/geometry"
	vis "github.com/accelone/simoncad/internal/agent/capabilities/vision"
	"github.com/accelone/simoncad/internal/agent/runtime"
	"github.com/accelone/simoncad/internal/app"
	"github.com/accelone/simoncad/internal/cadir"
	"github.com/accelone/simoncad/internal/events"
	"github.com/accelone/simoncad/internal/history"
	"github.com/accelone/simoncad/internal/rendering"
	"github.com/accelone/simoncad/internal/selection"
	"github.com/accelone/simoncad/internal/tools"
	"github.com/accelone/simoncad/internal/vision"
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
	Clock       func() time.Time

	mu    sync.Mutex
	app   *app.App
	path  string
	dirty bool
	level string
	chat  []ChatMessage
	sel   []string
	selAt string // geometry hash (appearance excluded) the selection was made against

	rend *rendering.Renderer
	hub  *Hub

	busy bool
	// draft holds the ChangeSets an agent turn committed at the Review level; the user keeps
	// or discards them as one batch.
	draft []string
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
	Time      string   `json:"time"`
}

func NewService(a *app.App, path string) *Service {
	s := &Service{NewProvider: runtime.NewProviderFromEnv, Clock: time.Now, level: LevelReview,
		rend: rendering.New(), hub: NewHub()}
	s.attach(a, path)
	return s
}

// Hub streams service events to subscribers (SSE in the web server, Wails events later).
func (s *Service) Hub() *Hub { return s.hub }

func (s *Service) attach(a *app.App, path string) {
	if s.app != nil {
		s.app.Close()
	}
	s.app, s.path, s.dirty, s.sel, s.chat, s.draft = a, path, false, nil, nil, nil
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
	s.level = level
	s.hub.Publish(Notice{Kind: "state"})
	return nil
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

// ---- selection ------------------------------------------------------------------------

// PickRequest is a viewport click, already resolved to a body and a kind.
type PickRequest struct {
	Body     string `json:"body"`
	Kind     string `json:"kind"`     // body | face | edge | vertex
	Index    int    `json:"index"`    // kernel index of the face/edge/vertex
	Triangle int    `json:"triangle"` // for faces: the clicked triangle, when Index is unknown (-1 otherwise)
	Mode     string `json:"mode"`     // replace (default) | add | toggle
}

// Pick turns a click into a semantic selector and updates the selection context.
func (s *Service) Pick(p PickRequest) (SelectionView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p.Kind == "" && p.Body == "" { // click on empty space
		if p.Mode != "add" && p.Mode != "toggle" {
			return s.setSelection(nil)
		}
		return s.selectionView(), nil
	}
	m, err := s.app.Model()
	if err != nil {
		return SelectionView{}, err
	}
	bm, _, err := s.rend.Body(p.Body, m.Nodes[p.Body])
	if err != nil {
		return SelectionView{}, err
	}
	idx := p.Index
	if p.Kind == rendering.PickFace && p.Triangle >= 0 {
		f, ok := bm.FaceOfTriangle(p.Triangle)
		if !ok {
			return SelectionView{}, fmt.Errorf("triangle %d is not on body %s", p.Triangle, p.Body)
		}
		idx = f
	}
	sel, err := bm.SelectorFor(p.Kind, idx)
	if err != nil {
		return SelectionView{}, err
	}
	next := []string{sel}
	switch p.Mode {
	case "add":
		next = appendUnique(s.sel, sel)
	case "toggle":
		next = toggle(s.sel, sel)
	}
	return s.setSelection(next)
}

// SetSelection replaces the selection with selector strings (chips, agent suggestions).
func (s *Service) SetSelection(raw []string) (SelectionView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.setSelection(raw)
}

func (s *Service) setSelection(raw []string) (SelectionView, error) {
	if err := s.app.SetSelection(raw); err != nil {
		return SelectionView{}, err
	}
	s.sel = append([]string(nil), raw...)
	s.selAt = s.app.Snapshot().GeometryHash()
	s.hub.Publish(Notice{Kind: "selection"})
	return s.selectionView(), nil
}

func appendUnique(list []string, v string) []string {
	for _, x := range list {
		if x == v {
			return append([]string(nil), list...)
		}
	}
	return append(append([]string(nil), list...), v)
}

func toggle(list []string, v string) []string {
	var out []string
	found := false
	for _, x := range list {
		if x == v {
			found = true
			continue
		}
		out = append(out, x)
	}
	if !found {
		out = append(out, v)
	}
	return out
}

// SelectionView is the selection as the agent sees it, plus what the viewport must highlight.
type SelectionView struct {
	Entities   []selection.Entity `json:"entities"`
	Selectors  []string           `json:"selectors"`
	Highlights []rendering.Pick   `json:"highlights"`
}

func (s *Service) selectionView() SelectionView {
	v := SelectionView{Entities: []selection.Entity{}, Selectors: []string{}, Highlights: []rendering.Pick{}}
	ctx := s.app.SelectionContext()
	if ctx.Empty() {
		return v
	}
	v.Entities, v.Selectors = ctx.Entities, ctx.Selectors()
	m, err := s.app.Model()
	if err != nil {
		return v
	}
	for _, raw := range v.Selectors {
		id := selectorFeature(raw)
		bm, _, err := s.rend.Body(id, m.Nodes[id])
		if err != nil {
			continue
		}
		if p, err := bm.PickFor(raw); err == nil {
			v.Highlights = append(v.Highlights, p)
		}
	}
	return v
}

func selectorFeature(raw string) string {
	i := strings.Index(raw, "feature=")
	if i < 0 {
		return ""
	}
	rest := raw[i+len("feature="):]
	if j := strings.IndexAny(rest, ",)"); j >= 0 {
		rest = rest[:j]
	}
	return strings.TrimSpace(rest)
}

// Selection returns the current selection view.
func (s *Service) Selection() SelectionView {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.selectionView()
}

// ---- scene ----------------------------------------------------------------------------

// Scene brings a client holding `known` (body id → key) up to date.
func (s *Service) Scene(known map[string]string) (*rendering.Delta, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.app.Model()
	if err != nil {
		return nil, err
	}
	d, err := s.rend.Scene(m, known)
	if err != nil {
		return nil, err
	}
	// Appearance is read from the CAD-IR on every call: it is tiny and independent of
	// the mesh keys, so an appearance-only change never re-tessellates anything.
	g := s.app.Snapshot()
	d.Appearance = map[string]*cadir.Appearance{}
	for _, id := range d.Order {
		if n := g.Get(id); n != nil && !n.Appearance.IsZero() {
			d.Appearance[id] = n.Appearance
		}
	}
	return d, nil
}

// Ghost is the preview overlay of a proposed ChangeSet: the bodies it would add, and the
// live bodies it would replace.
type Ghost struct {
	ChangeSet ChangeSetCard         `json:"changeset"`
	Added     []*rendering.BodyMesh `json:"added"`
	Removed   []string              `json:"removed"`
	Triangles int                   `json:"triangles"`
	// Sketches are outlines of sketch nodes the ChangeSet would add or change.
	Sketches []*rendering.SketchLines `json:"sketches"`
}

// Preview rebuilds the model a proposed ChangeSet would produce and diffs its bodies
// against the live ones. Nothing is applied.
func (s *Service) Preview(csID string) (*Ghost, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cand, cs, err := s.app.PreviewModel(csID)
	if err != nil {
		return nil, err
	}
	live, err := s.app.Model()
	if err != nil {
		return nil, err
	}
	liveKeys := map[string]string{}
	for _, id := range live.Leaves {
		liveKeys[id] = live.Nodes[id].Key
	}
	g := &Ghost{ChangeSet: app.CardOf(cs), Added: []*rendering.BodyMesh{}, Removed: []string{}, Sketches: []*rendering.SketchLines{}}
	liveSketch := map[string]string{}
	for _, id := range live.Sketches {
		liveSketch[id] = live.Nodes[id].Key
	}
	for _, id := range cand.Sketches {
		if liveSketch[id] == cand.Nodes[id].Key {
			continue
		}
		sl, err := s.rend.Sketch(id, cand.Nodes[id])
		if err != nil {
			return nil, err
		}
		g.Sketches = append(g.Sketches, sl)
	}
	candKeys := map[string]bool{}
	for _, id := range cand.Leaves {
		candKeys[id+"|"+cand.Nodes[id].Key] = true
		if liveKeys[id] == cand.Nodes[id].Key {
			continue
		}
		bm, _, err := s.rend.Body(id, cand.Nodes[id])
		if err != nil {
			return nil, err
		}
		g.Added = append(g.Added, bm)
		g.Triangles += bm.Triangles
	}
	for _, id := range live.Leaves {
		if !candKeys[id+"|"+live.Nodes[id].Key] {
			g.Removed = append(g.Removed, id)
		}
	}
	return g, nil
}

// ---- agent ----------------------------------------------------------------------------

// unlocking releases the service lock while the model thinks, so reads stay responsive.
type unlocking struct {
	runtime.Provider
	mu *sync.Mutex
}

func (u unlocking) Complete(ctx context.Context, r runtime.Request) (*runtime.Response, error) {
	u.mu.Unlock()
	defer u.mu.Lock()
	return u.Provider.Complete(ctx, r)
}

// keepDraft accepts the open draft batch and records its checkpoint. Caller holds mu.
func (s *Service) keepDraft() {
	if len(s.draft) == 0 {
		return
	}
	s.draft = nil
	s.app.Checkpoint(s.lastIntent(), false)
}

func (s *Service) lastIntent() string {
	if p := s.app.AH.Prompts; len(p) > 0 {
		return p[len(p)-1].Text
	}
	return "Agent changes"
}

// KeepDraft accepts the agent's draft batch.
func (s *Service) KeepDraft() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.guard(); err != nil {
		return err
	}
	if len(s.draft) == 0 {
		return fmt.Errorf("no draft to keep")
	}
	s.keepDraft()
	s.say("system", "Draft kept.", "", true, "")
	s.hub.Publish(Notice{Kind: "state"})
	return nil
}

// DiscardDraft undoes the agent's draft batch, newest step first.
func (s *Service) DiscardDraft() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.guard(); err != nil {
		return err
	}
	if len(s.draft) == 0 {
		return fmt.Errorf("no draft to discard")
	}
	for i := len(s.draft) - 1; i >= 0; i-- {
		cs, err := s.app.Undo()
		if err != nil {
			s.draft = nil
			s.changed()
			return err
		}
		if cs.ID != s.draft[i] {
			s.draft = nil
			s.changed()
			return fmt.Errorf("the model changed after the draft; use Undo instead")
		}
	}
	s.draft = nil
	s.say("system", "Draft discarded.", "", true, "")
	s.changed()
	return nil
}

// ClearChat empties the conversation (and so the history replayed to the model). The
// model itself is untouched.
func (s *Service) ClearChat() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.guard(); err != nil {
		return err
	}
	s.chat = nil
	s.hub.Publish(Notice{Kind: "state"})
	return nil
}

// Prompt starts one agent turn on a goroutine. Progress arrives on the Hub as chat
// messages; the turn ends with a "agent_done" notice.
func (s *Service) Prompt(text string, thinking bool) error {
	return s.PromptWith(text, thinking, nil)
}

// PromptWith is Prompt with uploaded images (ids from UploadImage) attached to the request.
func (s *Service) PromptWith(text string, thinking bool, images []string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return fmt.Errorf("empty prompt")
	}
	if len(images) > maxPromptImages {
		return fmt.Errorf("attach at most %d images to one request", maxPromptImages)
	}
	s.mu.Lock()
	if err := s.guard(); err != nil {
		s.mu.Unlock()
		return err
	}
	for _, id := range images {
		if _, ok := s.app.Images.Get(id); !ok {
			s.mu.Unlock()
			return fmt.Errorf("image %q was not uploaded", id)
		}
	}
	prov, err := s.NewProvider()
	if err != nil {
		s.mu.Unlock()
		return err
	}
	if c, ok := prov.(runtime.ImageCapable); ok && len(images) > 0 && !c.SupportsImages() {
		s.mu.Unlock()
		return fmt.Errorf("provider %s does not support image input; set SIMONCAD_PROVIDER to claude or openai", prov.Name())
	}
	s.keepDraft() // a new request implicitly accepts the previous batch
	s.busy = true
	prior := s.history()
	s.sayWith("user", text, "", true, "", append([]string(nil), images...))
	s.hub.Publish(Notice{Kind: "state"})
	level := s.level
	s.mu.Unlock()
	go func() {
		defer func() {
			s.mu.Lock()
			s.busy = false
			s.hub.Publish(Notice{Kind: "agent_done"})
			s.hub.Publish(Notice{Kind: "state"})
			s.mu.Unlock()
		}()
		// The lock is held for the turn, except while waiting on the model (see unlocking).
		s.mu.Lock()
		defer s.mu.Unlock()
		rt := &runtime.Runtime{Provider: unlocking{prov, &s.mu}, Caps: []runtime.Capability{geo.New(), vis.New()}, History: prior, NoThinking: !thinking, ImageIDs: images}
		rt.OnStep = func(name string, args json.RawMessage, o app.Outcome) {
			cs := ""
			if o.ChangeSet != nil {
				cs = o.ChangeSet.ID
			}
			msg := o.Message
			if o.Error != "" {
				msg = o.Error
			}
			s.say("tool", msg, name, o.OK, cs)
			if o.Committed && level == LevelReview && !o.ChangeSet.AppearanceOnly() {
				s.draft = append(s.draft, cs)
			}
			s.hub.Publish(Notice{Kind: "state"})
		}
		// Review applies each step as a draft so later steps can build on earlier ones; the
		// user then keeps or discards the whole batch.
		pol := app.PolicyCommit
		if level == LevelAuto {
			pol = app.PolicyAuto
		}
		res, err := rt.Run(context.Background(), s.app, text, pol)
		switch {
		case err != nil:
			s.say("system", "Agent error: "+err.Error(), "", false, "")
		case res.Final != "":
			s.say("agent", res.Final, "", true, "")
		}
		if len(s.draft) > 0 {
			s.say("draft", fmt.Sprintf("%d step(s) applied as a draft — keep or discard them.", len(s.draft)), "", true, "")
		} else if cp := s.app.Checkpoint(text, false); cp != nil {
			s.hub.Publish(Notice{Kind: "state"})
		}
		s.app.SetActor(app.Actor{CreatedBy: "user"})
		s.changed()
	}()
	return nil
}

// maxPromptImages bounds the images of one request (a proposal is built from one drawing).
const maxPromptImages = 4

// ---- images ----------------------------------------------------------------------------

// ImageInfo describes an uploaded image.
type ImageInfo struct {
	ID        string `json:"id"`
	MediaType string `json:"media_type"`
	Width     int    `json:"width"`
	Height    int    `json:"height"`
	Retained  bool   `json:"retained"`
}

func imageInfo(im *vision.Image) ImageInfo {
	return ImageInfo{ID: im.ID, MediaType: im.MediaType, Width: im.Width, Height: im.Height, Retained: im.Retained}
}

// UploadImage stores a PNG, JPEG or WebP image for this session. It does not touch the model;
// the image is only saved with the project if it is explicitly retained.
func (s *Service) UploadImage(data []byte) (ImageInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	im, err := s.app.Images.Put(data)
	if err != nil {
		return ImageInfo{}, err
	}
	return imageInfo(im), nil
}

// ImageData returns an uploaded image's bytes and media type.
func (s *Service) ImageData(id string) ([]byte, string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	im, ok := s.app.Images.Get(id)
	if !ok {
		return nil, "", false
	}
	return im.Data, im.MediaType, true
}

// RetainImage chooses whether an image is stored inside the project (assets/images/).
func (s *Service) RetainImage(id string, keep bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.app.Images.Retain(id, keep); err != nil {
		return err
	}
	s.dirty = true
	s.hub.Publish(Notice{Kind: "state"})
	return nil
}

// ProposalView is an image interpretation as the panels show it.
type ProposalView struct {
	ID       string           `json:"id"`
	ImageID  string           `json:"image_id"`
	ImageW   int              `json:"image_w"`
	ImageH   int              `json:"image_h"`
	Revision int              `json:"revision"`
	Ready    bool             `json:"ready"`
	Used     []string         `json:"used,omitempty"`
	Proposal *vision.Proposal `json:"proposal"`
	Report   *vision.Report   `json:"report"`
}

func (s *Service) proposals() []ProposalView {
	out := []ProposalView{}
	for _, it := range s.app.Vision.List() {
		v := ProposalView{ID: it.ID, ImageID: it.Proposal.ImageID, Revision: it.Revision, Used: it.Used, Proposal: it.Proposal, Report: it.Report()}
		v.Ready = v.Report.Ready
		if im, ok := s.app.Images.Get(it.Proposal.ImageID); ok {
			v.ImageW, v.ImageH = im.Width, im.Height
		}
		out = append(out, v)
	}
	return out
}

// maxHistory bounds how many prior chat messages are replayed to the model each turn.
const maxHistory = 30

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
	if len(out) > maxHistory {
		out = out[len(out)-maxHistory:]
	}
	for len(out) > 0 && out[0].Role != "user" {
		out = out[1:]
	}
	return out
}

// ---- ChangeSets and tools -------------------------------------------------------------

// Outcome is a tool result as the UI shows it.
type Outcome struct {
	OK        bool           `json:"ok"`
	Error     string         `json:"error,omitempty"`
	Data      any            `json:"data,omitempty"`
	ChangeSet *ChangeSetCard `json:"changeset,omitempty"`
	Committed bool           `json:"committed"`
}

// CallTool runs one registered tool directly (palette, inline parameter edits). It is the
// same path the agent and the CLI use.
func (s *Service) CallTool(name string, args json.RawMessage) (Outcome, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.guard(); err != nil {
		return Outcome{}, err
	}
	s.app.SetActor(app.Actor{CreatedBy: "user"})
	pol := s.policy()
	if tools.IsAppearanceTool(name) {
		pol = app.PolicyAuto // presentation edits never wait for approval, even in Review
	}
	o := s.app.ApplyTool(name, args, pol)
	return s.finishTool(name, o), nil
}

func (s *Service) finishTool(name string, o app.Outcome) Outcome {
	out := Outcome{OK: o.OK, Error: o.Error, Data: o.Data, Committed: o.Committed}
	if o.ChangeSet != nil {
		c := app.CardOf(o.ChangeSet)
		out.ChangeSet = &c
	}
	if o.Committed && !o.ChangeSet.AppearanceOnly() {
		s.app.Checkpoint(firstNonEmpty(o.ChangeSet.Intent, name), false)
	}
	s.say("tool", firstNonEmpty(o.Error, o.Message, name), name, o.OK, func() string {
		if o.ChangeSet != nil {
			return o.ChangeSet.ID
		}
		return ""
	}())
	s.changed()
	return out
}

// DeleteRequest is a UI delete (Delete/Backspace, context menu). Features empty = the
// current selection. ResolveOwner and Cascade are the user's answers to the two questions
// Delete may come back with.
type DeleteRequest struct {
	Features     []string `json:"features,omitempty"`
	ResolveOwner bool     `json:"resolve_owner,omitempty"`
	Cascade      bool     `json:"cascade,omitempty"`
}

// DeleteResult is an Outcome, or a question for the user. NeedsConfirm is "owner" (faces,
// edges or vertices were selected: topology cannot be deleted, offer the owning feature) or
// "dependents" (other features depend on the targets); nothing has changed in either case.
type DeleteResult struct {
	Outcome
	NeedsConfirm string                `json:"needs_confirm,omitempty"`
	Analysis     *tools.DeleteAnalysis `json:"analysis,omitempty"`
}

// Delete removes the selected objects as one ChangeSet through delete_objects. It first asks
// check_delete what would happen, so the user is warned before anything is committed.
func (s *Service) Delete(req DeleteRequest) (DeleteResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.guard(); err != nil {
		return DeleteResult{}, err
	}
	s.app.SetActor(app.Actor{CreatedBy: "user"})
	args := map[string]any{}
	if len(req.Features) > 0 {
		args["features"] = req.Features
	}
	if req.ResolveOwner {
		args["resolve_owner"] = true
	}
	raw, _ := json.Marshal(args)
	chk := s.app.ApplyTool("check_delete", raw, app.PolicyPreview)
	if !chk.OK {
		return DeleteResult{Outcome: Outcome{Error: chk.Error}}, nil
	}
	an, _ := chk.Data.(*tools.DeleteAnalysis)
	if an == nil {
		return DeleteResult{}, fmt.Errorf("check_delete returned no analysis")
	}
	switch {
	case len(an.SubEntities) > 0 && !req.ResolveOwner:
		return DeleteResult{NeedsConfirm: "owner", Analysis: an}, nil
	case len(an.Dependents) > 0 && !req.Cascade:
		return DeleteResult{NeedsConfirm: "dependents", Analysis: an}, nil
	}
	if req.Cascade {
		args["cascade"] = true
	}
	ids := append(append([]string(nil), an.Targets...), an.Dependents...)
	args["intent"] = "Delete " + strings.Join(ids, ", ")
	raw, _ = json.Marshal(args)
	// A deliberate delete is committed at once (it stays undoable); the ChangeSet keeps the
	// dependency warnings for the record.
	return DeleteResult{Outcome: s.finishTool("delete_objects", s.app.ApplyTool("delete_objects", raw, app.PolicyCommit)), Analysis: an}, nil
}

// SetParameter is an inline design-tree edit: modify_parameter, as a user ChangeSet. Single
// parameter edits are low-risk, so they commit immediately regardless of the level.
func (s *Service) SetParameter(feature, parameter string, value any) (Outcome, error) {
	args, err := json.Marshal(map[string]any{"feature": feature, "parameter": parameter, "value": value,
		"intent": fmt.Sprintf("Set %s.%s to %v", feature, parameter, value)})
	if err != nil {
		return Outcome{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.guard(); err != nil {
		return Outcome{}, err
	}
	s.app.SetActor(app.Actor{CreatedBy: "user"})
	return s.finishTool("modify_parameter", s.app.ApplyTool("modify_parameter", args, app.PolicyAuto)), nil
}

// Commit approves a proposed ChangeSet and records a semantic checkpoint.
func (s *Service) Commit(id string) (ChangeSetCard, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.guard(); err != nil {
		return ChangeSetCard{}, err
	}
	cs, err := s.app.Commit(id)
	if err != nil {
		return ChangeSetCard{}, err
	}
	s.app.Checkpoint(firstNonEmpty(cs.Intent, "Committed "+cs.ID), false)
	s.say("system", "Committed "+cs.ID+": "+firstNonEmpty(cs.Intent, cs.Explanation), "", true, cs.ID)
	s.changed()
	return app.CardOf(cs), nil
}

// Reject discards a proposed ChangeSet.
func (s *Service) Reject(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.guard(); err != nil {
		return err
	}
	if err := s.app.Reject(id); err != nil {
		return err
	}
	s.say("system", "Rejected "+id, "", true, id)
	s.hub.Publish(Notice{Kind: "state"})
	return nil
}

func (s *Service) Undo() (ChangeSetCard, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.guard(); err != nil {
		return ChangeSetCard{}, err
	}
	s.keepDraft()
	cs, err := s.app.Undo()
	if err != nil {
		return ChangeSetCard{}, err
	}
	s.say("system", "Undid "+cs.ID+": "+cs.Intent, "", true, cs.ID)
	s.changed()
	return app.CardOf(cs), nil
}

func (s *Service) Redo() (ChangeSetCard, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.guard(); err != nil {
		return ChangeSetCard{}, err
	}
	cs, err := s.app.Redo()
	if err != nil {
		return ChangeSetCard{}, err
	}
	s.say("system", "Redid "+cs.ID+": "+cs.Intent, "", true, cs.ID)
	s.changed()
	return app.CardOf(cs), nil
}

// ---- timeline -------------------------------------------------------------------------

type CheckpointView struct {
	ID         string   `json:"id"`
	Seq        int      `json:"seq"`
	Label      string   `json:"label"`
	Actor      string   `json:"actor"`
	Time       string   `json:"time"`
	ChangeSets []string `json:"changesets"`
	Current    bool     `json:"current"`
}

func (s *Service) checkpoints() []CheckpointView {
	hash := s.app.Snapshot().Hash()
	out := []CheckpointView{}
	for _, c := range s.app.Hist.Checkpoints {
		out = append(out, CheckpointView{ID: c.ID, Seq: c.Seq, Label: c.Label, Actor: c.Actor, Time: c.Time,
			ChangeSets: c.ChangeSets, Current: c.GraphHash == hash})
	}
	return out
}

// Restore proposes a return to a checkpoint. It is never auto-committed: the user previews
// and commits it like any other ChangeSet.
func (s *Service) Restore(ref string) (Outcome, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.guard(); err != nil {
		return Outcome{}, err
	}
	s.app.SetActor(app.Actor{CreatedBy: "user"})
	return s.finishTool("restore_checkpoint", s.app.Restore(ref, app.PolicyPreview)), nil
}

// Comparison is a semantic diff between two checkpoints (or one and the live model).
type Comparison struct {
	A         string       `json:"a"`
	B         string       `json:"b"`
	Diff      history.Diff `json:"diff"`
	VolumeA   float64      `json:"volume_a_mm3"`
	VolumeB   float64      `json:"volume_b_mm3"`
	FeaturesA int          `json:"features_a"`
	FeaturesB int          `json:"features_b"`
	Identical bool         `json:"identical"`
	LabelA    string       `json:"label_a"`
	LabelB    string       `json:"label_b"`
}

// Compare diffs two checkpoints; an empty b means the live model.
func (s *Service) Compare(a, b string) (*Comparison, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ca, err := s.app.Hist.Find(a)
	if err != nil {
		return nil, err
	}
	gb, lb := s.app.Snapshot(), "current model"
	if b != "" {
		cb, err := s.app.Hist.Find(b)
		if err != nil {
			return nil, err
		}
		gb, lb = cb.Snapshot, cb.Label
	}
	d := history.Compare(ca.Snapshot, gb)
	ea, err := s.app.Evaluate(ca.Snapshot)
	if err != nil {
		return nil, err
	}
	eb, err := s.app.Evaluate(gb)
	if err != nil {
		return nil, err
	}
	return &Comparison{A: ca.ID, B: b, LabelA: ca.Label, LabelB: lb, Diff: d, VolumeA: ea.Volume, VolumeB: eb.Volume,
		FeaturesA: len(ca.Snapshot.Nodes), FeaturesB: len(gb.Nodes), Identical: d.Empty()}, nil
}

// ---- playback -------------------------------------------------------------------------

// Frame is one step of Instant Design playback.
type Frame struct {
	Index     int              `json:"index"` // number of ChangeSets applied (0 = empty)
	Total     int              `json:"total"`
	ChangeSet *ChangeSetCard   `json:"changeset,omitempty"`
	Scene     *rendering.Delta `json:"scene"`
}

// PlaybackFrame rebuilds the model after the first i committed ChangeSets, so geometry
// appears feature by feature, driven by the recorded ChangeSets.
func (s *Service) PlaybackFrame(i int, known map[string]string) (*Frame, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sets := s.app.Committed()
	m, err := s.app.ModelAfter(i)
	if err != nil {
		return nil, err
	}
	d := &rendering.Delta{Order: []string{}, Keys: map[string]string{}, Changed: []*rendering.BodyMesh{}, Removed: []string{}, Sketches: []*rendering.SketchLines{}}
	if len(m.Leaves) > 0 || len(m.Sketches) > 0 {
		if d, err = s.rend.Scene(m, known); err != nil {
			return nil, err
		}
	} else {
		for id := range known {
			d.Removed = append(d.Removed, id)
		}
		sort.Strings(d.Removed)
	}
	f := &Frame{Index: i, Total: len(sets), Scene: d}
	if i > 0 {
		c := app.CardOf(sets[i-1])
		f.ChangeSet = &c
	}
	return f, nil
}

// ---- project --------------------------------------------------------------------------

// New starts an empty project.
func (s *Service) New(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.guard(); err != nil {
		return err
	}
	if name == "" {
		name = "untitled"
	}
	s.attach(app.New(name, s.Clock), "")
	s.hub.Publish(Notice{Kind: "state"})
	return nil
}

// Open loads a .scadproj directory.
func (s *Service) Open(dir string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.guard(); err != nil {
		return err
	}
	dir = s.findProject(dir)
	a, err := app.Open(dir, s.Clock)
	if err != nil {
		return err
	}
	s.attach(a, dir)
	s.hub.Publish(Notice{Kind: "state"})
	return nil
}

const (
	projectExt = ".scadproj"
	// OutputDir is where projects saved with a relative path are written.
	OutputDir = "projects_output"
)

// resolveProjectPath places a bare or relative project path under OutputDir; absolute
// paths and paths already under OutputDir are kept as typed.
func resolveProjectPath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" || filepath.IsAbs(p) || strings.HasPrefix(p, "~") {
		return p
	}
	clean := filepath.Clean(p)
	if clean == OutputDir || strings.HasPrefix(clean, OutputDir+string(filepath.Separator)) {
		return clean
	}
	return filepath.Join(OutputDir, clean)
}

// withProjectExt restores the .scadproj extension when the user typed a bare name.
func withProjectExt(p string) string {
	p = strings.TrimSpace(p)
	if p == "" || strings.HasSuffix(strings.ToLower(strings.TrimRight(p, "/")), projectExt) {
		return p
	}
	return strings.TrimRight(p, "/") + projectExt
}

// ProjectInfo describes a saved project found in OutputDir.
type ProjectInfo struct {
	Name     string    `json:"name"`
	Path     string    `json:"path"`
	Modified time.Time `json:"modified"`
}

// ListProjects returns the .scadproj directories in OutputDir, newest first.
func (s *Service) ListProjects() []ProjectInfo {
	out := []ProjectInfo{}
	entries, err := os.ReadDir(OutputDir)
	if err != nil {
		return out
	}
	for _, e := range entries {
		if !e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), projectExt) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, ProjectInfo{
			Name:     e.Name()[:len(e.Name())-len(projectExt)],
			Path:     filepath.Join(OutputDir, e.Name()),
			Modified: info.ModTime(),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Modified.After(out[j].Modified) })
	return out
}

// findProject resolves a typed path to an existing project: as typed, then under
// OutputDir, each with and without the .scadproj extension.
func (s *Service) findProject(p string) string {
	for _, c := range []string{p, withProjectExt(p), resolveProjectPath(p), resolveProjectPath(withProjectExt(p))} {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	return p
}

// Save writes the project; an empty dir saves in place.
func (s *Service) Save(dir string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if dir == "" {
		dir = s.path
	}
	if dir == "" {
		return "", fmt.Errorf("this project has no location yet; give a path")
	}
	dir = resolveProjectPath(withProjectExt(dir))
	if err := s.app.Save(dir); err != nil {
		return "", err
	}
	s.path, s.dirty = dir, false
	s.hub.Publish(Notice{Kind: "state"})
	return dir, nil
}

// Export writes STEP or STL through the export tools.
func (s *Service) Export(format, path string) (Outcome, error) {
	if format != "step" && format != "stl" {
		return Outcome{}, fmt.Errorf("format must be step or stl")
	}
	if strings.TrimSpace(path) == "" {
		return Outcome{}, fmt.Errorf("give an output path")
	}
	args, _ := json.Marshal(map[string]any{"path": path})
	s.mu.Lock()
	defer s.mu.Unlock()
	o := s.app.ApplyTool("export_"+format, args, app.PolicyAuto)
	out := Outcome{OK: o.OK, Error: o.Error, Data: o.Data}
	return out, nil
}

// ---- state ----------------------------------------------------------------------------

// ChangeSetCard is a ChangeSet as the conversation and review UI show it.
type ChangeSetCard = app.ChangeSetCard

// Validation is one check result on a ChangeSet card.
type Validation = app.Validation

func nz(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}

// TreeNode is one design-tree row.
type TreeNode struct {
	ID         string            `json:"id"`
	Name       string            `json:"name"`
	Type       string            `json:"type"`
	Status     string            `json:"status"`
	Errors     []string          `json:"errors"`
	Intent     string            `json:"intent"`
	CreatedBy  string            `json:"created_by"`
	Capability string            `json:"capability,omitempty"`
	Suppressed bool              `json:"suppressed"`
	Visible    bool              `json:"visible"` // produces a body nothing else consumes (a leaf)
	Appearance *cadir.Appearance `json:"appearance,omitempty"`
	Deps       []string          `json:"deps"`
	Params     map[string]any    `json:"params"`
	Exprs      map[string]string `json:"expressions,omitempty"`
	Group      string            `json:"group,omitempty"` // macro grouping (Phase 3)
}

// State is everything the panels render.
type State struct {
	Name        string           `json:"name"`
	Path        string           `json:"path"`
	Dirty       bool             `json:"dirty"`
	Units       string           `json:"units"`
	Level       string           `json:"level"`
	Busy        bool             `json:"busy"`
	Tree        []TreeNode       `json:"tree"`
	Pending     []ChangeSetCard  `json:"pending"`
	Recent      []ChangeSetCard  `json:"recent"`
	Checkpoints []CheckpointView `json:"checkpoints"`
	Chat        []ChatMessage    `json:"chat"`
	Selection   SelectionView    `json:"selection"`
	CanUndo     bool             `json:"can_undo"`
	CanRedo     bool             `json:"can_redo"`
	Volume      float64          `json:"volume_mm3"`
	Failed      []string         `json:"failed"`
	Frames      int              `json:"frames"`
	Draft       []string         `json:"draft"`
	Provider    string           `json:"provider"`
	Images      []ImageInfo      `json:"images"`
	Proposals   []ProposalView   `json:"proposals"`
}

// State snapshots the project for the panels.
func (s *Service) State() (*State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.app.Model()
	if err != nil {
		return nil, err
	}
	g := s.app.Snapshot()
	leaf := map[string]bool{}
	for _, id := range m.Leaves {
		leaf[id] = true
	}
	st := &State{Name: s.app.Meta.Name, Path: s.path, Dirty: s.dirty, Units: g.Units, Level: s.level, Busy: s.busy,
		Tree: []TreeNode{}, Pending: []ChangeSetCard{}, Recent: []ChangeSetCard{}, Checkpoints: s.checkpoints(),
		Chat: append([]ChatMessage{}, s.chat...), Selection: s.selectionView(), CanUndo: s.app.CanUndo(),
		Draft: append([]string{}, s.draft...), CanRedo: s.app.CanRedo(), Volume: m.Volume(), Failed: nz(m.Failed()), Frames: len(s.app.Committed())}
	st.Images, st.Proposals = []ImageInfo{}, s.proposals()
	for _, im := range s.app.Images.All() {
		st.Images = append(st.Images, imageInfo(im))
	}
	if p, err := runtime.ProviderNameFromEnv(); err == nil {
		st.Provider = p
	}
	for _, n := range g.Nodes {
		name := n.Metadata.Name
		if name == "" {
			name = n.ID
		}
		st.Tree = append(st.Tree, TreeNode{ID: n.ID, Name: name, Type: n.Type, Status: n.Status, Errors: nz(n.Errors),
			Intent: n.Intent, CreatedBy: n.Provenance.CreatedBy, Capability: n.Provenance.Capability,
			Suppressed: n.Suppressed, Visible: leaf[n.ID], Appearance: n.Appearance, Deps: nz(n.Dependencies), Params: n.Parameters, Exprs: n.Expressions})
	}
	for _, cs := range s.app.Pending() {
		st.Pending = append(st.Pending, app.CardOf(cs))
	}
	sets := s.app.Committed()
	for i := len(sets) - 1; i >= 0 && len(st.Recent) < 8; i-- {
		st.Recent = append(st.Recent, app.CardOf(sets[i]))
	}
	return st, nil
}

// ---- palette --------------------------------------------------------------------------

// PaletteEntry is one command-palette row. Every kind routes through the tool registry:
// "tool" calls it directly, "intent" and "ask" hand natural language to the agent (which
// can only act through tools), and "command" is the ChangeSet lifecycle or project I/O.
type PaletteEntry struct {
	Kind        string         `json:"kind"` // intent | tool | command | ask
	ID          string         `json:"id"`
	Title       string         `json:"title"`
	Description string         `json:"description"`
	Template    string         `json:"template,omitempty"` // prompt prefix or JSON args skeleton
	Mutating    bool           `json:"mutating,omitempty"`
	Schema      map[string]any `json:"schema,omitempty"`
}

// IntentVerbs are the user-facing engineering verbs. Until macros land (Phase 3) they are
// natural-language openers for the agent, which maps them onto the Phase 1 tools.
var IntentVerbs = []string{"Mount", "Attach", "Connect", "Reinforce", "Fit", "Clear", "Hollow", "Support",
	"Align", "Center", "Protect", "Lighten", "Adapt", "Enclose"}

// Palette lists intent verbs, every registered tool, and the built-in commands.
func (s *Service) Palette() []PaletteEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []PaletteEntry
	for _, v := range IntentVerbs {
		out = append(out, PaletteEntry{Kind: "intent", ID: strings.ToLower(v), Title: v + "…",
			Description: "Describe what to " + strings.ToLower(v) + "; the agent turns it into tool calls", Template: v + " "})
	}
	for _, name := range s.app.Reg.Names() {
		t, _ := s.app.Reg.Get(name)
		skel := map[string]any{}
		if req, ok := t.Schema["required"].([]string); ok {
			for _, r := range req {
				skel[r] = ""
			}
		}
		b, _ := json.Marshal(skel)
		out = append(out, PaletteEntry{Kind: "tool", ID: t.Name, Title: t.Name, Description: t.Description,
			Template: string(b), Mutating: t.Mutating(), Schema: t.Schema})
	}
	for _, c := range []PaletteEntry{
		{ID: "undo", Title: "Undo", Description: "Undo the last committed ChangeSet"},
		{ID: "redo", Title: "Redo", Description: "Redo the last undone ChangeSet"},
		{ID: "save", Title: "Save project", Description: "Write the .scadproj directory"},
		{ID: "export_step", Title: "Export STEP", Description: "Write a validated STEP file"},
		{ID: "export_stl", Title: "Export STL", Description: "Write a watertight STL for printing"},
		{ID: "playback", Title: "Play back the design", Description: "Replay the build feature by feature"},
		{ID: "new", Title: "New project", Description: "Start an empty project"},
	} {
		c.Kind = "command"
		out = append(out, c)
	}
	return out
}
