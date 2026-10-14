// Settings → Rendering end-to-end: the lighting and shadow preferences preview live in the
// viewport, Cancel reverts them, Save persists them, and a reload applies them again.
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
  const state = () => page.evaluate(() => new Promise((res) => requestAnimationFrame(() => requestAnimationFrame(() => res(null))))).then(() => page.evaluate(() => { const v = window.simonViewport, r = v.renderer; return { exposure: r.toneMappingExposure, tone: r.toneMapping, shadows: v.lighting.shadowsOn, castShadow: v.lighting.key.castShadow, envIntensity: v.scene.environmentIntensity, floor: v.lighting.floor.visible }; }));
  const open = async () => { await page.click('#btn-settings'); await page.click('#stab-rendering'); await page.waitForSelector('#set-render-tone'); };
  const setRange = (id, v) => page.evaluate(([i, val]) => { const e = document.getElementById(i); e.value = String(val); e.dispatchEvent(new Event('input', { bubbles: true })); }, [id, v]);

  await api('level', { level: 'auto' });
  await api('tool', { name: 'create_box', args: { id: 'plate', name: 'plate', dx: 120, dy: 80, dz: 40 } });
  await page.waitForFunction(() => window.simonViewport.bodies.size === 1);
  const def = await state();
  assert.equal(def.exposure, 1); assert.equal(def.shadows, true); assert.equal(def.envIntensity, 1);

  // 1. Live preview, then Cancel reverts.
  await open();
  await page.screenshot({ path: join(shots, 'rendering-settings.png') });
  await page.selectOption('#set-render-tone', 'aces');
  await page.selectOption('#set-render-shadows', 'off');
  await setRange('set-render-exposure', 150);
  await setRange('set-render-env-intensity', 50);
  const live = await state();
  assert.equal(live.exposure, 1.5); assert.equal(live.shadows, false); assert.equal(live.castShadow, false);
  assert.equal(live.envIntensity, 0.5); assert.equal(live.floor, false, 'the ground shadow catcher goes with the shadows');
  assert.notEqual(live.tone, def.tone);
  await page.click('.dialog.settings button:has-text("Cancel")');
  assert.deepEqual(await state(), def, 'Cancel restores the saved look');

  // 2. Save persists, and the backend reports it.
  await open();
  await page.selectOption('#set-render-shadows', 'high');
  await page.selectOption('#set-render-env', 'room');
  await page.selectOption('#set-render-tone', 'agx');
  await page.click('.dialog.settings button[type=submit]');
  await page.waitForSelector('.dialog.settings', { state: 'detached' });
  const saved = (await api('settings')).render;
  assert.deepEqual(saved, { environment: 'room', envIntensity: 100, exposure: 100, toneMapping: 'agx', shadows: 'high' });
  assert.equal((await state()).shadows, true);

  // 3. A reload applies the saved preferences (the room environment draws without errors).
  await page.reload();
  await page.waitForSelector('#tree');
  await page.waitForFunction(() => window.simonViewport.renderPrefs.environment === 'room');
  assert.equal(await page.evaluate(() => window.simonViewport.lighting.key.shadow.mapSize.x), 4096);

  assert.deepEqual(errors, [], 'no browser errors: ' + errors.join(' | '));
  console.log('e2e rendering settings flow OK; screenshots in out/e2e/');
} catch (e) {
  failed = true;
  console.error(e);
} finally {
  await browser.close();
  server.kill();
}
process.exit(failed ? 1 : 0);
