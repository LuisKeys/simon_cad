// Component system end-to-end: create a component from a selection, place more instances,
// edit the component as its own project, come back and see the parent report it as out of
// date, sync it, and detach. This is the flow that catches the two failure modes unit tests
// are weakest at — a stale rebuild cache serving old geometry, and nested OCCT shape
// lifetimes — because it goes through real saves, reopens and the real kernel.
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

const work = mkdtempSync(join(tmpdir(), 'simon-comp-'));
const parent = join(work, 'car.scadproj');
const server = spawn(join(root, 'out', 'simoncad'),
  ['--no-browser', '--recording', join(root, 'testdata/ui/m2_four_holes.recording.json'), '--project', parent],
  { cwd: root, stdio: ['ignore', 'pipe', 'inherit'], env: { ...process.env, SIMONCAD_COMPONENT_LIBRARY: work } });
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
  const tool = async (name, args) => {
    const o = await api('tool', { name, args });
    assert.ok(o.ok, `${name}: ${o.error}`);
    // Creating or detaching a component moves geometry between projects, so it is a
    // multi-command ChangeSet: it is proposed for review rather than auto-committed.
    for (const cs of (await state()).pending) {
      const c = await api(`changeset/${cs.id}/commit`);
      assert.ok(c.id || c.ok !== false, `commit ${cs.id}: ${c.error}`);
    }
    return o;
  };

  await api('level', { level: 'auto' }); // low-risk edits commit immediately

  // 1. A wheel, turned into a reusable component. The geometry moves out; one reference stays.
  await tool('create_box', { id: 'wheel', name: 'Wheel', dx: 10, dy: 10, dz: 10, intent: 'a wheel' });
  const solid = (await state()).volume_mm3;
  await tool('create_component', { name: 'Wheel', description: 'a road wheel', tags: ['chassis'], features: ['wheel'] });

  let st = await state();
  assert.equal(st.tree.filter((t) => t.component).length, 1, 'one component instance');
  assert.ok(!st.tree.some((t) => t.id === 'wheel'), 'the original geometry moved into the component');
  assert.ok(Math.abs(st.volume_mm3 - solid) < 1e-6, `the instance resolves to the same volume: ${st.volume_mm3}`);
  const instance = st.tree.find((t) => t.component).id;

  // The design tree says so: the row carries a Component badge.
  await page.waitForSelector('#tree .src.component');
  await page.screenshot({ path: join(shots, 'components-1-created.png') });

  // The component shows up in the library panel.
  await page.click('#btn-components');
  await page.waitForSelector('.comp .comp-name');
  assert.equal(await page.locator('.comp .comp-name').first().textContent(), 'Wheel');
  await page.screenshot({ path: join(shots, 'components-2-library.png') });

  // 2. Three more instances: four references, four bodies, one stored geometry.
  const list = await api('components', { query: '' });
  assert.equal(list.length, 1, 'one reusable project in the library');
  const wheel = list[0].project_id;
  for (const at of [{ dx: 100 }, { dy: 100 }, { dx: 100, dy: 100 }]) await tool('insert_component', { component: wheel, ...at });
  st = await state();
  assert.equal(st.tree.filter((t) => t.component).length, 4, 'four instances');
  assert.ok(Math.abs(st.volume_mm3 - 4 * solid) < 1e-6, `four bodies: ${st.volume_mm3}`);
  await page.waitForFunction(() => window.simonViewport.bodies.size === 4);
  await page.screenshot({ path: join(shots, 'components-3-four.png') });

  // 3. Save, then open the component as an ordinary project and make it taller.
  await api('project/save', { path: parent });
  const opened = await api('components/open', { component: wheel });
  assert.ok(opened.ok, opened.error);
  await page.waitForFunction(() => document.querySelectorAll('#tree .row').length === 1);
  await tool('modify_parameter', { feature: 'wheel', parameter: 'dz', value: 20 });
  await api('project/save', { path: opened.path });

  // 4. Back in the car: the instances are reported out of date, and syncing rebuilds them.
  // A stale rebuild cache would show the old volume here.
  await api('project/open', { path: parent });
  await page.waitForFunction(() => window.simonViewport.bodies.size === 4);
  st = await state();
  assert.equal(st.stale_components, 4, 'all four instances are behind their component');
  assert.ok(st.tree.filter((t) => t.out_of_date).length === 4, 'the tree marks them');
  await page.waitForSelector('#tree .src.component.stale');
  await page.screenshot({ path: join(shots, 'components-4-stale.png') });

  await tool('sync_component', {});
  st = await state();
  assert.equal(st.stale_components, 0, 'nothing is out of date after a sync');
  assert.ok(Math.abs(st.volume_mm3 - 8 * solid) < 1e-6, `the component edit reached the car: ${st.volume_mm3}`);
  await page.screenshot({ path: join(shots, 'components-5-synced.png') });

  // 5. Detaching one instance turns it into local geometry that the component no longer drives.
  const before = (await state()).volume_mm3;
  await tool('detach_component', { instance });
  st = await state();
  assert.equal(st.tree.filter((t) => t.component).length, 3, 'one instance became local geometry');
  assert.ok(Math.abs(st.volume_mm3 - before) < 1e-6, `detaching preserves geometry: ${st.volume_mm3}`);

  assert.deepEqual(errors, []);
  console.log('components e2e: OK');
} catch (e) {
  failed = true;
  console.error(e);
} finally {
  await browser.close();
  server.kill();
}
process.exit(failed ? 1 : 0);
