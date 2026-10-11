// Pure path math for the viewport: the same curves, flattening and frames as the Go path engine
// (internal/geometry/curve), so the live preview matches what the kernel will build. It is
// verified against testdata/curve_cases.json, which the Go side generates and checks too.
//
// A path is { type, closed, points: [{ p:[x,y,z], in?, out?, w?, h?, twist? }] } (the parameters of
// a `path` node). Vectors are plain [x, y, z] arrays.

const add = (a, b) => [a[0] + b[0], a[1] + b[1], a[2] + b[2]];
const sub = (a, b) => [a[0] - b[0], a[1] - b[1], a[2] - b[2]];
const mul = (a, s) => [a[0] * s, a[1] * s, a[2] * s];
const dot = (a, b) => a[0] * b[0] + a[1] * b[1] + a[2] * b[2];
const cross = (a, b) => [a[1] * b[2] - a[2] * b[1], a[2] * b[0] - a[0] * b[2], a[0] * b[1] - a[1] * b[0]];
const norm = (a) => Math.sqrt(dot(a, a));
const dist = (a, b) => norm(sub(a, b));
const lerp = (a, b, t) => add(mul(a, 1 - t), mul(b, t));
const unit = (a) => { const n = norm(a); return n < 1e-300 ? [0, 0, 0] : mul(a, 1 / n); };

export const TYPES = ['linear', 'bezier', 'catmull_rom', 'bspline'];
export const TYPE_LABELS = { linear: 'Polyline', bezier: 'Bézier', catmull_rom: 'Smooth (Catmull-Rom)', bspline: 'B-spline' };

/** A point given as [x,y,z] or { p, ... } becomes the object form. */
export function normalizePoint(pt) {
  return Array.isArray(pt) ? { p: [...pt] } : { ...pt, p: [...pt.p] };
}

export function normalizePath(path) {
  return { type: path.type ?? 'linear', closed: !!path.closed, points: (path.points ?? []).map(normalizePoint) };
}

/** Point scale factors: an unset (0) width or height means 1. */
export const scaleOf = (pt) => [pt.w || 1, pt.h || 1];

// ---- segments ------------------------------------------------------------------------------

/** Cubic Bezier segments of a path (n-1 open, n closed): every interpolation converts to them. */
export function segments(path) {
  const pts = path.points, n = pts.length;
  if (n < 2) return [];
  const count = path.closed ? n : n - 1;
  const pos = (i) => pts[i].p;
  const idx = (i) => {
    if (path.closed) return pos(((i % n) + n) % n);
    if (i < 0) return sub(mul(pos(0), 2), pos(1));
    if (i > n - 1) return sub(mul(pos(n - 1), 2), pos(n - 2));
    return pos(i);
  };
  const out = [];
  for (let i = 0; i < count; i++) {
    const a = pos(i), b = pos((i + 1) % n);
    let s;
    switch (path.type) {
      case 'bezier':
        s = [a, pts[i].out ?? a, pts[(i + 1) % n].in ?? b, b];
        break;
      case 'catmull_rom': {
        const p0 = idx(i - 1), p3 = idx(i + 2);
        s = [a, add(a, mul(sub(b, p0), 1 / 6)), sub(b, mul(sub(p3, a), 1 / 6)), b];
        break;
      }
      case 'bspline': {
        const p0 = idx(i - 1), p1 = idx(i), p2 = idx(i + 1), p3 = idx(i + 2);
        s = [mul(add(add(p0, mul(p1, 4)), p2), 1 / 6), mul(add(mul(p1, 2), p2), 1 / 3),
          mul(add(p1, mul(p2, 2)), 1 / 3), mul(add(add(p1, mul(p2, 4)), p3), 1 / 6)];
        break;
      }
      default:
        s = [a, lerp(a, b, 1 / 3), lerp(a, b, 2 / 3), b];
    }
    out.push(s);
  }
  return out;
}

export function segAt(s, u) {
  const v = 1 - u, a = v * v * v, b = 3 * v * v * u, c = 3 * v * u * u, d = u * u * u;
  return [0, 1, 2].map((i) => a * s[0][i] + b * s[1][i] + c * s[2][i] + d * s[3][i]);
}

export function segDeriv(s, u) {
  const v = 1 - u, a = 3 * v * v, b = 6 * v * u, c = 3 * u * u;
  return [0, 1, 2].map((i) => a * (s[1][i] - s[0][i]) + b * (s[2][i] - s[1][i]) + c * (s[3][i] - s[2][i]));
}

