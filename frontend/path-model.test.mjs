import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import {
  Curve, flatten, segments, frames, previewTube, convertType, movePoint, insertOnSegment, removePoint,
  nearestToRay, normalizePath, autoHandles, keyedAt, scaleOf, profileRadius, sweepPreview,
} from './path-model.js';

const cases = JSON.parse(readFileSync(new URL('../testdata/curve_cases.json', import.meta.url)));
const near = (a, b, tol = 1e-9) => a.every((v, i) => Math.abs(v - b[i]) <= tol);

for (const c of cases) {
  test(`shared case ${c.name} matches the Go engine`, () => {
    const path = normalizePath(c);
    const cv = new Curve(path);
    assert.equal(cv.segs.length, c.segments);
    assert.ok(Math.abs(cv.length - c.length) <= 1e-9 * Math.max(1, c.length), `length ${cv.length} vs ${c.length}`);
    assert.equal(flatten(path, 0.1).length, c.flat_points_tol_0_1);
    for (const s of c.samples) {
      const e = cv.at(s.t);
      assert.ok(near(e.p, s.p), `t=${s.t} p ${e.p} vs ${s.p}`);
      assert.ok(near(e.t, s.tangent), `t=${s.t} tangent ${e.t} vs ${s.tangent}`);
    }
  });
}

const helix = (r, pitch, n) => Array.from({ length: n }, (_, i) => {
  const a = 2 * Math.PI * i / 16;
  return { p: [r * Math.cos(a), r * Math.sin(a), pitch * a / (2 * Math.PI)] };
});

test('frames are orthonormal and do not spin about the tangent', () => {
  const cv = new Curve({ type: 'catmull_rom', closed: false, points: helix(10, 5, 33) });
  const pts = [], tans = [];
  for (let i = 0; i < 200; i++) { const e = cv.at(i / 199); pts.push(e.p); tans.push(e.t); }
  const fr = frames(pts, tans);
  let worst = 0;
  fr.forEach((f, i) => {
    const dot = (a, b) => a[0] * b[0] + a[1] * b[1] + a[2] * b[2];
    assert.ok(Math.abs(dot(f.n, f.t)) < 1e-9 && Math.abs(Math.hypot(...f.n) - 1) < 1e-9);
    if (i) worst = Math.max(worst, Math.acos(Math.min(1, dot(fr[i - 1].n, f.n))));
  });
  assert.ok(worst < 0.1, `step rotation ${worst}`);
});

test('twist rotates the frame about the tangent', () => {
  const fr = frames([[0, 0, 0], [0, 0, 1], [0, 0, 2]], [[0, 0, 1], [0, 0, 1], [0, 0, 1]], false, [0, 90, 0], [1, 0, 0]);
  const d = fr[0].n[0] * fr[1].n[0] + fr[0].n[1] * fr[1].n[1] + fr[0].n[2] * fr[1].n[2];
  assert.ok(Math.abs(d) < 1e-9);
});

test('closed frames meet at the seam', () => {
  const path = { type: 'catmull_rom', closed: true, points: [{ p: [10, 0, 0] }, { p: [0, 10, 4] }, { p: [-10, 0, 0] }, { p: [0, -10, -4] }] };
  const cv = new Curve(path), pts = cv.uniform(240), tans = [];
  for (let i = 0; i < 240; i++) tans.push(cv.at(i / 240).t);
  const fr = frames(pts, tans, true);
  const a = Math.acos(Math.min(1, fr[0].n[0] * fr[239].n[0] + fr[0].n[1] * fr[239].n[1] + fr[0].n[2] * fr[239].n[2]));
  assert.ok(a < 0.1, `seam angle ${a}`);
});

test('previewTube builds an indexed, closed-capped mesh that follows w/h', () => {
  const path = { type: 'linear', closed: false, points: [{ p: [0, 0, 0], w: 1, h: 1 }, { p: [20, 0, 0], w: 3, h: 3 }] };
  const m = previewTube(path, { radius: 2, sides: 8, samples: 6 });
  assert.equal(m.positions.length, m.normals.length);
  assert.ok(m.indices.every((i) => i < m.positions.length / 3));
  const rAt = (ring) => Math.hypot(m.positions[(ring * 8) * 3 + 1], m.positions[(ring * 8) * 3 + 2]);
  assert.ok(Math.abs(rAt(0) - 2) < 1e-9 && Math.abs(rAt(5) - 6) < 1e-9, `${rAt(0)} ${rAt(5)}`);
  assert.equal(previewTube({ type: 'linear', points: [{ p: [0, 0, 0] }] }), null);
});

test('convertType to bezier keeps the shape', () => {
  for (const type of ['catmull_rom', 'bspline', 'linear']) {
    const path = { type, closed: false, points: helix(5, 2, 9) };
    const q = convertType(path, 'bezier');
    const a = new Curve(normalizePath(path)), b = new Curve(q);
    for (const t of [0, 0.2, 0.5, 0.9, 1]) assert.ok(near(a.at(t).p, b.at(t).p, 1e-6), `${type} t=${t}`);
  }
});

