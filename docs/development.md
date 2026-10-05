# Development guide

## Build and test targets

| Command | Does |
|---|---|
| `make occt` | Build the pinned OCCT 7.8.1 into `third_party/occt-install` (idempotent) |
| `make native` | Build `libsimon_occ.a` and the C++ `bridge_test` |
| `make build` | `native`, then `out/simoncad-cli` |
| `make test` | `native`, run `bridge_test`, `go vet`, `go test ./...` |
| `make test-go` | Go checks only (no native rebuild) |
| `make leakcheck` | Valgrind over `bridge_test` (slow; 0 errors expected) |
| `make eval` | Recorded agent evals |
| `make demo` | M1 demo (`scripts/demo_m1.sh`) |
| `make docs` | Regenerate `docs/tools.md` from the live registry |

Notes:
- `go test ./...` skips `third_party/` because it contains its own `go.mod`.
- The Makefile sets `CGO_CFLAGS=-DSIMON_NATIVE_STAMP=<checksum of libsimon_occ.a>`. Go's build cache
  doesn't track native library changes; the stamp forces cgo packages to relink after a C++ change.
  If you run `go build` / `go test` by hand after editing C++, export the same variable.
- Run tests with `LD_LIBRARY_PATH=$PWD/third_party/occt-install/lib` if you invoke Go directly (the
  Makefile exports it).
- The race detector works: `go test -race ./internal/...`.

## Test layers

| Layer | Where | Verifies |
|---|---|---|
| C++ ABI | `native/occ/tests/bridge_test.cpp` | Volumes, validity, error paths, handle count returns to 0 |
| Kernel API | `geometry/api` | 10k create/boolean/release cycles: flat RSS, 0 live handles |
| CAD-IR, expressions, selectors | `cadir`, `expr`, `selectors` | Canonical bytes, hashing, dependency rules, parsing, bounds |
| Rebuild | `geometry/rebuild` | Real geometry volumes, patterns, failure chains, suppression, cache counters |
| Commands / ChangeSets | `commands` | Exact inverses, lifecycle, 1,000-sequence undo/redo property test |
| Tools | `tools`, `app` | Schema validation, actionable errors, policies, exports |
| Persistence | `app` | Byte-identical save/load/rebuild/save, replay, traceability |
| Performance | `app` | 50-feature incremental edit < 300 ms |
| Agent | `agent/runtime` | Wire formats (Claude, OpenAI), provider selection, text never mutates, only registered tools, recorded evals |
| Dogfood | `app/dogfood_test.go` | Servo bracket via tools equals the hand-built kernel part |

## Agent evals

Cases live in `testdata/agent/*.json`:

```json
{ "name": "02_add_four_holes",
  "setup":  [ {"tool": "create_box", "args": {…}} ],
  "selection": ["face(feature=plate,side=+Z)"],
  "prompt": "Add four 3.2 mm holes.",
  "recorded": { "source": "scripted", "responses": [ … ] },
  "expect": {
    "tool_calls": [ {"name": "add_hole", "args": {"diameter": 3.2, "positions": {"len": 4}}} ],
    "model": { "volume_mm3": 23871.3, "features": 2, "failed": 0 },
    "pending": 0 } }
```

- Only **successful** state-changing calls are compared, in order; read-only calls
  (`get_model_summary`, `measure_distance`) are ignored unless listed. A call that failed validation
  and was retried does not count.
- Argument matching is a subset: extra arguments the model supplies are fine.
- Matchers: numbers (1e-6 relative), strings, booleans, arrays (ordered), objects (subset), and the
  special forms `{"approx":v,"tol":t}`, `{"any":true}`, `{"oneof":[…]}`, `{"contains":"text"}`,
  `{"len":n}`, `{"set":[…]}`.
- Model facts: `volume_mm3`, `features`, `bodies`, `failed`.

**Recorded mode** (default; CI) replays `recorded.responses`. The current recordings are
**hand-scripted**, so they test the plumbing and matchers, not model behaviour.

**Live mode** needs a provider configured in `.env`:
```bash
./out/simoncad-cli eval --live                       # pass rate against a real model
./out/simoncad-cli eval --live --record /tmp/rec     # also capture each case's real run
```
Copy captured `*.recording.json` content into the cases' `recorded` field to replace scripted ones.

## Adding a feature type

1. Native op needed? Add it to `occ_bridge.h` + a `.cpp`, a function in `geometry/bridge`, a method in
   `geometry/api`, and a C++ test. Keep OCCT types out of everything else.
2. Add a builder in `geometry/features` and `register` it. Return a **new** shape; never mutate
   inputs; release intermediates (use `arena`). Provide `Target` if it modifies a body (enables
   suppression pass-through) and `Tool` if patterns should be able to repeat it.
3. Add a rebuild test with a closed-form expected volume.

## Adding a tool

1. Define it in `internal/tools` with `Name`, `Description` (written for the model), `Capability`, an
   `object(...)` schema (unknown arguments are rejected), and exactly one of `Plan` (returns
   commands) or `Query`.
2. Put semantic checks in `Plan` and make error messages say what to change (ranges, available ids).
3. Mutating tools must **only** return commands; never touch the graph.
4. Add the feature's editable parameters to `knownParams` (`edit.go`) so `modify_parameter` accepts
   them.
5. Run `make docs` to regenerate `docs/tools.md`, and add an eval case.

## Adding a capability

Implement `runtime.Capability` (`Name`, `SystemPrompt`, `ToolSubset`, `BuildContext`), tag its tools
with the capability name, and pass it to `runtime.Runtime{Caps: …}`. Capabilities share the runtime,
provider and tool registry; they must not add new mutation paths.

## Conventions

- Millimetres and degrees at the tool boundary; radians only inside the bridge calls.
- Feature ids: `^[a-z][a-z0-9_]{0,47}$`.
- Go formatting with `gofmt`; `go vet` must be clean.
- Use the PR template's seven-question checklist.
- Robot-driven priorities: don't add conventional CAD features without a robot requirement; log gaps
  in [robot-backlog.md](robot-backlog.md).