function split(s) {
  const m01 = lerp(s[0], s[1], .5), m12 = lerp(s[1], s[2], .5), m23 = lerp(s[2], s[3], .5);
  const m012 = lerp(m01, m12, .5), m123 = lerp(m12, m23, .5), m = lerp(m012, m123, .5);
  return [[s[0], m01, m012, m], [m, m123, m23, s[3]]];
}

function distToLine(p, a, b) {
  const ab = sub(b, a), l2 = dot(ab, ab);
  if (l2 < 1e-24) return dist(p, a);
  return dist(p, add(a, mul(ab, dot(sub(p, a), ab) / l2)));
}

const flat = (s) => Math.max(distToLine(s[1], s[0], s[3]), distToLine(s[2], s[0], s[3]));

/** Flattens the path so no chord deviates more than tol: the same recursion as the Go side. */
export function flatten(path, tol = 0.05) {
  const segs = segments(path);
  if (!segs.length) return [];
  const out = [segs[0][0]];
  const rec = (s, depth) => {
    if (depth >= 18 || flat(s) <= tol) { out.push(s[3]); return; }
    const [a, b] = split(s);
    rec(a, depth + 1); rec(b, depth + 1);
  };
  for (const s of segs) rec(s, 0);
  if (path.closed && out.length > 1 && dist(out[0], out[out.length - 1]) < 1e-12) out.pop();
  const dd = [];
  for (const p of out) if (!dd.length || dist(dd[dd.length - 1], p) > 1e-12) dd.push(p);
  return dd;
}

// ---- arc length ----------------------------------------------------------------------------

const GLX = [-0.9602898564975363, -0.7966664774136267, -0.5255324099163290, -0.1834346424956498,
  0.1834346424956498, 0.5255324099163290, 0.7966664774136267, 0.9602898564975363];
const GLW = [0.1012285362903763, 0.2223810344533745, 0.3137066458778873, 0.3626837833783620,
  0.3626837833783620, 0.3137066458778873, 0.2223810344533745, 0.1012285362903763];

function segLen(s, u0, u1) {
  const panels = 4, h = (u1 - u0) / panels;
  let total = 0;
  for (let k = 0; k < panels; k++) {
    const mid = u0 + k * h + h / 2, half = h / 2;
    for (let i = 0; i < GLX.length; i++) total += GLW[i] * norm(segDeriv(s, mid + half * GLX[i]));
  }
  return total * h / 2;
}

/** A path prepared for arc-length queries (build once per edit). */
export class Curve {
  constructor(path) {
    this.path = path;
    this.segs = segments(path);
    this.cum = [];
    let acc = 0;
    for (const s of this.segs) { acc += segLen(s, 0, 1); this.cum.push(acc); }
  }

  get length() { return this.cum.length ? this.cum[this.cum.length - 1] : 0; }

  /** Arc length at the start of each segment. */
  get starts() { return this.cum.map((_, i) => (i ? this.cum[i - 1] : 0)); }

  _locate(s) {
    const total = this.length;
    if (s <= 0) return [0, 0];
    if (s >= total) return [this.segs.length - 1, 1];
    let lo = 0, hi = this.cum.length - 1;
    while (lo < hi) { const mid = (lo + hi) >> 1; if (this.cum[mid] >= s) hi = mid; else lo = mid + 1; }
    const i = lo, start = i ? this.cum[i - 1] : 0, segL = this.cum[i] - start;
    if (segL < 1e-300) return [i, 0];
    const target = s - start;
    let a = 0, b = 1, u = target / segL;
    for (let it = 0; it < 40; it++) {
      const d = segLen(this.segs[i], 0, u) - target;
      if (Math.abs(d) < 1e-12 * Math.max(1, segL)) break;
      if (d > 0) b = u; else a = u;
      const sp = norm(segDeriv(this.segs[i], u));
      let next = u - d / Math.max(sp, 1e-12);
      if (next <= a || next >= b) next = (a + b) / 2;
      u = next;
    }
    return [i, u];
  }

  /** Position and unit tangent at the normalized arc-length parameter t in [0, 1]. */
  at(t) {
    t = Math.min(1, Math.max(0, t));
    const [i, u] = this._locate(t * this.length);
    const s = this.segs[i];
    let tan = unit(segDeriv(s, u));
    if (!tan[0] && !tan[1] && !tan[2]) tan = unit(sub(s[3], s[0]));
    return { p: segAt(s, u), t: tan };
  }

  /** n points equally spaced by arc length (a closed curve does not repeat its start). */
  uniform(n) {
    n = Math.max(2, n);
    const den = this.path.closed ? n : n - 1;
    return Array.from({ length: n }, (_, i) => this.at(i / den).p);
  }
}

