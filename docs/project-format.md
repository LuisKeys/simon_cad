# Project format: `.scadproj`

A project is a **directory** of canonical JSON (sorted keys, 2-space indent, trailing newline),
so it diffs well, is readable by humans and agents, and is never binary-only.

```
my_part.scadproj/
  project.json         metadata and schema version
  model.json           the CAD-IR (authoritative)
  components.json      catalog instances (empty until Phase 3)
  constraints.json     reserved
  materials.json       reserved
  assets/materials/    project-specific visual materials (<id>/material.json + textures); optional
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
  → refusal; older → `persistence.migrate`. v2 (current) adds `assets/images/` and
  `agent-history.json.interpretations`; both are optional, so v1 projects load as they are.
- **Self-verifying:** `simoncad-cli replay` re-derives the model from the recorded ChangeSets and
  checks it equals `model.json`.

## `project.json`

```json
{ "schema_version": 1, "name": "servo_bracket", "units": "mm",
  "occt_version": "7.8.1", "created": "2026-01-02T03:04:05Z" }
```
`occt_version` records the kernel used, for the determinism contract (same project + same OCCT
version ⇒ same geometry).

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
`manufacturing`, `component`, `selection_refs`, `suppressed`, `errors`. `status` and `errors` are
**derived** by rebuild and refreshed on every save; they are excluded from the content hash.

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
"SetExpression" | "Reorder" | "Suppress" | "Rename" | "RestoreSnapshot", "data": { … } }`;
`rollback` holds the exact inverses in forward order.

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
