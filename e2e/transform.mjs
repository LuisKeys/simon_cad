// Interactive transformations end-to-end: select → G / R (and Right-click → Transform), the
// object follows the mouse with no request to the server, X/Y/Z and Shift+X/Y/Z constraints,
// Esc / right-click cancel restoring the object exactly with no history entry, click / Enter
// confirming one undoable change, grid and angular snapping, shortcuts ignored while typing, and
// an operation cancelled safely when its target is rebuilt underneath it.
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
  // Every request the page makes to the API while a transform previews is a failure.
  const calls = [];
  page.on('request', (r) => { if (r.url().includes('/api/') && !r.url().includes('/api/events')) calls.push(r.url()); });
  await page.goto(url);
  await page.waitForSelector('#tree');

  const api = (path, body) => page.evaluate(async ([p, b]) => {
    const r = await fetch('/api/' + p, { method: b === undefined ? 'GET' : 'POST', headers: { 'X-Simon-Token': document.querySelector('meta[name=simon-token]').content, 'Content-Type': 'application/json' }, body: b === undefined ? undefined : JSON.stringify(b) });
    return r.json();
  }, [path, body]);
  const leaf = async () => (await api('state')).tree.filter((n) => n.visible && n.id !== 'other').map((n) => n.id).pop();
  const origin = async (id) => {
    const o = await api('tool', { name: 'get_object_transform', args: { object_id: id ?? await leaf() } });
    assert.ok(o.ok, o.error);
    return o.data.origin;
  };
  const mode = () => page.evaluate(() => window.simonTransform.mode);
  // The previewed motion of a body: its group matrix (column-major), translation at 12..14.
  const matrix = (id) => page.evaluate((x) => [...window.simonViewport.bodies.get(x).group.matrix.elements], id);
  const isIdentity = (m) => m.every((v, i) => Math.abs(v - (i % 5 === 0 ? 1 : 0)) < 1e-12);
  const select = async (id) => {
    await page.locator('.node .row', { hasText: id }).first().click();
    await page.waitForFunction((x) => document.querySelector(`.dim-label[data-object="${x}"]`), id);
  };
  const vpBox = await page.locator('#viewport').boundingBox();
  const cx = vpBox.x + vpBox.width / 2, cy = vpBox.y + vpBox.height / 2;
  const hash = async () => { const st = await api('state'); return JSON.stringify([st.can_undo, st.can_redo, st.tree.map((n) => n.id)]); };

  await api('level', { level: 'auto' });
  await api('tool', { name: 'create_box', args: { id: 'b', name: 'b', dx: 20, dy: 20, dz: 20 } });
  await api('tool', { name: 'create_box', args: { id: 'other', name: 'other', dx: 10, dy: 10, dz: 10, x: 120 } });
  await page.waitForFunction(() => window.simonViewport.bodies.size === 2);
  await api('level', { level: 'review' }); // a drag is direct manipulation: applied at once even in Review
  await page.mouse.move(cx, cy);
  await page.keyboard.press('f');
  await select('b');

  // 1. G starts Move: no jump at begin, the object follows the mouse, nothing is sent.
  await page.mouse.move(cx, cy);
  const before = await hash();
  calls.length = 0;
  await page.keyboard.press('g');
  assert.equal(await mode(), 'TRANSLATING');
  assert.ok(isIdentity(await matrix('b')), 'activating Move must not move the object');
  assert.ok(await page.locator('.xform-hud').isVisible(), 'the HUD shows while transforming');
  for (let i = 1; i <= 10; i++) await page.mouse.move(cx + i * 12, cy - i * 4);
  let m = await matrix('b');
  assert.ok(Math.abs(m[12]) > 1 || Math.abs(m[13]) > 1, 'the object follows the mouse');
  assert.match(await page.locator('.xform-hud .main').textContent(), /Move · XY plane \(grid\)/);
  // 2. X constrains to the X axis; Shift+Z is the XY plane again; Y the Y axis.
  await page.keyboard.press('x');
  m = await matrix('b');
  assert.ok(Math.abs(m[12]) > 1 && m[13] === 0 && m[14] === 0, `X axis only: ${m.slice(12, 15)}`);
  assert.match(await page.locator('.xform-hud .main').textContent(), /X axis/);
  await page.screenshot({ path: join(shots, 'transform-1-move-x.png') });
  await page.keyboard.press('y');
  m = await matrix('b');
  assert.ok(m[12] === 0 && m[14] === 0, 'Y axis only');
  await page.keyboard.press('Shift+Z');
  assert.match(await page.locator('.xform-hud .main').textContent(), /XY plane/);
  // Shortcuts that would conflict do nothing meanwhile (M would toggle Measure, Delete delete).
  await page.keyboard.press('m');
  await page.keyboard.press('Delete');
  assert.equal(calls.length, 0, `a preview makes no request: ${calls}`);
  // 3. Esc cancels: the original transform exactly, and nothing in the history.
  await page.keyboard.press('Escape');
  assert.equal(await mode(), 'IDLE');
  assert.ok(isIdentity(await matrix('b')), 'cancel restores the object exactly');
  assert.equal(await hash(), before, 'a cancelled transform leaves no history entry');
  assert.equal(await page.evaluate(() => window.simonViewport.measure.on), false, 'M was swallowed while transforming');

  // 4. Click confirms one ChangeSet; Ctrl+Z undoes it, Ctrl+Shift+Z redoes it.
  await page.mouse.move(cx, cy);
  await page.keyboard.press('g');
  await page.keyboard.press('x');
  await page.mouse.move(cx + 150, cy + 30);
  await page.mouse.click(cx + 150, cy + 30);
  await page.waitForFunction(() => window.simonTransform.mode === 'IDLE' && !window.simonTransform.committing);
  await page.waitForFunction(() => window.simonViewport.bodies.size === 2 && !window.simonViewport.bodies.has('b'));
  const moved = await leaf();
  const o1 = await origin(moved);
  assert.ok(o1[0] > 1 && o1[1] === 0 && o1[2] === 0, `moved along X only: ${o1}`);
  assert.ok(isIdentity(await matrix(moved)), 'the committed body is drawn from the model, not the preview');
  assert.equal((await api('state')).selection.selectors.length, 1, 'the moved object stays selected');
  await page.mouse.move(cx, cy - 200);
  await page.keyboard.press('Control+z');
  await page.waitForFunction(() => window.simonViewport.bodies.has('b'));
  assert.deepEqual(await origin('b'), [0, 0, 0], 'undo restores the original position');
  await page.keyboard.press('Control+Shift+Z');
  await page.waitForFunction((x) => window.simonViewport.bodies.has(x), moved);
  assert.deepEqual(await origin(moved), o1, 'redo restores the move');

  // 5. Grid snapping: the origin lands on the 10 mm grid.
  await api('tool', { name: 'configure_snapping', args: { enabled: true, grid: true, translation: true, rotation: true, rotation_increment: 15 } });
  await page.waitForFunction(() => window.simonTransform.hooks.workspace()?.snap?.enabled); // the client has the new settings
  await select(moved);
  await page.mouse.move(cx, cy);
  await page.keyboard.press('g');
  await page.mouse.move(cx - 97, cy + 61);
  assert.match(await page.locator('.xform-hud .main').textContent(), /Snap on/);
  await page.keyboard.press('Enter');
  await page.waitForFunction((x) => window.simonTransform.mode === 'IDLE' && !window.simonTransform.committing && !window.simonViewport.bodies.has(x), moved);
  const snapped = await origin(await leaf());
  assert.ok(snapped.every((v) => Math.abs(v / 10 - Math.round(v / 10)) < 1e-9), `snapped to the grid: ${snapped}`);

  // 6. Rotate (R) about Z with 15° snapping; the angle is shown and stored snapped.
  await select(await leaf());
  await page.mouse.move(cx + 200, cy);
  await page.keyboard.press('r');
  assert.equal(await mode(), 'ROTATING');
  await page.keyboard.press('z');
  for (let a = 0; a <= 70; a += 10) await page.mouse.move(cx + 200 * Math.cos((a * Math.PI) / 180), cy - 200 * Math.sin((a * Math.PI) / 180));
  assert.match(await page.locator('.xform-hud .main').textContent(), /Rotate · Z axis · .*° · pivot: object origin · Snap on/);
  await page.keyboard.press('p');
  assert.match(await page.locator('.xform-hud .main').textContent(), /pivot: object center/);
  await page.screenshot({ path: join(shots, 'transform-2-rotate.png') });
  const id6 = await leaf();
  await page.mouse.click(cx + 200 * Math.cos(1.22), cy - 200 * Math.sin(1.22));
  await page.waitForFunction((x) => !window.simonTransform.committing && !window.simonViewport.bodies.has(x), id6);
  const deg = (await api('tool', { name: 'get_object_transform', args: { object_id: await leaf() } })).data.rotation_deg;
  assert.ok(Math.abs(deg / 15 - Math.round(deg / 15)) < 1e-9 && deg !== 0, `rotation snapped to 15°: ${deg}`);

  assert.ok(await page.evaluate(() => window.simonViewport.selGroup.matrix.elements.every((v, i) => Math.abs(v - (i % 5 === 0 ? 1 : 0)) < 1e-12)),
    'the selection outline is drawn from the model after a commit');

  // 7. Right-click → Transform → Move; a right click cancels it and no menu opens.
  const target = await leaf();
  const sc = await page.evaluate((id) => {
    const vp = window.simonViewport, c = vp.bodyCenter(id), p = vp.projectToPixels(new (vp.scene.position.constructor)(...c));
    const r = vp.canvas.getBoundingClientRect();
    return { x: r.left + p.x, y: r.top + p.y };
  }, target);
  await page.mouse.click(sc.x, sc.y, { button: 'right' });
  await page.waitForSelector('#ctxmenu:not([hidden]) li[data-xform="move"]');
  assert.equal(await page.locator('#ctxmenu li.head', { hasText: 'Transform' }).count(), 1);
  assert.match(await page.locator('#ctxmenu li[data-xform="rotate"]').textContent(), /Rotate\s*R/);
  await page.locator('#ctxmenu li[data-xform="move"]').click();
  assert.equal(await mode(), 'TRANSLATING');
  await page.mouse.move(sc.x + 80, sc.y);
  await page.mouse.click(sc.x + 80, sc.y, { button: 'right' });
  assert.equal(await mode(), 'IDLE');
  assert.ok(isIdentity(await matrix(target)), 'right-click cancel restores the object');
  await page.waitForTimeout(100);
  assert.equal(await page.locator('#ctxmenu').isHidden(), true, 'the cancelling right click opens no menu');

  // 8. Shortcuts are ignored while typing.
  await page.locator('#prompt').click();
  await page.keyboard.type('gr');
  assert.equal(await mode(), 'IDLE');
  assert.equal(await page.inputValue('#prompt'), 'gr');
  await page.fill('#prompt', '');

  // 9. A transform whose target is rebuilt underneath it (an agent edit) is cancelled safely.
  await select(target);
  await page.mouse.move(cx, cy);
  await page.keyboard.press('g');
  await page.mouse.move(cx + 60, cy);
  await api('level', { level: 'auto' });
  const edit = await api('tool', { name: 'modify_parameter', args: { feature: 'b', parameter: 'dx', value: 30 } });
  assert.ok(edit.committed, 'the concurrent edit is applied');
  await page.waitForFunction(() => window.simonTransform.mode === 'IDLE', null, { timeout: 5000 });
  assert.match(await page.locator('#toast').textContent(), /changed while it was being transformed/);

  // 10. No selection: G does nothing but explain.
  await page.mouse.click(vpBox.x + 20, vpBox.y + vpBox.height - 20);
  await page.waitForFunction(() => !(window.simonViewport && document.querySelector('.dim-label')));
  await page.keyboard.press('g');
  assert.equal(await mode(), 'IDLE');

  assert.deepEqual(errors, [], 'no page errors');
  console.log('transform e2e: ok');
} catch (e) {
  failed = true;
  console.error(e);
} finally {
  await browser.close();
  server.kill();
}
process.exit(failed ? 1 : 0);
