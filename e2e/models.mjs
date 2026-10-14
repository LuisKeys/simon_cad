// Primary / Fallback models end-to-end: the two Settings sections, per-provider token prices
// (never for Ollama), provider switches that keep what was typed, persistence across a
// reopen, and the model indicator in the Simon Agent header.
import { chromium } from 'playwright-core';
import { spawn } from 'node:child_process';
import { mkdtempSync, mkdirSync, existsSync, readFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, resolve, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';
import assert from 'node:assert/strict';

const root = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const shots = join(root, 'out', 'e2e');
mkdirSync(shots, { recursive: true });
const chrome = process.env.CHROME_BIN || ['/usr/bin/google-chrome', '/usr/bin/chromium', '/usr/bin/chromium-browser', '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome'].find((p) => existsSync(p));

// A configuration that predates the fallback slot: Ollama selected, OpenAI configured. It
// runs in an empty directory with its own settings file, so no .env or saved settings leak in.
const work = mkdtempSync(join(tmpdir(), 'simon-models-'));
const settingsFile = join(work, 'settings.json');
const env = { ...process.env, SIMONCAD_CONFIG: settingsFile, SIMONCAD_PROVIDER: 'ollama', OLLAMA_MODEL: 'qwen3', OLLAMA_BASE_URL: 'http://127.0.0.1:1', OLLAMA_VISION: '',
  OPENAI_API_KEY: 'sk-test', OPENAI_MODEL: 'gpt-x', OPENAI_INPUT_PRICE: '2', ANTHROPIC_API_KEY: '' };
for (const k of Object.keys(env)) if (k.startsWith('SIMONCAD_FALLBACK_')) delete env[k];
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
  // The consent request is a 409 by design; the browser logs every non-2xx response.
  page.on('console', (m) => { if (m.type() === 'error' && !/status of 409/.test(m.text())) errors.push('console: ' + m.text()); });
  await page.goto(url);
  await page.waitForSelector('#tree');
  const visible = (sel) => page.locator(sel).isVisible();

  // Header indicator: the configured primary model, before any request.
  await page.waitForSelector('#agent-model:not([hidden])');
  assert.equal((await page.textContent('#agent-model')).trim(), 'Ollama · qwen3');
  assert.match(await page.getAttribute('#agent-model', 'class'), /idle/);
  await page.locator('#right').screenshot({ path: join(shots, 'models-header.png') });

  // An image qwen3 cannot read needs the remote fallback: the user is asked first, and on
  // Cancel nothing is sent anywhere (the request never reaches a model).
  await page.setInputFiles('#file-input', join(root, 'testdata/agent/images/plate_sketch.png'));
  await page.waitForSelector('#attachments .thumb, #attachments img');
  await page.fill('#prompt', 'Model this plate.');
  await page.click('#send');
  await page.waitForSelector('#confirm:not([hidden])');
  assert.match(await page.textContent('#confirm-body'), /^Ollama · qwen3 cannot handle it locally.*OpenAI · gpt-x/);
  assert.equal(await visible('#confirm-remember'), true);
  await page.locator('#confirm .dialog').screenshot({ path: join(shots, 'models-consent.png') });
  await page.uncheck('#confirm-remember-box'); // cancel just this once
  await page.click('#confirm-cancel');
  await page.waitForSelector('#confirm', { state: 'hidden' });
  const st = await page.evaluate(async () => (await fetch('/api/state', { headers: { 'X-Simon-Token': document.querySelector('meta[name=simon-token]').content } })).json());
  assert.equal(st.chat.filter((m) => m.role === 'user').length, 0, 'a cancelled request is not sent');
  assert.equal(await page.inputValue('#prompt'), 'Model this plate.', 'the prompt is kept to try again');

  const openSettings = async () => {
    await page.click('#btn-settings');
    await page.waitForSelector('#set-primary-provider');
  };
  await openSettings();
  // Primary: Ollama, no key, no prices. Fallback: migrated from the OpenAI configuration.
  assert.equal(await page.inputValue('#set-primary-provider'), 'ollama');
  assert.equal(await visible('#set-primary-key'), false);
  assert.equal(await visible('#set-primary-price-in'), false, 'Ollama never shows token prices');
  assert.equal(await visible('#set-primary-ctx'), true, 'Ollama has a context window');
  await page.fill('#set-primary-ctx', '65536');
  assert.equal(await page.isChecked('#set-fallback-enabled'), true);
  assert.equal(await page.inputValue('#set-fallback-provider'), 'openai');
  assert.equal(await page.inputValue('#set-fallback-model'), 'gpt-x');
  assert.equal(await page.inputValue('#set-fallback-price-in'), '2');
  assert.equal(await visible('#set-fallback-consent'), true, 'a local primary asks before sending data out');
  assert.deepEqual(await page.$$eval('#set-fallback-provider option', (o) => o.map((x) => x.value)), ['openai', 'claude']);
  assert.match(await page.textContent('.dialog.settings'), /existing OpenAI configuration/);
  await page.locator('.dialog.settings').screenshot({ path: join(shots, 'models-settings-ollama.png') });

  // Switching the primary shows prices immediately and hides the consent row; back to Ollama
  // hides them again and keeps what was typed for OpenAI.
  await page.selectOption('#set-primary-provider', 'openai');
  assert.equal(await visible('#set-primary-price-in'), true);
  assert.equal(await visible('#set-fallback-consent'), false);
  assert.equal(await visible('#set-primary-ctx'), false, 'cloud models have no window setting');
  await page.fill('#set-primary-model', 'gpt-small');
  await page.fill('#set-primary-price-in', '0.5');
  await page.selectOption('#set-primary-provider', 'ollama');
  assert.equal(await visible('#set-primary-price-in'), false);
  await page.selectOption('#set-primary-provider', 'openai');
  assert.equal(await page.inputValue('#set-primary-model'), 'gpt-small');
  assert.equal(await page.inputValue('#set-primary-price-in'), '0.5');
  await page.selectOption('#set-primary-provider', 'ollama');

  // The fallback keeps its own model and prices, and switching its provider keeps them too.
  await page.fill('#set-fallback-model', 'gpt-big');
  await page.fill('#set-fallback-price-in', '10');
  await page.selectOption('#set-fallback-provider', 'claude');
  assert.equal(await visible('#set-fallback-price-in'), true);
  await page.fill('#set-fallback-key', 'sk-ant');
  await page.selectOption('#set-fallback-provider', 'openai');
  assert.equal(await page.inputValue('#set-fallback-model'), 'gpt-big');
  await page.selectOption('#set-fallback-consent', 'allow');
  await page.click('.dialog.settings button[type=submit]');
  await page.waitForSelector('#settings', { state: 'hidden' });

  const saved = JSON.parse(readFileSync(settingsFile, 'utf8'));
  assert.equal(saved.SIMONCAD_PROVIDER, 'ollama');
  assert.equal(saved.SIMONCAD_FALLBACK_ENABLED, 'true');
  assert.equal(saved.SIMONCAD_FALLBACK_OPENAI_MODEL, 'gpt-big');
  assert.equal(saved.SIMONCAD_FALLBACK_OPENAI_INPUT_PRICE, '10');
  assert.equal(saved.SIMONCAD_FALLBACK_ANTHROPIC_API_KEY, 'sk-ant');
  assert.equal(saved.SIMONCAD_FALLBACK_CONSENT, 'allow');
  assert.equal(saved.OPENAI_MODEL, 'gpt-small', 'the primary-slot OpenAI model is its own');
  assert.equal(saved.OLLAMA_INPUT_PRICE, undefined, 'no price is stored for a local model');
  assert.equal(saved.OLLAMA_NUM_CTX, '65536');

  // Reopened: everything is where it was left.
  await openSettings();
  assert.equal(await page.inputValue('#set-fallback-model'), 'gpt-big');
  assert.equal(await page.inputValue('#set-fallback-consent'), 'allow');
  assert.doesNotMatch(await page.textContent('.dialog.settings'), /existing OpenAI configuration/, 'saved: no longer migrated');
  // Fallback off with a local primary: the consequence is spelled out.
  await page.uncheck('#set-fallback-enabled');
  assert.match(await page.textContent('.dialog.settings'), /refused, never sent elsewhere/);
  await page.keyboard.press('Escape');

  assert.deepEqual(errors, []);
  console.log('models e2e: ok');
} catch (e) {
  failed = true;
  console.error(e);
} finally {
  await browser.close();
  server.kill();
}
process.exit(failed ? 1 : 0);
