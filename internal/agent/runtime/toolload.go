package runtime

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/accelone/simoncad/internal/tools"
)

// Tool loading modes (Runtime.ToolLoading or the SIMON_TOOL_LOADING environment variable).
const (
	// ToolLoadingAll sends every tool definition on every step (the default).
	ToolLoadingAll = "all"
	// ToolLoadingProgressive sends only the core geometry tools plus the categories the model
	// asked for with load_tool_category. Loaded categories stay loaded for the conversation.
	ToolLoadingProgressive = "progressive"

	ToolLoadingEnv = "SIMON_TOOL_LOADING"

	// loadCategoryTool is the meta-tool that loads categories. The runtime answers it
	// itself; it never reaches App.ApplyTool.
	loadCategoryTool = "load_tool_category"
)

// toolLoader holds back the definitions of the tools that are not core, by category, and
// releases a category when the model asks for it. Loading only ever adds tools, so the
// definitions already sent keep their position and the prompt cache keeps hitting.
type toolLoader struct {
	pending map[string][]ToolDef // category -> definitions not loaded yet
	catOf   map[string]string    // not-yet-loaded tool -> category
	cats    []tools.Category     // categories that have held-back tools, in presentation order
}

// splitTools separates defs into those sent from the start and those held back. Only geometry
// tools are held back: the vision and visual capabilities are already loaded as a whole,
// and only when relevant.
func splitTools(reg *tools.Registry, defs []ToolDef) (base []ToolDef, l *toolLoader) {
	l = &toolLoader{pending: map[string][]ToolDef{}, catOf: map[string]string{}}
	for _, d := range defs {
		t, ok := reg.Get(d.Name)
		if !ok || t.Capability != tools.CapGeometry || tools.IsCore(d.Name) || t.Category == "" {
			base = append(base, d)
			continue
		}
		l.pending[t.Category] = append(l.pending[t.Category], d)
		l.catOf[d.Name] = t.Category
	}
	for _, c := range tools.Categories() {
		if len(l.pending[c.Name]) > 0 {
			l.cats = append(l.cats, c)
		}
	}
	if len(l.cats) == 0 {
		return defs, nil
	}
	return base, l
}

// metaDef is the definition of load_tool_category.
func (l *toolLoader) metaDef() ToolDef {
	names := make([]any, len(l.cats))
	for i, c := range l.cats {
		names[i] = c.Name
	}
	return ToolDef{
		Name: loadCategoryTool,
		Description: "Load the tools of one or more categories (see the category list in the instructions) so you can call them. " +
			"Call it before using any tool that is not currently available; loaded tools stay available for the rest of the conversation.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"categories": map[string]any{
					"type":        "array",
					"description": "Categories to load; list every one you need in a single call.",
					"items":       map[string]any{"type": "string", "enum": names},
					"minItems":    1,
				},
			},
			"required": []any{"categories"},
		},
	}
}

// index describes the categories whose tools are not loaded yet, for the system prompt.
func (l *toolLoader) index() string {
	var sb strings.Builder
	sb.WriteString("## Tool categories\nOnly the everyday tools are loaded. The others are grouped in the categories below; " +
		"before using one of them, call " + loadCategoryTool + " with every category you need. " +
		"Loaded tools stay available for the rest of the conversation.\n")
	for _, c := range l.cats {
		var names []string
		for _, d := range l.pending[c.Name] {
			names = append(names, d.Name)
		}
		fmt.Fprintf(&sb, "- %s: %s Tools: %s.\n", c.Name, c.Description, strings.Join(names, ", "))
	}
	return sb.String()
}

// unloaded names the category of a held-back tool ("" when the tool is loaded or unknown).
func (l *toolLoader) unloaded(tool string) string {
	if l == nil {
		return ""
	}
	return l.catOf[tool]
}

// load releases the categories and returns their definitions plus the tool names per category.
func (l *toolLoader) load(cats []string) ([]ToolDef, map[string][]string, error) {
	valid := map[string]bool{}
	var names []string
	for _, c := range l.cats {
		valid[c.Name] = true
		names = append(names, c.Name)
	}
	for _, c := range cats {
		if !valid[c] {
			return nil, nil, fmt.Errorf("unknown category %q; categories: %s", c, strings.Join(names, ", "))
		}
	}
	var defs []ToolDef
	loaded := map[string][]string{}
	for _, c := range cats {
		for _, d := range l.pending[c] {
			defs = append(defs, d)
			loaded[c] = append(loaded[c], d.Name)
			delete(l.catOf, d.Name)
		}
		delete(l.pending, c)
	}
	return defs, loaded, nil
}

// answerLoad serves a load_tool_category call: it appends the definitions to c.defs and
// returns the tool_result content.
func (c *Conversation) answerLoad(input json.RawMessage) (content string, ok bool) {
	var in struct {
		Categories []string `json:"categories"`
	}
	if err := json.Unmarshal(input, &in); err != nil || len(in.Categories) == 0 {
		return `{"ok":false,"error":"pass categories: a non-empty array of category names"}`, false
	}
	defs, loaded, err := c.loader.load(in.Categories)
	if err != nil {
		b, _ := json.Marshal(map[string]any{"ok": false, "error": err.Error()})
		return string(b), false
	}
	c.defs = append(c.defs, defs...)
	b, _ := json.Marshal(map[string]any{"ok": true, "loaded": loaded, "note": "these tools are now available (already loaded categories are skipped)"})
	return string(b), true
}
