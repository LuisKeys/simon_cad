# Visual agentic CAD (Phase 2 / M2)

The desktop UI is a **client of the same services as the CLI and the agent**. It has no mutation
path of its own: every change is a tool call (`App.ApplyTool` → Commands → ChangeSet) or a step of
the ChangeSet lifecycle (commit, reject, undo, redo, restore).

```
 browser / Wails webview            Go
 ┌───────────────────────┐   HTTP   ┌──────────────────────────────────────────────┐
 │ frontend/ (ES modules │ ───────▶ │ internal/ui  Service (+ Server, Hub)          │
 │  Three.js viewport,   │ ◀─ SSE ─ │   │ rendering: meshes, pick IDs ↔ selectors   │
 │  panels, palette)     │          │   │ app: ApplyTool / Commit / Undo / Preview  │
 └───────────────────────┘          │   └ agent runtime (Geometry capability)       │
                                    └──────────────────────────────────────────────┘
```

## Running it

```bash
make ui                                   # builds, serves on a loopback port, opens the browser
./out/simoncad --project my.scadproj      # open (or create on first save) a project
./out/simoncad --no-browser --recording testdata/ui/m2_four_holes.recording.json   # scripted agent, no API key
make e2e                                  # headless-Chrome run of the M2 flow (needs Chrome + `npm i` in e2e/)
```

The agent needs the same `.env` as the CLI (`ANTHROPIC_API_KEY`, …). Without it everything except
free-form prompts still works (palette tools, parameter edits, undo/redo, timeline).

## Pieces

| Package | Role |
|---|---|
| `native/occ/mesh.cpp` | `occ_tessellate`: triangles **grouped per face**, edge polylines, vertices, plus per-face/edge descriptors (surface type, normal, centroid, area, tight bbox). Faces/edges/vertices are numbered in `TopExp::MapShapes` order, so index *N* is the same entity on every rebuild of the same shape. |
| `internal/selectors/topology.go` | Topology-backed selectors (see below) and the inverse mapping topology → selector. |
| `internal/rendering` | `Renderer.Scene(model, known)` meshes the visible bodies (the model's leaves), caches by node content key and returns only bodies the client does not have. `BodyMesh.SelectorFor(kind, index)` / `PickFor(selector)` are the bidirectional pick-ID ↔ selector map. |
| `internal/ui` | `Service` (state, pick, selection, scene, preview ghost, prompt, ChangeSets, timeline, playback, project I/O, palette), `Server` (JSON API + SSE on loopback, per-run token, Host check), `Hub` (non-blocking notices). |
| `internal/app/view.go`, `cards.go` | Read-only views the UI uses: `Snapshot`, `PreviewModel`, `ModelAfter`, `ChangeSetCard`. |
| `frontend/` | `viewport.js` (Three.js, picking, overlays, view cube), `app.js` (panels, palette, timeline), `api.js`. No build step; Three.js r186 is vendored under `frontend/vendor/`. |
| `cmd/simoncad` | Launcher. |

## Selectors are topology-backed now

Phase 1 resolved faces from bounding boxes, which is wrong for pocketed or filleted parts. Now:

| Selector | Meaning |
|---|---|
| `face(feature=F, side=+Z)` | planar face facing +Z at the extreme of the part |
| `face(feature=F, side=+Z, at=6)` | planar face facing +Z in the plane z=6 (e.g. a pocket floor) |
| `face(feature=F, index=N)` | any face (cylinder walls, ambiguous coplanar faces) by kernel index |
| `edge(feature=F, index=N)`, `vertex(feature=F, index=N)` | edge / vertex by kernel index |
| `body(feature=F)` | the whole body |

A click is turned into the *most semantic* selector that resolves back to the same face
(`FaceSelector`): `side=` when it is unique at the extreme, `side=`+`at=` when it is unique in its
plane, otherwise `index=`. `index=` selectors are only meaningful for the model they were picked on,
so the UI drops the selection whenever the model changes.

The agent sees the descriptor of every selected entity (surface type, normal, centroid, bbox, area)
in its context, exactly as with `--select` on the CLI.

`fillet` / `chamfer` accept `edges: ["edge(feature=T,index=N)", …]` to treat only the selected edges
(native `occ_fillet_edges` / `occ_chamfer_edges`); omit it for every edge. This closes the Phase 1
backlog item "round only the outer edges after pocketing".

## The interaction model

- **Viewport:** orbit/pan/zoom, view cube, pick filter (Auto/Face/Edge/Vertex/Body), shift-click
  multi-select, hover highlight, section plane (X/Y/Z + offset), two-point measurement overlay, fit.
  Edges/vertices are picked in screen space and only when not occluded.
- **Selection → conversation:** the selection is shown as chips above the prompt and sent with every
  agent request. "Add four M3 holes here" resolves against the selected face.
- **Interaction level:** *Review* (default) previews every ChangeSet; *Auto* commits low-risk
  single-feature ChangeSets immediately. Multi-feature changes and restores are always previewed.
- **Preview:** *Preview* on a proposed ChangeSet rebuilds the candidate model (`App.PreviewModel`,
  nothing applied) and draws the bodies it would add as a translucent ghost, with the bodies it
  would replace faded and outlined. The card lists commands, affected features, warnings, validation
  results and the volume delta.
- **Design tree:** features with status dots, intent, user/agent provenance badges and inline
  parameter editing (each edit is a `modify_parameter` ChangeSet).
- **Palette (Ctrl/Cmd+K):** intent verbs (natural-language openers for the agent until macros arrive
  in Phase 3), every registered tool (with an argument skeleton and the current selection prefilled),
  and commands (undo/redo, save, export, playback, new). Every entry ends in the tool registry.
- **Semantic timeline:** checkpoints with Restore (proposed as a ChangeSet, never auto-committed) and
  Compare (feature and parameter diff plus volume).
- **Instant Design playback:** replays the committed ChangeSets one at a time, rebuilding the model
  after each, so geometry appears feature by feature.
- **Concurrency:** the agent turn runs on its own goroutine and releases the service lock while it
  waits for the model, so the UI keeps reading state; edits are refused (`409`) while it works.

## Guarantees and how they are tested

| Claim | Test |
|---|---|
| The UI has no mutation path around the tool registry | `internal/ui/nomutation_test.go` parses the UI packages: no `commands`/`bridge`/`features`/`persistence` imports and no access to `App.Graph`, `Mgr`, `Engine`, `K`. A control test proves the checker catches a bypass. |
| Pick ID ↔ selector is a bijection for every face/edge/vertex | `rendering.TestPickSelectorBijection` |
| Only changed bodies are re-meshed and resent | `rendering.TestSceneDeltaOnlyResendsChangedBodies`, `TestServerEndpointsEndToEnd` |
| Select face → prompt → preview → commit → undo | `ui.TestM2SelectPromptPreviewCommitUndo` (service) and `make e2e` (real browser, real kernel) |
| Preview never touches the live model | same tests; `Manager.Preview` works on a clone |
| The local server is not reachable cross-site | `TestServerRejectsMissingTokenAndForeignHost` (token + loopback Host check) |
| Selection context with a non-extreme face | eval `08_hole_in_pocket_floor`; dogfood `TestDogfoodElbowClevis` |
| Selective fillet removes exactly the expected material | `TestSelectiveFilletOfSelectedEdges` (analytic volume to 1e-6) |

## Not done / not verified

- **Wails shell.** The workstation has no `webkit2gtk-4.0`, so a Wails binary cannot be built or run
  here. `ui.Service` is deliberately free of HTTP types so Wails can bind its methods directly; the
  frontend is plain ES modules that load unchanged in a webview. Until then `cmd/simoncad` serves
  the same UI to the system browser. The plan's TypeScript is plain JavaScript (checked by the e2e
  run) to avoid a build step.
