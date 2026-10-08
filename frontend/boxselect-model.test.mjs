import test from 'node:test';
import assert from 'node:assert/strict';
import { rectFrom, boundsOf, rectHit } from './boxselect-model.js';

test('rectFrom normalises and detects crossing', () => {
  const r = rectFrom({ x: 50, y: 40 }, { x: 10, y: 5 });
  assert.deepEqual(r, { x0: 10, y0: 5, x1: 50, y1: 40, crossing: true });
  assert.equal(rectFrom({ x: 0, y: 0 }, { x: 5, y: 5 }).crossing, false);
});

test('window mode needs full containment, crossing mode needs overlap', () => {
  const b = boundsOf([{ x: 10, y: 10 }, { x: 30, y: 30 }]);
  const win = { x0: 0, y0: 0, x1: 20, y1: 40, crossing: false };
  assert.equal(rectHit(b, win), false);
  assert.equal(rectHit(b, { ...win, crossing: true }), true);
  assert.equal(rectHit(b, { x0: 0, y0: 0, x1: 40, y1: 40, crossing: false }), true);
  assert.equal(rectHit(b, { x0: 50, y0: 50, x1: 60, y1: 60, crossing: true }), false);
  assert.equal(rectHit(null, win), false);
});
