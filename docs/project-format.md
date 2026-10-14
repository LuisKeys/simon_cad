# Project format: `.scadproj`

A project is a **directory** of canonical JSON (sorted keys, 2-space indent, trailing newline),
so it diffs well, is readable by humans and agents, and is never binary-only.

```
my_part.scadproj/
  project.json         metadata and schema version
  model.json           the CAD-IR (authoritative)
  components.json      derived list of the reusable projects this one depends on
  constraints.json     reserved
  materials.json       reserved
  assets/materials/    project-specific visual materials (<id>/material.json + textures); optional
  workspace.json       display units, grid and snapping (presentation; never an input to geometry)
  agent-history.json   prompts, tool calls, ChangeSets, checkpoints
  events.jsonl         append-only event recorder, one JSON object per line
  thumbnails/          reserved (.gitkeep)
  assets/images/       source images, only if explicitly retained (<id>.png|jpg|webp)
```

An example lives in [testdata/robot/servo_bracket.scadproj](../testdata/robot/servo_bracket.scadproj).

## Guarantees

- **Byte-stable:** saving the same state twice produces identical bytes, and
  `save → load → rebuild → save` is byte-identical (tested).
- **Atomic writes:** each file is written to a temp file and renamed.
- **Versioned:** `project.json.schema_version` is checked on load. Newer than the build supports
  → refusal; older → `persistence.migrate`. v2 adds `assets/images/` and
  `agent-history.json.interpretations`; v3 adds the project `id` and the reusable-component
  metadata; v4 (current) adds `workspace.json`. Every addition is optional, so v1–v3 projects load
  as they are.
- **Self-verifying:** `simoncad-cli replay` re-derives the model from the recorded ChangeSets and
  checks it equals `model.json`.

## `project.json`

```json
{ "schema_version": 4, "id": "prj_9f31c4a0b72e", "name": "servo_bracket", "units": "mm",
  "occt_version": "7.8.1", "created": "2026-01-02T03:04:05Z",
  "reusable": true, "description": "the bracket a servo bolts to", "tags": ["robotics"],
  "component_version": 4 }
```
`occt_version` records the kernel used, for the determinism contract (same project + same OCCT
version ⇒ same geometry).

`id` is the stable identity a component instance references: it survives renaming the directory,
which the directory name does not. A project that predates v3 gets one derived from its name and
creation time, so migrating it twice yields the same id. "Save as" mints a new id and records the
old one in `derived_from`, so two directories never claim one identity.

`reusable` makes the project a **component**: it appears in the component library and other
projects may instantiate it. There is no separate component concept — a component is a reusable
project. `component_version` advances on a save that changed the content.

## `model.json` — the CAD-IR

`{ "units": "mm", "nodes": [ … ] }`. Node order is significant (evaluation tie-break). Example node:

```json
{
  "id": "ear_holes",
  "type": "hole",
  "parameters": { "diameter": 3.2, "target": "servo_pocket",
                  "positions": [[10.25, 15], [10.25, 25], [59.75, 15], [59.75, 25]] },
  "dependencies": ["servo_pocket"],
  "intent": "M3 clearance holes for the servo mounting ears",
  "provenance": { "created_by": "cli", "tool_call_id": "tc_0004", "changeset_id": "cs_0004" },
  "metadata": { "name": "ear_holes", "created": "…", "modified": "…" },
  "status": "ok"
}
```
Optional fields appear only when set: `expressions`, `references`, `constraints`,
`manufacturing`, `component`, `selection_refs`, `suppressed`, `appearance`, `group`,
`lock_proportions`, `errors`.

A **component instance** is an ordinary node of type `component_instance`. Its geometry is not
stored here: the node holds a reference and a placement, and the geometry is resolved from the
referenced project at rebuild time.

```json
{
  "id": "wheel_fl",
  "type": "component_instance",
  "parameters": { "component": "prj_7f2a91c0de45", "version": "latest",
                  "content_hash": "a3f91c…", "dx": 120, "dy": -80, "angle_deg": 0 },
  "metadata": { "name": "Wheel FL" }
}
```

`content_hash` pins the instance to a specific state of the component. Keeping it in
`parameters` is what makes a component edit visible to the rebuild cache, to `Graph.Hash()`,
to checkpoints and to the stale-selection check — all of which would otherwise miss it entirely.
Re-pinning (`sync_component`) is an ordinary, undoable `SetParameter` ChangeSet. v1 keeps no
archive of past versions, so what is on disk is always what gets built; a pin that no longer
matches is *reported* as out of date rather than silently resolving to something else. That is
the limitation explicit version locking (`"version": "v3"`) will lift.

Resolution is recursive and has no depth limit: a component may contain components. Circular
references are refused, both when inserting and when resolving.
`group` is a design-tree group label (organization only, like `appearance` never an input to
geometry); older projects simply have none. `lock_proportions` (`true` when set) is the object's
dimension-editing preference: a width/depth/height edit then scales all three. It is never an
input to geometry either and is excluded from `GeometryHash`.

