// Body and face editing end-to-end: Align, Duplicate, Scale and Group from the action bar on
// selected bodies, the group row in the design tree, and Extrude / Inset on a selected face.
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
  const state = () => api('state');
  const select = async (selectors) => { await api('select', { selectors }); await page.waitForFunction((n) => (document.querySelectorAll('#chips .chip').length === n), selectors.length); };
  const act = async (id) => {
    const b = page.locator(`#act-${id}`);
    if (await b.count()) return b.click();
    await page.locator('#ctx-actions button[title="More actions"]').click();   // overflowed into More
    await page.locator('#ctxmenu li', { hasText: new RegExp(`^${id.replace('face-', '')}`, 'i') }).first().click();
  };
  const dialog = async (values, ok) => {
    await page.locator('#formdlg:not([hidden])').waitFor();
    for (const [k, v] of Object.entries(values)) {
      const el = page.locator(`#fd-${k}`);
      if (await el.evaluate((e) => e.tagName) === 'SELECT') await el.selectOption(String(v)); else await el.fill(String(v));
    }
    await page.locator('#fd-ok', { hasText: ok }).click();
  };
  const bodies = (n) => page.waitForFunction((k) => window.simonViewport.bodies.size === k, n);

  await api('level', { level: 'auto' });   // low-risk edits commit immediately
  await api('tool', { name: 'create_box', args: { id: 'base', dx: 40, dy: 30, dz: 10 } });
  await api('tool', { name: 'create_box', args: { id: 'cap', dx: 10, dy: 10, dz: 4, x: 60, y: 3, z: 12 } });
  await bodies(2);

  // 1. Align: two bodies → the second moves; one ChangeSet.
  await select(['body(feature=base)', 'body(feature=cap)']);
  await act('align');
  await dialog({ axes: 'x,y', mode: 'center', to: 'first' }, 'Align');
  await page.waitForFunction(() => [...document.querySelectorAll('#tree .row .name')].some((e) => e.textContent === 'align_cap'));
  let st = await state();
  const cap = st.tree.find((t) => t.id === 'align_cap');
  assert.ok(cap && cap.visible && Math.abs(cap.params.dx - (20 - 65)) < 1e-6 && Math.abs(cap.params.dy - (15 - 8)) < 1e-6, JSON.stringify(cap?.params));

  // 2. Duplicate the base with Ctrl+D: the source stays, the copy appears.
  await select(['body(feature=base)']);
  await page.keyboard.press('Control+d');
  await dialog({ dx: 50 }, 'Duplicate');
  await bodies(3);
  st = await state();
  assert.ok(st.tree.find((t) => t.id === 'base').visible && st.tree.find((t) => t.id === 'base_copy').visible, 'source and copy are both bodies');

  // 3. Scale the copy uniformly about its base.
  await select(['body(feature=base_copy)']);
  await act('scale');
  await dialog({ f: 0.5, about: 'base' }, 'Scale');
  await page.waitForFunction(() => [...document.querySelectorAll('#tree .row .name')].some((e) => e.textContent === 'scale'));

  // 4. Group base + aligned cap; the tree shows a group row, clicking it selects both.
  await select(['body(feature=base)', 'body(feature=align_cap)']);
  await act('group');
  await dialog({ name: 'Chassis' }, 'Group');
  await page.locator('#tree .row.grp', { hasText: 'Chassis' }).waitFor();
  await page.screenshot({ path: join(shots, 'modeling-1-group.png') });
  await select([]);
  await page.locator('#tree .row.grp', { hasText: 'Chassis' }).click();
  await page.waitForFunction(() => document.querySelectorAll('#chips .chip').length === 2);
  await page.locator('#act-ungroup, #ctx-actions button[title="More actions"]').first().waitFor();
  st = await state();
  assert.deepEqual(st.tree.filter((t) => t.group === 'Chassis').map((t) => t.id).sort(), ['align_cap', 'base']);

  // 5. Face extrude on the base's top face (pull 5 mm): the result joins the group.
  await select(['face(feature=base,side=+Z)']);
  await act('face-extrude');
  await dialog({ distance: 5 }, 'Extrude');
  await page.waitForFunction(() => [...document.querySelectorAll('#tree .row .name')].some((e) => e.textContent === 'face_extrude'));
  st = await state();
  const fx = st.tree.find((t) => t.id === 'face_extrude');
  assert.ok(fx.visible && fx.group === 'Chassis' && fx.status === 'ok', JSON.stringify(fx));

  // 6. Inset the new top as a 3 mm deep pocket.
  await select(['face(feature=face_extrude,side=+Z)']);
  await act('face-inset');
  await dialog({ inset: 2, depth: -3 }, 'Inset');
  await page.waitForFunction(() => [...document.querySelectorAll('#tree .row .name')].some((e) => e.textContent === 'face_inset'));
  st = await state();
  assert.ok(Math.abs(st.volume_mm3 - (40 * 30 * 15 - 36 * 26 * 3 + 10 * 10 * 4 + 40 * 30 * 10 * 0.125)) < 1e-3, `volume ${st.volume_mm3}`);
  await page.screenshot({ path: join(shots, 'modeling-2-faces.png') });

  // Undo the inset and ungroup with the keyboard.
  await page.keyboard.press('Control+z');
  await page.waitForFunction(() => ![...document.querySelectorAll('#tree .row .name')].some((e) => e.textContent === 'face_inset'));
  await select(['body(feature=face_extrude)']);
  await page.keyboard.press('Control+Shift+g');
  await page.waitForFunction(() => document.querySelectorAll('#tree .group .members .node').length === 2);
  st = await state();
  assert.equal(st.tree.find((t) => t.id === 'face_extrude').group ?? '', '');
  assert.deepEqual(errors, []);
  console.log('modeling e2e: OK');
} catch (e) {
  failed = true;
  console.error(e);
} finally {
  await browser.close();
  server.kill();
}
process.exit(failed ? 1 : 0);
