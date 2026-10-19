// STL import end-to-end: the Import button validates the file before adding it as a body; a mesh
// with a hole is refused with the reason and leaves the model alone; the body undoes, redoes and
// is saved with the project.
import { chromium } from 'playwright-core';
import { spawn } from 'node:child_process';
import { mkdtempSync, mkdirSync, existsSync, readdirSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, resolve, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';
import assert from 'node:assert/strict';

const root = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const shots = join(root, 'out', 'e2e');
mkdirSync(shots, { recursive: true });
const chrome = process.env.CHROME_BIN || ['/usr/bin/google-chrome', '/usr/bin/chromium', '/usr/bin/chromium-browser', '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome'].find((p) => existsSync(p));
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
  const page = await (await browser.newContext({ bypassCSP: true, viewport: { width: 1440, height: 1000 } })).newPage();
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

  // A closed cube (binary STL), and the same cube missing one triangle.
  const cube = (s, triangles = 12) => {
    const p = [[0, 0, 0], [s, 0, 0], [s, s, 0], [0, s, 0], [0, 0, s], [s, 0, s], [s, s, s], [0, s, s]];
    const q = [[0, 3, 2, 1], [4, 5, 6, 7], [0, 1, 5, 4], [1, 2, 6, 5], [2, 3, 7, 6], [3, 0, 4, 7]];
    const tris = q.flatMap((f) => [[f[0], f[1], f[2]], [f[0], f[2], f[3]]]).slice(0, triangles);
    const b = Buffer.alloc(84 + 50 * tris.length);
    b.writeUInt32LE(tris.length, 80);
    tris.forEach((t, i) => t.forEach((v, j) => p[v].forEach((c, k) => b.writeFloatLE(c, 84 + 50 * i + 12 + 12 * j + 4 * k))));
    return b;
  };
  // A cube whose faces are n x n quads: many triangles, all redundant (what decimation is for).
  const gridCube = (s, n) => {
    const tris = [];
    const face = (o, du, dv) => {
      const at = (a, b) => [0, 1, 2].map((k) => o[k] + du[k] * a / n + dv[k] * b / n);
      for (let i = 0; i < n; i++) for (let j = 0; j < n; j++) {
        const [a, b, c, d] = [at(i, j), at(i + 1, j), at(i + 1, j + 1), at(i, j + 1)];
        tris.push([a, b, c], [a, c, d]);
      }
    };
    const X = [s, 0, 0], Y = [0, s, 0], Z = [0, 0, s], O = [0, 0, 0];
    face(O, Y, X); face(Z, X, Y); face(O, X, Z); face(Y, Z, X); face(O, Z, Y); face(X, Y, Z);
    const b = Buffer.alloc(84 + 50 * tris.length);
    b.writeUInt32LE(tris.length, 80);
    tris.forEach((t, i) => t.forEach((v, j) => v.forEach((c, k) => b.writeFloatLE(c, 84 + 50 * i + 12 + 12 * j + 4 * k))));
    return b;
  };
  const upload = (name, buffer) => page.setInputFiles('#import-input', { name, mimeType: 'model/stl', buffer });
  const toastText = () => page.locator('#toast').innerText();

  // Everything the busy dialog says during the test.
  await page.evaluate(() => {
    window.__busy = [];
    const b = document.getElementById('busy');
    new MutationObserver(() => { if (!b.hidden) window.__busy.push(document.getElementById('busy-stage').textContent); })
      .observe(b, { attributes: true, childList: true, subtree: true, characterData: true });
  });

  // 1. A valid file: the units dialog opens (the file does not say), then the body appears.
  await upload('block.stl', cube(20));
  await page.waitForSelector('#dialog:not([hidden])');
  await page.click('#dialog-list li:has-text("cm")');
  await page.waitForFunction(() => window.simonViewport.bodies.size === 1);
  const state = await api('state');
  const node = state.tree.find((n) => n.id === 'block');
  assert.ok(node, 'the imported body is in the design tree');
  assert.equal(node.type, 'import_mesh');
  const m = await api('tool', { name: 'get_model_summary', args: {} });
  assert.ok(m.ok, m.error);
  await page.keyboard.press('f');
  await page.screenshot({ path: join(shots, 'import-stl.png') });

  assert.ok((await page.evaluate(() => window.__busy)).length > 0, 'the busy dialog was shown during the import');
  assert.equal(await page.locator('#busy').isHidden(), true, 'and is gone when it is done');

  // 1b. A heavy mesh keeps the interface locked while it is built, with a stage and a progress bar.
  const heavy = gridCube(30, 40); // 19 200 triangles
  await upload('heavy.stl', heavy);
  await page.waitForSelector('#dialog:not([hidden])');
  await page.click('#dialog-list li:has-text("mm")');
  await page.waitForSelector('#busy:not([hidden])');
  await page.waitForFunction(() => /Building the solid from 19200 triangles/.test(document.getElementById('busy-stage').textContent), null, { timeout: 20000 });
  const barNow = await page.evaluate(() => document.getElementById('busy-bar').getAttribute('aria-valuenow'));
  assert.notEqual(barNow, null, 'a stage with an estimate draws a determinate bar');
  await page.screenshot({ path: join(shots, 'import-busy.png') });
  // The page behind it cannot be reached: the dialog is what a click on Save would hit.
  const covered = await page.evaluate(() => { const r = document.getElementById('btn-save').getBoundingClientRect(); return !!document.elementFromPoint(r.x + r.width / 2, r.y + r.height / 2)?.closest('#busy'); });
  assert.equal(covered, true, 'the interface is locked');
  await page.keyboard.press('f'); // swallowed: no fit-to-view while waiting
  await page.waitForSelector('#busy', { state: 'hidden', timeout: 120000 });
  assert.equal((await api('state')).tree.filter((n) => n.type === 'import_mesh').length, 2);

  // 1c. decimate_mesh really removes triangles, keeping the cube a cube, and undo brings them back.
  const full = (await api('state')).tree.find((n) => n.id === 'heavy');
  assert.ok(full, 'the heavy mesh is in the tree');
  const info = () => api('tool', { name: 'get_model_summary', args: {} });
  const d = await api('tool', { name: 'decimate_mesh', args: { feature: 'heavy', target_triangles: 200 } });
  assert.ok(d.ok, d.error);
  assert.match(d.changeset?.explanation ?? d.message ?? '', /Reduced heavy from 19200/);
  const reduced = (await api('state')).tree.find((n) => n.id === 'heavy');
  assert.notEqual(reduced.params.mesh, full.params.mesh, 'the body now uses the reduced mesh');
  await api('undo', {});
  assert.equal((await api('state')).tree.find((n) => n.id === 'heavy').params.mesh, full.params.mesh, 'undo restores the full mesh');
  await api('redo', {});

  // 2. A mesh whose only fault is a hole: the user is asked, and declining keeps the model as it was.
  await upload('holed.stl', cube(20, 11));
  await page.waitForSelector('#dialog:not([hidden])');
  await page.click('#dialog-ok');
  await page.waitForSelector('#confirm:not([hidden])');
  assert.match(await page.locator('#confirm-body').innerText(), /not watertight/);
  await page.click('#confirm-cancel');
  await page.waitForFunction(() => /holed\.stl was not imported/.test(document.querySelector('#toast').textContent));
  assert.equal((await api('state')).tree.filter((n) => n.type === 'import_mesh').length, 2, 'the refused mesh left no node');

  // 2b. Agreeing closes the hole and imports it.
  await upload('holed.stl', cube(20, 11));
  await page.waitForSelector('#dialog:not([hidden])');
  await page.click('#dialog-ok');
  await page.waitForSelector('#confirm:not([hidden])');
  await page.click('#confirm-ok');
  await page.waitForFunction(() => window.simonViewport.bodies.size === 3);
  assert.equal((await api('state')).tree.filter((n) => n.type === 'import_mesh').length, 3);

  // 2c. A mesh with another fault is refused outright, with no offer to close anything.
  const crossed = cube(20); // the corner (20,20,20) pulled down through the cube: every triangle that has it moves
  for (let i = 0; i < 12; i++) for (let j = 0; j < 3; j++) {
    const o = 84 + 50 * i + 12 + 12 * j;
    if (crossed.readFloatLE(o) === 20 && crossed.readFloatLE(o + 4) === 20 && crossed.readFloatLE(o + 8) === 20) crossed.writeFloatLE(-10, o + 8);
  }
  await upload('crossed.stl', crossed);
  await page.waitForSelector('#dialog:not([hidden])');
  await page.click('#dialog-ok');
  await page.waitForFunction(() => /crossed\.stl was not imported/.test(document.querySelector('#toast').textContent));
  assert.equal(await page.locator('#confirm').isHidden(), true);

  // 3. It is an ordinary body: undo removes it, redo brings it back, and it survives a save.
  await api('undo', {});
  await page.waitForFunction(() => window.simonViewport.bodies.size === 2);
  await api('redo', {});
  await page.waitForFunction(() => window.simonViewport.bodies.size === 3);
  const saved = await api('project/save', { path: '' });
  assert.ok(!saved.error, saved.error);
  // Three bodies, two stored meshes: the cube with its hole closed is the same mesh as the first
  // one, and only the reduced version of the heavy mesh is kept (the full one is no longer used).
  assert.equal(readdirSync(join(tmp, 'p.scadproj', 'assets', 'meshes')).filter((f) => f.endsWith('.stl')).length, 2);

  // 4. A STEP file: exact geometry and its own units, so no units dialog; it becomes an import_step
  // body, survives a save, and a file that is not STEP is refused without touching the model.
  const stepPath = join(root, 'out', 'e2e-source.step');
  assert.ok((await api('tool', { name: 'create_box', args: { name: 'src', dx: 40, dy: 25, dz: 10 } })).ok);
  assert.ok((await api('tool', { name: 'export_step', args: { path: stepPath } })).ok);
  await api('undo', {});
  await page.waitForFunction(() => window.simonViewport.bodies.size === 3);
  await page.setInputFiles('#import-input', stepPath);
  await page.waitForFunction(() => window.simonViewport.bodies.size === 4);
  assert.equal(await page.locator('#dialog').isHidden(), true, 'a STEP file does not ask for units');
  const stepNode = (await api('state')).tree.find((n) => n.type === 'import_step');
  assert.ok(stepNode, 'the STEP body is in the design tree');
  assert.equal(stepNode.type, 'import_step');
  await page.setInputFiles('#import-input', { name: 'broken.step', mimeType: 'application/step', buffer: Buffer.from('definitely not a STEP file') });
  await page.waitForFunction(() => /broken\.step was not imported/.test(document.querySelector('#toast').textContent));
  assert.equal((await api('state')).tree.filter((n) => n.type === 'import_step').length, 1, 'the refused file left no node');
  assert.ok(!(await api('project/save', { path: '' })).error);
  assert.equal(readdirSync(join(tmp, 'p.scadproj', 'assets', 'steps')).filter((f) => f.endsWith('.step')).length, 1);

  // The browser logs the refused upload's 400 as a console error; that one is the point of step 2.
  assert.deepEqual(errors.filter((e) => !/status of 400/.test(e)), []);
  console.log('import e2e: OK');
} catch (e) {
  failed = true;
  console.error(e);
} finally {
  await browser.close();
  server.kill();
}
process.exit(failed ? 1 : 0);
