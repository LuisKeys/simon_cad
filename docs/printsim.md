# Print Simulation

Print Simulation is a native SimonCAD mode that slices the current model, plays the slicer's toolpaths
back as a 3D printing animation in the same viewport, and (planned, see [Roadmap](#roadmap)) checks the
design for printability and lets the agent propose fixes.

> **Simulation, not manufacturing validation.** The simulation plays back toolpaths. It does not predict
> adhesion, warping, thermal behaviour or hardware faults, and it is not a guarantee that a part will
> print. The default machine is a generic *simulation profile*, not a verified printer profile. The
> product says so wherever a result is shown.

## What is implemented

| | Status |
|---|---|
| Print profile model, validation, generic FDM simulation profile | done |
| Provider-independent slicer interface, PrusaSlicer adapter (subprocess) | done |
| Built-in preview slicer (no external dependency) | done |
| G-code interpreter → normalized toolpaths, explicit handling of unsupported commands | done |
| Background jobs: cancellation, stale-result protection, cache by geometry revision and settings | done |
| Print Simulation mode: build plate, animated nozzle, progressive filament, timeline, layer inspection | done |
| Three visualization presets (Realistic / Technical / Diagnostics palette), filters, original-model overlay | done |
| Printability analysis (`PrintabilityAnalyzer`), issue highlighting | planned (phase 3) |
| Agent tools, Critic integration, approved fixes with undo | planned (phase 4) |
| Cinematic camera, printer frame/gantry, material polish | planned (phase 5) |

## Using it

1. Design a part. Click **Print** in the top bar (or `Ctrl+K` → *Print simulation*).
2. The viewport switches to the printer: a build plate with millimetre marks, the model's toolpaths, and a
   hotend. The **Print** tab opens in the right panel and slicing starts immediately; a cached result
   returns at once.
3. Use the bottom bar: play / pause / restart, speed `1x 10x 50x 100x 500x`, a time slider, a layer slider,
   *All layers / Up to layer / Single layer*, estimated time and filament. `Space` plays and pauses,
   `[` and `]` step layers. Orbit, pan and zoom work during playback.
4. Change settings (layer height, walls, infill, pattern, supports, adhesion, temperatures, speeds, machine)
   and press **Slice again**. Presets, filters and the overlay change instantly: they never re-slice.
5. Press **Print** again to leave. The CAD scene, selection and undo history are exactly as you left them.

If the model changes while the simulation is open, the panel says the slice is stale (the result carries
the geometry revision it was made from).

### Slicers

| Provider | When | Notes |
|---|---|---|
| `prusaslicer` | `prusa-slicer` on `PATH`, the macOS app bundle, or `SIMONCAD_PRUSASLICER=/path/to/binary` | production toolpaths: supports, bridges, gyroid/honeycomb infill |
| `builtin` | always | quick preview: perimeters + rectilinear/grid infill, solid top/bottom, skirt/brim; **no supports, no bridges** |
| `auto` (default) | | PrusaSlicer if available, otherwise the preview slicer, with a visible note |

The slicer path is read from the environment once at startup and is deliberately *not* accepted over HTTP:
a web page must never choose which program the server runs. A missing slicer produces an actionable message
(install hint per OS) and the preview slicer remains usable.

## Architecture

```
ui.Service ──PrintShape()──▶ job.Manager ──▶ meshprep ──▶ slicing.Slicer ──▶ toolpath.SliceResult
 (HTTP + SSE)   read-only      cancel,        STL on the     prusaslicer        normalized, slicer-
                copy of the    cache,         bed, bounds    (+ gcode parser)   independent
                result shape   stale guard    checked        builtin
                                                                   │ Encode (40 B / segment)
                                                                   ▼
        frontend: decode ─▶ Playback (pure) ─▶ PrintSim (Three.js, same scene) ─▶ instanced filament
```

Backend packages (`internal/printsim/…`, pure Go, **no cgo**, so they build and test without OpenCASCADE):

| Package | Responsibility |
|---|---|
| `profile` | `PrintProfile`, `GenericFDM()` (labelled as a simulation profile), `Validate()`, `Hash()` over the slicing-relevant fields only |
| `toolpath` | `SliceResult`/`Layer`/`Segment`, deterministic timing, indexed lookups, binary `Encode`/`Decode` |
| `gcode` | state-machine interpreter (see below) |
| `meshprep` | exports the shape, validates it, centres it on the bed with its lowest point on the plate; the CAD geometry is never modified |
| `slicing` | `Slicer` interface, registry/selection; `prusaslicer/`, `builtin/` adapters |
| `job` | one job at a time, cancellation, generation guard, LRU cache, temp-dir cleanup |
| `testmesh` | tiny STL builders for tests |

Integration points in existing code (all additive):

- `internal/app/view.go` — `App.PrintShape()`: a read-only copy of the result solid plus its geometry
  revision. The UI layer may not touch `App.K` (enforced by `nomutation_test.go`), so this is the only door.
- `internal/ui/printsim.go` + routes in `server.go` + `Notice.PrintSim` in `hub.go`.
- `frontend/viewport.js` — `simMode`, `boundsOverride`, `frameHooks` (three small hooks, below).
- `frontend/viewlayout.js` — `ViewPane.pose()/restorePose()` so the user's camera comes back.

### Decisions

- **One renderer, one scene.** The printer is a `Group` added to the existing Three.js scene. Entering the
  mode hides the CAD bodies (their geometry stays loaded, nothing is rebuilt); leaving removes the group and
  disposes every GPU resource it made. The e2e test asserts geometry and texture counts return to baseline.
- **The CAD model stays authoritative.** Slicing reads a copy (`PrintShape`) and never writes. The original
  overlay *shares* the CAD `BufferGeometry` objects; nothing is duplicated.
- **Wire format.** Toolpaths travel as one little-endian binary buffer (header, layer table, 40-byte
  segments), not JSON. The browser uses the segment block as an interleaved instance buffer without copying.
  A golden file (`internal/printsim/toolpath/testdata/sample.scps`) is encoded by Go and decoded by the
  Node test, so the two sides cannot drift.
- **Deterministic time.** Durations are recomputed as `length / feed rate` for every provider, so the
  timeline behaves identically whichever slicer ran. The slicer's own estimate is shown beside it.
- **Filament rendering.** One instanced draw call: each segment is an instance of a small cylinder that the
  vertex shader stretches to its segment (`frontend/printsim-shader.js`). Playback time, layer range, kind
  mask, colours and the "completed only" flag are uniforms; the visible prefix is `instanceCount`. Seeking
  is a binary search over start times plus a uniform update — nothing is replayed or rebuilt. A matching
  depth material makes the filament cast correct shadows.
- **Cache key** is `(GeometryHash, Profile.Hash, provider)`. Appearance-only edits keep the revision, so a
  colour change does not invalidate a slice; any geometry edit does.
- **Why PrusaSlicer first.** One binary with a stable CLI (`--export-gcode --load cfg.ini`), packages for
  Linux and macOS, and `;TYPE:` role comments in its G-code. CuraEngine needs a chain of machine/extruder
  definition files and is harder to distribute on macOS. Both are behind the same interface; adding
  CuraEngine later means one adapter.

### G-code semantics

The interpreter (`internal/printsim/gcode`) tracks absolute/relative XYZ (`G90/G91`), absolute/relative
extrusion (`M82/M83`), `G92` offsets, retraction debt (a retract followed by an unretract does not widen the
next bead), modal feed rate, the active tool (`T`), layer markers (`;LAYER_CHANGE`, Cura `;LAYER:`; otherwise
a deposition at a new height), and feature roles (`;TYPE:` for PrusaSlicer and Cura names). `G2/G3` arcs
(I/J form) are discretized within 0.05 mm.

Nothing is silently misread:

- **Fails the parse:** inch units (`G20`), other arc planes, `R`-form arcs, splines, work-coordinate
  systems, probing moves, malformed words (`G1 X` with no value, or text where a word should be), absurd
  coordinates, unknown `G` commands, and input over the size, line-length or segment limits.
- **Reported as diagnostics:** every command that was *not* simulated (temperatures, fans, firmware
  retraction `G10/G11`, unknown `M` codes, unrecognized `;TYPE:`), once per code with a count and first line.

## Safety and reliability

- No printer hardware is ever contacted; G-code is parsed, never executed or streamed.
- The slicer and its G-code are untrusted input: bounded stdout/stderr capture, G-code size/line/segment
  limits, coordinate range checks, no shell (fixed argument vector with paths we created), private temp
  directory per job (always removed), the slicer's data directory redirected into it.
- Wall-clock timeout per run (10 min) and cancellation that kills the slicer's **whole process group**
  (tested with a child that would otherwise outlive its parent). A memory limit is *not* applied: an address-space
  rlimit breaks GUI-linked binaries such as PrusaSlicer. See [Known limitations](#known-limitations).
- A starting job cancels the previous one; a superseded job can never replace a newer result or announce
  itself (generation guard); its result is still cached under its own key. Project switch and shutdown cancel
  and wait for jobs *before* the kernel closes.
- Invalid models give plain messages: not watertight (with edge counts), inside-out, empty, larger than the
  build volume (with both sizes), failing features, slicer missing, slicer error text, timeout.

## HTTP API

Same token/Host protection as the rest of the API. Notices arrive on the existing SSE stream as
`{"kind":"printsim","printsim":{job,state,message,provider,error,cached,summary}}`.

| Route | |
|---|---|
| `GET /api/printsim/info` | default profile, provider status, current `geom_hash`, whether there is geometry |
| `POST /api/printsim/slice` `{profile?, provider?}` | 202 `{job, cached}`; errors (bad profile, missing slicer, no geometry) are 400 with a message |
| `POST /api/printsim/cancel` | stops the running job |
| `GET /api/printsim/result/{job}` | `application/octet-stream` toolpath; 404 for failed, cancelled or replaced jobs |

States: `queued → preparing → slicing → parsing → done | error | cancelled`.

## Third-party dependencies and licences

| Component | Licence | How it is used | Obligation |
|---|---|---|---|
| PrusaSlicer | AGPL-3.0 | run as a **separate program** the user installed; invoked through its CLI; never linked, bundled or redistributed by SimonCAD | none for SimonCAD beyond not distributing it with the app. If it is ever bundled (installer, container), review AGPL §13 and source-offer duties first. |
| CuraEngine (not integrated) | AGPL-3.0 | would be the same pattern | same |
| Three.js (already vendored) | MIT | rendering | existing notice in `frontend/vendor/THREE-LICENSE` |
| Everything new in Go/JS | project licence | — | **no new Go modules or JS packages**; `go.mod` still has no requirements and the front end still has no build step |

Output G-code is data produced by the user's own run of the slicer; SimonCAD only reads it.

## Performance (measured)

Measured on this development machine (Linux, 24 hardware threads). Reproduce with
`go test -run xxx -bench . ./internal/printsim/...` and `node` against `frontend/printsim-model.js`.

| Stage | Input | Result |
|---|---|---|
| G-code parse | 100k / 500k / 1M moves | 41 ms / 180 ms / 357 ms (≈ 1 alloc per line) |
| Built-in slice | 80 mm tube, 20 / 60 / 120 mm tall (0.2 mm layers) | 79 ms / 226 ms / 468 ms for 100k / 291k / 579k segments |
| Wire size | any | 40 bytes per segment (1M segments ≈ 38 MiB) |
| Client decode | 100k / 500k / 1M segments | 5 ms / 5 ms / 6 ms |
| Seek | up to 1M segments | ≈ 0.3 µs (binary search) |
| GPU memory | per segment | 48 bytes (40 interleaved + 8 meta) ≈ 46 MiB per 1M segments |
| Draw calls | 8.9k-segment demo | 1 instanced call for all filament (6 calls total in a frame including the shadow pass) |
| Demo slice, end to end | 40×30×8 mm part with a hole, preview slicer | under 1 s from click to playable |

**Frame rate on a real GPU has not been measured**: this environment only has a software rasterizer
(SwiftShader), which ran the 8.9k-segment demo at ≈ 12 fps — that number says nothing about hardware.
Measure on the target machine with `await simonPrintSim.sim.benchmark()` in the browser console (plays 4 s at
100x and returns fps, mean/p95/worst frame time, draw calls and triangles). Triangle cost scales with the
bead count: 12 sides per bead up to 80k segments, 8 up to 250k, 6 beyond that.

## Testing

- Go: `profile`, `toolpath` (incl. golden file), `gcode` (state machine, arcs, retraction, limits,
  cancellation), `meshprep`, `slicing` registry, `prusaslicer` (fake slicer script: argument isolation,
  failure text, timeout, **process-tree cancellation**, config contents), `builtin` (cube/tube/islands/
  skirt/brim/retractions/determinism), `job` (cache keys and eviction, cancellation, stale job, errors,
  `Release` exactly once, temp dir cleanup — stress-run with `-race`), and `ui` (HTTP flow, no model change, no
  kernel-shape leak, rejection paths). `TestRealPrusaSlicerIfInstalled` is skipped unless PrusaSlicer exists.
- Node: `frontend/printsim-model.test.mjs` — decode (golden file), seek forward/backward, speeds, pause/
  resume/restart, end-of-print, layer modes, kind mask, formatting, framing.
- Browser: `e2e/printsim.mjs` (`make e2e`) — enters from a real model, slices, plays, seeks, inspects a layer,
  switches presets without re-slicing, overlay shares CAD geometry, orbit works, stale detection, leaving
  restores the CAD scene and releases GPU resources, history untouched, no console or shader errors.
  Screenshots land in `out/e2e/printsim-*.png`.

## Known limitations

- **Preview slicer** is an approximation: simple miter offsets (thin features lose inner walls), no supports,
  bridges, seams, ironing, adaptive layers or top-surface detection under sparse infill (solid layers are only
  the first/last N). Infill patterns other than rectilinear and grid are drawn as rectilinear.
- Only the first PrusaSlicer-compatible dialect (`;TYPE:`, `;LAYER_CHANGE`, Cura names) is mapped; other
  roles become *Other* with a diagnostic. Start-up purge lines print as an extra first "layer".
- Firmware retraction (`G10/G11`) lengths are unknown to the simulation and not modelled; multi-extruder files
  record the tool but are drawn in one colour.
- Preview slicer, more: top/bottom skins use the part's global first/last layers (a stepped part's lower top
  surfaces get sparse infill), skirt/brim rings are per island and can cross a neighbour closer than 3–5 mm,
  and cancellation is checked once per layer.
- Up to four results stay cached; a pathological 8M-segment result costs gigabytes (segments + encoded copy).
- A cache hit shows the profile label of the earlier run (labels and analysis thresholds are not in the key).
- No memory cap on the slicer subprocess (time-limited and cancellable only).
- The slicer path comes from `SIMONCAD_PRUSASLICER` (or `PATH`); there is no settings screen for it yet.
- Realistic mode uses one filament colour and a basic hotend model; the printer frame/gantry, cinematic
  camera and richer materials are phase 5. The moving nozzle does not cast a shadow (the shadow map is
  refreshed ≈ 4×/s while playing, which is right for the part but not for a moving object).
- macOS: the code paths (app-bundle lookup, process groups) are written for it but this was developed and
  tested on Linux only.

## Roadmap

Phases 3–5 are specified in the implementation plan:

3. **Printability analysis** — a `PrintabilityAnalyzer` independent of the visualization: geometry integrity
   (non-manifold, open boundaries, degenerate triangles, normals, self-intersections, shells), build-volume
   and placement checks, and FDM feasibility (thin walls, overhangs, bridges, islands, small bed contact,
   tall/narrow parts, sub-nozzle features), each as a `PrintIssue` with severity, confidence, region and a
   *verified vs heuristic* flag; issues highlighted on the original model with camera focus.
4. **Agent and Critic** — `analyze_printability`, `slice_model`, `get_print_issues`, `get_print_profile`,
   `simulate_print`, `propose_print_fixes` registered in the existing tool registry; fixes are ordinary
   modelling tool calls that go through ChangeSets (approval, undo/redo), followed by automatic re-analysis
   and re-slice and a before/after comparison; a Critic that separates verified errors from heuristic risks.
5. **Cinematic presentation** — printer frame and gantry, camera presets, material polish, LOD for very large
   toolpaths, and a documented GPU performance pass.
