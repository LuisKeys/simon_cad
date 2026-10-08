import { test } from 'node:test';
import assert from 'node:assert/strict';
import { normalizeRender, renderParams, RENDER_DEFAULTS } from './render-model.js';

test('missing or invalid preferences fall back to the defaults', () => {
  assert.deepEqual(normalizeRender(), RENDER_DEFAULTS);
  assert.deepEqual(normalizeRender({ environment: 'hdri', envIntensity: 999, exposure: 'x', toneMapping: 'y', shadows: 'z' }), RENDER_DEFAULTS);
});

test('valid preferences are kept', () => {
  const r = { environment: 'room', envIntensity: 150, exposure: 80, toneMapping: 'aces', shadows: 'high' };
  assert.deepEqual(normalizeRender(r), r);
});

test('renderParams turns percents into factors and shadows into a map size', () => {
  const p = renderParams({ envIntensity: 150, exposure: 80, shadows: 'high' });
  assert.equal(p.envFactor, 1.5);
  assert.equal(p.exposureFactor, 0.8);
  assert.equal(p.shadowMapSize, 4096);
  assert.equal(renderParams({ shadows: 'off' }).shadowMapSize, 0);
  assert.equal(renderParams({}).shadowMapSize, 2048);
});
