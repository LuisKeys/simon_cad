// A small bounding-volume hierarchy over a triangle mesh, for ray picking. Pure math (no
// three.js) so it can be unit-tested. Built lazily on the first pick of a body: a model that
// is never hovered pays nothing, and a rebuilt body gets a fresh one.

const LEAF = 8;
const EPS = 1e-12;

/** Builds a BVH for positions (xyz floats) and triangle indices. */
export function buildBVH(pos, idx) {
  const T = Math.floor(idx.length / 3);
  const cen = new Float32Array(T * 3);
  for (let t = 0; t < T; t++) {
    for (let k = 0; k < 3; k++) {
      cen[t * 3 + k] = (pos[idx[t * 3] * 3 + k] + pos[idx[t * 3 + 1] * 3 + k] + pos[idx[t * 3 + 2] * 3 + k]) / 3;
    }
  }
  const order = new Uint32Array(T);
  for (let t = 0; t < T; t++) order[t] = t;

  const boxes = [], left = [], right = [], start = [], count = [];
  const stack = [[0, T, -1, 0]]; // range start, end, parent, side
  while (stack.length) {
    const [s, e, parent, side] = stack.pop();
    const node = boxes.length / 6;
    if (parent >= 0) (side === 0 ? left : right)[parent] = node;
    const b = [Infinity, Infinity, Infinity, -Infinity, -Infinity, -Infinity];
    const cb = [Infinity, Infinity, Infinity, -Infinity, -Infinity, -Infinity];
    for (let i = s; i < e; i++) {
      const t = order[i];
      for (let v = 0; v < 3; v++) {
        for (let k = 0; k < 3; k++) {
          const x = pos[idx[t * 3 + v] * 3 + k];
          if (x < b[k]) b[k] = x;
          if (x > b[k + 3]) b[k + 3] = x;
        }
      }
      for (let k = 0; k < 3; k++) {
        const c = cen[t * 3 + k];
        if (c < cb[k]) cb[k] = c;
        if (c > cb[k + 3]) cb[k + 3] = c;
      }
    }
    boxes.push(...b);
    left.push(-1); right.push(-1); start.push(s); count.push(e - s);
    if (e - s <= LEAF) continue;
    let axis = 0, ext = cb[3] - cb[0];
    for (let k = 1; k < 3; k++) if (cb[k + 3] - cb[k] > ext) { ext = cb[k + 3] - cb[k]; axis = k; }
    if (ext <= 0) continue; // every centroid coincides: keep it as one (large) leaf
    const mid = (s + e) >> 1;
    select(order, cen, axis, s, e - 1, mid);
    count[node] = 0; // inner node
    // pushed right first so the left child is numbered right after its parent
    stack.push([mid, e, node, 1], [s, mid, node, 0]);
  }
  return {
    pos, idx, order,
    boxes: Float32Array.from(boxes),
    left: Int32Array.from(left), right: Int32Array.from(right),
    start: Int32Array.from(start), count: Int32Array.from(count),
  };
}

/** Quickselect: rearranges order[lo..hi] so order[k] holds the k-th smallest centroid on axis. */
function select(order, cen, axis, lo, hi, k) {
  const key = (i) => cen[order[i] * 3 + axis];
  while (lo < hi) {
    const pivot = key((lo + hi) >> 1);
    let i = lo, j = hi;
    while (i <= j) {
      while (key(i) < pivot) i++;
      while (key(j) > pivot) j--;
      if (i <= j) { const t = order[i]; order[i] = order[j]; order[j] = t; i++; j--; }
    }
    if (k <= j) hi = j; else if (k >= i) lo = i; else return;
  }
}

function hitsBox(boxes, n, ox, oy, oz, ix, iy, iz) {
  const o = n * 6;
  let t0 = -Infinity, t1 = Infinity;
  let a = (boxes[o] - ox) * ix, b = (boxes[o + 3] - ox) * ix;
  t0 = Math.max(t0, Math.min(a, b)); t1 = Math.min(t1, Math.max(a, b));
  a = (boxes[o + 1] - oy) * iy; b = (boxes[o + 4] - oy) * iy;
  t0 = Math.max(t0, Math.min(a, b)); t1 = Math.min(t1, Math.max(a, b));
  a = (boxes[o + 2] - oz) * iz; b = (boxes[o + 5] - oz) * iz;
  t0 = Math.max(t0, Math.min(a, b)); t1 = Math.min(t1, Math.max(a, b));
  return t1 >= Math.max(t0, 0);
}

/**
 * Every intersection of the ray (origin o, direction d, both [x,y,z]) with the mesh, as
 * {t, tri} (unsorted; t is the distance along d). cullBack skips triangles facing away from
 * the ray, which is what a front-face-only material does.
 */
export function raycastBVH(bvh, o, d, cullBack = false) {
  const out = [];
  if (!bvh.count.length) return out;
  const { pos, idx, order, boxes, left, right, start, count } = bvh;
  const [ox, oy, oz] = o, [dx, dy, dz] = d;
  const ix = 1 / dx, iy = 1 / dy, iz = 1 / dz; // Infinity on a zero component works with the slab test
  const stack = [0];
  while (stack.length) {
    const n = stack.pop();
    if (!hitsBox(boxes, n, ox, oy, oz, ix, iy, iz)) continue;
    if (count[n] === 0) { stack.push(right[n], left[n]); continue; }
    for (let i = start[n], e = start[n] + count[n]; i < e; i++) {
      const tri = order[i];
      const a = idx[tri * 3] * 3, b = idx[tri * 3 + 1] * 3, c = idx[tri * 3 + 2] * 3;
      const e1x = pos[b] - pos[a], e1y = pos[b + 1] - pos[a + 1], e1z = pos[b + 2] - pos[a + 2];
      const e2x = pos[c] - pos[a], e2y = pos[c + 1] - pos[a + 1], e2z = pos[c + 2] - pos[a + 2];
      const px = dy * e2z - dz * e2y, py = dz * e2x - dx * e2z, pz = dx * e2y - dy * e2x;
      const det = e1x * px + e1y * py + e1z * pz;
      // det > 0 means the triangle's front (counter-clockwise) side faces the ray
      if (cullBack ? det <= EPS : Math.abs(det) <= EPS) continue;
      const inv = 1 / det;
      const sx = ox - pos[a], sy = oy - pos[a + 1], sz = oz - pos[a + 2];
      const u = (sx * px + sy * py + sz * pz) * inv;
      if (u < 0 || u > 1) continue;
      const qx = sy * e1z - sz * e1y, qy = sz * e1x - sx * e1z, qz = sx * e1y - sy * e1x;
      const v = (dx * qx + dy * qy + dz * qz) * inv;
      if (v < 0 || u + v > 1) continue;
      const t = (e2x * qx + e2y * qy + e2z * qz) * inv;
      if (t >= 0) out.push({ t, tri });
    }
  }
  return out;
}
