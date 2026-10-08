import test from 'node:test';
import assert from 'node:assert/strict';
import { confidenceClass, arcPath, circleThrough, entityShapes, entityBBox, drawingWidthPx, entityRoles, uncertain, ambiguityEdits, pct } from './vision-geom.js';

test('confidence bands', () => {
  assert.equal(confidenceClass(0.95), 'ok');
  assert.equal(confidenceClass(0.85), 'ok');
  assert.equal(confidenceClass(0.7), 'warn');
  assert.equal(confidenceClass(0.3), 'bad');
  assert.equal(confidenceClass(undefined), 'bad');
  assert.equal(pct(0.862), '86%');
  assert.equal(pct(7), '100%');
});

test('circle through three points', () => {
  const k = circleThrough([10, 0], [0, 10], [-10, 0]);
  assert.ok(Math.abs(k.r - 10) < 1e-9 && Math.abs(k.c[0]) < 1e-9 && Math.abs(k.c[1]) < 1e-9);
  assert.equal(circleThrough([0, 0], [1, 1], [2, 2]), null);
});

test('arc path direction and size', () => {
  // quarter circle in the +angle direction (clockwise on screen, y down)
  assert.equal(arcPath([10, 0], [7.07, 7.07], [0, 10]), 'M 10 0 A 10 10 0 0 1 0 10');
  // the same quarter drawn the other way round flips the sweep flag
  assert.equal(arcPath([0, 10], [7.07, 7.07], [10, 0]), 'M 0 10 A 10 10 0 0 0 10 0');
  // three quarters of a circle is a large arc
  assert.match(arcPath([10, 0], [-10, 0], [0, 10]), /A 10 10 0 1 /);
  assert.match(arcPath([0, 0], [1, 1], [2, 2]), /^M 0 0 L 2 2$/);
});

test('entity shapes', () => {
  assert.deepEqual(entityShapes({ kind: 'circle', cx: 5, cy: 6, r: 2 }), [{ tag: 'circle', attrs: { cx: 5, cy: 6, r: 2 } }]);
  assert.deepEqual(entityShapes({ kind: 'rectangle', x: 1, y: 2, w: 3, h: 4 })[0].attrs, { x: 1, y: 2, width: 3, height: 4 });
  assert.equal(entityShapes({ kind: 'polygon', points: [[0, 0], [4, 0], [0, 3]] })[0].attrs.points, '0,0 4,0 0,3');
  const slot = entityShapes({ kind: 'slot', x1: 0, y1: 0, x2: 10, y2: 0, width: 4 })[0].attrs.d;
  assert.match(slot, /^M 0 2 L 10 2 A 2 2 0 0 \d 10 -2 L 0 -2 A 2 2 0 0 \d 0 2 Z$/);
  // malformed geometry from a model never throws, it just draws nothing
  assert.deepEqual(entityShapes({ kind: 'circle', cx: 'x', cy: 1, r: 2 }), []);
  assert.deepEqual(entityShapes({ kind: 'polygon', points: [[0, 0]] }), []);
  assert.deepEqual(entityShapes({ kind: 'nope' }), []);
  assert.deepEqual(entityShapes({ kind: 'line', p1: [0, 0] }), []);
});

test('bounding boxes and drawing width', () => {
  assert.deepEqual(entityBBox({ kind: 'circle', cx: 10, cy: 10, r: 5 }), [5, 5, 15, 15]);
  assert.deepEqual(entityBBox({ kind: 'line', p1: [3, 9], p2: [1, 2] }), [1, 2, 3, 9]);
  const p = {
    entities: [{ id: 'a', kind: 'rectangle', x: 50, y: 50, w: 320, h: 200 }, { id: 'h', kind: 'circle', cx: 100, cy: 100, r: 10 }, { id: 'far', kind: 'circle', cx: 900, cy: 0, r: 5 }],
    profiles: [{ id: 'p', outer: ['a'], holes: [['h']] }],
  };
  assert.equal(drawingWidthPx(p), 320);
  assert.deepEqual(entityRoles(p), { a: 'outer', h: 'hole', far: 'unused' });
  assert.equal(drawingWidthPx({ entities: [], profiles: [] }), 0);
});

test('uncertain items', () => {
  const p = { entities: [{ id: 'a', confidence: 0.2 }, { id: 'b', confidence: 0.9 }], dimensions: [{ id: 'w', confidence: 0.5 }, { id: 't', confidence: 1 }] };
  assert.deepEqual(uncertain(p, { unconfirmed: ['w'] }).map((u) => u.kind + ':' + u.id), ['entity:a', 'dimension:w']);
});

test('ambiguity choices become explicit operations', () => {
  const ops = (c) => ambiguityEdits(['pa', 'pb'], c).map((e) => `${e.id}:${e.value.kind}:${e.value.profile ?? ''}${e.value.a ?? ''}${e.value.b ?? ''}`);
  assert.deepEqual(ops('union'), ['o1:extrude:pa', 'o2:extrude:pb', 'o3:union:o1o2']);
  assert.deepEqual(ops('subtract_ba'), ['o1:extrude:pa', 'o2:extrude:pb', 'o3:subtract:o1o2']);
  assert.deepEqual(ops('subtract_ab'), ['o1:extrude:pb', 'o2:extrude:pa', 'o3:subtract:o1o2']);
  assert.deepEqual(ops('intersect'), ['o1:extrude:pa', 'o2:extrude:pb', 'o3:intersect:o1o2']);
  assert.deepEqual(ops('separate'), ['o1:extrude:pa', 'o2:extrude:pb']);
  assert.throws(() => ambiguityEdits(['a', 'b'], 'zap'));
});
