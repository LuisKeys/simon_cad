import test from 'node:test';
import assert from 'node:assert/strict';
import { DEFAULT_GRID, gridLines, gridShown, gridKey } from './grid-model.js';

const segs = (a) => { const out = []; for (let i = 0; i < a.length; i += 6) out.push([a.slice(i, i + 3), a.slice(i + 3, i + 6)]); return out; };

test('lines sit on exact multiples of the spacing from the origin', () => {
  const g = gridLines({ ...DEFAULT_GRID, spacing: 10, extent: 100, major_interval: 5 });
  assert.equal(g.cells, 10);
  const all = [...segs(g.minor), ...segs(g.major), ...g.axes.flatMap((a) => segs(a.positions))];
  assert.equal(all.length, 22); // 11 lines each way
  for (const [p, q] of all) {
    assert.equal(p[2], 0); assert.equal(q[2], 0); // XY plane, at the origin's Z
    for (const c of [...p.slice(0, 2), ...q.slice(0, 2)]) assert.equal(Math.abs(c) % 10, 0);
  }
  // Majors every 5 cells (±50), and the centre lines are the axes through the origin.
  assert.equal(segs(g.major).length, 4); // u = ±50 and v = ±50
  assert.ok(segs(g.major).every(([p, q]) => (p[0] === q[0] && Math.abs(p[0]) === 50) || (p[1] === q[1] && Math.abs(p[1]) === 50)));
  assert.equal(g.axes[0].axis, 0); assert.equal(g.axes[1].axis, 1); assert.equal(g.normal, 2);
  assert.deepEqual(segs(g.axes[0].positions), [[[-50, 0, 0], [50, 0, 0]]]);
});

test('origin, plane and extent are respected', () => {
  const g = gridLines({ ...DEFAULT_GRID, spacing: 2, extent: 7, origin: [1, 2, 3] }, 'XZ');
  assert.equal(g.normal, 1);
  assert.equal(g.cells, 2); // floor(3.5 / 2) = 1 cell each side: the grid ends on a line
  for (const [p, q] of segs([...g.minor, ...g.major])) {
    assert.equal(p[1], 2); assert.equal(q[1], 2); // in the plane y = origin y
    assert.equal(((p[0] - 1) % 2 + 2) % 2, 0);
  }
  assert.deepEqual(segs(g.axes[0].positions), [[[-1, 2, 3], [3, 2, 3]]]); // the X line, ±2 around x = 1
  const yz = gridLines({ ...DEFAULT_GRID, spacing: 5, extent: 10 }, 'YZ');
  assert.equal(yz.normal, 0);
  assert.ok(segs(yz.minor).every(([p, q]) => p[0] === 0 && q[0] === 0));
});

test('visibility and rebuild key', () => {
  assert.equal(gridShown(DEFAULT_GRID), true);
  assert.equal(gridShown({ ...DEFAULT_GRID, visible: false }), false);
  assert.equal(gridShown({ ...DEFAULT_GRID, enabled: false }), false);
  assert.equal(gridKey(DEFAULT_GRID), gridKey({ ...DEFAULT_GRID, visible: false, plane: 'XZ' }));
  assert.notEqual(gridKey(DEFAULT_GRID), gridKey({ ...DEFAULT_GRID, spacing: 5 }));
});
