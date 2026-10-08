// Pure helpers of the interactive dimensions (no DOM, no Three.js; unit tested): where the
// dimension lines of an object go. Sizes are shown and read in the display units (units.js).
// The measurement itself comes from the server (DimensionService, get_object_dimensions);
// edits go through the set_object_dimension tool like every other change.

/** Width, depth and height are the extents along the object's local X, Y and Z (Z up). */
export const AXES = ['width', 'depth', 'height'];
export const AXIS_LABEL = { width: 'Width', depth: 'Depth', height: 'Height' };
export const ANCHORS = [['center', 'Center'], ['object_origin', 'Object origin']];
/** The order the Properties panel lists them in. */
export const PANEL_ORDER = ['width', 'height', 'depth'];

/** The size the other dimensions will get when `axis` becomes `value` (Lock Proportions). */
export function proportionalSize(d, axis, value, lock) {
  const cur = { width: d.width, depth: d.depth, height: d.height };
  if (!lock) return { ...cur, [axis]: value };
  const k = cur[axis] > 0 ? value / cur[axis] : 1;
  return { width: cur.width * k, depth: cur.depth * k, height: cur.height * k };
}

const add = (a, b) => [a[0] + b[0], a[1] + b[1], a[2] + b[2]];
const scale = (a, k) => [a[0] * k, a[1] * k, a[2] * k];

/** Local box point → world: world = axes · local (axes are the local X, Y, Z in world terms). */
export function toWorld(d, p) {
  const [ax, ay, az] = d.axes;
  return add(add(scale(ax, p[0]), scale(ay, p[1])), scale(az, p[2]));
}

/**
 * The three dimension lines of an object, in world coordinates. Each is drawn off the box like
 * an exterior dimension on a drawing: width along the front-bottom edge, depth along the
 * right-bottom edge, height up the front-left edge, each pushed out by `gap` with extension
 * lines back to the box. Returns [{ axis, value, a, b, mid, ext: [[p, q], [p, q]], ticks }].
 */
export function dimensionLines(d, opts = {}) {
  const { min, max } = d;
  const size = [max[0] - min[0], max[1] - min[1], max[2] - min[2]];
  const big = Math.max(...size, 1e-6);
  const gap = opts.gap ?? Math.max(big * 0.12, 1);
  const tick = gap * 0.25;
  // [axis, start corner, end corner, outward direction] in local coordinates.
  const defs = [
    ['width', [min[0], min[1], min[2]], [max[0], min[1], min[2]], [0, -1, 0]],
    ['depth', [max[0], min[1], min[2]], [max[0], max[1], min[2]], [1, 0, 0]],
    ['height', [min[0], min[1], min[2]], [min[0], min[1], max[2]], [-1, -1, 0].map((v) => v / Math.SQRT2)],
  ];
  return defs.map(([axis, s, e, out], i) => {
    const off = scale(out, gap);
    const a = add(s, off), b = add(e, off);
    const along = [0, 0, 0]; along[i] = 1;
    // A tick at each end: a short slash across the dimension line (architectural style).
    const slash = scale(add(out, along), tick / Math.SQRT2);
    const ticks = [[add(a, scale(slash, -1)), add(a, slash)], [add(b, scale(slash, -1)), add(b, slash)]];
    const ext = [[add(s, scale(out, gap * 0.15)), add(a, scale(out, gap * 0.2))], [add(e, scale(out, gap * 0.15)), add(b, scale(out, gap * 0.2))]];
    const w = (p) => toWorld(d, p);
    return {
      axis, value: d[axis],
      a: w(a), b: w(b), mid: w(scale(add(a, b), 0.5)),
      ext: ext.map(([p, q]) => [w(p), w(q)]),
      ticks: ticks.map(([p, q]) => [w(p), w(q)]),
    };
  });
}

/** A label is worth showing in a view only when its dimension line is not seen end-on. */
export function labelVisible(screenA, screenB, minPx = 14) {
  if (!screenA || !screenB) return false;
  if (screenA.z > 1 || screenB.z > 1 || screenA.z < -1 || screenB.z < -1) return false;
  return Math.hypot(screenB.x - screenA.x, screenB.y - screenA.y) >= minPx;
}
