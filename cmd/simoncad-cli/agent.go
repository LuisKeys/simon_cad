package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	geo "github.com/accelone/simoncad/internal/agent/capabilities/geometry"
	vis "github.com/accelone/simoncad/internal/agent/capabilities/vision"
	"github.com/accelone/simoncad/internal/agent/runtime"
	"github.com/accelone/simoncad/internal/app"
)

type agentFlags struct {
	preview, commit bool
	label           string
	recording       string
	record          string
	sel             multiFlag
}

// provider returns the LLM backend: a recorded run when --recording is given, otherwise
// the live Claude API (optionally capturing the run with --record).
func (f *agentFlags) provider() (runtime.Provider, func() error, error) {
	if f.recording != "" {
		b, err := os.ReadFile(f.recording)
		if err != nil {
			return nil, nil, err
		}
		var rec runtime.Recording
		if err := json.Unmarshal(b, &rec); err != nil {
			return nil, nil, fmt.Errorf("%s: %w", f.recording, err)
		}
		return &runtime.Replay{Rec: rec}, func() error { return nil }, nil
	}
	c, err := runtime.NewProviderFromEnv()
	if err != nil {
		return nil, nil, err
	}
	if f.record == "" {
		return c, func() error { return nil }, nil
	}
	rec := runtime.NewRecorder(c)
	return rec, func() error { return rec.Recording().Save(f.record) }, nil
}

func runAgentTurn(a *app.App, intent string, f *agentFlags, pol app.Policy, quiet bool) (*runtime.Result, error) {
	prov, finish, err := f.provider()
	if err != nil {
		return nil, err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	rt := &runtime.Runtime{Provider: prov, Caps: []runtime.Capability{geo.New(), vis.New()}}
	if !quiet && !jsonOut {
		rt.OnStep = func(name string, args json.RawMessage, o app.Outcome) {
			fmt.Printf("  → %s %s\n", name, strings.TrimSpace(string(args)))
			fmt.Print("    " + strings.ReplaceAll(strings.TrimRight(describeOutcome(o), "\n"), "\n", "\n    ") + "\n")
		}
	}
	res, err := rt.Run(ctx, a, intent, pol)
	if ferr := finish(); ferr != nil && err == nil {
		err = ferr
	}
	return res, err
}

func agentFlagSet(name string) (*agentFlags, func([]string) ([]string, error)) {
	f := &agentFlags{}
	fs := newFlags(name)
	fs.BoolVar(&f.preview, "preview", false, "propose ChangeSets without committing")
	fs.BoolVar(&f.commit, "commit", false, "commit every valid ChangeSet, even multi-step ones")
	fs.StringVar(&f.label, "label", "", "semantic checkpoint label (default: the intent)")
	fs.StringVar(&f.recording, "recording", "", "replay a recorded agent run instead of calling the API")
	fs.StringVar(&f.record, "record", "", "save the live agent run to this recording file")
	fs.Var(&f.sel, "select", "selection context selector (repeatable)")
	out := fs.String("out", "project.scadproj", "project path")
	force := fs.Bool("force", false, "overwrite an existing project (create)")
	return f, func(args []string) ([]string, error) {
		pos, err := parseInterspersed(fs, args)
		if name == "create" {
			pos = append([]string{*out, fmt.Sprint(*force)}, pos...)
		}
		return pos, err
	}
}

func finishTurn(a *app.App, path, intent string, f *agentFlags, res *runtime.Result, runErr error) error {
	var cpID string
	label := f.label
	if label == "" {
		label = intent
	}
	if cp := a.Checkpoint(label, false); cp != nil {
		cpID = cp.ID
	}
	if err := a.Save(path); err != nil {
		return err
	}
	if runErr != nil {
		return runErr
	}
	m, _ := a.Model()
	vol := 0.0
	if m != nil {
		vol = m.Volume()
	}
	var pending []string
	for _, cs := range a.Pending() {
		pending = append(pending, cs.ID)
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "\n%s\n\n", res.Final)
	fmt.Fprintf(&sb, "project: %s   tool calls: %d   committed: %v   pending approval: %v\n", path, res.ToolCalls, res.Committed, pending)
	fmt.Fprintf(&sb, "model volume: %.3f mm³", vol)
	if cpID != "" {
		fmt.Fprintf(&sb, "   checkpoint: %s", cpID)
	}
	for _, id := range pending {
		fmt.Fprintf(&sb, "\napprove with: simoncad-cli commit %s %s", path, id)
	}
	emit(map[string]any{
		"project": path, "final": res.Final, "tool_calls": res.ToolCalls, "committed": res.Committed,
		"pending": pending, "checkpoint": cpID, "volume_mm3": vol,
		"usage": map[string]int{"input_tokens": res.Usage.InputTokens, "output_tokens": res.Usage.OutputTokens},
	}, sb.String())
	return nil
}

func runCreate(args []string) error {
	f, parse := agentFlagSet("create")
	pos, err := parse(args)
	if err != nil {
		return err
	}
	if len(pos) != 3 {
		return usageError{`usage: create "<intent>" [--out proj.scadproj]`}
	}
	path, force, intent := pos[0], pos[1] == "true", pos[2]
	if _, err := os.Stat(path); err == nil && !force {
		return fmt.Errorf("%s already exists (use --force to overwrite, or `modify` to change it)", path)
	}
	pol, err := policyFrom(f.preview, f.commit, app.PolicyAuto)
	if err != nil {
		return err
	}
	name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	a := app.New(name, time.Now)
	defer a.Close()
	if err := ensureSelection(a, f.sel); err != nil {
		return err
	}
	res, runErr := runAgentTurn(a, intent, f, pol, false)
	return finishTurn(a, path, intent, f, res, runErr)
}

func runModify(args []string) error {
	f, parse := agentFlagSet("modify")
	pos, err := parse(args)
	if err != nil {
		return err
	}
	if len(pos) != 2 {
		return usageError{`usage: modify <project> "<intent>" [--select SEL]`}
	}
	pol, err := policyFrom(f.preview, f.commit, app.PolicyAuto)
	if err != nil {
		return err
	}
	a, err := openProject(pos[0])
	if err != nil {
		return err
	}
	defer a.Close()
	if err := ensureSelection(a, f.sel); err != nil {
		return err
	}
	res, runErr := runAgentTurn(a, pos[1], f, pol, false)
	return finishTurn(a, pos[0], pos[1], f, res, runErr)
}
