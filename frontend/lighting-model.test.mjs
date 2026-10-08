import { test } from 'node:test';
import assert from 'node:assert/strict';
import { shadowFrame, KEY_DIR } from './lighting-model.js';

test('shadowFrame rejects empty or invalid boxes', () => {
  assert.equal(shadowFrame([Infinity, Infinity, Infinity], [-Infinity, -Infinity, -Infinity]), null);
  assert.equal(shadowFrame([0, 0, 0], [NaN, 1, 1]), null);
});

test('shadowFrame scales with the model and keeps the whole model between near and far', () => {
  for (const s of [1, 50, 5000]) {
    const f = shadowFrame([0, 0, 0], [s, s, s]);
    const toLight = Math.hypot(...f.position.map((p, i) => p - f.center[i]));
    assert.ok(Math.abs(toLight - f.radius * 4) < 1e-9 * s);
    assert.ok(f.near > 0 && f.near < toLight - f.radius, 'near plane is in front of the model');
    assert.ok(f.far > toLight + f.radius, 'far plane is behind the model');
    assert.ok(f.half >= f.radius);
    assert.equal(f.floorZ, 0);
  }
});

test('the key light is above the model (Z up) and unit length', () => {
  assert.ok(KEY_DIR[2] > 0);
  assert.ok(Math.abs(Math.hypot(...KEY_DIR) - 1) < 1e-12);
});

test('a flat or point-like model still gets a usable frame', () => {
  const f = shadowFrame([1, 1, 1], [1, 1, 1]);
  assert.ok(f.radius > 0 && f.far > f.near);
});
