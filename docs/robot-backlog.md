# Robot backlog

Gaps found while dogfooding real robot parts. An item stays here until a robot requirement
needs it; items without a robot requirement are deprioritized.

## Servo bracket (dogfood 1, Phase 1) — `testdata/robot/servo_bracket`

Re-created through `create_box → fillet → extrude_profile(cut) → add_hole`; matches the Phase 0
hand-built geometry to 1e-9 relative volume with identical face count.

| Gap | Robot requirement | Planned |
|---|---|---|
| A servo bracket takes four tool calls and hand-computed pocket/ear coordinates | "Mount the shoulder servo" should need no dimensions | `ServoMount` macro + MG996R catalog entry (Phase 3) |
| `fillet`/`chamfer` act on every edge, so rounding the outer edges must happen *before* cutting the pocket | Round only external plate edges after pocketing | **Done (Phase 2):** `fillet`/`chamfer` take `edges: [edge(feature=…,index=N)]`; edges are pickable. Semantic `kind=external` selection is still open |
| Selectors resolve from bounding boxes: exact for prismatic parts, wrong for a face of a filleted or pocketed body | Select the pocket floor or a bracket ear | **Done (Phase 2):** topology-backed selectors (`side`/`at`/`index`) with pick IDs |
| Holes can only start from axis-aligned planar faces | Angled gussets, servo horns | Revisit when a robot part needs it |
| No visual check of a ChangeSet | Judge a bracket before committing | **Done (Phase 2):** preview ghost with diff list |

## Elbow joint (dogfood 2, Phase 2) — `testdata/robot/elbow_joint`

A clevis block with a link slot, an 8 mm pivot bore through both ears and four M4 base holes; matches
its analytic volume to 1e-6 and is a valid solid.

| Gap | Robot requirement | Planned |
|---|---|---|
| The forearm link and the pivot pin are separate parts; nothing relates their frames or checks the joint's range of motion | A revolute joint between clevis and link | Assembly graph, joints and kinematics (Phase 6) |
| The pivot bore position is computed by hand (`[20, 0, 14]`) | "Center the pivot in the ears" | `ShaftSupport` / `BearingSeat` macros and an `align`/`center` intent (Phase 3) |
| No bearing seat or retaining feature for the pin | A 608 bearing pressed in each ear | `BearingSeat` macro + catalog (Phase 3) |
| Ear thickness and slot clearance are not checked against print tolerance | Print-ready slot clearance | Material/tolerance profiles and wall-thickness analysis (Phase 3–4) |
| Selecting "the external vertical edges" needs one shift-click per edge | Round all outer corners | A `kind=external` edge-set selector (open; no part needs it often enough yet) |

## Deferred by scope (no robot requirement yet)
Loft, sweep, shell, advanced sketch constraints, splines.
