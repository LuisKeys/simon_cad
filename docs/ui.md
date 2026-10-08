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
| `frontend/` | `viewport.js` (Three.js, picking, overlays, view cube), `viewlayout.js` (Single/Quad views, cameras, dividers), `axes.js` (coordinate axes, axis gizmo), `app.js` (panels, palette, timeline), `api.js`. No build step; Three.js r186 is vendored under `frontend/vendor/`. |
| `cmd/simoncad` | Launcher. |

## Help microsite

The header's **Help** link opens `frontend/help.html` (with `help.css`, `help.js`) in a new tab: the full
user guide with copy-paste example prompts for every feature, the tool reference and keyboard shortcuts.
It is served by the same embedded static handler, so it ships in the binary. When you add a tool or a
user-visible feature, update the prompts in `help.html` and the `TOOLS` table in `help.js`.

## Shortcuts panel

The header's keyboard button, **More → Keyboard shortcuts** or <kbd>?</kbd> (outside text fields) opens a
popup over the main screen listing every keyboard and mouse shortcut, grouped and ordered from most to
least used. <kbd>?</kbd> again, <kbd>Esc</kbd>, ✕ or a click outside closes it; while it is open no other
shortcut fires. The list lives in `frontend/shortcuts-model.js` (`SECTIONS`); when you add or change a
shortcut, update it there and in the shortcuts table of `help.html`.

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
- **Single / Quad View:** *View ▾ → Viewport layout* (or Ctrl+Alt+4) switches between one view
  and a 2×2 grid: TOP | PERSPECTIVE over FRONT | RIGHT. There is still one `WebGLRenderer` and
  one scene: `frontend/viewlayout.js` (`ViewportLayoutManager`) owns four panes, each a
  transparent overlay with its own perspective and orthographic cameras, OrbitControls, axis
  gizmo, view-cube hit area and label, and `Viewport.render` draws every visible pane into its
  rectangle of the shared canvas (scissor). The cube is drawn by the same renderer; nothing is
  duplicated. Picking raycasts with the camera of the pane under the pointer; selection,
  hover, ghost, section and measurement are scene state, so every view shows them. A click
  makes a view active (accent border): Fit, the View menu's standard views and the
  measurement label act on it. Orbit only in perspective; orthographic views pan (left,
  middle or right drag) and zoom (wheel, towards the cursor) with a fixed orientation.
  Each pane has a `ViewportConfig` (`id`, `projectionType`, `orientation`, `cameraState`,
  `gridPlane`, `showAxisGizmo`; pure model and validation in `viewlayout-model.js`). Clicking
  the label picks one of seven orientations for that view only (Perspective, Top, Bottom,
  Front, Back, Left, Right); the change is animated and a projection switch keeps the target
  and visible size. Grid planes follow the Z-up model: Top/Bottom XY, Front/Back XZ,
  Left/Right YZ (the orthographic grid sits behind the model); the perspective view shows the
  project's configured grid plane. Double-clicking a label
  maximizes that view and again restores the quad; the dividers resize the views. Single
  View shows the active pane, so its configuration carries over between layouts. Layout,
  split and cameras are saved per project path in `localStorage` (`simon.views.v1:<path>`),
  never in the project file or its history.
- **Dimensions:** when an object is selected (its body, or any face/edge/vertex of it) the
  viewport shows its exterior dimensions — width, depth and height along the object's own axes —
  as lines in the shared scene (so every Single/Quad view shows them) with one label per view,
  placed with that view's camera; a dimension seen end-on (height in TOP) has no label there.
  Hovering a label shows a text cursor; clicking it turns it into a number field: **Enter**
  confirms, **Escape** (or leaving the field) cancels, and an invalid value is refused in place.
  The edit is a `set_object_dimension` tool call applied at once even in Review (like an inline
  parameter edit) and undoable with Ctrl+Z; the object stays selected and its dimensions
  recompute. The Selection panel (object properties) has a **Dimensions** section — Width,
  Height, Depth, **Lock proportions** (`set_object_proportional_scaling`, stored with the object)
  and **Anchor** (Center / Object origin, a per-user preference in `localStorage`) — always in
  sync with the viewport labels, both rendered from `selection.dimensions` (the server's
  measurement). Fit frames the dimension lines with the object. Pieces: `frontend/dimensions.js`
  (DimensionRenderer, DimensionEditor) and `frontend/dimensions-model.js` (line placement,
  parsing; unit tested). See [architecture.md §6a](architecture.md) for the measurement and the
  resize.
