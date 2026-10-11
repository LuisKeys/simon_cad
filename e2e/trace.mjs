// Trace image end-to-end: Create ▸ Trace image… opens the dialog, a PNG with a hole is chosen, the
// live preview and the findings appear, the size is required, and Create adds one sketch (with the
// hole) that extrudes to the traced area; centerline mode traces strokes into paths; Esc closes.
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
const chrome = process.env.CHROME_BIN || ['/usr/bin/google-chrome', '/usr/bin/chromium', '/usr/bin/chromium-browser',
  '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome'].find((p) => existsSync(p));

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
  await api('level', { level: 'auto' });

  // a 400x400 PNG: a black disc with a white hole, drawn in the page
  const logo = await page.evaluate(async () => {
    const c = document.createElement('canvas'); c.width = c.height = 400;
    const g = c.getContext('2d');
    g.fillStyle = '#fff'; g.fillRect(0, 0, 400, 400);
    g.fillStyle = '#000'; g.beginPath(); g.arc(200, 200, 150, 0, 7); g.fill();
    g.fillStyle = '#fff'; g.beginPath(); g.arc(200, 200, 70, 0, 7); g.fill();
    const blob = await new Promise((r) => c.toBlob(r, 'image/png'));
    return Array.from(new Uint8Array(await blob.arrayBuffer()));
  });
  const png = Buffer.from(logo);

  // open from the Create menu
  await page.locator('#ctx-actions button', { hasText: 'Create' }).first().click();
  await page.locator('.ctxmenu', { hasText: 'Trace image' }).locator('text=Trace image').click();
  await page.waitForSelector('#trace:not([hidden])');
  assert.ok(await page.locator('#trace .trace-hint').isVisible(), 'the drop hint shows before a file is chosen');
  assert.ok(await page.locator('#trace button[type=submit]').isDisabled(), 'Create waits for an image and a size');

  await page.locator('#trace input[type=file]').setInputFiles({ name: 'logo.png', mimeType: 'image/png', buffer: png });
  await page.waitForSelector('#trace .trace-overlay:not([hidden])');
  await page.waitForFunction(() => /region/.test(document.querySelector('#trace .trace-status')?.textContent ?? ''));
  const status = await page.locator('#trace .trace-status').textContent();
  assert.match(status, /1 region.*1 hole/);
  await page.screenshot({ path: join(shots, 'trace-1-preview.png') });

  // the real size is required: Create stays disabled until it is given
  assert.ok(await page.locator('#trace button[type=submit]').isDisabled());
  assert.match(await page.locator('#trace .trace-note').textContent(), /real width/i);
  await page.locator('#trace .trace-row', { hasText: 'Real width' }).locator('input').fill('40');
  await page.locator('#trace .trace-row', { hasText: 'Real width' }).locator('input').dispatchEvent('change');
  await page.waitForFunction(() => /40 × 40/.test(document.querySelector('#trace .trace-status')?.textContent ?? ''));
  assert.match(await page.locator('#trace .trace-note').textContent(), /height 40/);
  await page.waitForFunction(() => !document.querySelector('#trace button[type=submit]').disabled);
  await page.screenshot({ path: join(shots, 'trace-2-sized.png') });

  await page.locator('#trace button[type=submit]').click();
  await page.waitForFunction(() => document.querySelector('#trace').hidden);
  const sketches = (await api('state')).tree.filter((n) => n.type === 'sketch');
  assert.equal(sketches.length, 1, 'one sketch was created');
  const sk = sketches[0];
  const ex = await api('tool', { name: 'extrude_sketch', args: { id: 'plaque', sketch: sk.id, depth: 3 } });
  assert.ok(ex.ok, ex.error);
  const v = await api('tool', { name: 'validate_geometry', args: { object: 'plaque' } });
  const want = Math.PI * (15 * 15 - 7 * 7) * 3; // 150 px → 15 mm, 70 px → 7 mm at 10 px/mm
  assert.ok(Math.abs(v.data.bodies[0].volume_mm3 - want) / want < 0.02, `volume ${v.data.bodies[0].volume_mm3} want ${want}`);

  // centerline mode on a stroke drawing: the same dialog, another mode
  const strokes = await page.evaluate(async () => {
    const c = document.createElement('canvas'); c.width = 400; c.height = 300;
    const g = c.getContext('2d');
    g.fillStyle = '#fff'; g.fillRect(0, 0, 400, 300);
    g.strokeStyle = '#000'; g.lineWidth = 8; g.lineCap = 'round';
    g.beginPath(); g.moveTo(60, 240); g.bezierCurveTo(120, 40, 280, 40, 340, 240); g.stroke();
    const blob = await new Promise((r) => c.toBlob(r, 'image/png'));
    return Array.from(new Uint8Array(await blob.arrayBuffer()));
  });
  await page.locator('#ctx-actions button', { hasText: 'Create' }).first().click();
  await page.locator('.ctxmenu', { hasText: 'Trace image' }).locator('text=Trace image').click();
  await page.waitForSelector('#trace:not([hidden])');
  await page.locator('#trace input[type=file]').setInputFiles({ name: 'curve.png', mimeType: 'image/png', buffer: Buffer.from(strokes) });
  await page.locator('#trace .trace-mode', { hasText: 'Centerlines' }).click();
  await page.waitForFunction(() => /stroke/.test(document.querySelector('#trace .trace-status')?.textContent ?? ''));
  assert.match(await page.locator('#trace .trace-status').textContent(), /1 stroke.*pen width/);
  await page.locator('#trace .trace-row', { hasText: 'Real width' }).locator('input').fill('80');
  await page.locator('#trace .trace-row', { hasText: 'Real width' }).locator('input').dispatchEvent('change');
  await page.waitForFunction(() => !document.querySelector('#trace button[type=submit]').disabled);
  await page.screenshot({ path: join(shots, 'trace-3-centerline.png') });
  await page.locator('#trace button[type=submit]').click();
  await page.waitForFunction(() => document.querySelector('#trace').hidden);
  const paths = (await api('state')).tree.filter((n) => n.type === 'path');
  assert.equal(paths.length, 1, 'the stroke became one path');
  assert.equal(paths[0].params.type, 'bezier');
  assert.ok(paths[0].params.points.length >= 2);
  await page.waitForFunction(() => window.simonViewport.pathData?.size === 1);

  // Stamp mode: plate size is asked for, the preview shows the physical stamp and its impression
  await page.locator('#ctx-actions button', { hasText: 'Create' }).first().click();
  await page.locator('.ctxmenu', { hasText: 'Trace image' }).locator('text=Trace image').click();
  await page.waitForSelector('#trace:not([hidden])');
  await page.locator('#trace input[type=file]').setInputFiles({ name: 'seal.png', mimeType: 'image/png', buffer: png });
  await page.locator('#trace .trace-target', { hasText: 'Stamp' }).click();
  await page.waitForFunction(() => /region/.test(document.querySelector('#trace .trace-status')?.textContent ?? ''));
  assert.ok(await page.locator('#trace button[type=submit]').isDisabled(), 'a stamp needs its size');
  await page.locator('#trace .trace-row', { has: page.getByText('Width (mm)', { exact: true }) }).locator('input').fill('60');
  await page.locator('#trace .trace-row', { has: page.getByText('Width (mm)', { exact: true }) }).locator('input').dispatchEvent('change');
  await page.locator('#trace .trace-row', { has: page.getByText('Height (mm)', { exact: true }) }).locator('input').fill('40');
  await page.locator('#trace .trace-row', { has: page.getByText('Height (mm)', { exact: true }) }).locator('input').dispatchEvent('change');
  await page.waitForSelector('#trace .trace-plate:not([hidden])');
  const tf = () => page.locator('#trace .trace-plate-design').evaluate((el) => el.style.transform);
  assert.equal(await tf(), 'scaleX(-1)', 'the physical stamp shows the mirrored relief');
  await page.locator('#trace .trace-plate-tabs button', { hasText: 'Impression' }).click();
  assert.equal(await tf(), 'none', 'the impression reads like the source');
  await page.screenshot({ path: join(shots, 'trace-4-stamp.png') });
  await page.locator('#trace .trace-row', { has: page.getByText('Relief height (mm)', { exact: true }) }).locator('input').fill('2');
  await page.locator('#trace .trace-row', { has: page.getByText('Relief height (mm)', { exact: true }) }).locator('input').dispatchEvent('change');
  await page.waitForFunction(() => !document.querySelector('#trace button[type=submit]').disabled);
  await page.locator('#trace button[type=submit]').click();
  await page.waitForFunction(() => document.querySelector('#trace').hidden);
  await page.waitForFunction(() => window.simonViewport.bodies.has('seal'));
  const rec = (await api('tool', { name: 'get_recipe', args: { recipe: 'seal' } })).data.recipe;
  assert.equal(rec.mirror, true);
  assert.equal(rec.base.width, 60);
  assert.equal(rec.design.relief_height, 2);
  assert.equal('shrinkage_percent' in rec && rec.shrinkage_percent !== 0, false, 'no shrinkage is assumed');
  const pr = (await api('tool', { name: 'validate_printability', args: { object: 'seal' } })).data;
  assert.equal(pr.printable, true);
  const imp = (await api('tool', { name: 'stamp_impression', args: { stamp: 'seal' } })).data;
  assert.ok(imp.impression_svg.includes('<svg'));

  // Esc closes the dialog without creating anything
  await page.locator('#ctx-actions button', { hasText: 'Create' }).first().click();
  await page.locator('.ctxmenu', { hasText: 'Trace image' }).locator('text=Trace image').click();
  await page.waitForSelector('#trace:not([hidden])');
  const count = (await api('state')).tree.length;
  await page.keyboard.press('Escape');
  await page.waitForFunction(() => document.querySelector('#trace').hidden);
  assert.equal((await api('state')).tree.length, count);

  assert.deepEqual(errors, [], 'no console errors');
  console.log('trace e2e: OK');
} catch (e) {
  failed = true;
  console.error(e);
} finally {
  await browser.close();
  server.kill();
}
process.exit(failed ? 1 : 0);
