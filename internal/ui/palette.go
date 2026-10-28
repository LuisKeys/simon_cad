package ui

import (
	"encoding/json"
	"strings"
)

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
		{ID: "screenshot", Title: "Screenshot", Description: "Save the whole viewport (all views in Quad) as a PNG in Downloads"},
		{ID: "playback", Title: "Play back the design", Description: "Replay the build feature by feature"},
		{ID: "print_simulation", Title: "Print simulation", Description: "Slice the model and watch it being 3D printed, layer by layer"},
		{ID: "new", Title: "New project", Description: "Start an empty project"},
	} {
		c.Kind = "command"
		out = append(out, c)
	}
	return out
}
