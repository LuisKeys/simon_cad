# Modules and components

Package-by-package reference. For how they fit together see [architecture.md](architecture.md).
Test names are given so each claim can be checked.

## Native layer — `native/occ/`

The only code that touches OpenCascade.

| File | Contents |
|---|---|
| `include/occ_bridge.h` | The stable C ABI. No OCCT type crosses it. `occ_handle` is a `uint64` (0 = invalid); `occ_status` 0 = OK. |
| `bridge.cpp` | Handle table (mutex-guarded map), thread-local error message, `guarded()` exception→status translation, release, translate / rotate / mirror, `occ_is_valid`, `occ_mass_props`. |
| `primitives.cpp` | `occ_box`, `occ_cylinder`, `occ_cone`, `occ_extrude_polygon` (closed XY polygon prism). |
| `booleans.cpp` | `occ_boolean_union`, `occ_boolean_subtract`, `occ_boolean_intersect` (inputs not consumed). A failure carries OCCT's own alerts in its message (e.g. `boolean union failed: BOPAlgo_AlertTooFewArguments`), which the agent reads to repair a model. |
| `fillets.cpp` | `occ_fillet_all_edges`, `occ_chamfer_all_edges`, and the selective `occ_fillet_edges`, `occ_chamfer_edges` (kernel edge indices). |
| `modify.cpp` | `occ_scale` (uniform `gp_Trsf` / non-uniform `gp_GTrsf`), `occ_unify_faces`, face edits by kernel index: `occ_face_get`, `occ_face_inset` (`BRepOffsetAPI_MakeOffset`), `occ_face_imprint_inset` (`BRepFeat_SplitShape`), `occ_offset_face` (`BRepOffset_MakeOffset::SetOffsetOnFace`, result re-closed into a valid solid). |
| `mesh.cpp` | `occ_tessellate` / `occ_mesh_*`: per-face triangle ranges, edge polylines, topological vertices and per-face/edge descriptors; numbering in `TopExp::MapShapes` order. `occ_topology` returns the same tables without triangulating (about 10x cheaper on a plate with many holes); selectors use it. |
| `export.cpp` | STEP export/import, STL export. Silences OCCT's stdout messages so `--json` output stays clean. |
| `tests/bridge_test.cpp` | ABI test: validity, volumes, error paths, handle-count returns to 0 after churn. Runs under valgrind. |
| `CMakeLists.txt` | Builds `libsimon_occ.a` and `bridge_test` against the local OCCT. |

`occ_props` returns volume, area, centre of mass, bounding box (exact, `AddOptimal`), and solid / face /
edge counts. Bounding boxes feed the selectors.

Scripts: `scripts/build-occt.sh` builds the pinned OCCT (7.8.1) into `third_party/occt-install`.

## `internal/geometry/bridge`

cgo bindings: the **only** package that imports `"C"`. Thin functions over handles (`Box`,
`Cylinder`, `Cone`, `ExtrudePolygon`, `Union`, `Subtract`, `FilletAllEdges`, `ChamferAllEdges`,
`FilletEdges`, `ChamferEdges`, `Tessellate`, `Translate`, `Rotate`, `Mirror`, `IsValid`, `MassProps`, `ExportSTEP`, `ExportSTL`, `ImportSTEP`,
`Release`, `LiveHandles`, `Version`). Errors wrap `ErrKernel` with the OCCT message. Never use it
directly; use `geometry/api`.

## `internal/geometry/api`

The idiomatic Go geometry API.
- `Kernel` — owns one worker goroutine locked to an OS thread; every kernel call is a job on its
  channel. `New()`, `Close()`, `Version()`, `LiveShapes()` (leak check), constructors `Box`,
  `Cylinder`, `Cone`, `ExtrudePolygon`, `Union`, `Subtract`, `ImportSTEP`.
