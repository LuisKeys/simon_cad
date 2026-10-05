package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/accelone/simoncad/internal/app"
)

// Case maps a prompt (plus optional setup and selection) to expected tool calls and
// resulting model facts. Recorded mode replays `recorded`; live mode uses a real provider.
type Case struct {
	Name      string      `json:"name"`
	Setup     []SetupCall `json:"setup,omitempty"`
	Selection []string    `json:"selection,omitempty"`
	Prompt    string      `json:"prompt"`
	Recorded  Recording   `json:"recorded"`
	Expect    Expectation `json:"expect"`
	Notes     string      `json:"notes,omitempty"`
}

type SetupCall struct {
	Tool string          `json:"tool"`
	Args json.RawMessage `json:"args"`
}

type Expectation struct {
	// ToolCalls are the successful, state-changing calls the agent must make, in order.
	// Read-only calls (get_model_summary, measure_distance) are ignored unless listed.
	ToolCalls []ExpectedCall `json:"tool_calls"`
	Model     map[string]any `json:"model,omitempty"`
	// Pending is the expected number of ChangeSets left awaiting approval (default 0).
	Pending int `json:"pending,omitempty"`
}

type ExpectedCall struct {
	Name string         `json:"name"`
	Args map[string]any `json:"args,omitempty"`
}

type CaseReport struct {
	Name     string
	Pass     bool
	Failures []string
	Final    string
	Calls    int
	Tokens   Usage
}

// LoadCases reads every *.json case in dir, sorted by name.
func LoadCases(dir string) ([]Case, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	var out []Case
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		var c Case
		if err := json.Unmarshal(b, &c); err != nil {
			return nil, fmt.Errorf("%s: %w", f, err)
		}
		out = append(out, c)
	}
	return out, nil
}

var queryTools = map[string]bool{"get_model_summary": true, "measure_distance": true}

// RunCase executes one case in a fresh in-memory project. Pass a nil provider for recorded mode.
func RunCase(ctx context.Context, c Case, caps []Capability, live Provider) CaseReport {
	rep := CaseReport{Name: c.Name}
	fail := func(f string, a ...any) { rep.Failures = append(rep.Failures, fmt.Sprintf(f, a...)) }
	fixed := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	a := app.New("eval", func() time.Time { return fixed })
	defer a.Close()

	for _, s := range c.Setup {
		if o := a.ApplyTool(s.Tool, s.Args, app.PolicyCommit); !o.OK {
			fail("setup %s failed: %s", s.Tool, o.Error)
			return rep
		}
	}
	if err := a.SetSelection(c.Selection); err != nil {
		fail("selection: %v", err)
		return rep
	}
	var p Provider = live
	if p == nil {
		p = &Replay{Rec: c.Recorded}
	}
	rt := &Runtime{Provider: p, Caps: caps}
	res, err := rt.Run(ctx, a, c.Prompt, app.PolicyAuto)
	if err != nil {
		fail("run: %v", err)
		return rep
	}
	rep.Final, rep.Calls, rep.Tokens = res.Final, res.ToolCalls, res.Usage

	// Collect successful calls from the transcript.
	var actual []ExpectedCall
	failedIDs := map[string]bool{}
	for _, m := range res.Transcript {
		for _, b := range m.Content {
			if b.Type == "tool_result" && b.IsError {
				failedIDs[b.ToolUseID] = true
			}
		}
	}
	listed := map[string]bool{}
	for _, e := range c.Expect.ToolCalls {
		listed[e.Name] = true
	}
	for _, m := range res.Transcript {
		for _, b := range m.Content {
			if b.Type != "tool_use" || failedIDs[b.ID] || (queryTools[b.Name] && !listed[b.Name]) {
				continue
			}
			var args map[string]any
			_ = json.Unmarshal(b.Input, &args)
			actual = append(actual, ExpectedCall{Name: b.Name, Args: args})
		}
	}
	if len(actual) != len(c.Expect.ToolCalls) {
		fail("expected %d tool call(s), got %d: %s", len(c.Expect.ToolCalls), len(actual), names(actual))
	} else {
		for i, e := range c.Expect.ToolCalls {
			if actual[i].Name != e.Name {
				fail("call %d: expected tool %s, got %s", i+1, e.Name, actual[i].Name)
				continue
			}
			for k, ev := range e.Args {
				av, ok := actual[i].Args[k]
				if !ok {
					fail("call %d (%s): missing argument %q", i+1, e.Name, k)
				} else if err := Match(ev, av); err != nil {
					fail("call %d (%s).%s: %v", i+1, e.Name, k, err)
				}
			}
		}
	}
	if got := len(a.Pending()); got != c.Expect.Pending {
		fail("expected %d pending ChangeSet(s), got %d", c.Expect.Pending, got)
	}
	if len(c.Expect.Model) > 0 {
		m, err := a.Model()
		if err != nil {
			fail("model: %v", err)
		} else {
			facts := map[string]any{"volume_mm3": m.Volume(), "features": float64(len(a.Graph.Nodes)), "bodies": float64(len(m.Leaves)), "failed": float64(len(m.Failed()))}
			for k, ev := range c.Expect.Model {
				av, ok := facts[k]
				if !ok {
					fail("unknown model fact %q", k)
				} else if err := Match(ev, av); err != nil {
					fail("model.%s: %v", k, err)
				}
			}
		}
	}
	rep.Pass = len(rep.Failures) == 0
	return rep
}

