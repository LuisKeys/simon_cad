import { test } from 'node:test';
import assert from 'node:assert/strict';
import { TransformSession, MODE, freeAxes, constraintLabel, dragPoint, wrap } from './transform-model.js';
import { snapContext } from './snap.js';

const obj = { object_id: 'box', origin: [10, -20, 0], center: [15, -15, 5] };
// An orthographic top view: rays point down -Z from above the mouse position.
const top = (x, y) => ({ origin: [x, y, 1000], dir: [0, 0, -1] });
// A front view: rays point along +Y.
const front = (x, z) => ({ origin: [x, -1000, z], dir: [0, 1, 0] });
const close = (a, b, eps = 1e-9) => a.every((v, i) => Math.abs(v - b[i]) < eps);
const SNAP = { grid: { enabled: true, plane: 'XY', spacing: 10, origin: [0, 0, 0] },
  snap: { enabled: true, grid: true, translation: true, rotation: true, rotation_increment: 15 } };

test('state machine: begin picks the mode, the default constraint is the grid plane', () => {
  const t = new TransformSession('translate', obj, { gridPlane: 'XZ' });
  assert.equal(t.mode, MODE.TRANSLATING);
  assert.deepEqual(freeAxes(t.constraint), [true, false, true]);
  assert.equal(constraintLabel(t.constraint), 'XZ plane (grid)');
  const r = new TransformSession('rotate', obj, { gridPlane: 'XZ' });
  assert.equal(r.mode, MODE.ROTATING);
  assert.equal(r.axis, 1, 'the default rotation axis is the grid normal');
});

test('keyboard constraints: X/Y/Z axes, Shift+X/Y/Z planes, again returns to the default', () => {
  const t = new TransformSession('translate', obj);
  const expect = [['x', false, [true, false, false], 'X axis'], ['y', false, [false, true, false], 'Y axis'], ['z', false, [false, false, true], 'Z axis'],
    ['x', true, [false, true, true], 'YZ plane'], ['y', true, [true, false, true], 'XZ plane'], ['z', true, [true, true, false], 'XY plane']];
  for (const [k, shift, free, label] of expect) {
    t.constrain(k, shift);
    assert.deepEqual(freeAxes(t.constraint), free, `${shift ? 'Shift+' : ''}${k}`);
    assert.equal(constraintLabel(t.constraint), label);
  }
  t.constrain('z', true); // Shift+Z again → back to the grid plane
  assert.equal(t.constraint.isDefault, true);
  t.constrain('X'); t.constrain('x');
  assert.equal(t.constraint.isDefault, true);
  assert.equal(t.constrain('q'), false);
  const r = new TransformSession('rotate', obj);
  for (const [k, i] of [['x', 0], ['y', 1], ['z', 2]]) { r.constrain(k); assert.equal(r.axis, i); }
});

test('translation follows the mouse on each axis and plane (no jump at begin)', () => {
  const t = new TransformSession('translate', obj);
  assert.deepEqual(t.update({ ray: top(3, 4) }).delta, [0, 0, 0], 'the first update only records the reference');
  assert.ok(close(t.update({ ray: top(13, -1) }).delta, [10, -5, 0]));
  t.constrain('x');
  assert.ok(close(t.update({ ray: top(13, -1) }).delta, [10, 0, 0]));
  t.constrain('y');
  assert.ok(close(t.update({ ray: top(13, -1) }).delta, [0, -5, 0]));
  const f = new TransformSession('translate', obj);
  f.constrain('y', true); // XZ plane, seen face on from the front
  f.update({ ray: front(0, 0) });
  assert.ok(close(f.update({ ray: front(-7, 12) }).delta, [-7, 0, 12]));
  f.constrain('z');
  assert.ok(close(f.update({ ray: front(-7, 12) }).delta, [0, 0, 12]));
  f.constrain('x', true); // YZ plane: edge on from the front, only Z of the motion is in it
  assert.ok(close(f.update({ ray: front(-7, 12) }).delta, [0, 0, 12]));
});

test('a plane seen edge on and an axis pointing at the camera stay finite', () => {
  const t = new TransformSession('translate', obj); // XY plane, looked at from the front
  t.update({ ray: front(0, 0) });
  const d = t.update({ ray: front(25, 40) }).delta;
  assert.ok(d.every(Number.isFinite));
  assert.ok(close(d, [25, 0, 0]), 'the in-plane part of the motion is kept');
  const z = new TransformSession('translate', obj);
  z.constrain('z');
  z.update({ ray: top(0, 0) });
  assert.deepEqual(z.update({ ray: top(50, 50) }).delta, [0, 0, 0], 'Z points at a top camera: hold still');
  assert.equal(dragPoint(top(1, 1), obj.origin, { kind: 'axis', axis: 2 }), null);
});

test('mouse movement continuity: the result depends on the current mouse only', () => {
  const a = new TransformSession('translate', obj);
  a.update({ ray: top(0, 0) });
  for (let i = 1; i <= 500; i++) a.update({ ray: top(Math.sin(i) * 300, i * 0.37) });
  const last = a.update({ ray: top(123.456, -78.9) }).delta;
  const b = new TransformSession('translate', obj);
  b.update({ ray: top(0, 0) });
  assert.deepEqual(b.update({ ray: top(123.456, -78.9) }).delta, last, 'no drift from the path taken');
});

