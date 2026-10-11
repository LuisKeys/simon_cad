// Shortcuts panel end-to-end: the header button and "?" open the popup over the main screen,
// "?" / Esc / a click outside close it, "?" is ignored while typing, and other shortcuts
// (Delete, F, M…) do not fire while it is open.
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
  const panel = page.locator('#shortcuts');

  // Header button opens it; the most used section comes first.
  await page.click('#btn-shortcuts');
  await panel.waitFor({ state: 'visible' });
  assert.equal(await panel.locator('.sc-sec h3').first().textContent(), 'Essentials');
  assert.ok(await panel.locator('.sc-row').count() > 20);
  await page.screenshot({ path: join(shots, 'shortcuts.png') });

  // Esc closes it.
  await page.keyboard.press('Escape');
  await panel.waitFor({ state: 'hidden' });

  // "?" toggles it; while open, F / M do nothing (measure stays off).
  await page.locator('#gl').click({ position: { x: 20, y: 20 } });
  await page.keyboard.press('?');
  await panel.waitFor({ state: 'visible' });
  await page.keyboard.press('m');
  await page.keyboard.press('?');
  await panel.waitFor({ state: 'hidden' });
  assert.notEqual(await page.textContent('#hud'), 'Measure: click two points');

  // A click outside the dialog closes it.
  await page.keyboard.press('?');
  await panel.waitFor({ state: 'visible' });
  await page.mouse.click(10, 890);
  await panel.waitFor({ state: 'hidden' });

  // Typing "?" in the prompt does not open it.
  await page.fill('#prompt', '');
  await page.focus('#prompt');
  await page.keyboard.type('why?');
  assert.equal(await page.inputValue('#prompt'), 'why?');
  assert.equal(await panel.isHidden(), true);

  // More menu entry.
  await page.click('#btn-more');
  await page.click('#ctxmenu li:has-text("Keyboard shortcuts")');
  await panel.waitFor({ state: 'visible' });
  await page.keyboard.press('Escape');
  await panel.waitFor({ state: 'hidden' });

  assert.deepEqual(errors, []);
  console.log('shortcuts e2e: ok');
} catch (e) {
  failed = true;
  console.error(e);
} finally {
  await browser.close();
  server.kill();
}
process.exit(failed ? 1 : 0);
