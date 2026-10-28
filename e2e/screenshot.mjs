// Viewport screenshot end-to-end: the Screenshot button saves one PNG of the whole viewport in
// ~/Downloads, named after the project and the time, in Single and in Quad (all four views in the
// one image, at the canvas resolution), never overwriting an earlier capture.
import { chromium } from 'playwright-core';
import { spawn } from 'node:child_process';
import { mkdtempSync, mkdirSync, existsSync, readdirSync, readFileSync, copyFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, resolve, dirname, basename } from 'node:path';
import { fileURLToPath } from 'node:url';
import assert from 'node:assert/strict';

const root = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const shots = join(root, 'out', 'e2e');
mkdirSync(shots, { recursive: true });
const chrome = process.env.CHROME_BIN || ['/usr/bin/google-chrome', '/usr/bin/chromium', '/usr/bin/chromium-browser', '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome'].find((p) => existsSync(p));

// A throwaway home, so the captures land in its Downloads and not the user's.
const home = mkdtempSync(join(tmpdir(), 'simon-home-'));
const downloads = join(home, 'Downloads');
const server = spawn(join(root, 'out', 'simoncad'),
  ['--no-browser', '--recording', join(root, 'testdata/ui/m2_four_holes.recording.json'), '--project', join(mkdtempSync(join(tmpdir(), 'simon-')), 'p.scadproj')],
  { cwd: root, env: { ...process.env, HOME: home }, stdio: ['ignore', 'pipe', 'inherit'] });
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
  await api('level', { level: 'auto' });
  await api('tool', { name: 'create_box', args: { id: 'plate', dx: 100, dy: 60, dz: 4 } });
  await page.waitForFunction(() => window.simonViewport.bodies.size === 1);
  const project = (await api('state')).name;
  const canvas = await page.evaluate(() => { const c = document.querySelector('#gl'); return [c.width, c.height]; });

  // Clicks Screenshot and returns the new file, checked: name, PNG, canvas-sized.
  const shoot = async (label) => {
    const before = existsSync(downloads) ? readdirSync(downloads) : [];
    await page.click('#btn-screenshot');
    await page.waitForFunction(() => !document.querySelector('#btn-screenshot').disabled);
    const added = readdirSync(downloads).filter((f) => !before.includes(f));
    assert.equal(added.length, 1, `${label}: one new file (${added})`);
    const name = added[0];
    assert.match(name, new RegExp(`^${project}_\\d{8}-\\d{6}(_\\d+)?\\.png$`), `${label}: named after the project and the time`);
    const png = readFileSync(join(downloads, name));
    assert.equal(png.subarray(1, 4).toString(), 'PNG');
    assert.deepEqual([png.readUInt32BE(16), png.readUInt32BE(20)], canvas, `${label}: the whole viewport at canvas resolution`);
    copyFileSync(join(downloads, name), join(shots, `screenshot-${label}.png`));
    return name;
  };

  // 1. Single view.
  const single = await shoot('single');
  await page.locator('.toast', { hasText: single }).waitFor();

  // 2. Quad: the four views in the one image; a capture in the same second gets a new name.
  await page.keyboard.press('Control+Alt+4');
  await page.waitForFunction(() => document.querySelector('#viewport').dataset.layout === 'quad');
  const quad = await shoot('quad');
  const again = await shoot('quad-again');
  assert.equal(new Set([single, quad, again]).size, 3, 'no capture overwrites another');

  assert.deepEqual(errors, []);
  console.log('screenshot e2e: ok', [single, quad, again].map((f) => basename(f)).join(' '));
} catch (e) {
  failed = true;
  console.error(e);
} finally {
  await browser.close();
  server.kill();
  process.exit(failed ? 1 : 0);
}