- **60 fps at 200k triangles.** Not measured: this machine only has software GL. The backend sends
  each body once and re-sends only changed bodies, and meshing deflection scales with body size, but
  the frame-rate budget needs a run on a real GPU.
- **Out-of-process kernel.** Evaluated against the Phase 1/2 record: no kernel crash has been
  observed (10k create/boolean/release cycles, the property tests, the dogfood parts, the e2e run);
  every OCCT exception is already converted to a status at the C boundary and surfaces as a failed
  feature. Decision: keep the kernel in-process for M2 and keep it behind `geometry/api`, so
  moving it out later needs no change above that layer. Revisit when live-agent sessions produce
  crash data.
- **macOS/Windows CI** (planned from Phase 2) is not set up; only Linux is built here.
- **Live LLM** use in the UI is unverified (no key was available); the UI path is covered with the
  same recorded-provider mechanism the evals use.

## Appearance

Expanding a feature in the design tree shows an **Appearance** group: color picker, hex / `rgb(r,g,b)`
field (empty = reset), opacity slider, visibility checkbox and Reset. The eye button on a tree row toggles
visibility. When several bodies are selected in the viewport, the Selection panel shows the same controls
for all of them (differing values read "mixed"). Every edit calls `set_color` / `set_opacity` /
`set_visibility` / `reset_appearance` through the tool registry, and the viewport updates from
`scene.appearance` without re-tessellating. Hidden bodies are not pickable and are excluded from Fit.

## Delete

`Delete` / `Backspace` (outside text fields) or the **Delete** context-menu item (right-click an object in the
viewport, or a row of the design tree) deletes the selection through `POST /api/delete` → `check_delete` →
`delete_objects`, committed as one ChangeSet that `Ctrl+Z` undoes. The UI only asks two questions, both before
anything changes: *a face/edge/vertex cannot be deleted — delete its owning feature instead?* and *these features
depend on it — delete them too?*. The agent does the same for "delete this" / "remove the selected part" /
"delete these objects"; at the Review level its delete is a draft you can discard.
