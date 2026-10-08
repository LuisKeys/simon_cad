import test from 'node:test';
import assert from 'node:assert/strict';
import { scoreRows, currentIteration, comparisonViews, parameterChanges, critiqueErrors, pct } from './visualcmp-model.js';

const stages = [{ name: 'BLOCKOUT', thresholds: { side_silhouette: 0.9, proportions: 0.9 } }];

test('score rows carry thresholds and pass/fail', () => {
  const rows = scoreRows({ stage: 'BLOCKOUT', scores: { side_silhouette: 0.91, proportions: 0.8, overall: 0.85 } }, stages);
  assert.deepEqual(rows.map((r) => [r.key, r.pass]), [['side_silhouette', true], ['proportions', false], ['overall', null]]);
  assert.equal(pct(0.905), '91%');
});

test('current iteration skips reverted ones', () => {
  assert.equal(currentIteration([{ iteration: 1 }, { iteration: 2 }, { iteration: 3, reverted: true }]).iteration, 2);
  assert.equal(currentIteration([]), null);
});

test('comparison views list references first', () => {
  const views = comparisonViews({ renders: { left: 'r1', 'ref:img_abc': 'r2', front_3q: 'r3' } }, [{ image_id: 'img_abc', calibration: { view: 'left' } }]);
  assert.deepEqual(views.map((v) => v.key), ['ref:img_abc', 'left', 'front_3q']);
  assert.equal(views[0].ref, 'img_abc');
});

test('parameter changes and critique errors', () => {
  assert.deepEqual(parameterChanges({ parameters_before: { 'v.h': 1600, 'v.l': 4000 }, parameters_after: { 'v.h': 1000, 'v.l': 4000 } }),
    [{ name: 'v.h', before: 1600, after: 1000 }]);
  const errs = critiqueErrors({ critique: { largest_errors: [{ region: 'a', severity: 0.2 }, { region: 'b', severity: 0.9 }] } });
  assert.deepEqual(errs.map((e) => e.region), ['b', 'a']);
  assert.deepEqual(critiqueErrors({}), []);
});