- **Interactive Move / Rotate:** with one object selected, **G** (move) or **R** (rotate) — or
  right-click → **Transform → Move / Rotate**, or the action bar's Move/Rotate — starts a modal
  operation (Blender style) in the view under the mouse; the shortcuts only fire when the viewport
  was the last thing clicked or is under the mouse, and never while typing. The object follows the
  mouse from the position it had when the operation began (no jump). Move defaults to the
  workspace grid plane; **X/Y/Z** constrain it to an axis and **Shift+X/Y/Z** to the plane normal
  to that axis (pressing the active one again returns to the default). Rotate defaults to the grid
  plane's normal; **X/Y/Z** pick the axis and **P** toggles the pivot between the object's origin
  and its box center. Snapping uses the project's settings (grid lines for the object's origin,
  translation and rotation increments) and is computed from the start every frame, never from
  accumulated steps; holding **Ctrl** suspends it. A HUD at the bottom of the viewport shows the
  constraint, the move and the resulting position (or the angle and pivot) in display units, and
  the scene shows the constraint axis/plane, the pivot and a rotation ring. **Left-click/Enter**
  confirms: one `move_object` / `rotate_object` call, applied at once even in Review, one undo step,
  the moved object stays selected. **Esc/right-click** (or clicking elsewhere in the app) cancels:
  the body's matrix goes back to the identity — nothing was sent, so there is nothing to undo.
  While it runs nothing reaches the server (the preview is the body's matrix; no rebuild, no
  history, no agent event) and the other shortcuts are disabled; middle-drag and the wheel still
  navigate. If the target is rebuilt underneath (an agent edit, undo, a project switch) or the
  selection changes, the operation is cancelled safely. Pieces: `frontend/transform.js`
  (InteractiveTransformController, TransformPreview), `frontend/transform-model.js` (the state
  machine and drag/rotation math; unit tested) and `frontend/snap.js` (the SnapService twin). See
  [architecture.md §6c](architecture.md).
- **Coordinate axes:** X red `#E05252`, Y green `#56B870`, Z blue `#4A90E2`, defined once in
  `frontend/axes.js` and shared by the two places that show them: the axes through the true origin
  (both directions, the negative half fainter, labels at the positive ends) and the axis gizmo in a
  corner of the viewport (click an
  axis end to look along it, the centre for the isometric view). The cube and the gizmo take the
  orientation of their view's camera directly, so they always agree; view changes from either are animated.
  All of it is presentation only: not pickable, not in the model bounds, never exported or recorded
  in the history. Settings → *Viewport* turns each part on or off and sets the axis opacity and the
  gizmo's size and corner; the choice is saved with the other settings.
- **Rendering and materials look:** the viewport lights the model with a procedural studio environment
  (`lighting.js`: softboxes on a gradient dome, reflected by metals and glossy plastics), a key light that casts soft
  shadows onto an invisible ground plane, and a fill. The shadow map is redrawn only when the model, its visibility or
  the section cut changes, not when the camera moves. Library materials may add clearcoat, transmission (glass),
  ior, sheen and emissive (rendered as a physical material only when used) and CC0 PBR textures
  (`materials/<id>/*.jpg`, see `docs/project-format.md`). Settings → *Rendering* sets the environment (studio or plain
  room), reflection intensity, exposure, tone mapping (neutral, ACES, AgX, none) and shadow quality (off, standard,
  high); changes preview live, Cancel reverts them and they are saved with the other settings (`internal/config/render.go`,
  `frontend/render-model.js`). Presentation only: never in the project, the history or an export.
- **Settings navigation:** the dialog has a left-hand section list (Primary Model, Fallback Model, UI Configuration,
  Viewport, Rendering and, with a project open, Workspace); only the active section is shown, all share one Save/Cancel, and
  the last section used is reopened. A validation error switches to the section holding the field.
- **Workspace / Units & Grid:** Settings › *Workspace / Units & Grid* edits the open project's
  display units (linear mm/cm/m/in/ft, angular degrees/radians, decimals), grid (show, plane,
  spacing, major line interval, extent, origin) and snapping (master switch, grid, translation,
  rotation, increments). Lengths in the form are shown in the unit chosen there; changes preview
  in the viewport at once and Cancel reverts them; invalid values (zero or negative spacing,
  extent or increments) are refused in place. Save calls the workspace tools — the same ones the
  agent uses — for the sections that changed; *Restore Defaults* loads the global defaults and
  *Use as default for new projects* stores the values as those defaults. Display units apply to
  dimension labels and fields, the measurement label, selection info, ChangeSet volumes and the
  modeling dialogs (Move, Fillet, Duplicate, face edits, component placement): values are typed
  in display units and sent to tools in mm (`frontend/units.js`, factors from the server). The
  grid (`frontend/grid.js`) is drawn world-aligned with major lines and axis-coloured centre
  lines; see [architecture.md §6b](architecture.md).
