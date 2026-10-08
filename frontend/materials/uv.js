// Automatic procedural UVs for CAD meshes (no artist-authored UVs exist). Pure functions:
// no Three.js, so they are unit-testable with node. Units are mm; `scale` is the texture
// repeat length in mm (one texture tile covers `scale` mm).
//
// Mapping is chosen per face from the kernel's surface type:
//   plane            -> planar, projected along the dominant normal axis
//   cylinder / cone  -> cylindrical about the face's fitted axis
//   anything else    -> box mapping, chosen per vertex from its dominant normal axis
// Faces that are neither (sphere, torus, free-form) have no distortion-free UV: they are
// flagged by triplanarFlags and drawn with triplanar sampling instead (see triplanar.js).

const sub = (a, b) => [a[0] - b[0], a[1] - b[1], a[2] - b[2]];
const dot = (a, b) => a[0] * b[0] + a[1] * b[1] + a[2] * b[2];
const cross = (a, b) => [a[1] * b[2] - a[2] * b[1], a[2] * b[0] - a[0] * b[2], a[0] * b[1] - a[1] * b[0]];
const len = (a) => Math.hypot(a[0], a[1], a[2]);
const norm = (a) => { const l = len(a) || 1; return [a[0] / l, a[1] / l, a[2] / l]; };

/** Dominant axis (0,1,2) of a vector. */
export function dominantAxis(n) {
  const ax = Math.abs(n[0]), ay = Math.abs(n[1]), az = Math.abs(n[2]);
  return ax >= ay && ax >= az ? 0 : ay >= az ? 1 : 2;
}

// Planar/box: drop the dominant axis, keep the other two (sign flips keep text un-mirrored).
function boxUV(p, n) {
  switch (dominantAxis(n)) {
    case 0: return [n[0] >= 0 ? p[1] : -p[1], p[2]];
    case 1: return [n[1] >= 0 ? -p[0] : p[0], p[2]];
    default: return [n[2] >= 0 ? p[0] : -p[0], p[1]];
  }
}

/** Axis of a cylindrical/conical face: perpendicular to every vertex normal (least squares via cross products). */
export function fitAxis(normals) {
  let best = [0, 0, 0], bestLen = 0;
  const step = Math.max(1, Math.floor(normals.length / 16));
  for (let i = 0; i < normals.length; i += step) {
    for (let j = i + step; j < normals.length; j += step) {
      const c = cross(normals[i], normals[j]);
      if (len(c) > bestLen) { bestLen = len(c); best = c; }
    }
  }
  return bestLen < 1e-6 ? null : norm(best);
}

/** Least-squares (cx, cy, R) for points p = c + R*n (2D). Falls back to R=1 about the origin if degenerate. */
function fitCircle(pts) {
  // rows: [1 0 nx | px], [0 1 ny | py]
  const A = [[0, 0, 0], [0, 0, 0], [0, 0, 0]], b = [0, 0, 0];
  for (const q of pts) {
    for (const [row, rhs] of [[[1, 0, q.nx], q.px], [[0, 1, q.ny], q.py]]) {
      for (let i = 0; i < 3; i++) { b[i] += row[i] * rhs; for (let j = 0; j < 3; j++) A[i][j] += row[i] * row[j]; }
    }
  }
  const det3 = (m) => m[0][0] * (m[1][1] * m[2][2] - m[1][2] * m[2][1]) - m[0][1] * (m[1][0] * m[2][2] - m[1][2] * m[2][0]) + m[0][2] * (m[1][0] * m[2][1] - m[1][1] * m[2][0]);
  const D = det3(A);
  if (Math.abs(D) < 1e-9) return { cx: 0, cy: 0, R: 1 };
  const col = (k) => A.map((r, i) => r.map((v, j) => (j === k ? b[i] : v)));
  return { cx: det3(col(0)) / D, cy: det3(col(1)) / D, R: det3(col(2)) / D };
}