test('movePoint carries handles; insert on a bezier segment keeps the curve', () => {
  const path = convertType({ type: 'catmull_rom', closed: false, points: helix(5, 2, 6) }, 'bezier');
  const moved = movePoint(path, 2, [1, 2, 3]);
  const d = [1 - path.points[2].p[0], 2 - path.points[2].p[1], 3 - path.points[2].p[2]];
  assert.ok(near(moved.points[2].out, path.points[2].out.map((v, i) => v + d[i])));
  const ins = insertOnSegment(path, 1, 0.4);
  assert.equal(ins.points.length, path.points.length + 1);
  const a = new Curve(path), b = new Curve(ins);
  assert.ok(Math.abs(a.length - b.length) < 1e-6, `length ${a.length} vs ${b.length}`);
});

test('removePoint refuses to go below the minimum', () => {
  assert.equal(removePoint({ type: 'linear', closed: false, points: [{ p: [0, 0, 0] }, { p: [1, 0, 0] }] }, 0), null);
  assert.equal(removePoint({ type: 'linear', closed: true, points: [{ p: [0, 0, 0] }, { p: [1, 0, 0] }, { p: [1, 1, 0] }] }, 0), null);
  assert.equal(removePoint({ type: 'linear', closed: false, points: [{ p: [0, 0, 0] }, { p: [1, 0, 0] }, { p: [2, 0, 0] }] }, 1).points.length, 2);
});

test('nearestToRay finds the segment under the cursor', () => {
  const path = { type: 'linear', closed: false, points: [{ p: [0, 0, 0] }, { p: [10, 0, 0] }, { p: [10, 10, 0] }] };
  const hit = nearestToRay(path, { origin: [10, 5, 5], dir: [0, 0, -1] });
  assert.equal(hit.seg, 1);
  assert.ok(Math.abs(hit.point[1] - 5) < 0.4 && hit.distance < 1e-6);
});

test('autoHandles follow the neighbours; keyedAt interpolates w/h/twist', () => {
  const path = { type: 'bezier', closed: false, points: [{ p: [0, 0, 0] }, { p: [10, 0, 0] }, { p: [20, 0, 0] }] };
  const h = autoHandles(path, 1);
  assert.ok(Math.abs(h.in[0] - 10 + 10 / 3) < 1e-9 && Math.abs(h.out[0] - 10 - 10 / 3) < 1e-9);
  const p2 = { type: 'linear', closed: false, points: [{ p: [0, 0, 0], w: 1, h: 1 }, { p: [10, 0, 0], w: 3, h: 2, twist: 90 }] };
  const v = keyedAt(p2, new Curve(p2), 5);
  assert.deepEqual(v.map((x) => +x.toFixed(9)), [2, 1.5, 45]);
  assert.deepEqual(scaleOf({}), [1, 1]);
});

test('segments count: open n-1, closed n', () => {
  const pts = helix(5, 1, 5);
  assert.equal(segments({ type: 'linear', closed: false, points: pts }).length, 4);
  assert.equal(segments({ type: 'bspline', closed: true, points: pts }).length, 5);
});

test('profileRadius covers every preset; sweepPreview picks the thickest profile of the path', () => {
  assert.equal(profileRadius({ shape: 'circle', radius: 3 }), 3);
  assert.equal(profileRadius({ shape: 'hollow_circle', outer_radius: 4, inner_radius: 2 }), 4);
  assert.equal(profileRadius({ shape: 'rectangle', width: 6, height: 8 }), 5);
  assert.ok(Math.abs(profileRadius({ shape: 'square', side: 2 }) - Math.SQRT2) < 1e-12);
  assert.equal(profileRadius({ shape: 'polygon', vertices: [[0, 0], [3, 4], [1, 1]] }), 5);
  assert.equal(profileRadius({ shape: 'custom', contours: [{ points: [[0, 0], [6, 8]] }] }), 10);
  assert.equal(profileRadius({}), 0);
  const tree = [
    { id: 'p', type: 'path', params: {} },
    { id: 'a', type: 'profile', params: { shape: 'circle', radius: 1 } }, { id: 'b', type: 'profile', params: { shape: 'circle', radius: 5 } },
    { id: 's1', type: 'sweep', params: { path: 'p', profile: 'a' } }, { id: 's2', type: 'sweep', params: { path: 'p', profile: 'b', up: [0, 0, 1] } },
    { id: 's3', type: 'sweep', params: { path: 'other', profile: 'b' } }];
  assert.deepEqual(sweepPreview(tree, 'p'), { radius: 5, up: [0, 0, 1] });
  assert.equal(sweepPreview(tree, 'none'), null);
});