- **Contextual action bar:** a fixed strip above the viewport that shows only the actions valid for
  the current selection (none / body / face / edge / vertex / several bodies / mixed). It is a pure
  function of the selection: `frontend/actions.js` maps each context to actions (id, label, icon,
  group, `visible` rule, `destructive` flag, handler), so a new CAD tool is one registry entry.
  The pick filter is the compact `Select: Auto ▾` control (Auto/Body/Face/Edge/Vertex). Actions
  that do not fit move into *More ⋯*; Delete is always last and set apart. Only actions backed by
  a tool are offered (move/rotate = `transform`, union/subtract/intersect = `boolean`,
  fillet/chamfer, hole, mirror, patterns, scale, duplicate, align, group/ungroup,
  face extrude/inset/offset, material, color, hide, delete). Body actions loop per body where the
  tool takes one target (move, rotate, scale); duplicate and align send the whole selection as one
  ChangeSet. Face actions take one face per body (a second face of the same body is picked again on
  the result).
- **Top bar:** project-name menu (New / Open), Undo, Redo, Save, Export, Help, Shortcuts and an overflow
  menu (command palette, keyboard shortcuts, agent level). Panels can be hidden from the bar's outer buttons.
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
  rename, suppress, duplicate, group and delete. Drag-reordering is not supported. Nodes with a
  `group` are listed under a collapsible group row (placed where its first member is): clicking it
  selects the group's visible bodies; its eye hides/shows them; its menu renames (regroup under
  a new name), duplicates or ungroups. Fold state persists in `localStorage`. Shortcuts: Ctrl/Cmd+D
  duplicate, Ctrl/Cmd+G group, Ctrl/Cmd+Shift+G ungroup.
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
| Dimension edits: only the edited dimension changes, local axes, anchors, lock, undo/redo, replay | `app.TestSetObjectDimensionChangesOnlyThatDimension` and the other tests in `internal/app/dimensions_test.go`; `ui.TestDimensionEditInReviewKeepsTheObjectSelected`; `e2e/dimensions.mjs` (real browser, Single and Quad) |
| Interactive move/rotate: axes, planes, snapping, pivots, ±180°, confirm/cancel, one undo step, component placements, agent/UI agreement | `internal/app/transform_test.go`; `internal/transform` unit tests; `ui.TestInteractiveTransformCommitsAtReviewAndKeepsSelection`; `frontend/transform-model.test.mjs`; `frontend/snap.test.mjs` + `workspace.TestSharedSnapCases` (same cases for both SnapServices); `e2e/transform.mjs` (real browser) |

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

## Token consumption and cost

Every model call is metered. Providers attribute each response's input/output tokens to the model
that produced them (`runtime.Usage.ByModel`, keyed `<provider>/<model>`), so a turn that spans
several models — vision routing, the planner, the CAD agent, the refinement critic — is accounted
per model rather than lumped together. `Service` keeps a per-turn figure and a running session
total (`State.Usage`), which survive **Clear** and reopening a project: the meter measures the
session, not the conversation.

The agent panel shows the session total above the composer (`#usage`, with the per-model split and
the last turn in its tooltip) and a one-line note under each answer. The CLI prints a `usage:` line
and carries `usage` (with `by_model`) and `cost` in `--json`; `eval` reports the suite total.

Input the provider served from its prompt cache is tracked as `CachedInputTokens`, a subset of
`InputTokens` (Anthropic reports cache tokens *beside* `input_tokens`, OpenAI *inside* them; the
providers normalize both to "cached is part of the input"). It is costed at the cached rate when
one is configured, and at the full input rate otherwise — never at a guessed discount.

