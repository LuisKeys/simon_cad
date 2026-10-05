# Architecture

## 1. Principles

1. **Agentic-first.** The engine is designed around manipulation by an agent from its first layer.
   The agent, the CLI and (Phase 2) the UI are three clients of one mutation path.
2. **The CAD-IR is authoritative; the B-Rep is derived.** The model is a graph of semantic
   features. Geometry is rebuilt from it at any time and may be discarded.
3. **One mutation path.** `tool call → Commands → ChangeSet → commit`. Nothing else changes the
   model. Free-form LLM text never mutates state.
4. **Determinism.** Same project + same OCCT version → same geometry. Serialization is canonical
   and byte-stable; evaluation order is deterministic.
5. **Everything is a ChangeSet.** Preview, validation, audit, undo/redo and (later) branching
   and review all operate on ChangeSets.

### The seven-question invariant

Every important feature, tool or abstraction must answer **yes** to all of:
human-manipulable visually · agent-manipulable semantically · deterministic · replayable ·
undoable · validatable · explainable. This is the PR checklist
([.github/pull_request_template.md](../.github/pull_request_template.md)).

## 2. The pipeline

```
 intent (text + selection)
        │
        ▼
 ┌───────────────┐   system prompt + tool subset + context
 │ agent runtime │◄──────────────── capability (geometry)
 └──────┬────────┘
        │ tool_use {name, JSON args}      (only registered tools are callable)
        ▼
 ┌───────────────┐  1. JSON-schema validation
 │ tool registry │  2. semantic validation (ids exist, ranges, selectors resolve, fits the face…)
 └──────┬────────┘  3. Plan = ordered list of Commands + intent + explanation
        ▼
 ┌───────────────┐  Propose: apply to a CLONE of the graph, collect inverses,
 │ ChangeSet mgr │  rebuild the clone, compute effects (volume Δ, added/removed/modified),
 └──────┬────────┘  run schema/semantic/rebuild/geometry checks
        │ policy: auto | commit | preview
        ▼
   Commit → live CAD-IR graph → event bus → rebuild → checkpoint → save
                                   │
                                   ▼
                      rebuild engine → OCCT (via cgo bridge) → STEP / STL
```

`app.ApplyTool` is the single entry point that runs steps 1–3, proposes the ChangeSet, applies the
commit policy and records the tool call. The CLI's `tool`, `create`, `modify`, `restore` and the
agent loop all call it.

### Commit policy
| Policy | Behaviour |
|---|---|
| `auto` (default) | Commit if the ChangeSet is valid, has **one** command, no warnings, and is not a removal/restore. Otherwise leave it *proposed* for approval. |
| `commit` | Commit every valid ChangeSet. |
| `preview` | Never commit; everything stays proposed. |

An invalid ChangeSet (e.g. the rebuild would fail) is rejected automatically and never committable.
A proposed ChangeSet also becomes **stale** if the model changes before it is committed (its base
hash no longer matches); it must be re-proposed.

## 3. Layers

```
 cmd/simoncad-cli ── thin CLI adapter
        │
 internal/app ────── composition root (wires everything; owns the session)
        │
 ┌──────┼───────────────────────────────────────────────────────────────┐
 │ agent/runtime, agent/capabilities     tools     commands   history   │  semantic layer
 │ selection   persistence               events                         │  (no geometry types)
 └──────┬───────────────────────────────────────────────────────────────┘
        │ cadir.Graph (pure data)
 ┌──────▼───────────────────────────────────────────────────────────────┐
 │ geometry/rebuild  →  geometry/features  →  geometry/api               │  geometry layer
 └──────┬───────────────────────────────────────────────────────────────┘
        │ opaque uint64 handles
 ┌──────▼──────────────┐
 │ geometry/bridge (cgo)│  ── only package importing "C"
 └──────┬──────────────┘
        │ flat C ABI (native/occ/include/occ_bridge.h)
 ┌──────▼──────────────┐
 │ C++ adapters → OCCT  │  ── exceptions never cross the boundary
 └─────────────────────┘
```

Dependency rules that hold today:
- No OCCT type appears outside `native/occ`; no `import "C"` outside `geometry/bridge`.
- `cadir`, `commands`, `history`, `events`, `selectors` know nothing about geometry or the kernel.
- `commands` reaches the kernel only through the small `Evaluator` interface, implemented by `app`.
- `tools` reads the world through the `tools.Env` interface, implemented by `app`.
- `agent/runtime` depends on `app` and `tools`; providers depend on nothing project-specific.

## 4. The CAD-IR

