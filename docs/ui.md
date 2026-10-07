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

## Help microsite

The header's **Help** link opens `frontend/help.html` (with `help.css`, `help.js`) in a new tab: the full
user guide with copy-paste example prompts for every feature, the tool reference and keyboard shortcuts.
It is served by the same embedded static handler, so it ships in the binary. When you add a tool or a
user-visible feature, update the prompts in `help.html` and the `TOOLS` table in `help.js`.

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

- **Viewport:** orbit/pan/zoom, view cube, shift-click multi-select, hover highlight, section plane
  (X/Y/Z + offset), two-point measurement overlay, fit. Selected geometry is an orange outline over
  a light tint. Edges/vertices are picked in screen space and only when not occluded. Nothing floats
  over the model; its controls live in the fixed action bar above it.
- **Contextual action bar:** a fixed strip above the viewport that shows only the actions valid for
  the current selection (none / body / face / edge / vertex / several bodies / mixed). It is a pure
  function of the selection: `frontend/actions.js` maps each context to actions (id, label, icon,
  group, `visible` rule, `destructive` flag, handler), so a new CAD tool is one registry entry.
  The pick filter is the compact `Select: Auto ▾` control (Auto/Body/Face/Edge/Vertex). Actions
  that do not fit move into *More ⋯*; Delete is always last and set apart. Only actions backed by
  a tool are offered (move/rotate = `transform`, union/subtract/intersect = `boolean`,
  fillet/chamfer, hole, mirror, patterns, material, color, hide, delete). Scale, duplicate,
  face extrude/inset/offset, group and align have no tool yet and are not shown.
- **Top bar:** project-name menu (New / Open), Undo, Redo, Save, Export, Help and an overflow menu
  (command palette, agent level). Panels can be hidden from the bar's outer buttons.
- **Panels:** the design tree (left) and Simon Agent (right) are resizable (drag the splitter,
  arrow keys when focused, double-click to reset) and collapsible; sizes persist in `localStorage`.
  The tree collapses first on narrow screens; below 760 px the panels become overlays.
- **Touch:** controls are at least 40 px (44 px on coarse pointers). Press-and-hold opens the
  context menu on tree rows and in the viewport (the touch equivalent of right-click); every tree
  row also has a visible *⋯* button when selected.
- **Selection → conversation:** the selection is shown as chips above the prompt and sent with every
  agent request. "Add four M3 holes here" resolves against the selected face.
- **Interaction level:** *Review* (default) previews every ChangeSet; *Auto* commits low-risk
  single-feature ChangeSets immediately. Multi-feature changes and restores are always previewed.
- **Preview:** *Preview* on a proposed ChangeSet rebuilds the candidate model (`App.PreviewModel`,
  nothing applied) and draws the bodies it would add as a translucent ghost, with the bodies it
  would replace faded and outlined. The card lists commands, affected features, warnings, validation
  results and the volume delta.
- **Design tree:** one dense row per feature: visibility toggle, type icon, name and a small
  `agent` badge. Click selects the object (Shift adds, Ctrl/Cmd toggles) and shows its details
  (intent, errors, appearance, parameters; each parameter edit is a `modify_parameter`
  ChangeSet); double-click or F2 renames (`rename_feature`); the context menu offers hide,
  rename, suppress and delete. Drag-reordering is not supported.
- **Palette (Ctrl/Cmd+K):** intent verbs (natural-language openers for the agent until macros arrive
  in Phase 3), every registered tool (with an argument skeleton and the current selection prefilled),
  and commands (undo/redo, save, export, playback, new). Every entry ends in the tool registry.
- **Semantic timeline:** a compact, collapsible strip of checkpoints (number, title, author, change
  count). Selecting one reveals Restore (proposed as a ChangeSet, never auto-committed) and Compare
  (feature and parameter diff plus volume).
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

## Attached specification documents

**Attach** (or drop) also accepts a text document (`.txt`, `.md`, `.json`, `.csv`, …, ≤ 200 KB each,
400 KB total) such as the specification of a part. It shows as a 📄 chip and is sent to the agent with
every request as short-term memory until **Clear** is pressed (or a project is opened/created). It is
never saved with the project. `POST /api/spec?name=…` (raw body) attaches; `POST /api/spec/remove` detaches.

## Image to CAD

Attach a sketch, scan, drawing or photo of paper/whiteboard (PNG, JPEG, WebP) with the **Attach** /
**Create from Image** buttons, by dropping it anywhere on the window, or by pasting it from the
clipboard. Large images are downscaled in the browser (≤ 2048 px) before upload; the upload endpoint
(`POST /api/image`, raw body, 20 MB) sniffs the content, not the name.

The agent's interpretation opens in the **Image interpretation** panel: the source image with an SVG
overlay of the detected geometry (solid = outline, dashed = hole, boxes = dimensions that were read),
confidence bands (≥ 85 % / ≥ 60 % / uncertain), the questions that are still open (usually just the
overall width and the thickness), detected dimensions with an editable value and a *confirm*
checkbox for low-confidence readings, a boolean-intent choice when overlapping shapes are ambiguous,
a placement selector (plane XY/XZ/YZ or the **selected planar face**) and "keep the source image in
the project". **Create & preview** calls `confirm_detected_dimension` / `set_sketch_scale` /
`update_sketch_proposal` / `create_from_image` through `/api/tool`, then shows the ordinary ghost
preview with Commit / Reject. **Correct with the agent…** re-attaches the image to the composer.
Nothing in the panel can change the model except through those tool calls.

## Appearance

Expanding a feature in the design tree shows an **Appearance** group: color picker, hex / `rgb(r,g,b)`
field (empty = reset), opacity slider, visibility checkbox and Reset. The eye button on a tree row toggles
visibility. When several bodies are selected in the viewport, the Selection panel shows the same controls
for all of them (differing values read "mixed"). Every edit calls `set_color` / `set_opacity` /
`set_visibility` / `reset_appearance` through the tool registry, and the viewport updates from
`scene.appearance` without re-tessellating. Hidden bodies are not pickable and are excluded from Fit.

### Materials

The Appearance group also holds a **material** row: **Choose…** opens a picker with search, category chips
and sphere thumbnails (rendered with the viewport's lighting). Click one to assign it to the selected
objects, or drag a thumbnail onto an object in the viewport or a row in the design tree (dropping on a
selected object applies to the whole selection). Right-click an object → recent materials, **Material…**
and **Remove material**. Texture scale multiplies the material's natural tile size; color and opacity act
as overrides on top of the material. All of it goes through `set_material` / `remove_material` /
`set_material_scale` — ChangeSets with undo/redo that never rebuild geometry. Rendering uses
`MeshStandardMaterial` with a shared RoomEnvironment for reflections; textures and materials are cached
and reference counted (`frontend/materials/cache.js`) and UVs are generated per face from its surface type
(`uv.js`: planar, cylindrical, box).

## Delete

`Delete` / `Backspace` (outside text fields) or the **Delete** context-menu item (right-click an object in the
viewport, or a row of the design tree) deletes the selection through `POST /api/delete` → `check_delete` →
`delete_objects`, committed as one ChangeSet that `Ctrl+Z` undoes. The UI only asks two questions, both before
anything changes: *a face/edge/vertex cannot be deleted — delete its owning feature instead?* and *these features
depend on it — delete them too?*. The agent does the same for "delete this" / "remove the selected part" /
"delete these objects"; at the Review level its delete is a draft you can discard.