// ---- frames --------------------------------------------------------------------------------

export function perp(t) {
  const a = Math.abs(t[0]) > 0.9 ? [0, 1, 0] : [1, 0, 0];
  return unit(cross(t, a));
}

function rotateAbout(v, k, angle) {
  const c = Math.cos(angle), s = Math.sin(angle);
  return add(add(mul(v, c), mul(cross(k, v), s)), mul(k, dot(k, v) * (1 - c)));
}

/** Rotation-minimizing (parallel transport) frames, double reflection; closed loops spread the
 *  residual twist; twist[i] (degrees) is applied on top. Returns [{ p, t, n, b }]. */
export function frames(pts, tans, closed = false, twist = [], up = [0, 0, 0]) {
  const n = pts.length;
  if (n < 2) return [];
  const tan = tans.map((t) => unit(t));
  const first = tan.findIndex((t) => t[0] || t[1] || t[2]);
  if (first < 0) return [];
  for (let i = 0; i < first; i++) tan[i] = tan[first];
  for (let i = first + 1; i < n; i++) if (!tan[i][0] && !tan[i][1] && !tan[i][2]) tan[i] = tan[i - 1];
  let n0 = perp(tan[0]);
  const u = unit(sub(up, mul(tan[0], dot(up, tan[0]))));
  if (u[0] || u[1] || u[2]) n0 = u;
  const fr = [{ p: pts[0], t: tan[0], n: n0, b: cross(tan[0], n0) }];
  const carry = (prev, p, q, tq) => {
    const v1 = sub(q, p), c1 = dot(v1, v1);
    if (c1 < 1e-24) return null;
    const rL = sub(prev.n, mul(v1, 2 * dot(v1, prev.n) / c1));
    const tL = sub(prev.t, mul(v1, 2 * dot(v1, prev.t) / c1));
    const v2 = sub(tq, tL), c2 = dot(v2, v2);
    return c2 > 1e-24 ? sub(rL, mul(v2, 2 * dot(v2, rL) / c2)) : rL;
  };
  for (let i = 1; i < n; i++) {
    let nn = carry(fr[i - 1], pts[i - 1], pts[i], tan[i]);
    if (nn === null) { fr.push({ ...fr[i - 1], p: pts[i] }); continue; }
    nn = unit(sub(nn, mul(tan[i], dot(nn, tan[i]))));
    fr.push({ p: pts[i], t: tan[i], n: nn, b: cross(tan[i], nn) });
  }
  if (closed) {
    const rL = carry(fr[n - 1], pts[n - 1], pts[0], tan[0]);
    if (rL !== null) {
      const ang = Math.atan2(dot(cross(fr[0].n, rL), fr[0].t), dot(fr[0].n, rL));
      for (let i = 1; i < n; i++) {
        fr[i].n = rotateAbout(fr[i].n, fr[i].t, -ang * i / n);
        fr[i].b = cross(fr[i].t, fr[i].n);
      }
    }
  }
  twist.forEach((tw, i) => {
    if (i < n && tw) { fr[i].n = rotateAbout(fr[i].n, fr[i].t, tw * Math.PI / 180); fr[i].b = cross(fr[i].t, fr[i].n); }
  });
  return fr;
}

// ---- keyed per-point scale / twist ---------------------------------------------------------

/** Interpolates (w, h, twist) along arc length s between the control points. */
export function keyedAt(path, curve, s) {
  const starts = curve.starts, L = curve.length, pts = path.points;
  const keys = pts.map((pt, i) => ({ s: i < starts.length ? starts[i] : L, v: [...scaleOf(pt), pt.twist ?? 0] }));
  if (path.closed) keys.push({ s: L, v: keys[0].v });
  if (s <= keys[0].s) return keys[0].v;
  for (let i = 1; i < keys.length; i++) {
    if (s <= keys[i].s) {
      const span = keys[i].s - keys[i - 1].s;
      const t = span > 0 ? (s - keys[i - 1].s) / span : 0;
      return keys[i - 1].v.map((x, k) => x + t * (keys[i].v[k] - x));
    }
  }
  return keys[keys.length - 1].v;
}

// ---- preview mesh --------------------------------------------------------------------------

/**
 * A reduced-resolution tube along the path for live feedback while editing: a rounded-polygon
 * profile of `radius` scaled by each point's width/height and twisted, as BufferGeometry arrays.
 * It is a preview only: the exact solid comes from the kernel when the edit ends.
 */
