package tools

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestSchemaValidation(t *testing.T) {
	s := object(props{"n": positive("n"), "k": enum("k", "a", "b"), "list": array("l", number("x"), 2, 3), "flag": boolean("f")}, "n")
	ok := []map[string]any{
		{"n": 1.0},
		{"n": 1.0, "k": "a", "list": []any{1.0, 2.0}, "flag": true},
	}
	for _, v := range ok {
		if err := ValidateSchema(s, v); err != nil {
			t.Errorf("%v should pass: %v", v, err)
		}
	}
	bad := map[string]string{
		`{}`:                     "n is required",
		`{"n":0}`:                "must be >",
		`{"n":"x"}`:              "must be a finite number",
		`{"n":1,"k":"c"}`:        "must be one of",
		`{"n":1,"list":[1]}`:     "at least 2",
		`{"n":1,"list":[1,"a"]}`: "number",
		`{"n":1,"extra":1}`:      "not a recognized",
		`{"n":1,"flag":"yes"}`:   "boolean",
		`{"n":1e999}`:            "valid JSON", // decode failure
	}
	for raw, frag := range bad {
		args, err := Decode([]byte(raw))
		if err == nil {
			err = ValidateSchema(s, map[string]any(args))
		}
		if err == nil || !strings.Contains(err.Error(), frag) {
			t.Errorf("%s: want error containing %q, got %v", raw, frag, err)
		}
	}
}

func TestRegistryInvariants(t *testing.T) {
	r := Default()
	if len(r.Names()) < 18 {
		t.Fatalf("expected the full Phase 1 tool set, got %v", r.Names())
	}
	for _, n := range r.Names() {
		tool, _ := r.Get(n)
		if tool.Capability == "" || tool.Description == "" || tool.Schema["type"] != "object" {
			t.Errorf("tool %s is missing capability/description/schema", n)
		}
		if tool.Schema["additionalProperties"] != false {
			t.Errorf("tool %s must reject unknown arguments", n)
		}
		if (tool.Plan == nil) == (tool.Query == nil) {
			t.Errorf("tool %s must be exactly one of mutating/query", n)
		}
	}
	for _, want := range []string{"create_box", "create_cylinder", "create_cone", "create_sphere", "create_torus", "add_hole", "extrude_profile", "create_sketch", "extrude_sketch", "linear_pattern", "circular_pattern",
		"boolean", "mirror", "fillet", "chamfer", "transform", "modify_parameter", "measure_distance", "get_model_summary", "export_step", "export_stl",
		"set_color", "set_opacity", "set_visibility", "reset_appearance", "get_appearance"} {
		if _, ok := r.Get(want); !ok {
			t.Errorf("missing required Phase 1 tool %s", want)
		}
	}
	if _, err := r.Invoke(nil, "rm_rf", nil); err == nil || !strings.Contains(err.Error(), "unknown tool") {
		t.Errorf("unknown tool must be refused: %v", err)
	}
}

// The Help page's tool reference (frontend/help.js) is written by hand, so nothing stops it
// drifting from the registry — a tool shipped without a Help entry is invisible to users who
// read the reference rather than the schemas. This pins the two together in both directions.
func TestHelpReferenceCoversEveryTool(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "frontend", "help.js"))
	if err != nil {
		t.Fatal(err)
	}
	// Entries look like: ['tool_name', 'edit'|'query', …]
	entry := regexp.MustCompile(`\['([a-z0-9_]+)',\s*'(edit|query)'`)
	documented := map[string]string{}
	for _, m := range entry.FindAllStringSubmatch(string(b), -1) {
		documented[m[1]] = m[2]
	}
	// Pre-existing gaps, from before this check existed. The list may shrink, never grow: a
	// new tool must come with its Help entry.
	undocumented := map[string]bool{
		"define_parameters": true, "bind_parameters": true, "set_parameter": true,
		"define_region": true, "lock_region": true, "unlock_region": true,
		"render_model": true, "get_visual_state": true, "calibrate_reference_image": true,
	}
	reg := Default()
	for _, name := range reg.Names() {
		kind, ok := documented[name]
		if !ok {
			if !undocumented[name] {
				t.Errorf("tool %q has no entry in frontend/help.js (the Help page's tool reference)", name)
			}
			continue
		}
		if undocumented[name] {
			t.Errorf("tool %q is documented now: remove it from the undocumented list", name)
		}
		tool, _ := reg.Get(name)
		want := "query"
		if tool.Mutating() {
			want = "edit"
		}
		if kind != want {
			t.Errorf("tool %q is listed as %q in frontend/help.js but is %q", name, kind, want)
		}
	}
	for name := range documented {
		if _, ok := reg.Get(name); !ok {
			t.Errorf("frontend/help.js documents %q, which is not a registered tool", name)
		}
	}
}
