import test from 'node:test';
import assert from 'node:assert/strict';
import { displayUnits, setUnits } from './units.js';

const ws = (linear, k, extra = {}) => ({ units: { linear, angular: 'deg', linear_precision: 2, angular_precision: 1, ...extra }, mm_per_unit: k });

test('defaults are millimetres and degrees', () => {
  const U = displayUnits(null);
  assert.equal(U.length(100), '100.00 mm');
  assert.equal(U.compact(150), '150');
  assert.equal(U.compact(12.3456), '12.35');
  assert.equal(U.angle(45), '45.0°');
  assert.equal(U.volume(1000), '1000.0 mm³');
});

test('the same millimetres shown in other units (the geometry value never changes)', () => {
  const mm = 100;
  assert.equal(displayUnits(ws('cm', 10)).length(mm), '10.00 cm');
  assert.equal(displayUnits(ws('in', 25.4)).length(mm), '3.94 in');
  assert.equal(displayUnits(ws('ft', 304.8, { linear_precision: 3 })).length(mm), '0.328 ft');
  assert.equal(displayUnits(ws('in', 25.4)).compact(25.4), '1');
  assert.equal(mm, 100);
});

test('typed values are read in the display unit and returned in mm', () => {
  const U = displayUnits(ws('in', 25.4));
  assert.deepEqual(U.parseLength('2'), { ok: true, value: 50.8 });
  assert.deepEqual(U.parseLength(' 2 in '), { ok: true, value: 50.8 });
  assert.deepEqual(U.parseLength('0,5'), { ok: true, value: 12.7 });
  for (const bad of ['', 'abc', '0', '-1', '2 mm']) assert.equal(U.parseLength(bad).ok, false, bad);
  assert.equal(U.parseLength('-1', { positive: false }).value, -25.4);
  assert.equal(U.toMM(U.toDisplay(123.4)), 123.4);
});

test('angles in radians', () => {
  const U = displayUnits({ units: { linear: 'mm', angular: 'rad', linear_precision: 2, angular_precision: 3 }, mm_per_unit: 1 });
  assert.equal(U.angle(180), '3.142 rad');
  assert.equal(U.angleToDeg(Math.PI / 2), 90);
  assert.equal(U.angleSymbol, 'rad');
});

test('setUnits reports a change only when the display changes', () => {
  assert.equal(setUnits(ws('mm', 1)), false);
  assert.equal(setUnits(ws('in', 25.4)), true);
  assert.equal(setUnits(ws('in', 25.4)), false);
  assert.equal(setUnits(ws('in', 25.4, { linear_precision: 4 })), true);
  setUnits(null);
});
