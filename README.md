# Simon CAD

Simon CAD is an **agentic-first** CAD environment for robotics, mechatronics and 3D-printable
mechanical design. Engineering intent, expressed in natural language and (from Phase 2) visual
selection, is turned by an agent into **deterministic, validated, replayable operations** on a
semantic CAD intermediate representation (the *CAD-IR*). Geometry is built by OpenCascade (OCCT)
behind a small C++ bridge driven from Go.

It is not a conventional CAD system with an assistant bolted on: the CLI, the agent and (later) the
UI all go through the same **tool → command → ChangeSet** path. There is no separate "AI mutation
path" and no UI-only mutation path.

```
Engineering intent (natural language + selection)
  → Simon CAD Agent (capabilities)
  → structured tool call (JSON) → schema + semantic validation
  → Commands → proposed ChangeSet → validation → commit → semantic checkpoint
  → CAD-IR (authoritative) → rebuild → OCCT B-Rep (derived) → STEP / STL
```

## Status

| Phase | Milestone | State |
|---|---|---|
| 0 | M0 Geometry proof (Go → C ABI → OCCT → STEP/STL, leak-free) | Done |
| 1 | M1 Agentic headless CAD engine | Implemented and verified offline (recorded evals). **Live-LLM runs are not yet verified** |
| 2 | M2 Visual agentic CAD (viewport, selection UI, preview) | Not started |
| 3–7 | Robotics-aware CAD, analysis, generative design, assemblies | Not started |

What exists today: a headless engine driven by `simoncad-cli`. There is no desktop UI yet.

## Quick start

### Prerequisites
- Linux with `g++` (C++17), `cmake`, `curl`, `make`
- Go 1.23+ (the repo was built with Go 1.27.1 placed at `third_party/go`; see below)
- Optional: `valgrind` for `make leakcheck`

### One-time setup
```bash
# 1. Go toolchain. The Makefile expects it at third_party/go (extract a tarball from https://go.dev/dl
#    so that third_party/go/bin/go exists), or override with: make GO=$(which go) ...
mkdir -p third_party
# 2. Build the pinned OCCT 7.8.1 into third_party/occt-install (takes a while, no sudo)
make occt
# 3. Environment (optional; only needed for the live agent)
cp .env.example .env     # then edit .env
```

### Build and test
```bash
make build        # builds the C++ bridge, then out/simoncad-cli
make test         # C++ bridge test + go vet + go test ./... (incl. recorded agent evals)
make leakcheck    # valgrind over the C++ ABI test
make eval         # recorded agent evals only
make demo         # M1 demo (replays a recording if no API key is configured)
```

> The Makefile stamps the checksum of `libsimon_occ.a` into `CGO_CFLAGS`. Go's build cache does not
> notice native library changes, so without this a rebuilt C++ layer could silently be ignored.

### Try it
```bash
CLI=./out/simoncad-cli

# A project is created by `create`. Replaying a recording needs no API key:
$CLI create "Create an 80 x 60 x 4 mm plate" --out demo.scadproj \
     --recording testdata/demo/m1_create.recording.json          # offline replay
# From here on every command is deterministic (no LLM):
$CLI tool demo.scadproj add_hole '{"target":"plate","diameter":3.2,"positions":[[6,6],[74,6],[6,54],[74,54]]}'
$CLI tool demo.scadproj modify_parameter '{"feature":"plate","parameter":"dz","value":5}'
$CLI build demo.scadproj
$CLI export demo.scadproj --format step --out demo.step
$CLI history demo.scadproj
$CLI undo demo.scadproj
$CLI replay demo.scadproj
```

With a live model (set `SIMONCAD_PROVIDER` and the matching key in `.env`):
```bash
$CLI create "Create an 80 x 60 x 4 mm plate" --out demo.scadproj
$CLI modify demo.scadproj "Add four 3.2 mm holes near the corners"
$CLI modify demo.scadproj "Put a 5 mm hole here" --select 'face(feature=plate,side=+Z)'
$CLI modify demo.scadproj "Change the thickness to 5 mm" --preview     # propose only
$CLI commit demo.scadproj cs_0004                                       # approve it
```

## Configuration (`.env`)

The CLI loads `.env` from the current directory at startup. Real environment variables win; empty
values are ignored. `.env` is git-ignored; `.env.example` documents everything.