/**
 * @param {ArrayLike<number>} positions xyz triples
 * @param {ArrayLike<number>} normals   xyz triples
 * @param {ArrayLike<number>} indices   triangle indices
 * @param {Array<{tri_start:number,tri_count:number,surface:string}>} faces
 * @param {number} scale mm per texture tile (>0)
 * @returns {Float32Array} uv pairs, one per vertex
 */
export function computeUVs(positions, normals, indices, faces, scale) {
  const nv = positions.length / 3;
  const uv = new Float32Array(nv * 2);
  const s = scale > 0 ? scale : 1;
  const P = (i) => [positions[i * 3], positions[i * 3 + 1], positions[i * 3 + 2]];
  const N = (i) => [normals[i * 3], normals[i * 3 + 1], normals[i * 3 + 2]];
  const done = new Uint8Array(nv);
  const put = (i, u, v) => { uv[i * 2] = u / s; uv[i * 2 + 1] = v / s; done[i] = 1; };

  for (const f of faces ?? []) {
    const verts = new Set();
    for (let t = f.tri_start; t < f.tri_start + f.tri_count; t++) {
      for (let k = 0; k < 3; k++) verts.add(indices[t * 3 + k]);
    }
    if (!verts.size) continue;
    const vs = [...verts];
    if (f.surface === 'cylinder' || f.surface === 'cone') {
      const axis = fitAxis(vs.map(N));
      if (axis) {
        const ref = norm(cross(axis, Math.abs(axis[2]) < 0.9 ? [0, 0, 1] : [1, 0, 0]));
        const ref2 = cross(axis, ref);
        // In the plane normal to the axis a cylinder satisfies p = c + R*n; fit (cx, cy, R)
        // by least squares so the angle is measured about the true axis, not the centroid.
        const fit = fitCircle(vs.map((i) => {
          const p = P(i), n = N(i);
          return { px: dot(p, ref), py: dot(p, ref2), nx: dot(n, ref), ny: dot(n, ref2) };
        }));
        const polar = vs.map((i) => {
          const p = P(i);
          return { i, h: dot(p, axis), a: Math.atan2(dot(p, ref2) - fit.cy, dot(p, ref) - fit.cx) };
        });
        const R = Math.abs(fit.R) || 1;
        // Unwrap so the face is contiguous: cut the circle at the widest empty gap between
        // vertex angles (a face spanning 180 deg or more has no safe "first vertex" reference).
        const sorted = polar.map((q) => q.a).sort((x, y) => x - y);
        let cut = sorted[0], gap = sorted[0] + 2 * Math.PI - sorted[sorted.length - 1];
        for (let k = 1; k < sorted.length; k++) {
          if (sorted[k] - sorted[k - 1] > gap) { gap = sorted[k] - sorted[k - 1]; cut = sorted[k]; }
        }
        for (const q of polar) {
          let da = q.a - cut;
          while (da < 0) da += 2 * Math.PI;
          put(q.i, da * R, q.h);
        }
        continue;
      }
    }
    for (const i of vs) { const [u, v] = boxUV(P(i), N(i)); put(i, u, v); }
  }
  for (let i = 0; i < nv; i++) { // vertices outside any face range
    if (!done[i]) { const [u, v] = boxUV(P(i), N(i)); put(i, u, v); }
  }
  return uv;
}

const UV_SURFACES = new Set(['plane', 'cylinder', 'cone']);

/** 1 for every vertex of a curved face the UVs cannot map well (sphere, torus, free-form), else 0. */
export function triplanarFlags(vertexCount, indices, faces) {
  const flags = new Float32Array(vertexCount);
  for (const f of faces ?? []) {
    if (!f.surface || UV_SURFACES.has(f.surface)) continue;
    for (let t = f.tri_start; t < f.tri_start + f.tri_count; t++) {
      for (let k = 0; k < 3; k++) flags[indices[t * 3 + k]] = 1;
    }
  }
  return flags;
}
