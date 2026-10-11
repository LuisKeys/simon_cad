import test from 'node:test';
import assert from 'node:assert/strict';
import { PathSession, MODE, DRAG, pointArg } from './path-editor-model.js';
import { snapContext } from './snap.js';

const noSnap = snapContext({});
const gridSnap = snapContext({ grid: { enabled: true, spacing: 5, plane: 'XY' }, snap: { enabled: true, grid: true } });
const down = (x, y) => ({ origin: [x, y, 100], dir: [0, 0, -1] }); // looking straight down onto XY

test('drawing: clicks add points, a duplicate click does not, finish needs the minimum', () => {
  const s = new PathSession('draw', { type: 'catmull_rom' });
  assert.equal(s.mode, MODE.DRAW);
  assert.equal(s.addPoint([0, 0, 0]), true);
  assert.equal(s.addPoint([0, 0, 0]), false);
  assert.equal(s.canFinish, false);
  s.addPoint([10, 5, 0]);
  assert.equal(s.canFinish, true);
  const cmd = s.finishCommand();
  assert.equal(cmd.name, 'create_path');
  assert.deepEqual(cmd.args, { points: [[0, 0, 0], [10, 5, 0]], type: 'catmull_rom' });
  s.popPoint();
  assert.equal(s.canFinish, false);
  assert.equal(s.finishCommand(), null);
});

test('drawing a closed bezier gives every point smooth handles', () => {
  const s = new PathSession('draw', { type: 'bezier' });
  [[0, 0, 0], [10, 0, 0], [10, 10, 0]].forEach((p) => s.addPoint(p));
  assert.equal(s.setClosed(true), null);
  const cmd = s.finishCommand();
  assert.equal(cmd.args.closed, true);
  assert.ok(cmd.args.points.every((p) => p.in && p.out));
  // open bezier: no handle sticks out of the ends
  const o = new PathSession('draw', { type: 'bezier' });
  [[0, 0, 0], [10, 0, 0], [10, 10, 0]].forEach((p) => o.addPoint(p));
  const oc = o.finishCommand();
  assert.equal(oc.args.points[0].in, undefined);
  assert.equal(oc.args.points[2].out, undefined);
});

test('a closed path needs 3 points', () => {
  const s = new PathSession('draw');
  s.addPoint([0, 0, 0]); s.addPoint([1, 0, 0]);
  assert.equal(s.setClosed(true), null);
  assert.match(s.error, /at least 3/);
  assert.equal(s.path.closed, false);
});

const lineEdit = () => new PathSession('edit', { id: 'spine', path: { type: 'linear', closed: false, points: [{ p: [0, 0, 0] }, { p: [10, 0, 0] }, { p: [20, 0, 0] }] } });

test('dragging a point stays local until the drag ends, then emits one update_path', () => {
  const s = lineEdit();
  assert.ok(s.beginDrag(DRAG.POINT, 1));
  s.dragTo(down(12, 7), noSnap);
  assert.deepEqual(s.path.points[1].p, [12, 7, 0]);
  assert.deepEqual(s.committed.points[1].p, [10, 0, 0]); // nothing committed yet
  const cmd = s.endDrag();
  assert.equal(cmd.name, 'update_path');
  assert.equal(cmd.args.path, 'spine');
  assert.deepEqual(cmd.args.points[1], [12, 7, 0]);
  assert.equal(s.dragging, false);
});

test('dragging snaps to the grid and Ctrl-style bypass disables it', () => {
  const s = lineEdit();
  s.beginDrag(DRAG.POINT, 1);
  s.dragTo(down(12.4, 7.4), gridSnap);
  assert.deepEqual(s.path.points[1].p, [10, 5, 0]);
  s.dragTo(down(12.4, 7.4), snapContext({ grid: { enabled: true, spacing: 5 }, snap: { enabled: true, grid: true } }, { bypass: true }));
  assert.deepEqual(s.path.points[1].p, [12.4, 7.4, 0]);
});

test('vertical drag changes only the height and keeps the plane position', () => {
  const s = lineEdit();
  s.beginDrag(DRAG.POINT, 1);
  // a ray from the side: looks along +Y at x=10, z=6
  s.dragTo({ origin: [10, -50, 6], dir: [0, 1, 0] }, noSnap, { vertical: true });
  const p = s.path.points[1].p;
  assert.ok(Math.abs(p[0] - 10) < 1e-9 && Math.abs(p[1]) < 1e-9 && Math.abs(p[2] - 6) < 1e-9, `${p}`);
});

test('an unchanged drag yields no command; cancel restores', () => {
  const s = lineEdit();
  s.beginDrag(DRAG.POINT, 1);
  assert.equal(s.endDrag(), null);
  s.beginDrag(DRAG.POINT, 1);
  s.dragTo(down(30, 30), noSnap);
  s.cancelDrag();
  assert.deepEqual(s.path.points[1].p, [10, 0, 0]);
});

