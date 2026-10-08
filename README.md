# Simon CAD

Simon CAD is an **agentic-first** CAD environment for robotics, mechatronics and 3D-printable
mechanical design. Engineering intent, expressed in natural language and visual
selection, is turned by an agent into **deterministic, validated, replayable operations** on a
semantic CAD intermediate representation (the *CAD-IR*). Geometry is built by OpenCascade (OCCT)
behind a small C++ bridge driven from Go.

It is not a conventional CAD system with an assistant bolted on: the CLI, the agent and the
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
| 2 | M2 Visual agentic CAD (viewport, selection UI, preview) | Implemented; verified headless and in a Chrome end-to-end run. **Wails shell, 60 fps budget and live-LLM use not verified** (see [docs/ui.md](docs/ui.md)) |
| 3–7 | Robotics-aware CAD, analysis, generative design, assemblies | Not started |

What exists today: a headless engine driven by `simoncad-cli`, and a visual front end (`simoncad`: Three.js viewport, selection-aware conversation, ChangeSet preview, design tree, palette, semantic timeline) over the same services.

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
| `SIMONCAD_PROVIDER` | Primary model provider: `claude` (default), `openai` or `ollama` |
| `ANTHROPIC_API_KEY`, `ANTHROPIC_MODEL`, `ANTHROPIC_BASE_URL` | Claude provider (model defaults to `claude-sonnet-5-5`) |
| `OPENAI_API_KEY`, `OPENAI_MODEL`, `OPENAI_BASE_URL` | OpenAI provider |
| `OLLAMA_MODEL`, `OLLAMA_BASE_URL` | Local Ollama (no key; default URL `http://localhost:11434`) |
| `OLLAMA_NUM_CTX` | Context window asked of Ollama (default `32768`; `0` = the server's setting). Ollama silently drops what does not fit; a truncated prompt is reported as an error |
| `OLLAMA_VISION` | `true`/`false` overrides whether the Ollama model accepts images (default: what the Ollama server reports, else detected from the model name) |
| `SIMONCAD_AUTONOMOUS` | `off` disables the plan → execute → validate → correct workflow in the UI (default on) |
| `SIMONCAD_FALLBACK_ENABLED`, `SIMONCAD_FALLBACK_PROVIDER` | Fallback model (`openai` or `claude`); unset, migrated from the provider configuration above |
| `SIMONCAD_FALLBACK_<PROVIDER VARIABLE>` | The fallback's own configuration, e.g. `SIMONCAD_FALLBACK_OPENAI_MODEL`; unset values inherit the provider's |
| `SIMONCAD_FALLBACK_CONSENT` | `ask` (default), `allow` or `deny`: may a local primary's request be sent to the cloud fallback |
| `ANTHROPIC_INPUT_PRICE`, `ANTHROPIC_CACHED_INPUT_PRICE`, `ANTHROPIC_OUTPUT_PRICE` | Optional cost per million tokens; same pattern for `OPENAI_*` (local Ollama models are never priced) |
| `SIMONCAD_CURRENCY` | Default label for the price amounts (default `USD`; `OPENAI_CURRENCY`/`ANTHROPIC_CURRENCY` per model); it performs no conversion |

**Primary and fallback models.** The primary model serves every request it can. The fallback
model, a cloud model (OpenAI or Anthropic), serves what the primary cannot: attachments it cannot
read (an image for a text-only Ollama model, a PDF), requests whose primary model became
unavailable after its own retries — at most one hand-over per request, never back — and, with a
local primary, every request that carries an attached image or document, or whose prompt asks for
it ("use the frontier/fallback model", "usa el modelo de frontera"; also with a cloud primary).
Without a usable fallback those requests stay on the primary. With the
fallback disabled, such requests are refused with a reason; nothing is rerouted silently. With a
local primary, SimonCAD asks before the first request it would send to the cloud (the answer can
be remembered; Settings › Fallback Model › Remote data changes it). Every fallback is named in the
conversation and logged with the model requested, the model used, the reason, the tokens and the
estimated cost; the Simon Agent header shows the model serving the request.

**Token consumption and cost.** Every model call is metered: input and output tokens are counted
per model, per turn and per session, and reported in the agent panel (session meter above the
composer, a note under each answer) and by the CLI (`usage:` line, `usage`/`cost` in `--json`).
Prices are optional and live next to each cloud model in Settings, separately for the primary
and the fallback model (or in the `*_PRICE` variables above), as cost per million tokens. Each
model is costed with the prices of the model that actually ran; local models are reported in
tokens and never counted as spend. With prices loaded, each turn and the session also carry an
estimated cost; with the slots empty, consumption is reported in tokens only — no cost is ever
guessed. A turn that spans several models (vision routing, the planner) is costed per model, and
a mix of priced and unpriced models is reported as a partial estimate.

Input served from the provider's prompt cache is counted and costed separately: the repeated part
of a turn (system prompt and tool definitions) is what gets cached, so on a long session most of
the input is billed at the cheaper cached rate. Fill *Cached input price* to see that reflected;
left empty, cached input is charged at the full input price rather than at a guessed discount.

**What a turn costs.** Tool definitions dominate the input: they are resent on every step of the
tool loop, and the full catalog is ~18k tokens against ~2.8k for the system prompt. The agent
therefore loads only the capabilities a turn can use — a project with no image gets the geometry
catalog alone (~14.5k tokens per call instead of ~21k), and the image and reference-driven tools
appear as soon as an image is attached, uploaded or design parameters exist.

**Visual refinement** (toolbar → Visual): attach reference images (side, front, top views work
best), describe the object and run it. The model is built coarse to fine (blockout → primary →
secondary → materials) from design parameters, rendered from fixed cameras after every change,
scored against the references (silhouette IoU/contour/box, proportions, landmarks, a vision
critic), corrected at most 3 changes at a time, and reverted automatically when a change makes it
look worse. Regions that are already right can be locked.

Providers are chosen by capability, not by name: the selected provider handles everything it can;
an attachment it cannot handle (an image or PDF with a text-only Ollama model) is processed by
OpenAI if configured, otherwise Claude, and Ollama keeps acting as the CAD agent. Image features
(Attach image, Create from Image) are hidden when no configured provider has vision.

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
(commit even multi-command ChangeSets), plus `--label` for the checkpoint name. `create` and
`modify` also accept `--autonomous` (plan, execute step by step, validate and auto-correct). Without either flag
the commit policy is *auto*: a single non-destructive command commits immediately; anything larger
stays **proposed** until you `commit` it.

## Repository layout

```
cmd/simoncad-cli/      CLI (the first-class headless interface)
cmd/simoncad/          desktop UI launcher (serves the frontend on a loopback port)
frontend/              viewport + panels (ES modules, vendored Three.js)
internal/
  app/                 composition root: the only place that wires everything together
  cadir/               CAD-IR schema, graph, canonical serialization
  expr/                safe expression evaluator
  selectors/           semantic selectors, topology-backed (face/edge/vertex/body)
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
  agent/runtime/       providers, capability registry, model router, tool-call loop, recordings, evals
  agent/attachments/   attachment classification and capability-based routing (vision, PDF)
  agent/workflow/      planner, executor, validator, correction loop, agent state machine
  agent/refine/        visual closed-loop refinement: planner, CAD builder, vision critic, controller
  visual/              offscreen renderer, silhouettes, proportions, stage scoring, regions/locks
  agent/capabilities/  pluggable capabilities (geometry, vision)
  rendering/           tessellation → viewport meshes, pick IDs ↔ selectors
  ui/                  UI service, local HTTP/SSE server, event hub
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
- [docs/ui.md](docs/ui.md) — the visual front end: rendering, picking, preview, guarantees, open items
- [docs/tools.md](docs/tools.md) — the tool reference (what the agent can call)
- [docs/project-format.md](docs/project-format.md) — the `.scadproj` on-disk format
- [docs/development.md](docs/development.md) — testing, evals, adding features and tools
- [docs/robot-backlog.md](docs/robot-backlog.md) — gaps found while dogfooding real robot parts

## Known limitations

- Selectors address planar faces by `side`/`at`; curved faces, edges and vertices by kernel `index`,
  which is only valid for the model they were picked on.
- Holes start only from axis-aligned planar faces.
- No streaming of model text, no robotics catalog or macros yet; intent verbs in the palette are
  natural-language openers for the agent.
- The desktop shell is a loopback web server for now; the Wails build is not done (see docs/ui.md).
- Agent evals in `testdata/agent` use hand-scripted recordings; live pass rates are unmeasured.
- API keys come from the environment/`.env`, not an OS keychain.
