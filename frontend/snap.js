// The SnapService on the client (no DOM, no Three.js; unit tested). It is the twin of
// internal/workspace/snap.go so that the viewport's interactive Move/Rotate can preview at
// frame rate without a server round trip. Both are held to the same answers by the shared cases
// in internal/workspace/testdata/snap_cases.json, so what the preview shows is exactly what the
// tools would compute. Settings are the project's workspace settings (state.workspace): grid and
// snap, lengths in mm, the rotation increment in degrees; angles in and out are radians.

const PLANE_AXES = { XY: [0, 1, 2], XZ: [0, 2, 1], YZ: [1, 2, 0] };

/** Rounds to 12 significant digits, as the Go service does (3 × 0.1 → 0.3); never "-0". */
function clean(v) {
  if (v === 0 || !Number.isFinite(v)) return v;
  const r = Number(v.toPrecision(12));
  return r === 0 ? 0 : r;
}

/** Go's math.Round: halves away from zero (JS Math.round rounds them up). */
const roundHalfAway = (x) => Math.sign(x) * Math.round(Math.abs(x));

/** Nearest multiple of step, symmetric for negative and positive values. */
function snapTo(v, step) {
  if (!(step > 0) || !Number.isFinite(v)) return v;
  return clean(roundHalfAway(clean(v / step)) * step);
}

/** A snapping context: the workspace settings and an optional one-off bypass (a held key). */
export function snapContext(settings, { bypass = false } = {}) {
  const s = settings ?? {};
  const grid = s.grid ?? {}, snap = s.snap ?? {};
  const on = !bypass && !!snap.enabled;
  return {
    grid,
    snap,
    gridActive: on && !!snap.grid && !!grid.enabled,
    translationActive: on && !!snap.translation,
    rotationActive: on && !!snap.rotation,
    translationStep: snap.translation_increment ?? grid.spacing,
  };
}

/**
 * Snaps a move of a reference point (the object's origin) from `start` by `delta`, on the
 * `free` axes only, and returns { delta, snapped }. Absolute: computed from start + delta
 * every time, never from accumulated frame steps. See workspace.SnapMove.
 */
export function snapMove(start, delta, free, ctx) {
  const ax = PLANE_AXES[ctx.grid.plane] ?? PLANE_AXES.XY;
  const origin = ctx.grid.origin ?? [0, 0, 0];
  const out = [0, 0, 0];
  let snapped = false;
  for (let i = 0; i < 3; i++) {
    if (!free[i]) continue;
    out[i] = delta[i];
    if (ctx.gridActive && (i === ax[0] || i === ax[1])) {
      const at = clean(origin[i] + snapTo(start[i] + delta[i] - origin[i], ctx.grid.spacing));
      out[i] = clean(at - start[i]); snapped = true;
    } else if (ctx.translationActive) {
      out[i] = snapTo(delta[i], ctx.translationStep); snapped = true;
    }
  }
  return { delta: out, snapped };
}

/** Rounds an angle (radians) to the rotation increment (degrees). See workspace.SnapAngle. */
export function snapAngle(rad, ctx) {
  if (!ctx.rotationActive) return { angle: rad, snapped: false };
  const deg = snapTo((rad * 180) / Math.PI, ctx.snap.rotation_increment);
  return { angle: (deg * Math.PI) / 180, snapped: true };
}