export function previewTube(path, { radius = 1, sides = 12, samples = 48, up = [0, 0, 0] } = {}) {
  const pa = normalizePath(path);
  if (pa.points.length < 2) return null;
  const curve = new Curve(pa);
  const L = curve.length;
  if (!(L > 1e-9)) return null;
  const n = Math.max(2, samples), den = pa.closed ? n : n - 1;
  const pts = [], tans = [], tw = [], sc = [];
  for (let i = 0; i < n; i++) {
    const s = L * i / den, e = curve.at(s / L);
    const [w, h, t] = keyedAt(pa, curve, s);
    pts.push(e.p); tans.push(e.t); tw.push(t); sc.push([w, h]);
  }
  const fr = frames(pts, tans, pa.closed, tw, up);
  if (!fr.length) return null;
  const positions = [], normals = [], indices = [];
  for (let i = 0; i < fr.length; i++) {
    for (let k = 0; k < sides; k++) {
      const a = 2 * Math.PI * k / sides, cx = Math.cos(a), cy = Math.sin(a);
      const off = add(mul(fr[i].n, cx * radius * sc[i][0]), mul(fr[i].b, cy * radius * sc[i][1]));
      positions.push(...add(fr[i].p, off));
      normals.push(...unit(add(mul(fr[i].n, cx / sc[i][0]), mul(fr[i].b, cy / sc[i][1]))));
    }
  }
  const rings = pa.closed ? fr.length : fr.length - 1;
  for (let i = 0; i < rings; i++) {
    const j = (i + 1) % fr.length;
    for (let k = 0; k < sides; k++) {
      const k2 = (k + 1) % sides;
      const a = i * sides + k, b = i * sides + k2, c = j * sides + k, d = j * sides + k2;
      indices.push(a, c, b, b, c, d);
    }
  }
  if (!pa.closed) { // flat caps
    for (const [ring, flip] of [[0, true], [fr.length - 1, false]]) {
      const base = positions.length / 3;
      positions.push(...fr[ring].p);
      const nrm = flip ? mul(fr[ring].t, -1) : fr[ring].t;
      normals.push(...nrm);
      for (let k = 0; k < sides; k++) {
        const k2 = (k + 1) % sides;
        if (flip) indices.push(base, ring * sides + k2, ring * sides + k);
        else indices.push(base, ring * sides + k, ring * sides + k2);
      }
    }
  }
  return { positions, normals, indices };
}

// ---- picking helpers -----------------------------------------------------------------------

/** Closest point of the flattened path to a ray ({ origin, dir }): { point, seg, u, distance }.
 *  seg is the index of the *control segment* the point lies on (insert before seg + 1). */
export function nearestToRay(path, ray, tol = 0.2) {
  const segs = segments(normalizePath(path));
  let best = null;
  segs.forEach((s, si) => {
    const steps = 32;
    let prev = s[0];
    for (let k = 1; k <= steps; k++) {
      const q = segAt(s, k / steps);
      const c = closestRaySegment(ray, prev, q);
      if (!best || c.d < best.distance) best = { point: c.point, seg: si, u: (k - 1 + c.t) / steps, distance: c.d };
      prev = q;
    }
  });
  return best;
}

function closestRaySegment(ray, a, b) {
  const d1 = ray.dir, d2 = sub(b, a), r = sub(ray.origin, a);
  const A = dot(d1, d1), E = dot(d2, d2), F = dot(d2, r);
  let t;
  if (E < 1e-24) t = 0;
  else {
    const B = dot(d1, d2), C = dot(d1, r), den = A * E - B * B;
    const s = den > 1e-18 ? (B * F - C * E) / den : 0;
    t = Math.min(1, Math.max(0, (B * s + F) / E));
  }
  const point = add(a, mul(d2, t));
  const along = Math.max(0, dot(sub(point, ray.origin), d1) / A);
  return { point, t, d: dist(point, add(ray.origin, mul(d1, along))) };
}

// ---- editing (the same operations as update_path) ----------------------------------------

export function clonePath(path) {
  return JSON.parse(JSON.stringify(normalizePath(path)));
}

/** Converts the interpolation. Bezier keeps the exact current shape by materializing handles;
 *  the other targets keep the control points and drop handles (like the Go Convert). */
export function convertType(path, to) {
  const out = clonePath(path);
  if (to === out.type) return out;
  if (to === 'bezier') {
    const segs = segments(out), n = out.points.length;
    for (const pt of out.points) { delete pt.in; delete pt.out; }
    segs.forEach((s, i) => {
      out.points[i].p = s[0];
      out.points[i].out = s[1];
      out.points[(i + 1) % n].in = s[2];
    });
    if (!out.closed) { delete out.points[0].in; delete out.points[n - 1].out; out.points[n - 1].p = segs[segs.length - 1][3]; }
  } else {
    for (const pt of out.points) { delete pt.in; delete pt.out; }
  }
  out.type = to;
  return out;
}

