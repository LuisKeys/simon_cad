import test from 'node:test';
import assert from 'node:assert/strict';
import { buildBVH, raycastBVH } from './bvh.js';

// Deterministic PRNG so failures reproduce.
function rng(seed) { let s = seed; return () => ((s = (s * 1664525 + 1013904223) >>> 0) / 4294967296); }

/** A unit cube [0,1]^3 with outward (counter-clockwise) triangles. */
function cube() {
  const pos = Float32Array.from([0,0,0, 1,0,0, 1,1,0, 0,1,0, 0,0,1, 1,0,1, 1,1,1, 0,1,1]);
  const idx = Uint32Array.from([
    0,2,1, 0,3,2,  4,5,6, 4,6,7,  0,1,5, 0,5,4,  2,3,7, 2,7,6,  1,2,6, 1,6,5,  3,0,4, 3,4,7,
  ]);
  return { pos, idx };
}

/** Brute-force reference: every hit, no acceleration. */
function brute(pos, idx, o, d, cullBack) {
  const out = [];
  for (let t = 0; t < idx.length / 3; t++) {
    const [a, b, c] = [idx[t * 3] * 3, idx[t * 3 + 1] * 3, idx[t * 3 + 2] * 3];
    const e1 = [pos[b] - pos[a], pos[b + 1] - pos[a + 1], pos[b + 2] - pos[a + 2]];
    const e2 = [pos[c] - pos[a], pos[c + 1] - pos[a + 1], pos[c + 2] - pos[a + 2]];
    const p = [d[1] * e2[2] - d[2] * e2[1], d[2] * e2[0] - d[0] * e2[2], d[0] * e2[1] - d[1] * e2[0]];
    const det = e1[0] * p[0] + e1[1] * p[1] + e1[2] * p[2];
    if (cullBack ? det <= 1e-12 : Math.abs(det) <= 1e-12) continue;
    const s = [o[0] - pos[a], o[1] - pos[a + 1], o[2] - pos[a + 2]];
    const u = (s[0] * p[0] + s[1] * p[1] + s[2] * p[2]) / det;
    if (u < 0 || u > 1) continue;
    const q = [s[1] * e1[2] - s[2] * e1[1], s[2] * e1[0] - s[0] * e1[2], s[0] * e1[1] - s[1] * e1[0]];
    const v = (d[0] * q[0] + d[1] * q[1] + d[2] * q[2]) / det;
    if (v < 0 || u + v > 1) continue;
    const tt = (e2[0] * q[0] + e2[1] * q[1] + e2[2] * q[2]) / det;
    if (tt >= 0) out.push({ t: tt, tri: t });
  }
  return out.sort((x, y) => x.t - y.t || x.tri - y.tri);
}
const sorted = (h) => [...h].sort((x, y) => x.t - y.t || x.tri - y.tri);

// Rays avoid the faces' shared diagonals: there a ray hits both triangles, as it should.
test('a ray through a cube hits the near and far faces', () => {
  const { pos, idx } = cube();
  const hits = sorted(raycastBVH(buildBVH(pos, idx), [0.7, 0.3, -1], [0, 0, 1]));
  assert.equal(hits.length, 2);
  assert.ok(Math.abs(hits[0].t - 1) < 1e-9 && Math.abs(hits[1].t - 2) < 1e-9);
});

test('cullBack keeps only faces turned toward the ray', () => {
  const { pos, idx } = cube();
  const bvh = buildBVH(pos, idx);
  const hits = raycastBVH(bvh, [0.7, 0.3, -1], [0, 0, 1], true);
  assert.equal(hits.length, 1);
  assert.ok(Math.abs(hits[0].t - 1) < 1e-9, 'the near (front-facing) face');
  // from the inside, every wall faces away: nothing is hit
  assert.equal(raycastBVH(bvh, [0.5, 0.5, 0.5], [0, 0, 1], true).length, 0);
});

test('a ray that misses returns nothing, and an empty mesh is safe', () => {
  const { pos, idx } = cube();
  assert.equal(raycastBVH(buildBVH(pos, idx), [5, 5, -1], [0, 0, 1]).length, 0);
  assert.equal(raycastBVH(buildBVH(new Float32Array(0), new Uint32Array(0)), [0, 0, 0], [0, 0, 1]).length, 0);
});

test('axis-parallel rays (zero direction components) work', () => {
  const { pos, idx } = cube();
  const bvh = buildBVH(pos, idx);
  assert.equal(raycastBVH(bvh, [-1, 0.3, 0.6], [1, 0, 0]).length, 2);
  assert.equal(raycastBVH(bvh, [0.3, -1, 0.6], [0, 1, 0]).length, 2);
});

test('matches brute force on a random triangle soup, culled and not', () => {
  const r = rng(7);
  const T = 3000;
  const pos = new Float32Array(T * 9), idx = new Uint32Array(T * 3);
  for (let t = 0; t < T; t++) {
    const c = [r() * 20, r() * 20, r() * 20];
    for (let v = 0; v < 3; v++) for (let k = 0; k < 3; k++) pos[t * 9 + v * 3 + k] = c[k] + (r() - 0.5) * 1.5;
    idx.set([t * 3, t * 3 + 1, t * 3 + 2], t * 3);
  }
  const bvh = buildBVH(pos, idx);
  let withHits = 0;
  for (let n = 0; n < 200; n++) {
    const o = [r() * 20, r() * 20, -5];
    const d = [(r() - 0.5) * 0.3, (r() - 0.5) * 0.3, 1];
    const len = Math.hypot(...d); d[0] /= len; d[1] /= len; d[2] /= len;
    for (const cull of [false, true]) {
      const got = sorted(raycastBVH(bvh, o, d, cull)), want = brute(pos, idx, o, d, cull);
      assert.equal(got.length, want.length, `ray ${n} cull=${cull}`);
      got.forEach((h, i) => { assert.equal(h.tri, want[i].tri); assert.ok(Math.abs(h.t - want[i].t) < 1e-6); });
      if (got.length) withHits++;
    }
  }
  assert.ok(withHits > 50, 'the test must actually exercise hits');
});

test('degenerate input: all triangles identical does not recurse forever', () => {
  const pos = new Float32Array([0, 0, 0, 1, 0, 0, 0, 1, 0]);
  const idx = new Uint32Array(300 * 3);
  for (let t = 0; t < 300; t++) idx.set([0, 1, 2], t * 3);
  assert.equal(raycastBVH(buildBVH(pos, idx), [0.2, 0.2, -1], [0, 0, 1]).length, 300);
});
