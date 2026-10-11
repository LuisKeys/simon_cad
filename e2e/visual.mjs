// Visual closed-loop refinement browser end-to-end: attach a side-view reference → run a
// visual refinement from the Visual comparison panel → blockout, critique, a correction that
// fixes the silhouette, stage advances to MATERIALS → browse iterations (reference | render |
// difference, scores against thresholds), compare with the previous iteration, lock a region
// and revert. Real server, frontend, renderer and OCCT kernel; the models' answers come from a
// scripted recording, so no network or API key is needed.
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
  ['--no-browser', '--recording', join(root, 'testdata/ui/visual_box.recording.json'), '--project', join(mkdtempSync(join(tmpdir(), 'simon-')), 'p.scadproj')],
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
  page.on('console', (m) => { if (m.type() === 'error' && !/404/.test(m.text())) errors.push('console: ' + m.text()); });
  await page.goto(url);
  await page.waitForSelector('#tree');

  // 1. Attach the reference and run the refinement from the panel.
  await page.setInputFiles('#file-input', join(root, 'testdata/ui/box_car_side.png'));
  await page.waitForSelector('#attachments .chip.attach.ready');
  await page.click('#btn-visual');
  await page.waitForSelector('#visual .vc-run textarea');
  await page.fill('#visual .vc-run textarea', 'A simple box car, 4 m long');
  await page.click('#visual .vc-run button.primary');

  // 2. It runs to the last stage; the chat reports the result.
  await page.waitForFunction(() => /Final quality reached/.test(document.querySelector('#chat')?.innerText ?? ''), null, { timeout: 60000 });
  await page.waitForFunction(() => document.querySelectorAll('#visual .vc-it').length >= 5);
  assert.match(await page.locator('#visual .interp-head').innerText(), /Stage MATERIALS/);
  assert.equal(await page.locator('#attachments .chip').count(), 0, 'the reference was taken by the run');

  // 3. The correction iteration: reference | render | difference, and better scores.
  await page.locator('#visual .vc-it', { hasText: '#2' }).click();
  await page.waitForSelector('#visual .vc-panes img');
  assert.equal(await page.locator('#visual .vc-panes figure').count(), 3);
  await page.waitForFunction(() => [...document.querySelectorAll('#visual .vc-panes img')].every((i) => i.complete && i.naturalWidth > 0));
  const scores = await page.locator('#visual .vc-scores').innerText();
  assert.match(scores, /Side silhouette/);
  assert.match(scores, /Overall/);
  assert.match(await page.locator('#visual .vc-details').innerText(), /vehicle\.height: 1600 → 1000/);
  await page.screenshot({ path: join(shots, 'visual-1-iteration.png') });

  // 4. Compare with the previous iteration.
  await page.click('#visual button:has-text("Compare previous")');
  assert.match(await page.locator('#visual .vc-panes').innerText(), /Previous \(iteration 1\)/);
  await page.screenshot({ path: join(shots, 'visual-2-previous.png') });

  // 5. Lock the body region, then revert the correction.
  await page.locator('#visual .vc-regions input[type=checkbox]').check();
  await page.waitForFunction(() => /🔒 body/.test(document.querySelector('#visual .vc-regions')?.innerText ?? ''));
  await page.locator('#visual .vc-regions input[type=checkbox]').uncheck();
  await page.waitForFunction(() => /🔓 body/.test(document.querySelector('#visual .vc-regions')?.innerText ?? ''));
  await page.click('#visual button:has-text("Revert")');
  await page.waitForFunction(() => document.querySelectorAll('#visual .vc-it.reverted').length >= 4);
  await page.screenshot({ path: join(shots, 'visual-3-reverted.png') });

  assert.deepEqual(errors, [], 'no browser errors: ' + errors.join(' | '));
  console.log('e2e visual refinement flow OK; screenshots in out/e2e/');
} catch (e) {
  failed = true;
  console.error(e);
} finally {
  await browser.close();
  server.kill();
}
process.exit(failed ? 1 : 0);
