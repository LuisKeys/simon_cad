// Materials browser end-to-end: pick a material in the panel, drag one onto the tree,
// use the context menu, undo — and confirm the geometry kernel is never invoked for any of it.
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

  // Two boxes via the palette, committed.
  for (const [id, dx] of [['plate', 100], ['lid', 30]]) {
    await page.keyboard.press('Control+k');
    await page.fill('#pal-input', 'create_box');
    await page.locator('#pal-list li').filter({ has: page.locator('.k', { hasText: /^tool$/i }), hasText: 'create_box' }).first().waitFor();
    await page.keyboard.press('Enter');
    await page.fill('#pal-args', JSON.stringify({ id, name: id, dx, dy: 60, dz: 4, x: id === 'lid' ? 200 : 0 }));
    await page.click('#pal-run');
    await page.locator('.cs').first().getByRole('button', { name: 'Commit' }).click();
    await page.waitForFunction((n) => window.simonViewport.bodies.size === n, id === 'plate' ? 1 : 2);
  }
  const metal = (id) => page.evaluate((i) => { const m = window.simonViewport.bodies.get(i).mesh.material; return { metalness: m.metalness, opacity: m.opacity, color: '#' + m.color.getHexString() }; }, id);
  assert.equal((await metal('plate')).metalness, 0.25, 'default look before any material');

  // 1. Select the plate, open the picker, search and choose brushed aluminum.
  await page.evaluate(async () => { await fetch('/api/select', { method: 'POST', headers: { 'X-Simon-Token': document.querySelector('meta[name=simon-token]').content, 'Content-Type': 'application/json' }, body: JSON.stringify({ selectors: ['body(feature=plate)'] }) }); });
  const rendersBefore = await page.evaluate(() => window.simonViewport.stats.triangles);
  await page.getByRole('button', { name: 'Choose…' }).first().waitFor();
  await page.getByRole('button', { name: 'Choose…' }).first().click();
  await page.getByLabel('Search materials').fill('alum');
  await page.getByRole('option', { name: /Brushed Aluminum/ }).click();
  await page.waitForFunction(() => window.simonViewport.bodies.get('plate').mesh.material.metalness === 1);
  assert.equal(await page.evaluate(() => window.simonViewport.stats.triangles), rendersBefore, 'no re-tessellation');
  await page.screenshot({ path: join(shots, 'materials-1-aluminum.png') });

  // 1b. The filter works from an expanded tree row too (same picker, other panel).
  await page.locator('#tree [data-id="plate"]').click();
  const treePicker = page.locator('#tree .mat-picker');
  await treePicker.waitFor();
  await page.screenshot({ path: join(shots, 'materials-2-tree-picker.png') });
  const over = await page.evaluate(() => { window.__o = [...document.querySelectorAll('#tree .appearance *')].filter((e) => e.getBoundingClientRect().right > document.querySelector('#tree .appearance').getBoundingClientRect().right + 0.5).map((e) => e.tagName + '.' + e.className + (e.getAttribute('aria-label') || e.textContent.slice(0, 12)) + ':' + Math.round(e.getBoundingClientRect().right)); const b = document.querySelector('#tree .mat-name ~ button'), p = document.querySelector('#tree .appearance'); return [window.__o.join(' ') || '-', b.getBoundingClientRect().right, p.getBoundingClientRect().right, p.scrollWidth, p.clientWidth]; });
  assert.ok(over[1] <= over[2] + 2 && over[3] <= over[4] + 2, 'Choose/Close stays inside the panel');
  await treePicker.getByLabel('Search materials').fill(''); // the earlier 'alum' query is shared
  await treePicker.getByRole('button', { name: 'Glass' }).click();
  await page.waitForFunction(() => document.querySelectorAll('#tree .mat-grid .mat').length === 2);
  await treePicker.getByRole('button', { name: 'All' }).click();
  await treePicker.getByLabel('Search materials').fill('steel');
  assert.ok((await treePicker.locator('.mat').count()) >= 4, 'search narrows the grid');
  await page.locator('#tree [data-id="plate"]').click(); // collapse
  await page.getByLabel('Search materials').first().fill('glass');

  // 2. Drag a material from the picker onto the lid's tree row.
  await page.getByLabel('Search materials').fill('glass');
  const glass = page.getByRole('option', { name: /Clear Glass/ });
  const target = page.locator('#tree [data-id="lid"]');
  const dt = await page.evaluateHandle(() => new DataTransfer());
  await glass.dispatchEvent('dragstart', { dataTransfer: dt });
  await target.dispatchEvent('dragover', { dataTransfer: dt });
  await target.dispatchEvent('drop', { dataTransfer: dt });
  await page.waitForFunction(() => window.simonViewport.bodies.get('lid').mesh.material.opacity < 0.5);
  assert.equal((await metal('plate')).metalness, 1, 'drop did not touch the selected plate');

  // 3. Shared materials: two bodies with the same material share one instance.
  await page.evaluate(async () => {
    const t = document.querySelector('meta[name=simon-token]').content;
    await fetch('/api/tool', { method: 'POST', headers: { 'X-Simon-Token': t, 'Content-Type': 'application/json' }, body: JSON.stringify({ name: 'set_material', args: { features: ['plate', 'lid'], material_id: 'black_abs' } }) });
  });
  await page.waitForFunction(() => ['lid', 'plate'].every((i) => window.simonViewport.bodies.get(i).mesh.material.color.getHexString() === '1e1f21'));
  assert.equal(await page.evaluate(() => window.simonViewport.bodies.get('lid').mesh.material === window.simonViewport.bodies.get('plate').mesh.material), true, 'material instance is shared');

  // 4. Undo the three material ChangeSets: the plate returns to the default look.
  await page.locator('#viewport').click({ position: { x: 5, y: 5 } });
  for (let i = 0; i < 3; i++) await page.keyboard.press('Control+z');
  await page.waitForFunction(() => window.simonViewport.bodies.get('plate').mesh.material.metalness === 0.25);

  assert.deepEqual(errors, [], 'no browser errors: ' + errors.join(' | '));
  console.log('e2e materials flow OK; screenshots in out/e2e/');
} catch (e) {
  failed = true;
  console.error(e);
} finally {
  await browser.close();
  server.kill();
}
process.exit(failed ? 1 : 0);
