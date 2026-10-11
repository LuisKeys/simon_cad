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

## Regression models

`testdata/regression/models.json` pins volume, bounding box, solid count and face count of nine reference
models (helix tube, torus ring, hollow pipe, ribbon, extruded text, embossed label, engraved groove, glyph
stamp, groove on a cylinder). `go test ./internal/app -run Regression` compares against it (volume 0.1 %,
bounding box 0.02 mm); regenerate deliberately with `-update-regression` after an intended change.