func names(cs []ExpectedCall) string {
	var n []string
	for _, c := range cs {
		n = append(n, c.Name)
	}
	return "[" + strings.Join(n, ", ") + "]"
}

// Match is the tolerant matcher. Numbers compare with 1e-6 relative tolerance; objects
// match as subsets; special forms: {"approx":v,"tol":t}, {"any":true}, {"oneof":[...]},
// {"contains":"text"}, {"len":n}, {"set":[...]} (order-insensitive array).
func Match(exp, act any) error {
	switch e := exp.(type) {
	case float64:
		a, ok := act.(float64)
		if !ok {
			return fmt.Errorf("expected number %g, got %v", e, act)
		}
		if math.Abs(a-e) > 1e-6*math.Max(1, math.Abs(e)) {
			return fmt.Errorf("expected %g, got %g", e, a)
		}
		return nil
	case string:
		if a, ok := act.(string); !ok || a != e {
			return fmt.Errorf("expected %q, got %v", e, act)
		}
		return nil
	case bool:
		if a, ok := act.(bool); !ok || a != e {
			return fmt.Errorf("expected %v, got %v", e, act)
		}
		return nil
	case []any:
		a, ok := act.([]any)
		if !ok || len(a) != len(e) {
			return fmt.Errorf("expected array of %d, got %v", len(e), act)
		}
		for i := range e {
			if err := Match(e[i], a[i]); err != nil {
				return fmt.Errorf("[%d]: %w", i, err)
			}
		}
		return nil
	case map[string]any:
		if v, ok := e["approx"]; ok {
			want, _ := v.(float64)
			tol, _ := e["tol"].(float64)
			if tol == 0 {
				tol = 0.01 * math.Max(1, math.Abs(want))
			}
			a, ok := act.(float64)
			if !ok || math.Abs(a-want) > tol {
				return fmt.Errorf("expected ≈%g (±%g), got %v", want, tol, act)
			}
			return nil
		}
		if _, ok := e["any"]; ok {
			return nil
		}
		if alts, ok := e["oneof"].([]any); ok {
			for _, alt := range alts {
				if Match(alt, act) == nil {
					return nil
				}
			}
			return fmt.Errorf("none of %v matched %v", alts, act)
		}
		if sub, ok := e["contains"].(string); ok {
			if s, ok := act.(string); !ok || !strings.Contains(strings.ToLower(s), strings.ToLower(sub)) {
				return fmt.Errorf("expected text containing %q, got %v", sub, act)
			}
			return nil
		}
		if n, ok := e["len"].(float64); ok {
			if a, ok := act.([]any); !ok || float64(len(a)) != n {
				return fmt.Errorf("expected %g items, got %v", n, act)
			}
			return nil
		}
		if set, ok := e["set"].([]any); ok {
			a, ok := act.([]any)
			if !ok || len(a) != len(set) {
				return fmt.Errorf("expected set of %d, got %v", len(set), act)
			}
			used := make([]bool, len(a))
			for _, want := range set {
				found := false
				for i, got := range a {
					if !used[i] && Match(want, got) == nil {
						used[i], found = true, true
						break
					}
				}
				if !found {
					return fmt.Errorf("no match for %v in %v", want, act)
				}
			}
			return nil
		}
		a, ok := act.(map[string]any)
		if !ok {
			return fmt.Errorf("expected object, got %v", act)
		}
		for k, v := range e {
			av, has := a[k]
			if !has {
				return fmt.Errorf("missing key %q", k)
			}
			if err := Match(v, av); err != nil {
				return fmt.Errorf("%s: %w", k, err)
			}
		}
		return nil
	}
	return fmt.Errorf("unsupported matcher %T", exp)
}

// Suite summarises a run over many cases.
type Suite struct {
	Reports []CaseReport
}

func (s Suite) Passed() int {
	n := 0
	for _, r := range s.Reports {
		if r.Pass {
			n++
		}
	}
	return n
}

func (s Suite) PassRate() float64 {
	if len(s.Reports) == 0 {
		return 0
	}
	return float64(s.Passed()) / float64(len(s.Reports))
}
