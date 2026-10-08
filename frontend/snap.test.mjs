import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { snapContext, snapMove, snapAngle } from './snap.js';

// The same cases internal/workspace's TestSharedSnapCases runs against the Go SnapService.
const { cases } = JSON.parse(readFileSync(new URL('../internal/workspace/testdata/snap_cases.json', import.meta.url)));

test('the JS SnapService answers every shared case like the Go one', () => {
  assert.ok(cases.length > 0);
  for (const c of cases) {
    const ctx = snapContext(c.settings);
    if (c.op === 'move') {
      const r = snapMove(c.start, c.delta, c.free, ctx);
      assert.deepEqual(r.delta.map((v) => v + 0), c.want, c.name); // +0 folds -0 into 0
      assert.equal(r.snapped, c.snapped, c.name);
    } else {
      const r = snapAngle(c.rad, ctx);
      assert.ok(Math.abs(r.angle - c.want) < 1e-12, `${c.name}: ${r.angle} vs ${c.want}`);
      assert.equal(r.snapped, c.snapped, c.name);
    }
  }
});

test('bypass suspends snapping', () => {
  const s = { grid: { enabled: true, plane: 'XY', spacing: 10, origin: [0, 0, 0] }, snap: { enabled: true, grid: true, translation: true, rotation: true, rotation_increment: 15 } };
  const ctx = snapContext(s, { bypass: true });
  assert.deepEqual(snapMove([0, 0, 0], [3, 4, 5], [true, true, true], ctx), { delta: [3, 4, 5], snapped: false });
  assert.equal(snapAngle(0.3, ctx).angle, 0.3);
});