- `Shape` — an owned reference with explicit `Release()` (safe to call twice). Methods: `Fillet`,
  `Chamfer`, `Translate`, `Rotate`, `Mirror`, `IsValid`, `Props`, `ExportSTEP`, `ExportSTL`.
  Using a released shape returns `ErrReleased`.
- Tests: `kernel_test.go` (including a 10k create/boolean/release churn that checks RSS stays flat and
  the live count returns to 0).

## `internal/export`

`CheckBinarySTL(path)` parses a binary STL and reports triangles, degenerate triangles, open edges
and non-manifold edges; `Watertight()` is true when there are none. Used by the STL export tool and
the Phase 0 bracket check.

## `internal/expr`

Safe expression evaluator for parameter expressions.
- Grammar: numbers, `+ - * / ^` (`^` right-associative, unary minus looser than `^`), parentheses,
  `pi`, functions `sin cos tan sqrt abs round min max`, and dotted identifiers `node.param`.
- Bounds: length ≤ 512, nesting ≤ 48, ≤ 10,000 evaluation steps, non-finite results and division by
  zero are errors. No I/O, no assignment.
- API: `Check`, `Variables`, `NodeOf`, `Eval(expr, env)`.

## `internal/selectors`

Semantic topology selectors. `Parse` / `String` (canonical form), `FeatureOf`, `Bounds`.
`ResolveTopo(selector, *Topology) → Descriptor` resolves every kind against real B-Rep topology
(`topology.go`): `body`; `face` by `side=` (extreme planar face), `side=`+`at=` (planar face in a
given plane) or `index=`; `edge` / `vertex` by `index=`. `Topology.FaceSelector` is the inverse used
for viewport picks (most semantic selector that resolves back to the face). The older bbox-only
`Resolve` remains for `body`. Side aliases (`top`, `bottom`, …) are accepted. See [ui.md](ui.md).

## `internal/rendering`

