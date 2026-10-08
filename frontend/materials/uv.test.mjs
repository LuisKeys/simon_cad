import test from 'node:test';
import assert from 'node:assert/strict';
import { computeUVs, dominantAxis, fitAxis } from './uv.js';

// One quad on the XY plane (z up normal) as two triangles.
const quad = {
  positions: [0, 0, 5, 10, 0, 5, 10, 20, 5, 0, 20, 5],
  normals: [0, 0, 1, 0, 0, 1, 0, 0, 1, 0, 0, 1],
  indices: [0, 1, 2, 0, 2, 3],
  faces: [{ tri_start: 0, tri_count: 2, surface: 'plane' }],
};

test('dominantAxis', () => {
  assert.equal(dominantAxis([0.1, -0.9, 0.2]), 1);
  assert.equal(dominantAxis([0, 0, -1]), 2);
});

test('planar mapping is metric and scaled by tile size', () => {
  const uv = computeUVs(quad.positions, quad.normals, quad.indices, quad.faces, 10);
  assert.deepEqual([...uv.slice(0, 4)], [0, 0, 1, 0]);        // 10 mm = 1 tile in u
  assert.deepEqual([...uv.slice(4, 8)], [1, 2, 0, 2]);        // 20 mm = 2 tiles in v
});

test('scale 0/invalid falls back to 1 and never produces NaN', () => {
  const uv = computeUVs(quad.positions, quad.normals, quad.indices, quad.faces, 0);
  assert.ok([...uv].every(Number.isFinite));
});

test('vertices outside any face range still get box UVs', () => {
  const uv = computeUVs(quad.positions, quad.normals, quad.indices, [], 1);
  assert.deepEqual([...uv.slice(2, 4)], [10, 0]);
});

test('cylindrical mapping: u is arc length, v is along the axis', () => {
  const R = 10, n = 16, pos = [], nor = [], idx = [];
  for (let i = 0; i <= n; i++) {
    const a = (i / n) * Math.PI; // half cylinder about Z
    for (const z of [0, 30]) { pos.push(R * Math.cos(a), R * Math.sin(a), z); nor.push(Math.cos(a), Math.sin(a), 0); }
  }
  for (let i = 0; i < n; i++) { const a = i * 2; idx.push(a, a + 2, a + 1, a + 1, a + 2, a + 3); }
  const faces = [{ tri_start: 0, tri_count: n * 2, surface: 'cylinder' }];
  const axis = fitAxis(nor.reduce((acc, _, i) => (i % 3 ? acc : [...acc, nor.slice(i, i + 3)]), []));
  assert.ok(Math.abs(Math.abs(axis[2]) - 1) < 1e-6, 'axis should be Z');
  const uv = computeUVs(pos, nor, idx, faces, 1);
  const us = [], vs = [];
  for (let i = 0; i < uv.length; i += 2) { us.push(uv[i]); vs.push(uv[i + 1]); }
  assert.ok(Math.abs(Math.max(...us) - Math.min(...us) - Math.PI * R) < 0.5, 'u spans ~ half circumference');
  assert.ok(Math.abs(Math.abs(Math.max(...vs) - Math.min(...vs)) - 30) < 1e-6, 'v spans the height');
});

test('triplanarFlags marks only curved faces the UVs cannot map', async () => {
  const { triplanarFlags } = await import('./uv.js');
  // two triangles per face: vertices 0-3 plane, 4-7 sphere, 8-10 unknown surface
  const indices = [0, 1, 2, 0, 2, 3, 4, 5, 6, 4, 6, 7, 8, 9, 10];
  const faces = [{ tri_start: 0, tri_count: 2, surface: 'plane' }, { tri_start: 2, tri_count: 2, surface: 'sphere' }, { tri_start: 4, tri_count: 1 }];
  assert.deepEqual([...triplanarFlags(11, indices, faces)], [0, 0, 0, 0, 1, 1, 1, 1, 0, 0, 0]);
  assert.deepEqual([...triplanarFlags(3, [], undefined)], [0, 0, 0]);
});
