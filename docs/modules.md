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
| `booleans.cpp` | `occ_boolean_union`, `occ_boolean_subtract` (inputs not consumed). |
| `fillets.cpp` | `occ_fillet_all_edges`, `occ_chamfer_all_edges`. |
| `export.cpp` | STEP export/import, STL export. Silences OCCT's stdout messages so `--json` output stays clean. |
| `tests/bridge_test.cpp` | ABI test: validity, volumes, error paths, handle-count returns to 0 after churn. Runs under valgrind. |
| `CMakeLists.txt` | Builds `libsimon_occ.a` and `bridge_test` against the local OCCT. |

`occ_props` returns volume, area, centre of mass, bounding box (exact, `AddOptimal`), and solid / face /
edge counts. Bounding boxes feed the selectors.

Scripts: `scripts/build-occt.sh` builds the pinned OCCT (7.8.1) into `third_party/occt-install`.

## `internal/geometry/bridge`

cgo bindings: the **only** package that imports `"C"`. Thin functions over handles (`Box`,
`Cylinder`, `Cone`, `ExtrudePolygon`, `Union`, `Subtract`, `FilletAllEdges`, `ChamferAllEdges`,
`Translate`, `Rotate`, `Mirror`, `IsValid`, `MassProps`, `ExportSTEP`, `ExportSTL`, `ImportSTEP`,
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

Semantic topology selectors. `Parse` / `String` (canonical form), `FeatureOf`, `Bounds`, and
`Resolve(selector, bounds) → Descriptor`. Resolves `body` and planar `face(side=±X/Y/Z)` from
bounding boxes; edge/vertex kinds parse but return "not resolvable yet". Side aliases
(`top`, `bottom`, `left`, `right`, `front`, `back`) are accepted.

## `internal/selection`

The selection context: `Context{Entities []Descriptor}`, `Resolve(rawSelectors, boundsFn)`,
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
| `union` / `subtract` | `target`, `tool` | Consume both inputs. |
| `fillet` / `chamfer` | `target`, `radius` / `distance` | All edges; size must be below half the thinnest dimension. |
| `transform` | `target`, `dx dy dz`, `angle_deg`, `ax ay az`, `ox oy oz` | Rotate first, then translate. |
| `mirror` | `target`, `plane`, `offset` | Result = original ∪ mirrored copy. |

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
| `provider.go` | `NewProviderFromEnv` — `SIMONCAD_PROVIDER` → Claude / OpenAI / Ollama |
| `claude.go` | Anthropic Messages API client (retries on 429 / 5xx) |
| `openai.go` | Chat Completions client for OpenAI and Ollama; converts between the neutral blocks and OpenAI messages / tool calls |
| `replay.go` | `Recording`, `Replay` provider, `Recorder` wrapper |
| `runtime.go` | `Capability` interface, `Runtime.Run` tool-call loop, `Result` |
| `eval.go` | `Case`, `RunCase`, `LoadCases`, `Match`, `Suite` |

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
| `dotenv.go` | `.env` loader |

## `testdata/`

| Path | Contents |
|---|---|
| `agent/*.json` | Six eval cases: create plate, four holes, thickness change, recovery from a validation error, selection ("here"), sequential calls in one turn. Recordings are **scripted**, not captured from a model. |
| `demo/m1_create.recording.json` | Recording used by `make demo` when no key is set |
| `robot/servo_bracket/steps.json` | The servo bracket as tool calls (dogfood) |
| `robot/servo_bracket.scadproj` | Permanent regression project (refresh with `UPDATE_ROBOT=1 go test ./internal/app`) |