test('grid snapping lands the origin on grid lines, negative coordinates included', () => {
  const t = new TransformSession('translate', obj);
  const snap = snapContext(SNAP);
  t.update({ ray: top(0, 0), snap });
  const r = t.update({ ray: top(-23.7, 4.2), snap });
  assert.equal(r.snapped, true);
  const at = [0, 1, 2].map((i) => obj.origin[i] + r.delta[i]);
  assert.deepEqual(at, [-10, -20, 0]); // 10 - 23.7 = -13.7 → -10; -20 + 4.2 = -15.8 → -20
  t.constrain('x');
  assert.deepEqual(t.update({ ray: top(-23.7, 4.2), snap }).delta, [-20, 0, 0], 'constrained axes keep their coordinate');
  // Snapping settings changed mid-drag apply on the next update.
  const off = snapContext({ ...SNAP, snap: { ...SNAP.snap, enabled: false } });
  assert.ok(close(t.update({ ray: top(-23.7, 4.2), snap: off }).delta, [-23.7, 0, 0]));
});

test('rotation follows the mouse around the pivot and crosses ±180° smoothly', () => {
  const r = new TransformSession('rotate', obj);
  const c = { x: 0, y: 0 }, toCamera = [0, 0, 1];
  const at = (deg) => ({ x: 100 * Math.cos((deg * Math.PI) / 180), y: -100 * Math.sin((deg * Math.PI) / 180) });
  r.update({ screen: at(0), pivotScreen: c, toCamera });
  for (const deg of [45, 90, 135, 179, 181, 225, 270, 359, 361, 400]) {
    const res = r.update({ screen: at(deg), pivotScreen: c, toCamera });
    assert.ok(Math.abs(res.angle - (deg * Math.PI) / 180) < 1e-9, `${deg}° → ${(res.angle * 180) / Math.PI}`);
  }
  for (const deg of [300, 200, 100, 0, -100, -190]) {
    const res = r.update({ screen: at(deg), pivotScreen: c, toCamera });
    assert.ok(Math.abs(res.angle - (deg * Math.PI) / 180) < 1e-9, `back to ${deg}°`);
  }
});

test('rotation direction, dead zone, pivot and snapping', () => {
  const c = { x: 0, y: 0 };
  const behind = new TransformSession('rotate', obj);
  behind.update({ screen: { x: 100, y: 0 }, pivotScreen: c, toCamera: [0, 0, -1] });
  assert.ok(Math.abs(behind.update({ screen: { x: 0, y: -100 }, pivotScreen: c, toCamera: [0, 0, -1] }).angle + Math.PI / 2) < 1e-9,
    'counter-clockwise seen from behind the axis is a negative turn');
  const r = new TransformSession('rotate', obj, { pivot: 'center' });
  assert.deepEqual(r.pivot, obj.center);
  r.update({ screen: { x: 100, y: 0 }, pivotScreen: c, toCamera: [0, 0, 1] });
  const before = r.result;
  assert.equal(r.update({ screen: { x: 2, y: -2 }, pivotScreen: c, toCamera: [0, 0, 1] }), before, 'too close to the pivot: hold');
  const snap = snapContext(SNAP);
  const res = r.update({ screen: { x: 100 * Math.cos(0.9), y: -100 * Math.sin(0.9) }, pivotScreen: c, toCamera: [0, 0, 1], snap });
  assert.equal(res.snapped, true);
  assert.ok(Math.abs((res.angle * 180) / Math.PI - 45) < 1e-9, '51.6° snaps to 45°');
  assert.deepEqual(res.pivot, obj.center, 'the pivot stays fixed');
  r.setPivot('origin');
  assert.deepEqual(r.result.pivot, obj.origin);
});

test('confirm commits one tool call; a no-op confirm is a cancel', () => {
  const t = new TransformSession('translate', obj);
  assert.equal(t.command(), null);
  t.update({ ray: top(0, 0) });
  t.constrain('x');
  t.update({ ray: top(12.5, 3) });
  assert.deepEqual(t.command(), { name: 'move_object', args: { object_id: 'box', dx: 12.5, dy: 0, dz: 0, intent: 'Move box' } });
  const r = new TransformSession('rotate', obj, { pivot: 'center' });
  r.constrain('x');
  const c = { x: 0, y: 0 };
  r.update({ screen: { x: 100, y: 0 }, pivotScreen: c, toCamera: [1, 0, 0] });
  r.update({ screen: { x: 0, y: -100 }, pivotScreen: c, toCamera: [1, 0, 0] });
  const cmd = r.command();
  assert.equal(cmd.name, 'rotate_object');
  assert.ok(Math.abs(cmd.args.angle - 90) < 1e-9);
  assert.deepEqual([cmd.args.axis, cmd.args.pivot], ['x', 'center']);
  assert.ok(Math.abs(wrap(2 * Math.PI + 0.1) - 0.1) < 1e-12);
});
