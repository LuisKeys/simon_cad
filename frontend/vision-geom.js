// Pure geometry/data helpers for the image-interpretation panel. No DOM, so they can be
// unit-tested with `node --test frontend/`.

/** Confidence bands used by the overlay and the lists: ≥0.85 ok, ≥0.6 warn, else bad. */
export function confidenceClass(c) {
  const v = Number(c);
  if (!(v >= 0)) return 'bad';
  return v >= 0.85 ? 'ok' : v >= 0.6 ? 'warn' : 'bad';
}

export const pct = (c) => `${Math.round(Math.max(0, Math.min(1, Number(c) || 0)) * 100)}%`;

const num = (v) => (typeof v === 'number' && Number.isFinite(v) ? v : null);
const pt = (p) => (Array.isArray(p) && p.length === 2 && num(p[0]) !== null && num(p[1]) !== null ? [p[0], p[1]] : null);
const f = (v) => +v.toFixed(2);

/** Circle through three points, or null when they are collinear. */
export function circleThrough(a, m, b) {
  const d = 2 * (a[0] * (m[1] - b[1]) + m[0] * (b[1] - a[1]) + b[0] * (a[1] - m[1]));
  if (Math.abs(d) < 1e-9) return null;
  const a2 = a[0] ** 2 + a[1] ** 2, m2 = m[0] ** 2 + m[1] ** 2, b2 = b[0] ** 2 + b[1] ** 2;
  const cx = (a2 * (m[1] - b[1]) + m2 * (b[1] - a[1]) + b2 * (a[1] - m[1])) / d;
  const cy = (a2 * (b[0] - m[0]) + m2 * (a[0] - b[0]) + b2 * (m[0] - a[0])) / d;
  return { c: [cx, cy], r: Math.hypot(a[0] - cx, a[1] - cy) };
}

const TAU = Math.PI * 2;
const norm = (x) => ((x % TAU) + TAU) % TAU;

/** SVG path of the arc a → m → b (m lies on it). Falls back to a line when collinear. */
export function arcPath(a, m, b) {
  const k = circleThrough(a, m, b);
  if (!k) return `M ${f(a[0])} ${f(a[1])} L ${f(b[0])} ${f(b[1])}`;
  const ang = (p) => Math.atan2(p[1] - k.c[1], p[0] - k.c[0]);
  const a0 = ang(a), am = ang(m), a1 = ang(b);
  const positive = norm(am - a0) < norm(a1 - a0); // reached m before b going in the +angle direction
  const sweepAngle = positive ? norm(a1 - a0) : norm(a0 - a1);
  return `M ${f(a[0])} ${f(a[1])} A ${f(k.r)} ${f(k.r)} 0 ${sweepAngle > Math.PI ? 1 : 0} ${positive ? 1 : 0} ${f(b[0])} ${f(b[1])}`;
}

/** The SVG element descriptions ({tag, attrs}) that draw one entity in image pixels. */
export function entityShapes(e) {
  switch (e.kind) {
    case 'line': {
      const a = pt(e.p1), b = pt(e.p2);
      return a && b ? [{ tag: 'line', attrs: { x1: a[0], y1: a[1], x2: b[0], y2: b[1] } }] : [];
    }
    case 'arc': {
      const a = pt(e.p1), m = pt(e.mid), b = pt(e.p2);
      return a && m && b ? [{ tag: 'path', attrs: { d: arcPath(a, m, b) } }] : [];
    }
    case 'circle':
      return num(e.cx) !== null && num(e.cy) !== null && num(e.r) > 0 ? [{ tag: 'circle', attrs: { cx: e.cx, cy: e.cy, r: e.r } }] : [];
    case 'rectangle':
      return [e.x, e.y, e.w, e.h].every((v) => num(v) !== null) ? [{ tag: 'rect', attrs: { x: e.x, y: e.y, width: e.w, height: e.h } }] : [];
    case 'polygon': {
      const pts = (e.points ?? []).map(pt);
      return pts.length >= 3 && pts.every(Boolean) ? [{ tag: 'polygon', attrs: { points: pts.map((p) => p.join(',')).join(' ') } }] : [];
    }
    case 'slot': {
      if (![e.x1, e.y1, e.x2, e.y2, e.width].every((v) => num(v) !== null)) return [];
      const l = Math.hypot(e.x2 - e.x1, e.y2 - e.y1);
      if (l < 1e-9 || !(e.width > 0)) return [];
      const r = e.width / 2, ux = (e.x2 - e.x1) / l, uy = (e.y2 - e.y1) / l, nx = -uy, ny = ux;
      const a1 = [e.x1 + nx * r, e.y1 + ny * r], a2 = [e.x2 + nx * r, e.y2 + ny * r];
      const b2 = [e.x2 - nx * r, e.y2 - ny * r], b1 = [e.x1 - nx * r, e.y1 - ny * r];
      const tip2 = [e.x2 + ux * r, e.y2 + uy * r], tip1 = [e.x1 - ux * r, e.y1 - uy * r];
      const cap = (p, m, q) => arcPath(p, m, q).replace(/^M [-\d. ]+/, '');
      const d = `M ${f(a1[0])} ${f(a1[1])} L ${f(a2[0])} ${f(a2[1])} ${cap(a2, tip2, b2)} L ${f(b1[0])} ${f(b1[1])} ${cap(b1, tip1, a1)} Z`;
      return [{ tag: 'path', attrs: { d } }];
    }
  }
  return [];
}