Prices are optional and configured per model — Settings → *Primary Model* / *Fallback Model* →
*Token Prices*, or the `*_INPUT_PRICE` / `*_CACHED_INPUT_PRICE` / `*_OUTPUT_PRICE` variables (with
the `SIMONCAD_FALLBACK_` prefix for the fallback) — as cost per million tokens, labelled by the
model's currency (default `SIMONCAD_CURRENCY`). Each model in `Usage.ByModel` is costed with the
prices of the slot that configures that exact model (`config.PriceForModel`). With prices loaded
the estimate accompanies the tokens; with the slots empty only tokens are reported. A cost is never
invented: a cloud model with no configured price contributes nothing to the amount and is listed in
`cost.unpriced`, which marks the estimate partial, and usage with no model attribution (recorded
runs, stubs) is not costed at all. Local (Ollama) models are never priced: their tokens are counted
in `cost.local_tokens` and never in the amount; `cost.cloud_tokens` is the API-billed part.

## Primary and fallback models

`runtime.Router` routes every model call. `Selected` is the primary model; `Fallbacks` holds the
one configured fallback model (cloud only), built by `NewRouterFromEnv` from `config.FallbackModel`
— empty, with `FallbackErr` saying why, when it is disabled or incomplete.

- **Capabilities.** `Route(need)` keeps a request on the primary whenever its declared capabilities
  (`ModelCapabilities`: a multimodal Ollama model reads images) satisfy it, and otherwise uses the
  fallback. The attachment processor and the visual refinement route through it.
- **Recoverable errors.** `Router.Agent()` is the CAD agent's provider for one request: when the
  primary fails with `ErrUnavailable` (unreachable, or 429/5xx after its own retries), the rest of
  the request moves to the fallback, once, and never back; a failing fallback fails the request.
- **Consent.** A request of a local primary is only sent to a cloud fallback when
  `Router.Consent` allows it. Otherwise `Route` returns a `ConsentError`, which the server turns into
  `409 {code: "fallback_consent"}`; the front end asks (Allow / Cancel, optionally remembered via
  `POST /api/fallback/consent`) and resends. Mid-request (failover) nothing can be asked, so an
  unauthorized fallback is reported, not used.
- **Transparency.** Every call through the router is reported to `Router.OnRoute` as a
  `RouteEvent`; the service publishes it as a `model` notice (the Simon Agent header indicator) and
  keeps the last one in `State.model`. Each fallback is named in the conversation
  (`Fallback: OpenAI · gpt-x — reason`) and logged with the model requested, the model used, the
  reason, its tokens and its estimated cost.

**Ollama.** The provider speaks Ollama's native `/api/chat`, the only Ollama API that honors a
context window per request: the OpenAI-compatible endpoint ignores it, and Ollama silently drops
whatever does not fit in the window the model was loaded with (4096 by default), so the model
would get the system prompt and tool list cut in half (~16k tokens for a CAD turn). Each request
sends `num_ctx` (`OLLAMA_NUM_CTX`, default 32768, capped at the model's own maximum). The answer's
`prompt_eval_count` is the prompt the model actually read; when it falls below a lower-bound
estimate of the prompt (characters / 6, images excluded) the request is reported as a
`ContextExceededError` instead of answered, and a prompt that cannot fit at all is not sent. That
error, like `ErrUnavailable`, lets the router hand the request to the fallback. Capabilities
(vision, tools, thinking) come from `/api/show`, cached for 5 minutes (15 s after a failure), with
the name-based registry as the fallback and `OLLAMA_VISION` overriding vision.

Configuration: the primary slot is the provider variables SimonCAD always used; the fallback slot
is `SIMONCAD_FALLBACK_ENABLED`, `SIMONCAD_FALLBACK_PROVIDER`, `SIMONCAD_FALLBACK_CONSENT` and the
provider variables prefixed `SIMONCAD_FALLBACK_`, each inheriting the primary-slot value while
unset. A configuration that predates the slot is migrated in place, without rewriting it: the
first complete cloud provider other than the primary becomes the fallback, enabled, as before.

## What a turn sends

Tool definitions dominate the input of an agent turn: they are resent on every step of the tool
loop, and the whole catalog is ~18k tokens against ~2.8k for the system prompt and ~100 for the
project state. A capability whose tools cannot be used is therefore paid for on every call.

`runtime.SelectCaps` keeps only the capabilities worth sending. A capability opts in by
implementing `runtime.Conditional`; one that does not is always loaded, so the geometry
capability needs no change. `vision` and `visual` declare themselves relevant when the turn
attaches an image, the project has one uploaded, an interpretation exists, or (for `visual`)
design parameters, regions or references exist — the same condition each one already used to
decide its `BuildContext` had nothing to report. A plain modeling project therefore sends ~14.5k
tokens per call instead of ~21k, and nothing is lost: the image tools reappear with the image.

The eval suite keeps the full catalog deliberately, so its numbers stay comparable across
projects.

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
