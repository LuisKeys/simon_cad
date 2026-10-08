// Workspace / Units & Grid end-to-end: the configurable grid in the viewport, the Settings
// section (live preview, Cancel reverts, invalid input refused, Save through the workspace
// tools), display units in the dimension labels and typed sizes, global defaults, and the
// agent tools changing the same state. Geometry never changes with the units.
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
const tmp = mkdtempSync(join(tmpdir(), 'simon-'));

const server = spawn(join(root, 'out', 'simoncad'),
  ['--no-browser', '--recording', join(root, 'testdata/ui/m2_four_holes.recording.json'), '--project', join(tmp, 'p.scadproj')],
  { cwd: root, stdio: ['ignore', 'pipe', 'inherit'], env: { ...process.env, SIMONCAD_CONFIG: join(tmp, 'settings.json') } });
const url = await new Promise((res, rej) => {
  let buf = '';
  server.stdout.on('data', (d) => { buf += d; const m = /(http:\/\/\S+)/.exec(buf); if (m) res(m[1]); });
  server.on('exit', (c) => rej(new Error('server exited ' + c)));
  setTimeout(() => rej(new Error('server did not start')), 15000);
});

const browser = await chromium.launch({ executablePath: chrome, args: ['--use-gl=angle', '--use-angle=swiftshader', '--enable-unsafe-swiftshader', '--ignore-gpu-blocklist'] });
let failed = false;
try {
  const page = await (await browser.newContext({ viewport: { width: 1440, height: 1000 } })).newPage();
  const errors = [];
  page.on('pageerror', (e) => errors.push('pageerror: ' + e.message));
  page.on('console', (m) => { if (m.type() === 'error') errors.push('console: ' + m.text()); });
  await page.goto(url);
  await page.waitForSelector('#tree');

  const api = (path, body) => page.evaluate(async ([p, b]) => {
    const r = await fetch('/api/' + p, { method: b === undefined ? 'GET' : 'POST', headers: { 'X-Simon-Token': document.querySelector('meta[name=simon-token]').content, 'Content-Type': 'application/json' }, body: b === undefined ? undefined : JSON.stringify(b) });
    return r.json();
  }, [path, body]);
  // What the viewport draws: the grid settings in use and which plane is shown.
  const grid = () => page.evaluate(() => {
    const g = window.simonViewport.grid;
    const shown = Object.entries(g.planes).filter(([, p]) => p.group.visible).map(([k]) => k);
    const minor = g.planes.XY?.group.children[0]?.geometry.getAttribute('position');
    return { spacing: g.config.spacing, visible: g.config.visible, plane: g.config.plane, shown,
      firstMinorX: minor ? minor.getX(0) : null };
  });
  const frame = () => page.evaluate(() => new Promise((r) => { const n = window.simonViewport.frames; const t = () => (window.simonViewport.frames > n ? r() : requestAnimationFrame(t)); window.simonViewport.dirty(); t(); }));
  const label = (axis) => page.locator(`.vp-pane[data-pane="1"] .dim-label[data-axis="${axis}"]`);
  const openSettings = async () => { await page.click('#btn-settings'); await page.click('#stab-workspace'); await page.waitForSelector('#set-ws-spacing'); };

  await api('level', { level: 'auto' });
  await api('tool', { name: 'create_box', args: { id: 'plate', name: 'plate', dx: 120, dy: 80, dz: 40 } });
  await page.waitForFunction(() => window.simonViewport.bodies.size === 1);
  await page.keyboard.press('f');
  const before = await api('state');

  // 1. Factory grid: 10 mm on XY, drawn on exact multiples of the spacing.
  await frame();
  let g = await grid();
  assert.equal(g.spacing, 10); assert.equal(g.visible, true); assert.deepEqual(g.shown, ['XY']);
  assert.equal(Math.abs(g.firstMinorX) % 10, 0, 'grid lines on world multiples of the spacing');

  // 2. Settings preview is live and Cancel reverts it.
  await openSettings();
  await page.fill('#set-ws-spacing', '5');
  await frame();
  assert.equal((await grid()).spacing, 5, 'spacing previews immediately');
  await page.click('.dialog.settings button:has-text("Cancel")');
  await frame();
  assert.equal((await grid()).spacing, 10, 'Cancel reverts the preview');

  // 3. Invalid values are refused in place.
  await openSettings();
  await page.fill('#set-ws-spacing', '0');
  assert.equal(await page.getAttribute('#set-ws-spacing', 'aria-invalid'), 'true');
  assert.equal((await grid()).spacing, 10, 'an invalid spacing never reaches the viewport');
  await page.click('.dialog.settings button[type=submit]');
  assert.match(await page.locator('.dialog.settings [role=status].bad').first().textContent(), /greater than 0/);
  assert.equal((await api('state')).workspace.grid.spacing, 10, 'nothing saved');

  // 4. Switch to inches: lengths in the form follow the unit, the value underneath does not.
  await page.fill('#set-ws-spacing', '10');
  await page.selectOption('#set-ws-linear', 'in');
  assert.equal(await page.inputValue('#set-ws-spacing'), '0.393701');
  await page.fill('#set-ws-spacing', '0.25');
  await page.check('#set-ws-snap-enabled');
  await page.fill('#set-ws-rotation-increment', '45');
  await page.check('#set-ws-as-default');
  await page.screenshot({ path: join(shots, 'workspace-settings.png') });
  await page.click('.dialog.settings button[type=submit]');
  await page.waitForSelector('#set-ws-spacing', { state: 'detached' });
  await page.waitForFunction(() => window.simonViewport.grid.config.spacing === 6.35);
  const st = await api('state');
  assert.equal(st.workspace.units.linear, 'in');
  assert.equal(st.workspace.grid.spacing, 6.35, '0.25 in stored as mm');
  assert.equal(st.workspace.snap.enabled, true);
  assert.equal(st.workspace.snap.rotation_increment, 45);
  assert.equal((await api('settings')).workspace.units.linear, 'in', 'saved as the default for new projects');
  // The geometry did not change.
  assert.equal(st.volume_mm3, before.volume_mm3);
  assert.deepEqual(st.tree.find((n) => n.id === 'plate').params, before.tree.find((n) => n.id === 'plate').params);

  // 5. Dimensions are shown and typed in inches; the edit reaches the tool in mm.
  await page.locator('.node .row', { hasText: 'plate' }).first().click();
  await page.waitForFunction(() => document.querySelector('.dim-label[data-object="plate"]'));
  assert.equal(await label('width').textContent(), '4.72');
  assert.equal(await page.inputValue('#dim-width'), '4.72');
  await label('width').click();
  await page.fill('.dim-input', '5');
  await page.keyboard.press('Enter');
  await page.waitForFunction(() => document.querySelector('.vp-pane[data-pane="1"] .dim-label[data-axis="width"]')?.textContent === '5');
  const leaf = (await api('state')).tree.filter((n) => n.visible).map((n) => n.id).pop();
  const d = await api('tool', { name: 'get_object_dimensions', args: { object_id: leaf } });
  assert.ok(Math.abs(d.data.width - 127) < 1e-6, `5 in = 127 mm, got ${d.data.width}`);
  await page.keyboard.press('Escape');

  // 6. The agent's tools change the same state, and the viewport follows at once.
  let o = await api('tool', { name: 'configure_grid', args: { plane: 'XZ', spacing: 5, unit: 'mm' } });
  assert.ok(o.ok, o.error);
  await page.waitForFunction(() => window.simonViewport.grid.config.plane === 'XZ');
  await frame();
  g = await grid();
  assert.equal(g.spacing, 5); assert.deepEqual(g.shown, ['XZ']);
  o = await api('tool', { name: 'configure_grid', args: { visible: false } });
  assert.ok(o.ok, o.error);
  await page.waitForFunction(() => window.simonViewport.grid.config.visible === false);
  await frame();
  assert.deepEqual((await grid()).shown, [], 'hidden grid');
  o = await api('tool', { name: 'snap_calculate', args: { operation: 'position', point: [12, 3, 7] } });
  assert.ok(o.ok && o.data.snapped, o.error);
  assert.deepEqual(o.data.result_mm, [10, 3, 5], 'XZ grid, 5 mm: X and Z snap, Y is kept');
  o = await api('tool', { name: 'restore_workspace_defaults', args: { source: 'factory' } });
  assert.ok(o.ok, o.error);
  await page.waitForFunction(() => window.simonViewport.grid.config.visible === true && window.simonViewport.grid.config.spacing === 10);
  await page.mouse.move(700, 500);
  for (let i = 0; i < 6; i++) await page.mouse.wheel(0, 400);
  await frame();
  await page.screenshot({ path: join(shots, 'workspace-grid.png') });

  // 7. Persisted with the project.
  const saved = await api('project/save', { path: '' });
  assert.ok(!saved.error, saved.error);
  assert.ok(existsSync(join(tmp, 'p.scadproj', 'workspace.json')));

  assert.deepEqual(errors, []);
  console.log('workspace e2e: OK');
} catch (e) {
  failed = true;
  console.error(e);
} finally {
  await browser.close();
  server.kill();
}
process.exit(failed ? 1 : 0);
