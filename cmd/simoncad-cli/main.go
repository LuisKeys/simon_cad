// simoncad-cli is the first-class headless interface to Simon CAD. Every command can emit
// machine-readable JSON (--json) for coding agents, CI/CD and robotics pipelines.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"github.com/accelone/simoncad/internal/dotenv"
	"os"
	"strings"
)

var jsonOut bool

type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

// parseInterspersed parses flags that may appear before or after positional arguments.
func parseInterspersed(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		if fs.NArg() == 0 {
			return pos, nil
		}
		pos = append(pos, fs.Arg(0))
		args = fs.Args()[1:]
	}
}

func newFlags(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.BoolVar(&jsonOut, "json", false, "machine-readable JSON output")
	return fs
}

// emit prints v as JSON in --json mode, otherwise the human text.
func emit(v map[string]any, human string) {
	if jsonOut {
		v["ok"] = true
		b, _ := json.MarshalIndent(v, "", "  ")
		fmt.Println(string(b))
		return
	}
	fmt.Println(strings.TrimRight(human, "\n"))
}

type usageError struct{ msg string }

func (e usageError) Error() string { return e.msg }

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	dotenv.Load(".env")
	cmds := map[string]func([]string) error{
		"bracket": runBracket, "create": runCreate, "modify": runModify, "tool": runTool,
		"build": runBuild, "export": runExport, "history": runHistory, "restore": runRestore,
		"replay": runReplay, "commit": runCommit, "reject": runReject, "undo": runUndo,
		"eval": runEval, "tools": runTools,
	}
	run, ok := cmds[os.Args[1]]
	if !ok {
		usage()
		os.Exit(2)
	}
	err := run(os.Args[2:])
	if err == nil {
		return
	}
	code := 1
	if _, isUsage := err.(usageError); isUsage {
		code = 2
	}
	if err == flag.ErrHelp {
		os.Exit(0)
	}
	if jsonOut {
		b, _ := json.Marshal(map[string]any{"ok": false, "error": err.Error()})
		fmt.Println(string(b))
	} else {
		fmt.Fprintln(os.Stderr, "error:", err)
	}
	os.Exit(code)
}

func usage() {
	fmt.Fprint(os.Stderr, `usage: simoncad-cli <command> [args]

Agent commands (provider from SIMONCAD_PROVIDER=claude|openai|ollama in .env, or --recording FILE to replay):
  create "<intent>" [--out proj.scadproj]            create a new project from natural language
  modify proj.scadproj "<intent>" [--select SEL]...  change a project from natural language
                                                     [--preview | --commit] [--label TEXT]
Deterministic commands:
  tool proj.scadproj <tool> '<json>'                 invoke a tool directly [--preview | --commit]
  tools [--json]                                     list tools and their JSON schemas
  build proj.scadproj                                rebuild and report feature status
  export proj.scadproj --format step|stl [--out F]
  history proj.scadproj [--compare A,B]              checkpoints, ChangeSets and pending approvals
  restore proj.scadproj <checkpoint>                 return to a checkpoint (undoable)
  commit|reject proj.scadproj <changeset>            approve or discard a proposed ChangeSet
  undo proj.scadproj                                 undo the last committed ChangeSet
  replay proj.scadproj [--events]                    re-derive the model from recorded ChangeSets
Evaluation:
  eval [--dir testdata/agent] [--live] [--record DIR]
Other:
  bracket [--out DIR]                                Phase 0 servo bracket proof

Every command accepts --json.
`)
}