| Variable | Meaning |
|---|---|
| `SIMONCAD_PROVIDER` | `claude` (default), `openai` or `ollama` |
| `ANTHROPIC_API_KEY`, `ANTHROPIC_MODEL`, `ANTHROPIC_BASE_URL` | Claude provider (model defaults to `claude-sonnet-5-5`) |
| `OPENAI_API_KEY`, `OPENAI_MODEL`, `OPENAI_BASE_URL` | OpenAI provider |
| `OLLAMA_MODEL`, `OLLAMA_BASE_URL` | Local Ollama (no key; default URL `http://localhost:11434`) |

## CLI reference

Every command accepts `--json` for machine-readable output (`{"ok": …}`; errors become
`{"ok": false, "error": …}` with a non-zero exit code).

| Command | Purpose |
|---|---|
| `create "<intent>" [--out P] [--force]` | New project from natural language |
| `modify P "<intent>" [--select SEL]…` | Change a project from natural language |
| `tool P <tool> '<json>'` | Invoke one tool directly and deterministically |
| `tools` | List tools and their JSON schemas |
| `build P` | Rebuild; report per-feature status, bodies, cache stats |
| `export P --format step\|stl [--out F]` | Export, with STEP re-import and STL watertight checks |
| `history P [--compare A,B]` | Checkpoints, ChangeSets, pending approvals, semantic diff |
| `restore P <checkpoint>` | Return to a checkpoint (itself undoable) |
| `commit P <cs>` / `reject P <cs>` | Approve or discard a proposed ChangeSet |
| `undo P` | Undo the last committed ChangeSet |
| `replay P [--events]` | Re-derive the model from recorded ChangeSets and verify it matches |
| `eval [--dir D] [--live] [--record DIR]` | Run agent evals (recorded by default) |
| `bracket [--out DIR]` | Phase 0 servo bracket proof |

Agent commands (`create`, `modify`) and `tool` accept `--preview` (propose only) and `--commit`
(commit even multi-command ChangeSets), plus `--label` for the checkpoint name. Without either flag
the commit policy is *auto*: a single non-destructive command commits immediately; anything larger
stays **proposed** until you `commit` it.

## Repository layout

```
cmd/simoncad-cli/      CLI (the first-class headless interface)
internal/
  app/                 composition root: the only place that wires everything together
  cadir/               CAD-IR schema, graph, canonical serialization
  expr/                safe expression evaluator
  selectors/           semantic selectors (face(feature=…,side=…))
  selection/           selection context for the agent
  geometry/
    api/               Go geometry API (single kernel worker thread)
    bridge/            cgo bindings — the only package importing "C"
    features/          feature builders (box, hole, extrude, patterns, …)
    rebuild/           DAG evaluation, incremental cache, failure propagation
  commands/            commands, inverses, ChangeSet lifecycle, undo/redo
  events/              event bus and append-only recorder
  history/             semantic checkpoints, compare
  tools/               tool registry, JSON-schema validation, all tools
  persistence/         .scadproj read/write
  agent/runtime/       providers, tool-call loop, recordings, eval harness
  agent/capabilities/  pluggable capabilities (geometry so far)
  export/              STL watertightness checker
native/occ/            C ABI (occ_bridge.h) and C++ adapters over OCCT
testdata/              agent evals, demo recordings, robot dogfood projects
docs/                  architecture and reference documentation
scripts/               OCCT build, M1 demo
third_party/           (git-ignored) local Go toolchain and OCCT build
```

## Documentation

- [docs/architecture.md](docs/architecture.md) — principles, layers, data flow, key decisions
- [docs/modules.md](docs/modules.md) — every package and component in detail
- [docs/tools.md](docs/tools.md) — the tool reference (what the agent can call)
- [docs/project-format.md](docs/project-format.md) — the `.scadproj` on-disk format
- [docs/development.md](docs/development.md) — testing, evals, adding features and tools
- [docs/robot-backlog.md](docs/robot-backlog.md) — gaps found while dogfooding real robot parts

## Known limitations (Phase 1)

- Selectors resolve from bounding boxes: exact for prismatic parts, not for faces of filleted or
  pocketed bodies. Edge/vertex selectors parse but are not resolvable yet.
- `fillet` / `chamfer` apply to every edge of a body.
- Holes start only from axis-aligned planar faces.
- No UI, no streaming responses, no robotics catalog or macros yet.
- Agent evals in `testdata/agent` use hand-scripted recordings; live pass rates are unmeasured.
- API keys come from the environment/`.env`, not an OS keychain.
