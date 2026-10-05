# Robot backlog

Gaps found while dogfooding real robot parts. An item stays here until a robot requirement
needs it; items without a robot requirement are deprioritized.

## Servo bracket (dogfood 1, Phase 1) — `testdata/robot/servo_bracket`

Re-created through `create_box → fillet → extrude_profile(cut) → add_hole`; matches the Phase 0
hand-built geometry to 1e-9 relative volume with identical face count.

| Gap | Robot requirement | Planned |
|---|---|---|
| A servo bracket takes four tool calls and hand-computed pocket/ear coordinates | "Mount the shoulder servo" should need no dimensions | `ServoMount` macro + MG996R catalog entry (Phase 3) |
| `fillet`/`chamfer` act on every edge, so rounding the outer edges must happen *before* cutting the pocket | Round only external plate edges after pocketing | Edge selectors (`edges(feature=…, kind=external)`) in Phase 2 |
| Selectors resolve from bounding boxes: exact for prismatic parts, wrong for a face of a filleted or pocketed body | Select the pocket floor or a bracket ear | Topology-backed selectors with pick IDs (Phase 2) |
| Holes can only start from axis-aligned planar faces | Angled gussets, servo horns | Revisit when a robot part needs it |
| No visual check of a ChangeSet | Judge a bracket before committing | Preview overlay (Phase 2) |

## Deferred by scope (no robot requirement yet)
Loft, sweep, shell, advanced sketch constraints, splines.