/** Bounding box [minx, miny, maxx, maxy] of an entity in pixels, or null. */
export function entityBBox(e) {
  const pts = [];
  switch (e.kind) {
    case 'line': case 'arc': for (const k of ['p1', 'mid', 'p2']) { const p = pt(e[k]); if (p) pts.push(p); } break;
    case 'circle': if (num(e.cx) !== null && num(e.cy) !== null && num(e.r) !== null) pts.push([e.cx - e.r, e.cy - e.r], [e.cx + e.r, e.cy + e.r]); break;
    case 'rectangle': if ([e.x, e.y, e.w, e.h].every((v) => num(v) !== null)) pts.push([e.x, e.y], [e.x + e.w, e.y + e.h]); break;
    case 'polygon': for (const p of e.points ?? []) { const q = pt(p); if (q) pts.push(q); } break;
    case 'slot': if ([e.x1, e.y1, e.x2, e.y2, e.width].every((v) => num(v) !== null)) { const r = e.width / 2; pts.push([e.x1 - r, e.y1 - r], [e.x2 + r, e.y2 + r], [e.x1 + r, e.y1 + r], [e.x2 - r, e.y2 - r]); } break;
  }
  if (!pts.length) return null;
  return [Math.min(...pts.map((p) => p[0])), Math.min(...pts.map((p) => p[1])), Math.max(...pts.map((p) => p[0])), Math.max(...pts.map((p) => p[1]))];
}

/** Width in pixels of everything inside the profiles' outer loops (what "overall width" measures). */
export function drawingWidthPx(proposal) {
  const ids = new Set();
  for (const p of proposal.profiles ?? []) for (const id of p.outer ?? []) ids.add(id);
  let lo = Infinity, hi = -Infinity;
  for (const e of proposal.entities ?? []) {
    if (!ids.has(e.id)) continue;
    const b = entityBBox(e);
    if (b) { lo = Math.min(lo, b[0]); hi = Math.max(hi, b[2]); }
  }
  return hi > lo ? hi - lo : 0;
}

/** entity id → 'outer' | 'hole' | 'unused' according to the profiles. */
export function entityRoles(proposal) {
  const roles = {};
  for (const e of proposal.entities ?? []) roles[e.id] = 'unused';
  for (const p of proposal.profiles ?? []) {
    for (const id of p.outer ?? []) roles[id] = 'outer';
    for (const h of p.holes ?? []) for (const id of h) roles[id] = 'hole';
  }
  return roles;
}

/** Entities and dimensions the user should look at: low confidence or not yet trusted. */
export function uncertain(proposal, report) {
  const out = [];
  for (const e of proposal.entities ?? []) if (confidenceClass(e.confidence) === 'bad') out.push({ kind: 'entity', id: e.id, confidence: e.confidence });
  const unconfirmed = new Set(report?.unconfirmed ?? []);
  for (const d of proposal.dimensions ?? []) {
    if (unconfirmed.has(d.id)) out.push({ kind: 'dimension', id: d.id, confidence: d.confidence });
  }
  return out;
}

export const CHOICES = [
  { id: 'union', label: 'Join them into one solid' },
  { id: 'subtract_ba', label: 'Subtract the second from the first' },
  { id: 'subtract_ab', label: 'Subtract the first from the second' },
  { id: 'intersect', label: 'Keep only the overlap' },
  { id: 'separate', label: 'Keep them separate' },
];

/**
 * update_sketch_proposal edits that turn an ambiguity ([a, b] profile ids) into explicit
 * operations: extrude both profiles, then combine them. Depth comes from the tool call.
 */
export function ambiguityEdits(profiles, choice) {
  const [a, b] = profiles;
  const ex = (id, profile) => ({ op: 'set_operation', id, value: { kind: 'extrude', profile } });
  const edits = [];
  const first = choice === 'subtract_ab' ? b : a, second = choice === 'subtract_ab' ? a : b;
  edits.push(ex('o1', first), ex('o2', second));
  if (choice === 'union') edits.push({ op: 'set_operation', id: 'o3', value: { kind: 'union', a: 'o1', b: 'o2' } });
  else if (choice === 'subtract_ba' || choice === 'subtract_ab') edits.push({ op: 'set_operation', id: 'o3', value: { kind: 'subtract', a: 'o1', b: 'o2' } });
  else if (choice === 'intersect') edits.push({ op: 'set_operation', id: 'o3', value: { kind: 'intersect', a: 'o1', b: 'o2' } });
  else if (choice !== 'separate') throw new Error(`unknown choice ${choice}`);
  return edits;
}