A `cadir.Graph` is an ordered list of `Node`s plus units (`mm`). **Insertion order is the
tie-breaker** for evaluation, so the same graph always evaluates in the same order.

Every node carries the full schema from day one (even where unused yet):

| Field | Purpose |
|---|---|
| `id`, `type` | Stable identity; feature type (`box`, `hole`, …) |
| `parameters`, `expressions` | Literal values; safe expressions (`plate.dz * 0.5`) that override them |
| `dependencies` | Explicit upstream node ids |
| `references` | Semantic selectors (e.g. `face` → `face(feature=plate,side=+Z)`) |
| `constraints` | Reserved (symmetry, alignment, clearances…) |
| `intent` | The engineering intent in natural language |
| `manufacturing`, `component` | Reserved for Phases 3–4 |
| `selection_refs` | The selection context active when the node was created |
| `provenance` | `created_by` (user/agent/cli), capability, tool call id, ChangeSet id, model |
| `metadata` | Name, created/modified timestamps, tags |
| `suppressed` | Authoritative suppress flag |
| `status`, `errors` | **Derived** by rebuild: `ok / failed / stale / suppressed` + cause chain |

**Effective dependencies** are the union of explicit `dependencies`, nodes referenced from
expressions (`plate.dz` ⇒ `plate`) and features named in selector references. The DAG, the
topological order and "can I remove this?" all use effective dependencies, so a reference can never
silently dangle.

`Graph.Hash()` is a content hash that **excludes** derived status/errors and timestamps; it is what
checkpoints, stale-detection and replay verification compare.

## 5. Rebuild engine

`rebuild.Engine.Rebuild(graph)`:

1. Topologically order the graph (error on cycles / unknown deps).
2. For each node: resolve parameters (evaluate expressions against upstream resolved parameters),
   compute a **content key** = hash(builder version, type, resolved params, references,
   dependency keys), and look it up in the cache.
3. Cache hit → reuse the body. Miss → run the feature builder, then check the result is a valid
   solid with properties, and cache it.
4. **Failure isolation:** a failing node marks only itself and its dependents `failed`, with a cause
   chain (`dependency "x" failed` + the upstream errors). The rest of the model still rebuilds.
5. **Suppression:** a suppressed modifying feature passes its *target's* body through (it shares the
   cache entry); a suppressed primitive makes its dependents fail with "dependency is suppressed".
6. Cached bodies survive 8 rebuild generations unused, so preview rebuilds of candidate graphs do
   not evict the committed model's geometry.

The result, a `Model`, lists nodes in evaluation order and its **leaves** — body-producing nodes
that nothing consumes. Exports and "model volume" are defined over the leaves. `Engine.Stats`
(hits/misses) is how incremental behaviour is verified in tests.

A parameter edit on a 50-feature part re-evaluates in ~70 ms in tests (budget: 300 ms).

## 6. Features and the tool/target model

Feature builders turn resolved parameters into kernel shapes. The model is a chain: a modifying
feature names a `target` (a node whose body it modifies) and produces a new body.

- **Tool features** (`hole`, `extrude`) expose a *tool generator* (the cutter or the added solid)
  and a `subtract` flag; their build is `target ∘ tool`.
- **Patterns** (`linear_pattern`, `circular_pattern`) take a `source` hole/extrude feature,
  re-create its tool, transform copies of it and apply them to the source's output. That keeps
  patterns general without teaching them about holes.
- **Hole placement** works on a face selector: tools are authored along +Z with `z=0` at the face and
  oriented onto the face normal; positions are world coordinates where the component along the
  normal is ignored.

## 7. Commands and ChangeSets

Commands are small, serializable and **exactly invertible**:
`AddFeature`, `RemoveFeature`, `SetParameter`, `SetExpression`, `Reorder`, `Suppress`, `Rename`,
and `RestoreSnapshot` (checkpoint restore). `Apply(graph)` mutates the graph and returns its
inverse; failed commands leave the graph untouched.

A `ChangeSet` records commands, their inverses (the rollback info), affected features, expected
effects, warnings, the agent's explanation, validation results, provenance, and the base/result
graph hashes. Undo applies the stored inverses in reverse; redo re-applies the commands. The undo
stack is rebuilt from committed ChangeSets when a project is opened; the redo stack is not persisted.

A 1,000-sequence randomized test verifies that undo-all restores the initial graph byte-for-byte and
redo-all reproduces the final one.

## 8. Events, history and replay

- **Events:** a typed bus publishes `UserIntentReceived`, `AgentToolRequested/Result`,
  `ChangeSetProposed/Committed/Rejected/Undone/Redone`, `FeatureGraphUpdated`, `ModelRebuilt`,
  `CheckpointCreated`, `AgentResponseFinal`, `ExportCompleted`. A recorder keeps them in order and
  persists them as `events.jsonl`.