A dimension edit (`set_object_dimension`, `resize_object`) is stored as an ordinary `scale` node
tagged `"tags": ["resize"]` in its metadata, with literal `sx`/`sy`/`sz`, its anchor
(`about: center`, or `about: point` with the object origin resolved into `ox`/`oy`/`oz`) and, for
a rotated object, its local frame as an axis-angle rotation (`frame_ax`, `frame_ay`, `frame_az`,
`frame_angle_deg`): the scale acts along the frame's axes. `status` and `errors` are
**derived** by rebuild and refreshed on every save; they are excluded from the content hash.

## `workspace.json`

The project's display units, reference grid and snapping. Lengths are canonical millimetres and
angles degrees, whatever the display units; `translation_increment: null` follows the grid spacing.

```json
{
  "schema_version": 1,
  "units": { "linear": "mm", "angular": "deg", "linear_precision": 2, "angular_precision": 1 },
  "grid": { "enabled": true, "visible": true, "plane": "XY", "spacing": 10, "major_interval": 10,
            "extent": 1000, "origin": [0, 0, 0] },
  "snap": { "enabled": false, "grid": true, "translation": true, "rotation": false,
            "rotation_increment": 15, "translation_increment": null }
}
```

`extent` is the side of the square grid, centred on `origin`. Reading is lenient: unknown fields
are ignored, missing ones take the defaults, and a section that is malformed or invalid falls back
to the defaults as a whole (the project still opens). A project without the file (saved before v4)
opens with the factory defaults shown above and gains the file on its next save. The units of
`model.json` stay `mm`: display units never rescale geometry.

## `agent-history.json`

The full intent → tool call → ChangeSet → checkpoint trace. Every mutation of `model.json` is
traceable here.

| Key | Entries |
|---|---|
| `prompts` | `id` (`pr_0001`), `time`, `text`, `selection`, `capability`, `model`, `agent_summary` |
| `tool_calls` | `id` (`tc_0001`), `prompt_id`, `actor` (user/agent/cli), `name`, `args`, `status` (`ok`/`rejected`/`invalid`), `error`, `changeset_id`, `result` |
| `changesets` | `id` (`cs_0001`), `seq`, `state` (`proposed`/`committed`/`rejected`/`undone`), `intent`, `explanation`, `commands`, `rollback`, `affected`, `expected_effects`, `warnings`, `validation`, `provenance`, `base_hash`, `result_hash`, `time` |
| `checkpoints` | `id` (`cp_001`), `seq`, `label`, `actor`, `changesets`, `graph_hash`, `time`, `snapshot` (full CAD-IR) |

`tool_calls[].status`: `rejected` = failed schema/semantic validation before any ChangeSet existed;
`invalid` = a ChangeSet was proposed but failed validation (e.g. the rebuild would fail) and was
discarded.

`interpretations` (optional) holds the image proposals of the vision capability (entities in pixel
space, dimensions with confidence, operations, revision). They explain image-derived features but are
never needed to rebuild: nodes keep `provenance.source_image_id` / `interpretation_id` as plain
references, and the geometry is fully described by `model.json`. Images are written to
`assets/images/` only when the user retains them.

Commands inside a ChangeSet are `{ "type": "AddFeature" | "RemoveFeature" | "SetParameter" |
"SetExpression" | "Reorder" | "Suppress" | "Rename" | "SetGroup" | "SetColor" | "SetOpacity" |
"SetVisibility" | "SetMaterial" | "RemoveMaterial" | "SetMaterialScale" | "ResetAppearance" |
"RestoreSnapshot", "data": { … } }`;
`rollback` holds the exact inverses in forward order.

## `components.json`

The project's component dependencies, **derived** on save from its `component_instance` nodes —
never maintained alongside them, so it cannot disagree with `model.json`:

```json
{ "components": [ { "project_id": "prj_7f2a91c0de45", "name": "Wheel FL",
                    "version": "latest", "content_hash": "a3f91c…",
                    "instances": ["wheel_fl", "wheel_fr", "wheel_rl", "wheel_rr"] } ] }
```

A component's geometry is never copied in here. Four wheels are four references to one project.

## `events.jsonl`

One event per line: `{"seq": 1, "type": "ChangeSetCommitted", "time": "…", "data": { … }}`.
`seq` continues across sessions. View with `simoncad-cli replay <project> --events`.

## Visual materials

A feature's `appearance` may carry `material_id` (a library id), `texture_scale` (multiplier > 0) and a
`color` override on top of the material. Only these references are stored: the built-in library
(`materials/` in the repository, embedded in the binary) is never copied into a project. A project may add
or override materials in `assets/materials/<id>/material.json`; they are carried along on "Save as".
A reference to a material that no longer exists loads fine and renders with the generic gray fallback; the id
is preserved. Visual materials are independent of any engineering material (density, strength) — the
mapping `EngineeringDefaults` only suggests a default look.
