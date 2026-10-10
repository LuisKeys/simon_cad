package runtime_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/accelone/simoncad/internal/agent/runtime"
	"github.com/accelone/simoncad/internal/app"
)

// scripted answers each request from a fixed list and records the tools and system prompt it saw.
type scripted struct {
	replies []runtime.Response
	tools   [][]string
	system  []string
	results []string // tool_result contents of the last user message, per request
}

func (*scripted) Name() string { return "scripted" }

func (s *scripted) Complete(_ context.Context, req runtime.Request) (*runtime.Response, error) {
	var names []string
	for _, t := range req.Tools {
		names = append(names, t.Name)
	}
	s.tools = append(s.tools, names)
	s.system = append(s.system, req.System)
	last := req.Messages[len(req.Messages)-1]
	var res []string
	for _, b := range last.Content {
		if b.Type == "tool_result" {
			res = append(res, b.Content)
		}
	}
	s.results = append(s.results, strings.Join(res, "\n"))
	r := s.replies[len(s.tools)-1]
	return &r, nil
}

func call(id, name, input string) runtime.Response {
	return runtime.Response{Content: []runtime.Block{{Type: "tool_use", ID: id, Name: name, Input: json.RawMessage(input)}}}
}

func text(s string) runtime.Response {
	return runtime.Response{Content: []runtime.Block{{Type: "text", Text: s}}}
}

func has(names []string, n string) bool {
	for _, x := range names {
		if x == n {
			return true
		}
	}
	return false
}

func runScripted(t *testing.T, mode string, replies ...runtime.Response) (*scripted, *runtime.Result) {
	t.Helper()
	a := app.New("t", time.Now)
	t.Cleanup(a.Close)
	p := &scripted{replies: replies}
	rt := &runtime.Runtime{Provider: p, Caps: caps(), ToolLoading: mode}
	res, err := rt.Run(context.Background(), a, "show settings", app.PolicyAuto)
	if err != nil {
		t.Fatal(err)
	}
	return p, res
}

func TestProgressiveLoadingStartsWithCoreToolsOnly(t *testing.T) {
	p, _ := runScripted(t, runtime.ToolLoadingProgressive, text("ok"))
	tl := p.tools[0]
	if !has(tl, "create_box") || !has(tl, "load_tool_category") || has(tl, "get_workspace_settings") {
		t.Fatalf("tools = %v", tl)
	}
	if !strings.Contains(p.system[0], "## Tool categories") || !strings.Contains(p.system[0], "get_workspace_settings") {
		t.Fatalf("system prompt lacks the category index:\n%s", p.system[0])
	}
}

func TestLoadToolCategoryMakesToolsAvailable(t *testing.T) {
	p, res := runScripted(t, runtime.ToolLoadingProgressive,
		call("1", "load_tool_category", `{"categories":["workspace"]}`),
		call("2", "get_workspace_settings", `{}`),
		text("done"))
	if has(p.tools[0], "get_workspace_settings") || !has(p.tools[1], "get_workspace_settings") {
		t.Fatalf("workspace tools must appear only after loading: %v / %v", p.tools[0], p.tools[1])
	}
	if len(p.tools[1]) <= len(p.tools[0]) || !has(p.tools[2], "get_workspace_settings") {
		t.Fatal("loaded tools must stay loaded")
	}
	if !strings.Contains(p.results[1], `"ok":true`) || !strings.Contains(p.results[2], `"ok":true`) {
		t.Fatalf("results: %q / %q", p.results[1], p.results[2])
	}
	if res.ToolCalls != 1 {
		t.Fatalf("ToolCalls = %d: loading a category is not a model tool call", res.ToolCalls)
	}
}

func TestCallingAnUnloadedToolLoadsItsCategory(t *testing.T) {
	p, _ := runScripted(t, runtime.ToolLoadingProgressive,
		call("1", "get_workspace_settings", `{}`),
		call("2", "get_workspace_settings", `{}`),
		text("done"))
	if !strings.Contains(p.results[1], "not loaded") || !has(p.tools[1], "get_workspace_settings") {
		t.Fatalf("refusal %q; tools %v", p.results[1], p.tools[1])
	}
	if !strings.Contains(p.results[2], `"ok":true`) {
		t.Fatalf("retry result %q", p.results[2])
	}
}

func TestLoadingAnUnknownCategoryIsReported(t *testing.T) {
	p, _ := runScripted(t, runtime.ToolLoadingProgressive,
		call("1", "load_tool_category", `{"categories":["nope"]}`), text("done"))
	if !strings.Contains(p.results[1], `unknown category`) || !strings.Contains(p.results[1], "workspace") {
		t.Fatalf("result %q", p.results[1])
	}
}

func TestAllModeSendsEveryToolAndNoMetaTool(t *testing.T) {
	p, _ := runScripted(t, runtime.ToolLoadingAll, text("ok"))
	if !has(p.tools[0], "get_workspace_settings") || has(p.tools[0], "load_tool_category") ||
		strings.Contains(p.system[0], "## Tool categories") {
		t.Fatalf("tools = %v", p.tools[0])
	}
	q, _ := runScripted(t, "", text("ok")) // default
	if len(q.tools[0]) != len(p.tools[0]) {
		t.Fatal("the default must send every tool")
	}
}

func TestProgressiveLoadingShrinksTheToolset(t *testing.T) {
	all, _ := runScripted(t, runtime.ToolLoadingAll, text("ok"))
	prog, _ := runScripted(t, runtime.ToolLoadingProgressive, text("ok"))
	t.Logf("tools sent: all=%d progressive=%d", len(all.tools[0]), len(prog.tools[0]))
	if len(prog.tools[0])*2 > len(all.tools[0]) {
		t.Fatalf("progressive sends %d of %d tools", len(prog.tools[0]), len(all.tools[0]))
	}
}
