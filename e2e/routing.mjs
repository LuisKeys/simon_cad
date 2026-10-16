// Routing settings end-to-end: the Routing section (mode, recovery attempts, Advanced model)
// shows the defaults of a local primary, saves, and comes back after a reopen.
import { chromium } from 'playwright-core';
import { spawn } from 'node:child_process';
import { mkdtempSync, existsSync, readFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, resolve, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';
import assert from 'node:assert/strict';

const root = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const chrome = process.env.CHROME_BIN || ['/usr/bin/google-chrome', '/usr/bin/chromium', '/usr/bin/chromium-browser', '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome'].find((p) => existsSync(p));

const work = mkdtempSync(join(tmpdir(), 'simon-routing-'));
const settingsFile = join(work, 'settings.json');
const env = { ...process.env, SIMONCAD_CONFIG: settingsFile, SIMONCAD_PROVIDER: 'ollama', OLLAMA_MODEL: 'gemma4:12b', OLLAMA_BASE_URL: 'http://127.0.0.1:1',
  OPENAI_API_KEY: 'sk-test', OPENAI_MODEL: 'gpt-small', ANTHROPIC_API_KEY: '' };
for (const k of Object.keys(env)) if (/^SIMONCAD_(FALLBACK|ADVANCED|ROUTING|LOCAL_RETRIES|STANDARD_RETRIES)/.test(k)) delete env[k];
const server = spawn(join(root, 'out', 'simoncad'), ['--no-browser', '--project', join(work, 'p.scadproj')],
  { cwd: work, env, stdio: ['ignore', 'pipe', 'inherit'] });
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
  const openRouting = async () => {
    await page.click('#btn-settings');
    await page.waitForSelector('#stab-routing');
    await page.click('#stab-routing');
  };

  await openRouting();
  assert.equal(await page.inputValue('#set-routing-mode'), 'local_first', 'a local primary defaults to local first');
  assert.equal(await page.inputValue('#set-local-retries'), '2');
  assert.equal(await page.inputValue('#set-standard-retries'), '1');
  assert.equal(await page.isChecked('#set-advanced-enabled'), false);
  assert.match(await page.textContent('#spanel-routing'), /Routine operations run on the local model/);
  await page.selectOption('#set-routing-mode', 'local_only');
  assert.match(await page.textContent('#spanel-routing'), /Nothing is ever sent to a cloud model/);
  await page.fill('#set-local-retries', '3');
  await page.fill('#set-standard-retries', '0');
  await page.check('#set-advanced-enabled');
  await page.fill('#set-advanced-model', 'gpt-big');
  await page.click('.dialog.settings button[type=submit]');
  await page.waitForSelector('#settings', { state: 'hidden' });

  const saved = JSON.parse(readFileSync(settingsFile, 'utf8'));
  assert.equal(saved.SIMONCAD_ROUTING_MODE, 'local_only');
  assert.equal(saved.SIMONCAD_LOCAL_RETRIES, '3');
  assert.equal(saved.SIMONCAD_STANDARD_RETRIES, '0');
  assert.equal(saved.SIMONCAD_ADVANCED_ENABLED, 'true');
  assert.equal(saved.SIMONCAD_ADVANCED_OPENAI_MODEL, 'gpt-big');

  await openRouting();
  assert.equal(await page.inputValue('#set-routing-mode'), 'local_only');
  assert.equal(await page.inputValue('#set-advanced-model'), 'gpt-big');
  await page.keyboard.press('Escape');

  // Local only: asking for the frontier model is refused before anything is sent.
  await page.fill('#prompt', 'Use the frontier model to create a box.');
  await page.click('#send');
  await page.waitForFunction(() => /Local only/.test(document.body.textContent));

  assert.deepEqual(errors.filter((e) => !/status of (400|409)/.test(e)), []);
  console.log('routing e2e: ok');
} catch (e) {
  failed = true;
  console.error(e);
} finally {
  await browser.close();
  server.kill();
}
process.exit(failed ? 1 : 0);
