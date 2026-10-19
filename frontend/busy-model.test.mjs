import test from 'node:test';
import assert from 'node:assert/strict';
import { stageFraction, elapsedText, etaText } from './busy-model.js';

test('a stage with an estimate fills up to 90% by then, and never reaches 100%', () => {
  assert.equal(stageFraction(0, 10000), 0);
  assert.ok(Math.abs(stageFraction(5000, 10000) - 0.45) < 1e-9);
  assert.ok(Math.abs(stageFraction(10000, 10000) - 0.9) < 1e-9);
  const late = stageFraction(100000, 10000);
  assert.ok(late > 0.9 && late < 0.95);
  assert.ok(stageFraction(1e9, 10000) < 0.95);
});

test('the bar only moves forward', () => {
  let prev = -1;
  for (let t = 0; t <= 60000; t += 500) { const f = stageFraction(t, 8000); assert.ok(f >= prev); prev = f; }
});

test('a stage of unknown length has no fraction', () => {
  assert.equal(stageFraction(1234, 0), null);
  assert.equal(stageFraction(1234, undefined), null);
  assert.equal(stageFraction(-5, 1000), 0);
});

test('elapsed and estimated times read naturally', () => {
  assert.equal(elapsedText(0), '0 s');
  assert.equal(elapsedText(3999), '3 s');
  assert.equal(elapsedText(65000), '1 min 05 s');
  assert.equal(etaText(0), '');
  assert.equal(etaText(12400), 'about 12 s');
  assert.equal(etaText(100), 'about 1 s');
  assert.equal(etaText(90000), 'about 1 min 30 s');
});
