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
  assets/images/       images attached to a prompt, or retained by the user (<id>.png|jpg|webp)
  assets/documents/    attached reference documents (<id>.<ext>, plus <id>.extracted.md for a PDF)
  assets/meshes/       imported STL meshes in canonical binary form (mesh_<hash>.stl)
  assets/steps/        imported STEP files, byte for byte (step_<hash>.step)
```

An example lives in [testdata/robot/servo_bracket.scadproj](../testdata/robot/servo_bracket.scadproj).

## Guarantees

- **Byte-stable:** saving the same state twice produces identical bytes, and
  `save → load → rebuild → save` is byte-identical (tested).
- **Atomic writes:** each file is written to a temp file and renamed.
- **Versioned:** `project.json.schema_version` is checked on load. Newer than the build supports
  → refusal; older → `persistence.migrate`. v2 adds `assets/images/` and
  `agent-history.json.interpretations`; v3 adds the project `id` and the reusable-component
  metadata; v4 adds `workspace.json`; v5 adds `assets/documents/` with its
  `agent-history.json.documents` index and `prompts[].documents`, and stores every image attached to
  a prompt; v6 adds `assets/meshes/`; v7 (current) adds `assets/steps/`. Every addition is optional, so v1–v6 projects load
  as they are.
- **Self-verifying:** `simoncad-cli replay` re-derives the model from the recorded ChangeSets and
  checks it equals `model.json`.

## `project.json`

```json
{ "schema_version": 6, "id": "prj_9f31c4a0b72e", "name": "servo_bracket", "units": "mm",
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
| `prompts` | `id` (`pr_0001`), `time`, `text`, `selection`, `capability`, `model`, `agent_summary`, `images` (ids under `assets/images/`), `documents` (ids of the documents first sent with it) |
| `documents` | `id` (`doc_<hash>`), `name`, `file` (`assets/documents/<id>.<ext>`), `extracted` + `extracted_by` (text a model extracted from a PDF), `active` (still attached to the conversation) |
| `tool_calls` | `id` (`tc_0001`), `prompt_id`, `actor` (user/agent/cli), `name`, `args`, `status` (`ok`/`rejected`/`invalid`), `error`, `changeset_id`, `result` |
| `changesets` | `id` (`cs_0001`), `seq`, `state` (`proposed`/`committed`/`rejected`/`undone`), `intent`, `explanation`, `commands`, `rollback`, `affected`, `expected_effects`, `warnings`, `validation`, `provenance`, `base_hash`, `result_hash`, `time` |
| `checkpoints` | `id` (`cp_001`), `seq`, `label`, `actor`, `changesets`, `graph_hash`, `time`, `snapshot` (full CAD-IR) |

`tool_calls[].status`: `rejected` = failed schema/semantic validation before any ChangeSet existed;
`invalid` = a ChangeSet was proposed but failed validation (e.g. the rebuild would fail) and was
discarded.

`interpretations` (optional) holds the image proposals of the vision capability (entities in pixel
space, dimensions with confidence, operations, revision). They explain image-derived features but are
never needed to rebuild: nodes keep `provenance.source_image_id` / `interpretation_id` as plain
references, and the geometry is fully described by `model.json`.

Attachments live inside the project. Every image attached to a prompt (and any the user retains) is
written to `assets/images/`, and every reference document that is still attached or that a sent
prompt shows is written to `assets/documents/`; anything else is removed on save. A project that
already has a directory gets its copy the moment something is attached; a new project gets it on its
first save. From then on chat previews (`/api/image/<id>`, `/api/document/<id>`) and attachment
processing (PDF extraction) read the project's copy, never the file the user picked. Paths in the
`documents` index must stay directly inside `assets/documents/`, or the project is refused.

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

## Imported STEP files: `assets/steps/`

An `import_step` tool call stores the file as it came, as `assets/steps/step_<hash>.step`, and adds
an `import_step` node whose `step` parameter is that id (plus `placement` and the original file name
in `source`). Unlike a mesh, a STEP file keeps its exact B-Rep (planes, cylinders, B-spline
surfaces), so the body behaves like a modelled one: fillets, holes and face selectors work on it.
The file declares its own length unit and the reader converts it to millimetres, so the node has no
`units`. The id is the first 24 hex digits of the SHA-256 of the file, so a stored file that no
longer matches its id is refused (the node fails to rebuild) instead of silently changing the model.
Before it is stored (`internal/stepimport`) the kernel must read it, judge the geometry valid and
find at least one solid with a positive volume; unreadable, invalid, surface-only and empty files are
refused with the reason and nothing is stored. An assembly with several solids becomes one body that
holds all of them. A save writes the files the graph still references and removes the rest. A
reusable component carries its own files.

## Imported meshes: `assets/meshes/`

An `import_stl` tool call stores the validated mesh as `assets/meshes/mesh_<hash>.stl` and adds an
`import_mesh` node whose `mesh` parameter is that id (plus `units`, `placement` and the original
file name in `source`). The file is **canonical**: binary STL with a fixed header, normals recomputed
from the winding and zero attribute bytes, so the same model imported as ASCII or binary has the same
id. The id is the first 24 hex digits of the SHA-256 of the file, which makes it part of the node's
content key and of the graph hash, and means a stored mesh that no longer matches its id is refused
(the node fails to rebuild) instead of silently changing the model. A save writes the meshes the graph
still references and removes the rest. A reusable component carries its own meshes.

An OBJ goes through the same path (`import_obj`, or the Import button) and, like an STL, has no units. Only
`v` and `f` statements are read (positive or negative indices, `v/vt/vn` forms; quads and larger faces are
triangulated by ear clipping in their own plane); normals, texture coordinates, groups and materials are
ignored, and free-form curves or surfaces, unknown statements, bad indices and faces with no area are
refused. The same solid as OBJ and STL shares one content id when their triangles are the same.

A 3MF goes through the same path (`import_3mf`, or the Import button): `internal/meshimport` reads the
ZIP, finds the root model part through `_rels/.rels`, applies component trees and build transforms
(a mirroring transform reverses the winding), and converts the declared unit (micron to meter) to
millimetres on reading, so the stored mesh is in mm and the node keeps `units: mm`. An STL and a 3MF of
the same solid therefore share one content id. Colours and materials are ignored; external model
parts (production extension), support/surface objects, unknown required extensions, a bomb-sized
model part and more than the triangle limit are refused. Several build items are merged and then
validated as one mesh, so separate bodies are refused as several shells.

What an STL must satisfy to be imported (`internal/meshimport`): one closed shell where every edge is
shared by exactly two triangles walked in opposite directions (no holes, no flipped or duplicated
triangles, no non-manifold edges or vertices), no degenerate triangles or sub-micron edges, no two
non-adjacent triangles crossing or touching, a positive volume (a mesh that is wholly inside-out is
flipped and reported), finite sane coordinates and at most 100 000 triangles. The reasons for a
refusal are returned to the caller and nothing is stored. A mesh whose only fault is holes can be
imported with `fill_holes`, which closes every loop of open edges with triangles (ear clipping in the
hole's mean plane, a fan from the centroid when that fails) before the checks above run on the result:
it refuses holes of more than 3 000 border edges or patches that would add more than half the mesh's
surface, and what it closed is reported. The accepted mesh is turned into a B-Rep
solid by `occ_import_mesh`, which shares vertices and edges between faces (no sewing), merges
coplanar triangles into faces when that yields a valid solid, and checks the result's validity and
volume against the mesh's.

### Reducing a mesh

`decimate_mesh` (or `decimate_to` on `import_stl`) really reduces the triangles of an imported mesh,
unlike `set_mesh_detail`, which only changes how finely the viewport draws the same solid. Edges are
collapsed in order of the quadric error they add (Garland & Heckbert), never when the collapse would
change the topology (link condition) or fold a neighbouring triangle, so flat faces and sharp corners
survive: a cube whose faces were subdivided comes back as the same cube, and a 72 000-triangle sphere
goes to 10 000 in about 0.2 s with a volume change under 0.1 %. The result is validated with the same
checks as an import; if the reduction makes the surface cross itself it is retried with 30 % more
triangles (up to three times) before it is refused, and a mesh that cannot be reduced as far as asked
says so. The result is a new content-addressed mesh and the node's `mesh` parameter changes, so undo
restores the original (which stays in memory, and is not saved once nothing references it).

### Waiting for a long import

Turning triangles into a kernel solid costs about 0.4 ms each (a 72 000-triangle mesh takes about 30 s),
so the UI shows a modal "please wait" dialog while an import runs: it locks the interface, names the
stage the server reports (`progress` notices on the event stream: reading and checking, building the
solid) and draws a bar that follows the stage's estimated duration, never reaching 100 % before the
server answers. It cannot be cancelled.
