# Performance

Targets for the path / sweep / creative-geometry engine and what was measured.

**Reference hardware:** Apple M3 Max (arm64), macOS, Go 1.27, OpenCASCADE 7.8.1. Numbers are wall-clock per
operation including creating the project, running the tool and building the model, from
`go test ./internal/app -run xxx -bench . -benchtime 5x` and
`go test ./internal/vectorize -run xxx -bench Vectorize1024`.

| Operation | Target | Measured |
|---|---|---|
| Simple tube sweep (5 control points, 80 mm) | < 100 ms | ≈ 53 ms |
| Rebuild of that sweep after editing a path point | < 100 ms | ≈ 55 ms |
| Trace a 1024 px image (one mode) | < 1 s | ≈ 42 ms |
| Simple stamp from a 300 × 400 px design, with handle | < 2 s | ≈ 51 ms |
| Extruded text "Simon CAD" (≈ 8 glyph regions) | – | ≈ 160 ms |

## What made the sweep fast

A profile of the sweep rebuild showed 75 % of the time in `occ_mass_props` (volume + area on the swept
B-spline faces, run two or three times per rebuild) and only 17 % in the sweep itself. Two changes:

1. `BRepGProp::VolumeProperties` / `SurfaceProperties` now integrate adaptively to a relative accuracy of
   1e-5 (≈ 2 ms instead of ≈ 25 ms each). On the swept tube the result differs from the fixed-order value by
   2e-8 relative; every volume assertion in the suite still holds.
2. `api.Shape.Props` is memoised: a shape is immutable, and the feature's own volume check, the rebuild and
   the tools all asked for the same numbers.

The remaining time is the bounding box (`AddOptimal`, ≈ 12 ms on B-splines, kept exact because selectors and
the UI depend on it) and the sweep itself (≈ 10–40 ms depending on length).

## Rebuild caching

The rebuild engine caches by content key: a path edit regenerates only the sweeps that depend on it, and
an unrelated edit regenerates nothing. Data nodes (`path`, `profile`) cost nothing to build.

## Cancellation and limits

- Tracing runs under a timeout (`vectorizeTimeout`) and takes a `context.Context`, so a long trace is
  abandoned instead of blocking the single kernel thread. A UI-triggered cancel button is not wired.
- Limits that protect the kernel: 300 traced shapes or 2000 loops per call, 400 characters per text,
  sweeps in runs of at most 10 sections.
- Known limitation: swept ridges thinner than ≈ 0.35 mm can produce invalid kernel geometry; the
  stamp pipeline therefore builds stroke ridges as a 2D union (`StrokesToRegions`) and extrudes once.

## Optimizations after the sweep work

Measured on a 24-thread Linux machine (not the M3 Max above), Go 1.27, OpenCASCADE 7.8.1, Chrome 155.
Each row is before → after, with the benchmark or test that shows it.

| Area | What changed | Before → after |
|---|---|---|
| Linear/circular pattern | The copies of the tool are united by balanced pairwise reduction instead of folding into a growing accumulator (`applyCopies`). `BenchmarkLinearPattern50/100` | 50 copies 256 → 143 ms; 100 copies 811 → 310 ms (the cost stopped being quadratic) |
| Selector topology | `Body.Topology()` takes the face/edge/vertex tables from the exact B-Rep (`occ_topology`) instead of tessellating a throwaway mesh. `TestTopologyMatchesTessellateWithoutTriangles` pins that the tables are identical; `TestTopologyIsMuchCheaperThanMeshingManyHoles` | plate with 60 holes: 65 → 5.7 ms; with 100 holes meshing costs about 110 ms |
| Expression evaluation | Parsed expressions are remembered (bounded cache). `BenchmarkEvalRepeated` vs `BenchmarkParseUncached` | 1243 → 122 ns, 33 → 2 allocations |
| Mesh transfer | `positions`, `normals`, `indices` and `edge_points` travel as base64 of little-endian bytes (`rendering/packed.go`, `frontend/mesh-codec.js`); a plain array is still accepted. | 119k-triangle body: JSON 5.7 → 3.7 MB; server encode 24 → 8.6 ms; browser parse+decode 11.1 → 2.5 ms (9.1 ms with the `atob` fallback) |
| Picking | Face picking walks a bounding-volume hierarchy built on a body's first pick (`frontend/bvh.js`); hover is picked once per frame; the hover/selection overlay no longer deletes the body's GPU buffers (it used to dispose attributes it shared with the body, forcing a re-upload on every hover change) and reuses its outline | ~1M triangles: ray 8.0 ms → 0.022 ms (362x); building the hierarchy costs 735 ms once for a body that large (about 7 ms per 10k triangles) |
| Saving | Unchanged files are not rewritten; files are written and synced, then renamed together | an unchanged save writes nothing; a failed save leaves the previous project intact |
| LLM requests | The transcript carries a cache breakpoint on its last block, so each step of a tool loop re-reads the earlier steps from the prompt cache; context JSON is compact; replayed chat history is also bounded by size | (cost effect depends on the provider's cache; verify with `cached_input_tokens` in the usage report) |

Not changed, on purpose: `Translate(0,0,0)` as a shape copy costs 0.06 ms on a plain plate and 1.6 ms with
100 holes (not worth a native `share`); a failed boolean is not retried with a fuzzy tolerance (OCCT booleans
did not fail on any pathological input tried, so there is no case to prove it helps); checkpoints still store
full graph snapshots (projects are hundreds of KB).

## Regression models

`testdata/regression/models.json` pins volume, bounding box, solid count and face count of nine reference
models (helix tube, torus ring, hollow pipe, ribbon, extruded text, embossed label, engraved groove, glyph
stamp, groove on a cylinder). `go test ./internal/app -run Regression` compares against it (volume 0.1 %,
bounding box 0.02 mm); regenerate deliberately with `-update-regression` after an intended change.
