import test from 'node:test';
import assert from 'node:assert/strict';
import {
  ORIENTATIONS, ORIENTATION_IDS, QUAD_DEFAULT, defaultLayout, normalizeLayout, paneRects, paneAt, visiblePanes,
  toggledMode, toggledMaximize, withOrientation, orientationForDir, viewportConfig,
} from './viewlayout-model.js';

test('quad default is TOP | PERSPECTIVE over FRONT | RIGHT; single shows the perspective view', () => {
  const d = defaultLayout();
  assert.deepEqual(d.views.map((v) => v.orientation), ['top', 'perspective', 'front', 'right']);
  assert.deepEqual(QUAD_DEFAULT, ['top', 'perspective', 'front', 'right']);
  assert.equal(d.mode, 'single');
  assert.deepEqual(visiblePanes(d), [1]);
  assert.deepEqual(d.split, { x: 0.5, y: 0.5 });
});

test('every orientation has a projection, a grid plane and (for the six main views) a fixed direction', () => {
  assert.deepEqual(ORIENTATION_IDS, ['perspective', 'top', 'bottom', 'front', 'back', 'left', 'right']);
  // Z up: TOP/BOTTOM look along Z (XY grid), FRONT/BACK along Y (XZ), LEFT/RIGHT along X (YZ).
  const grids = Object.fromEntries(ORIENTATION_IDS.map((o) => [o, ORIENTATIONS[o].grid]));
  assert.deepEqual(grids, { perspective: 'XY', top: 'XY', bottom: 'XY', front: 'XZ', back: 'XZ', left: 'YZ', right: 'YZ' });
  for (const o of ORIENTATION_IDS) {
    const def = ORIENTATIONS[o];
    assert.equal(def.projection, o === 'perspective' ? 'perspective' : 'orthographic');
    if (!def.dir) continue;
    // The grid plane is perpendicular to the view direction; up lies in the screen.
    const normal = { XY: 2, XZ: 1, YZ: 0 }[def.grid];
    assert.equal(Math.abs(def.dir[normal]), 1, o);
    assert.equal(def.dir.reduce((s, c, i) => s + c * def.up[i], 0), 0, o);
  }
  assert.deepEqual(ORIENTATIONS.front.dir, [0, -1, 0]); // same as the view cube's FRONT face
});

test('a ViewportConfig carries id, projection, orientation, camera, grid plane and gizmo flag', () => {
  const c = viewportConfig(2);
  assert.deepEqual(Object.keys(c).sort(), ['cameraState', 'gridPlane', 'id', 'orientation', 'projectionType', 'showAxisGizmo']);
  assert.deepEqual([c.id, c.orientation, c.projectionType, c.gridPlane], ['vp2', 'front', 'orthographic', 'XZ']);
  const p = withOrientation({ ...c, cameraState: { position: [1, 2, 3] } }, 'perspective');
  assert.deepEqual([p.projectionType, p.gridPlane, p.cameraState.position], ['perspective', 'XY', [1, 2, 3]]);
  assert.equal(withOrientation(c, 'diagonal'), c, 'unknown orientations are ignored');
});

test('axis directions map to their orthographic view; others do not', () => {
  assert.equal(orientationForDir([0, 0, 1]), 'top');
  assert.equal(orientationForDir([0, 0, -5]), 'bottom');
  assert.equal(orientationForDir([0, -1, 0]), 'front');
  assert.equal(orientationForDir([1, 0, 0]), 'right');
  assert.equal(orientationForDir([1, -1, 1]), '');
  assert.equal(orientationForDir([0, 0, 0]), '');
});

test('quad rects tile the area at the split; single and maximized fill it', () => {
  const st = { ...defaultLayout(), mode: 'quad', split: { x: 0.25, y: 0.6 } };
  const r = paneRects(st, 800, 500);
  assert.deepEqual(r, [{ x: 0, y: 0, w: 200, h: 300 }, { x: 200, y: 0, w: 600, h: 300 }, { x: 0, y: 300, w: 200, h: 200 }, { x: 200, y: 300, w: 600, h: 200 }]);
  assert.equal(r.reduce((s, q) => s + q.w * q.h, 0), 800 * 500);
  assert.equal(paneAt(r, 199, 10), 0);
  assert.equal(paneAt(r, 200, 10), 1);
  assert.equal(paneAt(r, 650, 450), 3);
  assert.equal(paneAt(r, 900, 10), -1);
  assert.deepEqual(paneRects({ ...st, maximized: 2 }, 800, 500), [null, null, { x: 0, y: 0, w: 800, h: 500 }, null]);
  assert.deepEqual(paneRects({ ...st, mode: 'single', active: 3 }, 800, 500)[3], { x: 0, y: 0, w: 800, h: 500 });
});

test('toggling the layout and maximizing keep every view configuration', () => {
  let st = defaultLayout();
  st.views[0] = withOrientation(st.views[0], 'bottom');
  st = toggledMode(st);
  assert.equal(st.mode, 'quad');
  st = toggledMaximize(st, 3);
  assert.deepEqual([st.maximized, st.active], [3, 3]);
  assert.deepEqual(visiblePanes(st), [3]);
  st = toggledMaximize(st, 3);
  assert.equal(st.maximized, -1);
  assert.deepEqual(visiblePanes(st), [0, 1, 2, 3]);
  st = toggledMaximize(st, 1);
  st = toggledMode(st);
  assert.deepEqual([st.mode, st.maximized], ['single', -1], 'leaving quad drops the maximize');
  assert.equal(toggledMaximize(st, 2), st, 'no maximize in single view');
  assert.equal(st.views[0].orientation, 'bottom');
});

test('a stored layout is validated: bad values fall back, cameras survive', () => {
  assert.deepEqual(normalizeLayout(null), defaultLayout());
  assert.deepEqual(normalizeLayout('junk'), defaultLayout());
  const cam = { position: [1, 2, 3], target: [0, 0, 0], up: [0, 0, 1], zoom: 2, height: 40,
    lastPerspective: { position: [9, 9, 9], target: [0, 0, 0], up: [0, 0, 1] } };
  const st = normalizeLayout({ mode: 'quad', active: 7, maximized: 2, split: { x: 0.99, y: 'a' },
    views: [{ orientation: 'left', cameraState: cam, showAxisGizmo: false }, { orientation: 'sideways' }, null,
      { orientation: 'back', cameraState: { position: [1, NaN, 0], target: [0, 0, 0], up: [0, 0, 1] } }] });
  assert.equal(st.mode, 'quad');
  assert.equal(st.active, 1);
  assert.equal(st.maximized, 2);
  assert.deepEqual(st.split, { x: 0.85, y: 0.5 });
  assert.deepEqual(st.views.map((v) => v.orientation), ['left', 'perspective', 'front', 'back']);
  assert.deepEqual(st.views.map((v) => v.id), ['vp0', 'vp1', 'vp2', 'vp3']);
  assert.deepEqual(st.views[0].cameraState, cam);
  assert.equal(st.views[0].showAxisGizmo, false);
  assert.equal(st.views[0].gridPlane, 'YZ');
  assert.equal(st.views[3].cameraState, null, 'non-finite camera is dropped');
  assert.equal(normalizeLayout({ mode: 'single', maximized: 2 }).maximized, -1);
});
