// Single / Quad views end-to-end: Ctrl+Alt+4 and the View menu, the four default cameras,
// picking in an orthographic view, shared selection, independent zoom, the per-view
// orientation menu, maximize/restore, the dividers, and the layout restored after a reload.
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
const chrome = process.env.CHROME_BIN || ['/usr/bin/google-chrome', '/usr/bin/chromium', '/usr/bin/chromium-browser', '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome'].find((p) => existsSync(p));

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
  const page = await (await browser.newContext({ bypassCSP: true, viewport: { width: 1440, height: 900 } })).newPage();
  const errors = [];
  page.on('pageerror', (e) => errors.push('pageerror: ' + e.message));
  page.on('console', (m) => { if (m.type() === 'error') errors.push('console: ' + m.text()); });
  await page.goto(url);
  await page.waitForSelector('#tree');

  const api = (path, body) => page.evaluate(async ([p, b]) => {
    const r = await fetch('/api/' + p, { method: b === undefined ? 'GET' : 'POST', headers: { 'X-Simon-Token': document.querySelector('meta[name=simon-token]').content, 'Content-Type': 'application/json' }, body: b === undefined ? undefined : JSON.stringify(b) });
    return r.json();
  }, [path, body]);
  const visible = () => page.evaluate(() => [...document.querySelectorAll('.vp-pane:not([hidden])')].map((p) => Number(p.dataset.pane)));
  const labels = () => page.evaluate(() => window.simonViewport.layout.panes.map((p) => p.labelText.textContent));
  // Where each view's camera looks, rounded, plus its projection and zoom.
  const cams = () => page.evaluate(() => window.simonViewport.layout.panes.map((p) => {
    const c = p.camera, d = c.position.clone();
    c.getWorldDirection(d);
    return { ortho: !!c.isOrthographicCamera, dir: [d.x, d.y, d.z].map((v) => Math.round(v * 100) / 100 + 0), zoom: c.zoom,
      dist: Math.round(c.position.distanceTo(p.controls.target)) };
  }));
  const settle = () => page.waitForFunction(() => window.simonViewport.layout.panes.every((p) => !p.anim));
  const paneBox = (i) => page.locator(`.vp-pane[data-pane="${i}"]`).boundingBox();

  await api('level', { level: 'auto' });
  await api('tool', { name: 'create_box', args: { id: 'plate', dx: 100, dy: 60, dz: 4 } });
  await page.waitForFunction(() => window.simonViewport.bodies.size === 1);

  // 1. Single view by default: the perspective view, as before.
  assert.deepEqual(await visible(), [1]);
  assert.equal((await labels())[1], 'Perspective');
  assert.equal(await page.locator('#viewport').getAttribute('data-layout'), 'single');
  assert.equal((await cams())[1].ortho, false);

  // 2. Ctrl+Alt+4 → Quad: TOP | PERSPECTIVE over FRONT | RIGHT, each with its own camera.
  await page.locator('#viewport').click({ position: { x: 300, y: 300 } });
  await page.keyboard.press('Control+Alt+4');
  await page.waitForFunction(() => document.querySelector('#viewport').dataset.layout === 'quad');
  assert.deepEqual(await visible(), [0, 1, 2, 3]);
  assert.deepEqual(await labels(), ['Top', 'Perspective', 'Front', 'Right']);
  let c = await cams();
  assert.deepEqual(c.map((x) => x.ortho), [true, false, true, true]);
  assert.deepEqual(c[0].dir, [0, 0, -1], 'top looks down');
  assert.deepEqual(c[2].dir, [0, 1, 0], 'front looks along +Y');
  assert.deepEqual(c[3].dir, [-1, 0, 0], 'right looks along -X');
  assert.equal(await page.evaluate(() => window.simonViewport.scene.children.filter((o) => o.children?.some((m) => m.userData.body)).length), 1,
    'one copy of the geometry for four views');
  await page.screenshot({ path: join(shots, 'views-1-quad.png') });

  // 3. Picking in the TOP view uses its orthographic camera; the selection shows in every view.
  await api('select', { selectors: [] });
  const target = await page.evaluate(() => {
    const vp = window.simonViewport, pane = vp.layout.panes[0];
    const P = pane.camera.position.constructor, p = new P(20, 10, 4).project(pane.camera), r = pane.surface.getBoundingClientRect();
    return { x: r.left + ((p.x + 1) / 2) * r.width, y: r.top + ((1 - p.y) / 2) * r.height };
  });
  await page.mouse.click(target.x, target.y);
  await page.waitForSelector('.chip');
  assert.match(await page.locator('.chip').first().getAttribute('title'), /^face\(feature=plate,side=\+Z\)$/);
  assert.equal(await page.locator('.vp-pane.active').getAttribute('data-pane'), '0', 'the clicked view becomes active');
  assert.equal(await page.evaluate(() => window.simonViewport.selGroup.children.filter((o) => o.isMesh).length), 1);
  await page.screenshot({ path: join(shots, 'views-2-selected-in-top.png') });
  await page.keyboard.press('Escape');

  // 4. Zooming one view leaves the others alone.
  const before = await cams();
  const fb = await paneBox(2);
  await page.mouse.move(fb.x + fb.width / 2, fb.y + fb.height / 2);
  await page.mouse.wheel(0, -400);
  await page.waitForFunction(() => window.simonViewport.layout.panes[2].camera.zoom > 1.05);
  const after = await cams();
  assert.ok(after[2].zoom > before[2].zoom, 'front zoomed');
  for (const i of [0, 1, 3]) assert.deepEqual(after[i], before[i], `view ${i} unchanged`);

  // 5. The label menu changes only that view's orientation (here RIGHT → BACK, then → Perspective).
  await page.locator('.vp-pane[data-pane="3"] .vp-label').click();
  await page.locator('#ctxmenu:not([hidden])').waitFor();
  await page.locator('#ctxmenu li[data-orientation="back"]').click();
  await settle();
  c = await cams();
  assert.equal((await labels())[3], 'Back');
  assert.deepEqual(c[3].dir, [0, -1, 0]);
  assert.equal(await page.locator('#ctxmenu:not([hidden])').count(), 0, 'menu closed');
  for (const i of [0, 1, 2]) assert.deepEqual(c[i], after[i], `view ${i} unchanged by the orientation change`);
  await page.locator('.vp-pane[data-pane="3"] .vp-label').click();
  await page.locator('#ctxmenu:not([hidden])').waitFor();
  assert.equal(await page.locator('#ctxmenu li[data-orientation="back"]').getAttribute('aria-checked'), 'true', 'the current orientation is marked');
  await page.locator('#ctxmenu li[data-orientation="perspective"]').click();
  await settle();
  assert.equal((await cams())[3].ortho, false);
  assert.equal(await page.locator('.vp-pane[data-pane="3"] .vp-cube').isVisible(), true, 'the view cube shows in perspective views');
  assert.equal(await page.locator('.vp-pane[data-pane="0"] .vp-cube').isVisible(), false, 'and not in orthographic ones');

  // 6. Double-click a label maximizes that view; again restores the quad with the same cameras.
  const zoomed = (await cams())[2];
  await page.locator('.vp-pane[data-pane="2"] .vp-label').dblclick();
  await page.waitForFunction(() => document.querySelector('#viewport').dataset.layout === 'maximized');
  assert.deepEqual(await visible(), [2]);
  await page.waitForTimeout(300);
  assert.equal(await page.locator('#ctxmenu:not([hidden])').count(), 0, 'no menu on double-click');
  await page.screenshot({ path: join(shots, 'views-3-maximized.png') });
  await page.locator('.vp-pane[data-pane="2"] .vp-label').dblclick();
  await page.waitForFunction(() => document.querySelector('#viewport').dataset.layout === 'quad');
  assert.deepEqual((await cams())[2], zoomed, 'position and zoom kept');

  // 7. Dividers: drag the vertical one to 30 %.
  const vb = await page.locator('#viewport').boundingBox();
  const vd = await page.locator('.vp-split.v').boundingBox();
  await page.mouse.move(vd.x + vd.width / 2, vb.y + vb.height * 0.25);
  await page.mouse.down();
  await page.mouse.move(vb.x + vb.width * 0.3, vb.y + vb.height * 0.25, { steps: 5 });
  await page.mouse.up();
  const tb = await paneBox(0);
  assert.ok(Math.abs(tb.width / vb.width - 0.3) < 0.01, `top view is 30 % wide (${tb.width / vb.width})`);
  await page.screenshot({ path: join(shots, 'views-4-split.png') });

  // 8. View menu: Single View / Quad View with a check mark on the current one.
  await page.click('#act-view');
  assert.equal(await page.locator('#ctxmenu li[data-layout="quad"]').getAttribute('aria-checked'), 'true');
  await page.locator('#ctxmenu li[data-layout="single"]').click();
  await page.waitForFunction(() => document.querySelector('#viewport').dataset.layout === 'single');
  assert.equal((await visible()).length, 1);
  await page.click('#act-view');
  await page.locator('#ctxmenu li[data-layout="quad"]').click();
  await page.waitForFunction(() => document.querySelector('#viewport').dataset.layout === 'quad');

  // 9. Everything comes back after a reload of the project.
  const saved = { labels: await labels(), cams: await cams(), w: (await paneBox(0)).width };
  await page.waitForTimeout(500);   // the layout is saved shortly after the last change
  await page.reload();
  await page.waitForFunction(() => window.simonViewport?.bodies.size === 1);
  await page.waitForFunction(() => document.querySelector('#viewport').dataset.layout === 'quad');
  assert.deepEqual(await labels(), saved.labels);
  const restored = await cams();
  for (let i = 0; i < 4; i++) {
    assert.deepEqual(restored[i].dir, saved.cams[i].dir, `view ${i} orientation`);
    assert.ok(Math.abs(restored[i].zoom - saved.cams[i].zoom) < 1e-6, `view ${i} zoom`);
  }
  assert.ok(Math.abs((await paneBox(0)).width - saved.w) < 2, 'split restored');
  await page.screenshot({ path: join(shots, 'views-5-restored.png') });

  assert.deepEqual(errors, [], 'no browser errors: ' + errors.join(' | '));
  console.log('viewports e2e: OK');
} catch (e) {
  failed = true;
  console.error(e);
} finally {
  await browser.close();
  server.kill();
}
process.exit(failed ? 1 : 0);
