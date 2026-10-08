// Interactive dimensions end-to-end: the selected object's exterior dimensions in the viewport,
// click → number field → Enter / Escape, invalid input refused in place, the Dimensions
// section of the properties kept in sync, Lock Proportions, undo/redo, local axes of a
// rotated object, and the same labels in every view of the Quad layout.
import { chromium } from 'playwright-core';
import { spawn } from 'node:child_process';
import { mkdtempSync, mkdirSync, existsSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, resolve, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';
import assert from 'node:assert/strict';

const root = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const shots = join(root, 'out', 'e2e');
mkdirSync(shots, { recursive: true });
const chrome = process.env.CHROME_BIN || ['/usr/bin/google-chrome', '/usr/bin/chromium', '/usr/bin/chromium-browser'].find((p) => existsSync(p));

const server = spawn(join(root, 'out', 'simoncad'),
  ['--no-browser', '--recording', join(root, 'testdata/ui/m2_four_holes.recording.json'), '--project', join(mkdtempSync(join(tmpdir(), 'simon-')), 'p.scadproj')],
  { cwd: root, stdio: ['ignore', 'pipe', 'inherit'] });
const url = await new Promise((res, rej) => {
  let buf = '';
  server.stdout.on('data', (d) => { buf += d; const m = /(http:\/\/\S+)/.exec(buf); if (m) res(m[1]); });
  server.on('exit', (c) => rej(new Error('server exited ' + c)));
  setTimeout(() => rej(new Error('server did not start')), 15000);
});

const browser = await chromium.launch({ executablePath: chrome, args: ['--use-gl=angle', '--use-angle=swiftshader', '--enable-unsafe-swiftshader', '--ignore-gpu-blocklist'] });
let failed = false;
try {
  const page = await (await browser.newContext({ viewport: { width: 1440, height: 900 } })).newPage();
  const errors = [];
  page.on('pageerror', (e) => errors.push('pageerror: ' + e.message));
  page.on('console', (m) => { if (m.type() === 'error') errors.push('console: ' + m.text()); });
  await page.goto(url);
  await page.waitForSelector('#tree');

  const api = (path, body) => page.evaluate(async ([p, b]) => {
    const r = await fetch('/api/' + p, { method: b === undefined ? 'GET' : 'POST', headers: { 'X-Simon-Token': document.querySelector('meta[name=simon-token]').content, 'Content-Type': 'application/json' }, body: b === undefined ? undefined : JSON.stringify(b) });
    return r.json();
  }, [path, body]);
  const leaf = async () => (await api('state')).tree.filter((n) => n.visible).map((n) => n.id).pop();
  const size = async (id) => {
    const o = await api('tool', { name: 'get_object_dimensions', args: { object_id: id ?? await leaf() } });
    assert.ok(o.ok, o.error);
    return [o.data.width, o.data.depth, o.data.height];
  };
  // The labels shown in one view, as { axis: text }.
  const labelsIn = (pane) => page.evaluate((i) => Object.fromEntries([...document.querySelectorAll(`.vp-pane[data-pane="${i}"] .dim-label`)]
    .filter((el) => !el.hidden).map((el) => [el.dataset.axis, el.textContent])), pane);
  const label = (axis) => page.locator(`.vp-pane[data-pane="1"] .dim-label[data-axis="${axis}"]`);
  const select = async (id) => {
    await page.locator('.node .row', { hasText: id }).first().click();
    await page.waitForFunction((x) => document.querySelector(`.dim-label[data-object="${x}"]`), id);
  };

  await api('level', { level: 'auto' });
  await api('tool', { name: 'create_box', args: { id: 'plate', name: 'plate', dx: 120, dy: 80, dz: 40 } });
  await api('tool', { name: 'create_box', args: { id: 'other', name: 'other', dx: 10, dy: 10, dz: 10, x: 200 } });
  await page.waitForFunction(() => window.simonViewport.bodies.size === 2);
  await page.keyboard.press('f');
  // Typed dimensions apply at once even in Review, like an inline parameter edit.
  await api('level', { level: 'review' });

  // 1. Nothing selected: no dimensions. Selecting an object shows its three exterior dimensions.
  assert.equal(await page.locator('.dim-label').count(), 0);
  await select('plate');
  assert.deepEqual(await labelsIn(1), { width: '120', depth: '80', height: '40' });
  assert.equal(await label('width').evaluate((el) => getComputedStyle(el).cursor), 'text', 'an editable label shows a text cursor');
  assert.equal(await page.inputValue('#dim-width'), '120');
  assert.equal(await page.inputValue('#dim-height'), '40');
  assert.equal(await page.inputValue('#dim-depth'), '80');
  assert.equal(await page.isChecked('#dim-lock'), false, 'Lock Proportions defaults to off');
  await page.screenshot({ path: join(shots, 'dimensions-1-selected.png') });

  // 2. Click → field; Escape cancels without a change.
  const undoBefore = (await api('state')).can_undo;
  await label('width').click();
  await page.waitForSelector('.dim-input');
  await page.fill('.dim-input', '999');
  await page.keyboard.press('Escape');
  await page.waitForFunction(() => !document.querySelector('.dim-input'));
  assert.equal(await label('width').textContent(), '120');
  assert.deepEqual(await size('plate'), [120, 80, 40]);
  assert.equal((await api('state')).selection.selectors.length, 1, 'Escape in the field does not clear the selection');
  assert.equal((await api('state')).can_undo, undoBefore);

  // 3. Invalid input is refused in place; the field stays open.
  await label('width').click();
  await page.fill('.dim-input', 'abc');
  await page.keyboard.press('Enter');
  await page.waitForSelector('.toast.bad');
  assert.equal(await page.locator('.dim-input').count(), 1);
  assert.equal(await page.locator('.dim-input').getAttribute('aria-invalid'), 'true');
  await page.keyboard.press('Escape');

  // 4. Width 120 → 150 with Enter: only the width changes, geometry and labels update, the
  //    object stays selected and the other object is untouched.
  await label('width').click();
  await page.fill('.dim-input', '150');
  await page.keyboard.press('Enter');
  await page.waitForFunction(() => document.querySelector('.vp-pane[data-pane="1"] .dim-label[data-axis="width"]')?.textContent === '150');
  assert.deepEqual(await labelsIn(1), { width: '150', depth: '80', height: '40' });
  assert.deepEqual(await size(), [150, 80, 40]);
  assert.deepEqual(await size('other'), [10, 10, 10]);
  assert.equal(await page.inputValue('#dim-width'), '150', 'the properties follow the viewport');
  assert.equal((await api('state')).pending.length, 0, 'applied, not left for review');
  await page.screenshot({ path: join(shots, 'dimensions-2-width-150.png') });

  // 5. From the properties: height 40 → 25; the viewport follows. Consecutive edits fold into one resize.
  await page.fill('#dim-height', '25');
  await page.press('#dim-height', 'Enter');
  await page.waitForFunction(() => document.querySelector('.vp-pane[data-pane="1"] .dim-label[data-axis="height"]')?.textContent === '25');
  assert.deepEqual(await size(), [150, 80, 25]);
  assert.equal((await api('state')).tree.filter((n) => n.type === 'scale').length, 1);

  // 6. Lock Proportions: one dimension scales all three.
  await page.check('#dim-lock');
  await page.waitForFunction(() => document.querySelector('#dim-lock')?.checked);
  await label('depth').click();
  await page.fill('.dim-input', '160');
  await page.keyboard.press('Enter');
  await page.waitForFunction(() => document.querySelector('.vp-pane[data-pane="1"] .dim-label[data-axis="depth"]')?.textContent === '160');
  assert.deepEqual(await size(), [300, 160, 50]);

  // 7. Undo / redo restore the previous and next sizes.
  await page.keyboard.press('Control+z');
  await page.waitForTimeout(300);
  assert.deepEqual(await size(), [150, 80, 25]);
  await page.keyboard.press('Control+Shift+z');
  await page.waitForTimeout(300);
  assert.deepEqual(await size(), [300, 160, 50]);

  // 8. Quad View: each view labels the dimensions it can show (not those seen end-on).
  await select(await leaf());
  await page.keyboard.press('Control+Alt+4');
  await page.waitForFunction(() => document.querySelectorAll('.vp-pane:not([hidden])').length === 4);
  await page.waitForFunction(() => window.simonViewport.layout.panes.every((p) => !p.anim));
  await page.evaluate(() => { const vp = window.simonViewport; for (const i of [0, 2, 3, 1]) { vp.layout.setActive(i); vp.fit(); } });
  await page.waitForFunction(() => window.simonViewport.layout.panes.every((p) => !p.anim));
  await page.waitForTimeout(200);
  const quad = await Promise.all([0, 1, 2, 3].map(labelsIn));
  assert.deepEqual(Object.keys(quad[0]).sort(), ['depth', 'width'], 'TOP: width and depth');
  assert.deepEqual(Object.keys(quad[1]).sort(), ['depth', 'height', 'width'], 'PERSPECTIVE: all three');
  assert.deepEqual(Object.keys(quad[2]).sort(), ['height', 'width'], 'FRONT: width and height');
  assert.deepEqual(Object.keys(quad[3]).sort(), ['depth', 'height'], 'RIGHT: depth and height');
  // The lines follow the same rule: a dimension seen end-on draws nothing in that view (no stray
  // oblique stroke from its extension lines and ticks).
  const linesIn = (i) => page.evaluate((k) => {
    const d = window.simonDimensions, pane = window.simonViewport.layout.panes[k];
    d.beforePane(pane);
    return Object.fromEntries(d.items.map((it) => [it.line.axis, it.obj.visible]));
  }, i);
  assert.deepEqual(await linesIn(0), { width: true, depth: true, height: false }, 'TOP draws no height line');
  assert.deepEqual(await linesIn(2), { width: true, depth: false, height: true }, 'FRONT draws no depth line');
  assert.deepEqual(await linesIn(1), { width: true, depth: true, height: true }, 'PERSPECTIVE draws all three');
  // Edit from an orthographic view.
  await page.locator('.vp-pane[data-pane="0"] .dim-label[data-axis="width"]').click();
  await page.fill('.dim-input', '200');
  await page.keyboard.press('Enter');
  await page.waitForFunction(() => document.querySelector('.vp-pane[data-pane="2"] .dim-label[data-axis="width"]')?.textContent === '200');
  const quadSize = await size();
  [200, 160 * 200 / 300, 50 * 200 / 300].forEach((v, i) => assert.ok(Math.abs(quadSize[i] - v) < 1e-5, `${quadSize} after a locked edit in TOP`));
  await page.screenshot({ path: join(shots, 'dimensions-3-quad.png') });
  await page.evaluate(() => window.simonViewport.layout.setActive(1)); // Single shows the active view
  await page.keyboard.press('Control+Alt+4');

  // 9. A rotated object is measured along its own axes.
  await api('level', { level: 'auto' });
  await api('tool', { name: 'transform', args: { target: 'other', id: 'turned', angle_deg: 30, ax: 0, ay: 0, az: 1 } });
  await api('tool', { name: 'set_visibility', args: { features: ['resize'], visible: false } });
  await page.waitForFunction(() => window.simonViewport.bodies.size === 2);
  await page.waitForTimeout(200);
  await page.keyboard.press('f'); // frame the small object so its dimensions are long enough to label
  await select('turned');
  assert.deepEqual(await labelsIn(1), { width: '10', depth: '10', height: '10' });
  assert.match(await page.locator('.dim-note').textContent(), /own \(rotated\) axes/);

  assert.deepEqual(errors, []);
  console.log('dimensions e2e: OK');
} catch (e) {
  failed = true;
  console.error(e);
  try {
    const page = browser.contexts()[0].pages()[0];
    console.error('toast:', await page.locator('#toast').textContent(), '| labels:', await page.evaluate(() => [...document.querySelectorAll('.dim-label')].map((l) => `${l.closest('.vp-pane').dataset.pane}:${l.dataset.axis}=${l.textContent}${l.hidden ? '(hidden)' : ''}`).join(' ')));
    await page.screenshot({ path: join(shots, 'dimensions-failure.png') });
  } catch { /* the page may be gone */ }
} finally {
  await browser.close();
  server.kill();
}
process.exit(failed ? 1 : 0);