- **Checkpoints** record engineering intent (the prompt, or the ChangeSet's intent) plus the
  ChangeSets, the graph hash and a CAD-IR snapshot. `history` supports compare (added / removed /
  parameter changes) and restore (a `RestoreSnapshot` ChangeSet, so restore is itself undoable).
- **Replay** re-derives the model by applying all committed ChangeSets, in sequence order, to an
  empty graph and checks the result hash equals the saved model — a standing integrity check.

## 9. Selection context and selectors

Selectors are strings: `kind(feature=ID, key=value…)`, canonicalized (`feature` first, other keys
sorted). Kinds: `body`, `face` (needs `side=±X|±Y|±Z`), plus parsed-but-unresolved `edge`, `edges`,
`vertex`. A resolved selector becomes a `Descriptor`: surface type, normal, centroid, bbox, area.

The **selection context** is the current selection as a list of descriptors; it is injected into
every agent request and recorded on nodes created while it is active (`selection_refs`). In Phase 1
the CLI supplies it with `--select`; Phase 2 will feed it from viewport picking.

## 10. Agent runtime

One agent runtime with pluggable **capabilities** (only *geometry* exists). A capability supplies:
a system prompt, a tool subset and a deterministic context builder. This avoids premature
multi-agent orchestration; a capability can later be promoted to an independent agent without
changing tools, ChangeSets or the CAD-IR.

The loop (`runtime.Run`): record the prompt → build context (units, model summary as JSON,
selection, recent checkpoints) → call the provider with the tool definitions → for each `tool_use`,
refuse anything outside the capability's tool set, otherwise `app.ApplyTool` → return a
`tool_result` (success data or an actionable error) → repeat until the model answers in text, or the
step limit (12) is hit. Validation errors are fed back so the model can correct itself.

**Providers** (`SIMONCAD_PROVIDER`): `claude` (Anthropic Messages API), `openai` and `ollama` (Chat
Completions wire format; Ollama through its `/v1` endpoint). A `Recorder` captures live responses and a
`Replay` provider plays them back, which is how CI runs agent evals without a network.

**Eval harness:** `testdata/agent/*.json` maps a prompt (plus optional setup and selection) to
expected tool calls and resulting model facts, using tolerant matchers (numeric tolerance, subset
object match, `len`, `set`, `oneof`, `contains`, `approx`, `any`).

## 11. Persistence

A project is a `.scadproj` directory of canonical JSON — see [project-format.md](project-format.md).
Saving writes atomically (temp file + rename). Loading validates the schema version (migration hook
in place, no migrations yet) and the graph. Saving the same state twice yields identical bytes;
`save → load → rebuild → save` is byte-identical.

## 12. Native layer and threading

- **C ABI** (`occ_bridge.h`): opaque `uint64` shape handles in a C++ handle table, explicit
  `occ_release`, every call returns a status with a thread-local message, all OCCT exceptions are
  caught at the boundary. A kernel failure becomes a failed feature, never a crash.
- **Threading:** all OCCT calls run on one dedicated goroutine locked to an OS thread
  (`geometry/api.Kernel`), fed by a job channel. This serializes access to a non-thread-safe kernel
  and prepares for moving the kernel out of process.
- **Units and tolerance:** millimetres, `float64`, one modeling tolerance.
- **Leak accounting:** `occ_live_handle_count` is checked in tests (the engine must release every
  cached body on close), and valgrind runs over the ABI test (`make leakcheck`).

## 13. Security model

- The LLM can call **only registered tools**; unknown names are refused before reaching the app.
- Arguments pass JSON-schema validation (unknown arguments rejected) and then semantic validation.
- The expression evaluator has no I/O, no assignment, no loops; length, depth and step count are
  bounded.
- No generated code is ever executed.
- Secrets live in the environment / git-ignored `.env`.
- Not yet addressed: OS keychain storage, prompt-injection hygiene for imported files and catalog
  text (relevant once imports and the catalog exist).

## 14. Roadmap hooks

| Later phase | Where it plugs in |
|---|---|
| UI / viewport (Phase 2) | A new client of `app`; tessellation + pick IDs ↔ selectors in a new `rendering` package |
| Robotics catalog & macros (3) | `component` field, a `macros` package expanding to existing features, new capability + tools |
| Analysis / critic (4) | `manufacturing` field, `analysis` package, new capabilities |
| Multi-agent split | Promote a capability; tools and ChangeSets unchanged |
