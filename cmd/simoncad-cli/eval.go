package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	geo "github.com/accelone/simoncad/internal/agent/capabilities/geometry"
	"github.com/accelone/simoncad/internal/agent/runtime"
)

func runEval(args []string) error {
	fs := newFlags("eval")
	dir := fs.String("dir", "testdata/agent", "directory of eval cases")
	live := fs.Bool("live", false, "call the real model instead of replaying recordings")
	record := fs.String("record", "", "with --live: save each case's captured run into this directory")
	if _, err := parseInterspersed(fs, args); err != nil {
		return err
	}
	cases, err := runtime.LoadCases(*dir)
	if err != nil {
		return err
	}
	if len(cases) == 0 {
		return fmt.Errorf("no eval cases in %s", *dir)
	}
	caps := []runtime.Capability{geo.New()}
	var liveProv runtime.Provider
	if *live {
		if liveProv, err = runtime.NewProviderFromEnv(); err != nil {
			return err
		}
		if *record != "" {
			if err := os.MkdirAll(*record, 0o755); err != nil {
				return err
			}
		}
	}
	var suite runtime.Suite
	var sb strings.Builder
	var rows []map[string]any
	for _, c := range cases {
		var p runtime.Provider
		var rec *runtime.Recorder
		if *live {
			rec = runtime.NewRecorder(liveProv)
			p = rec
		}
		rep := runtime.RunCase(context.Background(), c, caps, p)
		suite.Reports = append(suite.Reports, rep)
		mark := "PASS"
		if !rep.Pass {
			mark = "FAIL"
		}
		fmt.Fprintf(&sb, "%s  %s\n", mark, rep.Name)
		for _, f := range rep.Failures {
			fmt.Fprintf(&sb, "      %s\n", f)
		}
		rows = append(rows, map[string]any{"name": rep.Name, "pass": rep.Pass, "failures": rep.Failures, "tool_calls": rep.Calls,
			"input_tokens": rep.Tokens.InputTokens, "output_tokens": rep.Tokens.OutputTokens})
		if rec != nil && *record != "" {
			c.Recorded = rec.Recording()
			_ = c.Recorded.Save(filepath.Join(*record, c.Name+".recording.json"))
		}
	}
	mode := "recorded"
	if *live {
		mode = "live (" + liveProv.Name() + ")"
	}
	fmt.Fprintf(&sb, "\n%s eval: %d/%d passed (%.0f%%)", mode, suite.Passed(), len(suite.Reports), suite.PassRate()*100)
	emit(map[string]any{"mode": mode, "passed": suite.Passed(), "total": len(suite.Reports), "pass_rate": suite.PassRate(), "cases": rows}, sb.String())
	if suite.Passed() != len(suite.Reports) {
		return fmt.Errorf("%d eval case(s) failed", len(suite.Reports)-suite.Passed())
	}
	return nil
}
