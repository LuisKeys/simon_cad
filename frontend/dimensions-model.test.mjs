import test from 'node:test';
import assert from 'node:assert/strict';
import { AXES, parseDimension, formatMM, proportionalSize, dimensionLines, labelVisible, toWorld } from './dimensions-model.js';

const box = { width: 120, depth: 80, height: 40, min: [0, 0, 0], max: [120, 80, 40], axes: [[1, 0, 0], [0, 1, 0], [0, 0, 1]] };
const close = (a, b) => a.every((v, i) => Math.abs(v - b[i]) < 1e-9);

test('typed sizes: plain numbers, a mm suffix and a decimal comma', () => {
  assert.deepEqual(parseDimension('150'), { ok: true, value: 150 });
  assert.deepEqual(parseDimension(' 150 mm '), { ok: true, value: 150 });
  assert.deepEqual(parseDimension('12,5'), { ok: true, value: 12.5 });
  assert.deepEqual(parseDimension('.5'), { ok: true, value: 0.5 });
  for (const bad of ['', 'abc', '0', '-3', '1/2', '2e9', 'NaN', '150 cm']) assert.equal(parseDimension(bad).ok, false, bad);
});

test('sizes show at most two decimals', () => {
  assert.equal(formatMM(150), '150');
  assert.equal(formatMM(12.3456), '12.35');
  assert.equal(formatMM(NaN), '');
});

test('lock proportions scales the other two dimensions by the same factor', () => {
  assert.deepEqual(proportionalSize(box, 'width', 150, false), { width: 150, depth: 80, height: 40 });
  assert.deepEqual(proportionalSize(box, 'width', 150, true), { width: 150, depth: 100, height: 50 });
});

test('dimension lines run along the local axes, off the box, with the measured value', () => {
  const lines = dimensionLines(box, { gap: 10 });
  assert.deepEqual(lines.map((l) => l.axis), AXES);
  const [w, d, h] = lines;
  assert.ok(close(w.a, [0, -10, 0]) && close(w.b, [120, -10, 0]), 'width along the front edge, pushed to -Y');
  assert.ok(close(d.a, [130, 0, 0]) && close(d.b, [130, 80, 0]), 'depth along the right edge, pushed to +X');
  assert.equal(h.a[2], 0); assert.equal(h.b[2], 40);
  assert.deepEqual(lines.map((l) => l.value), [120, 80, 40]);
  assert.ok(close(w.mid, [60, -10, 0]));
  for (const l of lines) assert.equal(l.ext.length, 2);
});

test('a rotated object gets its lines in its own frame', () => {
  const c = Math.SQRT1_2;
  const rotated = { ...box, axes: [[c, c, 0], [-c, c, 0], [0, 0, 1]] }; // 45° about Z
  const [w] = dimensionLines(rotated, { gap: 10 });
  const len = Math.hypot(...w.b.map((v, i) => v - w.a[i]));
  assert.ok(Math.abs(len - 120) < 1e-9, 'the width line is as long as the width');
  assert.ok(close(toWorld(rotated, [1, 0, 0]), [c, c, 0]));
});

test('labels hide when their line is seen end-on or behind the camera', () => {
  assert.equal(labelVisible({ x: 0, y: 0, z: 0 }, { x: 100, y: 0, z: 0 }), true);
  assert.equal(labelVisible({ x: 50, y: 50, z: 0 }, { x: 52, y: 51, z: 0 }), false);
  assert.equal(labelVisible({ x: 0, y: 0, z: 1.2 }, { x: 100, y: 0, z: 0 }), false);
  assert.equal(labelVisible(null, { x: 1, y: 1, z: 0 }), false);
});