Turns a rebuilt model into viewport meshes. `Renderer.Scene(model, known)` tessellates each visible
body (the model's leaves) at a size-scaled deflection, caches by node content key, and returns a
`Delta` with only bodies whose key the client lacks. `BodyMesh` carries positions, normals, indices,
face ranges, edge polylines, vertices and a selector per entity. `SelectorFor` / `PickFor` /
`FaceOfTriangle` map pick IDs ↔ selectors.

## `internal/ui`

`Service` (the facade a front end calls: `State`, `Scene`, `Pick`, `SetSelection`, `Preview`,
`Prompt`, `CallTool`, `SetParameter`, `Commit`, `Reject`, `Undo`, `Redo`, `Restore`, `Compare`,
`PlaybackFrame`, `New/Open/Save/Export`, `Palette`), `Server` (loopback HTTP + SSE with a heartbeat,
per-run token, Host check), `Hub` (non-blocking notices; a `selection` notice carries the selection).
`nomutation_test.go` statically enforces that nothing here bypasses the tool registry.

`Service` is one type spread over files by responsibility: `service.go` (the type, construction, the
lock discipline), `selection.go`, `scene.go` (scene delta and preview ghost), `routing.go` (model
router, fallback consent), `agentturn.go` (`Prompt`/`PromptWith`), `drafts.go`, `specs.go` (attached
documents), `images.go` (images, materials, interpretations), `chathistory.go` (what chat is replayed to
the model, bounded by count and size), `tools.go` (tool calls, delete, commit/undo/redo), `timeline.go`
(checkpoints, compare, playback), `project.go`, `state.go` (the state the page renders) and `palette.go`.

## `internal/app`

`App` is the project session: graph, rebuild engine, ChangeSet manager, history, vision and materials
stores. Files: `app.go` (the type, `New`/`Open`/`Save`), `modelaccess.go` (model, selection, workspace),
`toolenv.go` (the `tools.Env` the registry sees, and export), `changesets.go` (prompts, `ApplyTool`,
commit/undo/redo, checkpoints, `RevertLatest`). `Open` records `OpenWarnings` (for example a project
saved with another OpenCASCADE version).

## `internal/vec`

The 3D vector arithmetic (`Add`, `Sub`, `Scale`, `Dot`, `Cross`, `Len`, `Unit`, `Lerp`, `Dist`) shared by
`geometry/curve`, `geometry/project`, `geometry/sweep` and `meshgeo`, which forward their local helpers to it.

## `internal/selection`

The selection context: `Context{Entities []Descriptor}`, `Resolve(rawSelectors, topologyFn)`,
`Selectors()`, `JSON()`. Selecting something with no geometry is an error.

## `internal/cadir`

The CAD-IR: `Node`, `Graph`, `Provenance`, `Metadata`, `Constraint`. Key behaviour:
`Clone`, `Get/Index/Dependents`, `EffectiveDeps`, `TopoOrder` (Kahn, insertion-order tie-break),
`Validate` (ids, unknown deps, cycles, expression syntax), `Canonical()` (sorted keys, 2-space
indent, trailing newline), `Hash()` (ignores status/errors/timestamps), `Node.ContentKey()`.
Tests in `cadir_test.go`.

## `internal/geometry/features`

Feature builders registered by type. `Params` helpers (`Num`, `Positive`, …), `Body` (shape +
properties + `Bounds()`), and `Ctx` (kernel, node, resolved params, already-built bodies).

| Type | Parameters (mm / deg) | Notes |
|---|---|---|
| `box` | `dx dy dz` (>0), `x y z` | Min corner at (x,y,z). |
| `cylinder` | `radius height`, `axis` x/y/z, `x y z` | Base centre at (x,y,z). |
| `hole` | `target`, `diameter`, `depth`, `kind`, `cb_diameter`, `cb_depth`, `cs_diameter`, `cs_angle_deg`, `positions` | `references.face` selects the face (default +Z of target). Through when `depth` omitted. |
| `extrude` | `profile` (rectangle/circle/slot/polygon), `mode` add/cut, `target`, `plane` XY/XZ/YZ, `offset`, `depth`, `symmetric`, `through`, profile fields | Planes map by cyclic permutation. Slot = box + two end cylinders. |
| `linear_pattern` | `source`, `count`, `dx dy dz` | Repeats a hole / extrude source. |
| `circular_pattern` | `source`, `count`, `cx cy cz`, `axis`, `total_angle_deg` | 360° spreads evenly; less spans end to end. |
| `union` / `subtract` / `intersect` | `target`, `tool` | Consume both inputs. |
| `fillet` / `chamfer` | `target`, `radius` / `distance` | All edges; size must be below half the thinnest dimension. |
| `transform` | `target`, `dx dy dz`, `angle_deg`, `ax ay az`, `ox oy oz` | Rotate first, then translate. |
| `mirror` | `target`, `plane`, `offset` | Result = original ∪ mirrored copy. |
| `scale` | `target`, `factor` or `sx sy sz`, `about` center/base/origin/point, `ox oy oz` | Fixed point resolved from the target's bounds at build time. |
| `copy` | `source`, `dx dy dz` | Linked duplicate; does **not** consume `source` (`KeepsInputs`). |
| `face_extrude` | `target`, `distance` (signed), `references.face` (planar) | Prism of the face along its normal; + union, − cut; seams unified. |
| `face_inset` | `target`, `inset` (>0), `depth` (signed, optional), `references.face` (planar) | With depth: prism of the inset face; without: the inset outline is imprinted (face split). |
| `face_offset` | `target`, `distance` (signed), `references.face` (any surface) | Kernel per-face offset; planar faces fall back to a face sweep. |

Intermediate shapes are tracked in an `arena` so every error path releases them.

## `internal/geometry/rebuild`

`Engine` (cache, stats, `Rebuild`, `Close`, `CacheSize`) and `Model` (`Order`, `Nodes`, `Leaves`,
`Apply`, `Failed`, `Bounds`, `Volume`, `ResultShape`). See architecture §5. Tests: plate with holes,
hole kinds and side faces, patterns and extrude, mirror/transform/fillet, expressions and
incremental cache counters, failure propagation and suppression, unresolved selectors, deterministic
tie-break, no leaked shapes.

## `internal/commands`

- `commands.go` — the `Command` interface, `Record` (type + JSON data) with `Encode` / `Decode`,
  and the eight commands with exact inverses. Rejects duplicate / invalid ids
  (`^[a-z][a-z0-9_]{0,47}$`), unknown dependencies, cycles, removal of features with dependents, and
  setting a literal on an expression-driven parameter.
- `changeset.go` — `ChangeSet`, `Validation`, `Effects`, `Evaluator` interface, and `Manager`:
  `Propose`, `Commit`, `Reject`, `Undo`, `Redo`, `Restore` (rebuild undo stack on load),
  `CommittedIDs`, `ReplayOnto`. `AutoCommitEligible` implements the low-risk policy.
- Tests: inverses are exact, JSON round-trip, bad edits never mutate, lifecycle/stale/undo/redo, and
  the 1,000-sequence undo/redo property test.

## `internal/events`

`Bus` (`Publish`, `Subscribe`, `Events`, `Load`, `JSONL`) with an injectable clock, event-type
constants, `ParseJSONL`.

## `internal/history`

`Log` of `Checkpoint`s: `Create`, `Find` (id, number, `previous` / `last`, unique label substring),
`Recent`, and `Compare(a, b) → Diff{Added, Removed, Changed}` over CAD-IR snapshots.

## `internal/tools`

The tool registry. See [tools.md](tools.md) for each tool.
- `registry.go` — `Env` interface, `Args`, `Plan`, `Tool` (exactly one of `Plan` for editing tools or
  `Query` for read-only tools), `Registry.Invoke` (lookup → strict JSON decode → schema validation →
  plan/query), `Subset(capability)`.
- `schema.go` — `ValidateSchema` and builder helpers; supports type / properties / required / enum /
  min / max / exclusiveMinimum / items / min/maxItems / `additionalProperties:false`.
- `geometry.go`, `edit.go`, `query.go`, `helpers.go` — tool definitions and shared helpers
  (deterministic unique ids, node construction with provenance, bounds lookup).
- `ModelSummary(env)` — the deterministic model description used by `get_model_summary` and the
  agent's context builder.

## `internal/persistence`

`Save` / `Load` of the `.scadproj` directory (atomic writes, schema-version check, migration hook),
`Meta`, `Prompt`, `ToolCall`, `AgentHistory`, `Project`. See [project-format.md](project-format.md).

## `internal/app` — composition root

`App` owns the kernel, rebuild engine, graph, ChangeSet manager, history, event bus, registry and
agent history. It implements `commands.Evaluator` and (through `toolEnv`) `tools.Env`.

| Method | Role |
|---|---|
| `New`, `Open`, `Save`, `Close` | Session lifecycle |
| `ApplyTool(name, args, policy)` | **The** mutation entry point; returns an `Outcome` |
| `Commit`, `Reject`, `Undo`, `Redo`, `Pending` | ChangeSet control |
| `Checkpoint(label, force)` | Semantic checkpoint if anything was committed since the last one |
| `Restore(ref, policy)` | Checkpoint restore through the tool path |
| `Replay()` | Rebuild from committed ChangeSets and verify against the live graph |
| `Model()` | Incremental rebuild; writes derived status onto nodes |
| `SetSelection`, `SelectionContext` | Selection context (validated) |
| `RecordPrompt`, `SetPromptSummary`, `SetActor` | Agent bookkeeping |
| `EnvFor(app)` | Read-only tool environment for context builders |

Export (inside `app`) refuses to run while features are failing, requires a valid solid, then
re-imports STEP (volume must match to 1e-6) or checks STL watertightness.

Tests: target interactions via tools, policies and validation messages, save/load byte identity,
replay and tool-call traceability, checkpoint restore + export, selection and measuring, kernel
leak check, incremental-rebuild budget, and the servo-bracket dogfood test.

## `internal/agent/runtime`

| File | Contents |
|---|---|
| `types.go` | Provider-neutral `Message`, `Block`, `ToolDef`, `Request`, `Response`, `Provider` interface |
| `provider.go` | `NewProviderFromEnv` — `SIMONCAD_PROVIDER` → Claude / OpenAI / Ollama; `NewNamedProviderFromEnv` builds any one of them |
| `capabilities.go` | Provider capability registry: `ProviderCapabilities` (text, vision, attachments, structured output, tool calls), `ModelCapabilities(flavor, model)` (Ollama vision by model family or `OLLAMA_VISION`), `CapabilitiesOf` |
| `router.go` | Model router: `Router.Route(need)` → the selected provider if it has the capabilities, else OpenAI, else Claude, else `CapabilityUnavailableError` |
| `claude.go` | Anthropic Messages API client (retries on 429 / 5xx); image and PDF document blocks |
| `openai.go` | Responses API client for OpenAI (images, PDF `input_file`) and Chat Completions for Ollama (image parts for multimodal models); converts between the neutral blocks and OpenAI messages / tool calls |
| `replay.go` | `Recording`, `Replay` provider, `Recorder` wrapper |
| `runtime.go` | `Capability` interface, `Runtime.Run`, `Conversation` (`Begin` / `Send` / `Finish`: the tool-call loop, reusable one plan step at a time), `ImageInterpreter` hook for providers without vision, `Result` |
| `eval.go` | `Case`, `RunCase`, `LoadCases`, `Match`, `Suite` |

## `internal/agent/attachments` — attachment processor

`Classify` sorts an upload into `IMAGE` / `TEXT` / `DOCUMENT` (PDF) / `UNKNOWN`; `Requirements`
maps each kind to the provider capabilities it needs (vision, text, attachments). `Processor`
routes the work through the model router: with a text-only selected provider (e.g. Ollama) an
image goes to a vision model that runs `analyze_image` (no model edits) and returns a structured
description, which the selected provider then works from as the CAD agent; `ExtractDocument`
turns a PDF into text on a document-capable model. Nothing is routed by provider name.

## `internal/agent/workflow` — autonomous workflow

`User request → Plan → Execute → Validate → Correct → Final result`, with the states `IDLE`,
`PLANNING`, `EXECUTING`, `VALIDATING`, `CORRECTING`, `COMPLETED`, `FAILED` reported through
`Agent.OnState`.

| File | Contents |
|---|---|
| `planner.go` | `NeedsPlan` (simple edits skip planning), `Planner` (one `submit_plan` call → `Plan`: analysis, steps with refs and dependencies, expectations) |
| `executor.go` | `Executor` runs steps one `Conversation.Send` at a time; `StepResult` per step; dependents of a failed step are skipped; refs map to feature ids |
| `validator.go` | Deterministic `Validator`: failed/skipped steps, missing refs, dangling dependencies, failed or empty bodies, dimensions (2 % / 0.5 mm), position, material/color, body count; each issue blamed on a step |
| `correction.go` | `Corrector`: re-executes failed steps and their dependents, otherwise one targeted repair request; `MaxAutocorrectionAttempts = 3` |
| `agent.go` | `Agent.Run` orchestration; an attempt that adds structural problems is undone (latest valid state); final summary |

The UI uses the workflow by default (`SIMONCAD_AUTONOMOUS=off` disables it); the CLI with
`--autonomous`. Recorded evals keep using `Runtime.Run`.

## `internal/components` — reusable projects

Projects are composable. A **component is a project with `reusable` set**; an instance of one in
another project is a `component_instance` node holding a reference and a placement, never a copy
of the geometry. Four wheels are four references to one project.

Three jobs, deliberately kept apart from rendering and UI logic:

| File | Responsibility |
|---|---|
| `library.go` | Which reusable projects exist and where. Scans search roots, reading only `project.json` (cached on mtime+size). Also `Dependencies`/`Manifest`: the derived `components.json`. |
| `resolver.go` | `ComponentInstance → geometry`. Implements `features.ComponentResolver`. |
| `graph.go` | `DependencyGraph`: direct and transitive dependencies, cycle detection, traversal. Reads parameters only — a cycle check costs no kernel time. |

Three properties are load-bearing:

- **Search roots are anchored on the open project** (its parent, its `projects_output`) plus the
  configured library root — never the process working directory. Resolving by CWD would make the
  same `.scadproj` rebuild to different geometry from a different shell, breaking the determinism
  contract.
- **`rebuild.Engine` is not reentrant**: `Rebuild` holds its mutex across the builders. A component
  is therefore never resolved on the engine asking for it — each `(project, content, overrides)`
  frame gets its own engine, bounded by an LRU and reachable from the owning engine's `Close` so
  no OCCT shape leaks. A cycle is detected *before* entering a nested engine, or `A → B → A` would
  deadlock instead of returning an error.
- **The content hash lives in the node's parameters**, not in a side channel. That single choice is
  what makes a component edit visible to the rebuild cache key, `Graph.Hash()`, checkpoints,
  ChangeSet staleness and the selection check at once, and makes "update to the latest component"
  an ordinary undoable `SetParameter`.

Tools (`internal/tools/components.go`, Geometry capability): `list_components`,
`search_components`, `open_component` (queries); `create_component`, `insert_component`,
`sync_component`, `detach_component` (plans). `create_component` requires a selection that is
closed over `EffectiveDeps` in **both** directions — nothing outside may depend on it, and it may
not depend on anything outside — because an open selection would either write a project that
cannot be reopened or produce a ChangeSet that `RemoveFeature` rejects.

Writing the new project to disk is the one part of "create component" that is not a ChangeSet. The
directory is content-addressed so undo→redo lands on the same one; undoing without redoing leaves
the library project behind, which the UI says plainly.

## `internal/visual` — deterministic visual measurement

No language model and no geometry edits. `camera.go`: fixed cameras (left/right/front/rear/top
orthographic, front_3q/rear_3q perspective; azimuth 0 = front, 90 = left side) and a fixed
`Frame`, so renders of successive iterations share their pixel scale. `raster.go`: z-buffered
software rasterizer over the tessellated bodies (shaded with outlines, or silhouette: object
white, background black). `mask.go`: reference segmentation (alpha, or border-colour background
estimate + largest component + hole filling), normalization, IoU, chamfer contour distance,
bounding-box aspect, mirroring for side views, difference images. `proportions.go`: landmarks in
normalized image coordinates, ratios (wheelbase, height/length, overhangs, cabin, wheel diameter,
windshield, roof width), ratio comparison and landmark alignment. `scoring.go`: the stages
BLOCKOUT → PRIMARY → SECONDARY → MATERIALS with thresholds and per-stage weights. `state.go`:
references and calibrations, regions and locks, the render store (session only), the iteration
history and failed corrections (saved in `agent-history.json` under `visual`).

## `internal/agent/refine` — visual closed-loop refinement

Separate roles: `Planner` (modeling strategy: design parameters, regions, stage contents),
`Builder` (the CAD builder: the ordinary tool loop, at most 3 structural changes per correction
via `Runtime.MaxStructural`), the renderer (`App.RenderCamera`), `VisionCritic` (structured JSON
only, no tools that edit), `Analyzer` (reference view, calibration, landmarks) and `Controller`
(the loop: render → measure + critique → advance the stage when its thresholds pass, otherwise
apply ≤ 3 corrections → render → revert if the overall score drops by more than the tolerance,
remembering the failed correction; every iteration gets a checkpoint). The builder never needs
vision; the critic and analyzer use a vision model chosen by the router.

Design parameters are `parameters` nodes (no geometry) referenced by feature expressions
(`vehicle.wheelbase`); tools `define_parameters`, `set_parameter`, `bind_parameters`. Region
tools `define_region`, `lock_region`, `unlock_region`; `render_model`,
`calibrate_reference_image`, `get_visual_state` (capability `visual`). Locks are enforced in
`App.ApplyTool` for every caller (agent, UI, CLI): geometry of a locked region cannot change,
appearance can.

## `internal/agent/capabilities/geometry`

The Geometry capability: system prompt (millimetres, axis conventions, hole placement rules,
selection use, "ask when ambiguous", pending-approval behaviour), the `geometry` tool subset, and
`BuildContext` (units, model JSON, selection, recent checkpoints — deterministic).

## `cmd/simoncad-cli`

| File | Contents |
|---|---|
| `main.go` | Dispatch, `--json` handling, usage |
| `agent.go` | `create`, `modify`, provider setup, `--recording` / `--record` |
| `project.go` | `tool`, `tools`, `build`, `export`, `history`, `restore`, `commit`, `reject`, `undo`, `replay` |
| `eval.go` | `eval` command |
| `bracket.go` | Phase 0 servo bracket proof |
| `internal/dotenv` | `.env` loader (shared with `cmd/simoncad`) |

## `cmd/simoncad` and `frontend/`

Launcher (`--project`, `--recording`, `--no-browser`) and the embedded frontend. See [ui.md](ui.md).

## `testdata/`

| Path | Contents |
|---|---|
| `agent/*.json` | Eight eval cases: create plate, four holes, thickness change, recovery from a validation error, selection ("here"), sequential calls in one turn, the M2 prompt "four M3 holes here", and a hole on a non-extreme face (pocket floor). Recordings are **scripted**, not captured from a model. |
| `ui/m2_four_holes.recording.json` | Scripted agent turn for the browser e2e |
| `robot/elbow_joint/steps.json` | Dogfood 2: the elbow clevis as tool calls |
| `demo/m1_create.recording.json` | Recording used by `make demo` when no key is set |
| `robot/servo_bracket/steps.json` | The servo bracket as tool calls (dogfood) |
| `robot/servo_bracket.scadproj` | Permanent regression project (refresh with `UPDATE_ROBOT=1 go test ./internal/app`) |

## Path / sweep / creative-geometry packages

| Package | Role |
|---|---|
| `internal/geometry/curve` | Path math: evaluation, arc length, resample, simplify, smooth, rotation-minimising frames, self-intersection. Pure Go. |
| `internal/geometry/profile` | Section presets and custom contours, validation, orientation. |
| `internal/geometry/sweep` | Sweep planner (frames, miter, caps, analytic volume) feeding `native/occ/sweep.cpp`. |
| `internal/geometry/features` | `path`, `profile`, `sweep`, `relief` features and `CodedError`. |
| `internal/geometry/text` | Font outlines → Bézier regions (kerning, spacing, alignment, mirror, counters). |
| `internal/geometry/project` | Projection onto a tessellated surface (planar / normal / conform). |
| `internal/vectorize` | Image and SVG tracing: outline, centerline, filled regions; deterministic. |
| `internal/meshgeo`, `internal/meshcheck` | BVH (ray, closest point) and mesh integrity checks. |
| `internal/recipe` | Stamp recipe: parse, defaults/assumptions, merge patch, shrinkage factor. |
| `internal/tools` (`path`, `sweep`, `vectorize`, `relief`, `stamp`, `text`, `projection`, `sketchsolid`, `printability`, `impression`) | The agent/CLI/UI tools of the family. |