/** Moves a point and carries its handles. */
export function movePoint(path, i, pos) {
  const out = clonePath(path), pt = out.points[i], d = sub(pos, pt.p);
  pt.p = [...pos];
  if (pt.in) pt.in = add(pt.in, d);
  if (pt.out) pt.out = add(pt.out, d);
  return out;
}

/** Default handles for a new Bezier point: along the neighbours' direction, 1/3 of the gap. */
export function autoHandles(path, i) {
  const pts = path.points, n = pts.length;
  const prev = pts[(i - 1 + n) % n]?.p, next = pts[(i + 1) % n]?.p, p = pts[i].p;
  const a = (!path.closed && i === 0) ? p : prev, b = (!path.closed && i === n - 1) ? p : next;
  const dir = unit(sub(b, a));
  const lenIn = dist(p, a) / 3, lenOut = dist(p, b) / 3;
  return { in: sub(p, mul(dir, lenIn)), out: add(p, mul(dir, lenOut)) };
}

/** Inserts a point on segment `seg` (between points seg and seg+1) at the curve position u.
 *  Bezier paths split the segment with de Casteljau so the shape does not change. */
export function insertOnSegment(path, seg, u) {
  const out = clonePath(path), n = out.points.length;
  const segs = segments(out);
  const s = segs[seg];
  if (!s) return out;
  const q = segAt(s, u);
  const idx = seg + 1;
  if (out.type === 'bezier') {
    const m01 = lerp(s[0], s[1], u), m12 = lerp(s[1], s[2], u), m23 = lerp(s[2], s[3], u);
    const m012 = lerp(m01, m12, u), m123 = lerp(m12, m23, u);
    out.points[seg].out = m01;
    out.points[(seg + 1) % n].in = m23;
    out.points.splice(idx, 0, { p: segAt(s, u), in: m012, out: m123 });
  } else {
    out.points.splice(idx, 0, { p: q });
  }
  return out;
}

/** Removes a point unless the path would fall below its minimum. */
export function removePoint(path, i) {
  const out = clonePath(path), min = out.closed ? 3 : 2;
  if (out.points.length <= min || i < 0 || i >= out.points.length) return null;
  out.points.splice(i, 1);
  return out;
}

/** Axis-aligned bounding box of the flattened path. */
export function bounds(path, tol = 0.2) {
  const pts = flatten(normalizePath(path), tol);
  const lo = [Infinity, Infinity, Infinity], hi = [-Infinity, -Infinity, -Infinity];
  for (const p of pts) for (let k = 0; k < 3; k++) { lo[k] = Math.min(lo[k], p[k]); hi[k] = Math.max(hi[k], p[k]); }
  return { lo, hi };
}

// ---- sweep preview info ------------------------------------------------------------------

/** Reach of a profile node's parameters from its origin (mm): the radius of the preview tube. */
export function profileRadius(params = {}) {
  const num = (k) => (Number.isFinite(params[k]) ? params[k] : 0);
  const farthest = (pts) => pts.reduce((m, q) => Math.max(m, Math.hypot(q[0], q[1])), 0);
  switch (params.shape) {
    case 'circle': case 'semicircle': return num('radius');
    case 'ellipse': return Math.max(num('radius_x'), num('radius_y'));
    case 'hollow_circle': return num('outer_radius');
    case 'square': return num('side') * Math.SQRT1_2;
    case 'rectangle': case 'triangle': return Math.hypot(num('width'), num('height')) / 2;
    case 'polygon': return farthest(params.vertices ?? []);
    case 'custom': return farthest((params.contours ?? []).flatMap((c) => c.points ?? []));
    default: return 0;
  }
}

/** How the sweeps that use path `id` look, from the design tree: { radius, up } of the sweep
 *  with the thickest profile, or null when no sweep uses the path. */
export function sweepPreview(tree, id) {
  let best = null;
  for (const n of tree) {
    if (n.type !== 'sweep' || n.params?.path !== id) continue;
    const prof = tree.find((t) => t.id === n.params.profile);
    const radius = profileRadius(prof?.params);
    if (radius > 0 && (!best || radius > best.radius)) best = { radius, up: Array.isArray(n.params.up) ? n.params.up : [0, 0, 0] };
  }
  return best;
}