test('bezier handles stay collinear by default and break when smooth is off', () => {
  const path = { type: 'bezier', closed: false, points: [
    { p: [0, 0, 0], out: [3, 0, 0] }, { p: [10, 0, 0], in: [7, 0, 0], out: [13, 0, 0] }, { p: [20, 0, 0], in: [17, 0, 0] }] };
  const s = new PathSession('edit', { id: 'b', path });
  s.beginDrag(DRAG.HANDLE, 1, { which: 'out' });
  s.dragTo(down(10, 5), noSnap);
  assert.deepEqual(s.path.points[1].out, [10, 5, 0]);
  const inH = s.path.points[1].in; // opposite direction, original length 3
  assert.ok(Math.abs(inH[0] - 10) < 1e-9 && Math.abs(inH[1] + 3) < 1e-9, `${inH}`);
  s.cancelDrag();
  s.beginDrag(DRAG.HANDLE, 1, { which: 'out' });
  s.dragTo(down(10, 5), noSnap, { smooth: false });
  assert.deepEqual(s.path.points[1].in, [7, 0, 0]);
});

test('insert keeps the shape; remove respects the minimum', () => {
  const s = lineEdit();
  const cmd = s.insert(0, 0.5);
  assert.equal(cmd.args.points.length, 4);
  assert.equal(s.sel, 1);
  assert.ok(s.remove());
  assert.equal(s.path.points.length, 3);
  const two = new PathSession('edit', { id: 'x', path: { type: 'linear', closed: false, points: [{ p: [0, 0, 0] }, { p: [1, 0, 0] }] } });
  assert.equal(two.remove(0), null);
  assert.match(two.error, /at least 2/);
});

test('type change emits a type-only command (the server converts exactly)', () => {
  const s = lineEdit();
  const cmd = s.setType('bezier');
  assert.deepEqual(cmd.args, { path: 'spine', type: 'bezier' });
  assert.equal(s.path.type, 'bezier');
  assert.ok(s.path.points[1].in && s.path.points[1].out);
  assert.equal(s.setType('nope'), null);
});

test('width, height and twist by number; invalid values are refused with a reason', () => {
  const s = lineEdit();
  assert.deepEqual(s.setPointProp(1, 'w', 2).args.points[1], { p: [10, 0, 0], w: 2 });
  assert.equal(s.setPointProp(1, 'h', 0), null);
  assert.match(s.error, /greater than 0/);
  assert.deepEqual(s.setPointProp(1, 'twist', 45).args.points[1], { p: [10, 0, 0], w: 2, twist: 45 });
  assert.equal(s.setPointProp(1, 'depth', 1), null);
});

test('width gizmo scales with the distance along the frame axis', () => {
  const s = lineEdit();
  const gizmo = { p: [10, 0, 0], t: [1, 0, 0], n: [0, 1, 0], b: [0, 0, 1], radius: 2, base: [0, 1, 0] };
  s.beginDrag(DRAG.WIDTH, 1);
  s.dragTo({ origin: [10, 6, 50], dir: [0, 0, -1] }, noSnap, { gizmo });
  assert.equal(s.path.points[1].w, 3); // 6 / radius 2
  s.dragTo({ origin: [10, -6, 50], dir: [0, 0, -1] }, noSnap, { gizmo });
  assert.equal(s.path.points[1].w, 0.05); // never collapses
});

test('twist gizmo reads the angle around the tangent', () => {
  const s = lineEdit();
  const gizmo = { p: [10, 0, 0], t: [1, 0, 0], n: [0, 1, 0], b: [0, 0, 1], radius: 2, base: [0, 1, 0] };
  s.beginDrag(DRAG.TWIST, 1);
  s.dragTo({ origin: [-50, 0, 5], dir: [1, 0, 0] }, noSnap, { gizmo }); // hits the YZ plane at (10, 0, 5): a quarter turn
  assert.ok(Math.abs(s.path.points[1].twist - 90) < 1e-6, `${s.path.points[1].twist}`);
});

test('adopt follows outside changes but not while dragging', () => {
  const s = lineEdit();
  s.select(2);
  s.adopt({ type: 'linear', closed: false, points: [{ p: [0, 0, 0] }, { p: [5, 5, 5] }] });
  assert.equal(s.path.points.length, 2);
  assert.equal(s.sel, 1);
  s.beginDrag(DRAG.POINT, 0);
  s.adopt({ type: 'linear', closed: false, points: [{ p: [9, 9, 9] }, { p: [5, 5, 5] }] });
  assert.deepEqual(s.path.points[0].p, [0, 0, 0]);
});

test('pointArg is compact and round-trips extras', () => {
  assert.deepEqual(pointArg({ p: [1, 2, 3] }), [1, 2, 3]);
  assert.deepEqual(pointArg({ p: [1, 2, 3], w: 1, h: 1 }), [1, 2, 3]);
  assert.deepEqual(pointArg({ p: [1, 2, 3], w: 2, twist: 10, in: [0, 0, 0] }), { p: [1, 2, 3], in: [0, 0, 0], w: 2, twist: 10 });
});
