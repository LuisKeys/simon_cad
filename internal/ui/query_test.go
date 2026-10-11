package ui

import (
	"encoding/json"
	"strings"
	"testing"
)

// A live preview (trace sliders) runs read-only tools without touching the model or recording a
// tool call; a tool that edits is refused.
func TestQueryToolIsReadOnlyAndUnrecorded(t *testing.T) {
	s := newSvc(t)
	svg := `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 10 10"><rect x="1" y="1" width="8" height="8"/></svg>`
	args, _ := json.Marshal(map[string]any{"svg": svg, "mode": "filled_regions", "width_mm": 10, "preview": true})
	before := s.app.Graph.Hash()
	calls := len(s.app.AH.ToolCalls)
	data, err := s.QueryTool("vectorize_image", args)
	if err != nil {
		t.Fatal(err)
	}
	m := data.(map[string]any)
	if m["regions"].(int) != 1 || !strings.Contains(m["preview_svg"].(string), "<svg") {
		t.Fatalf("%v", m)
	}
	if s.app.Graph.Hash() != before || len(s.app.AH.ToolCalls) != calls {
		t.Fatal("a query must not change the model or the tool-call log")
	}
	if _, err := s.QueryTool("create_box", []byte(`{"dx":1,"dy":1,"dz":1}`)); err == nil || !strings.Contains(err.Error(), "edits the model") {
		t.Fatalf("mutating tool as a query: %v", err)
	}
	if _, err := s.QueryTool("nope", []byte(`{}`)); err == nil {
		t.Fatal("unknown tool accepted")
	}
	if s.app.Graph.Get("box") != nil {
		t.Fatal("create_box must not have run")
	}
}
