// Image-to-CAD browser end-to-end: attach a sketch → the agent's interpretation opens in the
// panel (overlay, confidence, questions) → answer width and thickness → Create & preview →
// commit → undo, against the real server, frontend and OCCT kernel. The agent's answer comes
// from a scripted recording, so no network or API key is needed.
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
const sketch = join(root, 'testdata/agent/images/plate_sketch.png');

const server = spawn(join(root, 'out', 'simoncad'),
  ['--no-browser', '--recording', join(root, 'testdata/ui/image_plate.recording.json'), '--project', join(mkdtempSync(join(tmpdir(), 'simon-')), 'p.scadproj')],
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

  // 1. Attach the sketch with the file picker, and ask.
  await page.setInputFiles('#file-input', sketch);
  await page.waitForSelector('#attachments .chip.attach');
  assert.match(await page.locator('#attachments .chip.attach').innerText(), /plate_sketch\.png · 400×300/);
  await page.fill('#prompt', 'Make this plate from my sketch.');
  await page.keyboard.press('Enter');

  // 2. The interpretation opens: image, overlay, confidence, and exactly the questions that are missing.
  await page.waitForSelector('#interp .interp-svg');
  assert.equal(await page.locator('#interp .interp-svg .ov').count(), 3, 'three detected entities are drawn');
  assert.equal(await page.locator('#interp .interp-svg .ov.hole').count(), 2, 'both circles are holes');
  assert.match(await page.locator('#interp .interp-head').innerText(), /needs/);
  assert.equal(await page.locator('#interp .questions .q').count(), 2, 'width and thickness are asked, nothing else');
  assert.equal(await page.locator('[data-act="create"]').isDisabled(), true, 'nothing can be created before the answers');
  assert.equal(await page.locator('#chat .thumb').count(), 1, 'the user message shows its image');
  await page.screenshot({ path: join(shots, 'image-1-interpretation.png') });

  // 3. Answer "80 mm wide, 5 mm thick" and create: a proposed ChangeSet, previewed as a ghost.
  await page.fill('[data-q="overall_width"]', '80');
  await page.fill('[data-q="thickness"]', '5');
  await page.locator('[data-act="create"]').click();
  await page.waitForSelector('#ghost-bar:not([hidden])');
  await page.waitForFunction(() => window.simonViewport.ghost.children.length === 1);
  assert.equal(await page.locator('.node').count(), 0, 'the model is untouched until commit');
  await page.screenshot({ path: join(shots, 'image-2-preview.png') });

  // 4. Commit, check the editable features, then undo.
  await page.locator('#ghost-bar').getByRole('button', { name: 'Commit' }).click();
  await page.waitForFunction(() => document.querySelectorAll('.node').length === 2);
  await page.waitForFunction(() => window.simonViewport.bodies.size === 1);
  const names = await page.evaluate(() => [...document.querySelectorAll('.node')].map((n) => n.dataset.type));
  assert.deepEqual(names.map((n) => n.trim()).sort(), ['extrude', 'sketch'].sort());
  await page.screenshot({ path: join(shots, 'image-3-committed.png') });
  await page.locator('#viewport').click({ position: { x: 5, y: 5 } });
  await page.keyboard.press('Control+z');
  await page.waitForFunction(() => document.querySelectorAll('.node').length === 0);

  // 5. Drag-and-drop / paste attach the same way.
  await page.evaluate(async () => {
    const bytes = await (await fetch('/' + 'assets/logo.png')).blob();
    const file = new File([bytes], 'dropped.png', { type: 'image/png' });
    const dt = new DataTransfer(); dt.items.add(file);
    document.dispatchEvent(new DragEvent('drop', { dataTransfer: dt, bubbles: true, cancelable: true }));
  });
  await page.waitForSelector('#attachments .chip.attach');
  assert.match(await page.locator('#attachments .chip.attach').first().innerText(), /dropped\.png/);

  assert.deepEqual(errors, [], 'no browser errors: ' + errors.join(' | '));
  console.log('e2e image-to-CAD flow OK; screenshots in out/e2e/');
} catch (e) {
  failed = true;
  console.error(e);
} finally {
  await browser.close();
  server.kill();
}
process.exit(failed ? 1 : 0);
