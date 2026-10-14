// Resilience e2e: the page says so when the event stream is down, comes back by itself, and
// refetches what it missed; toasts queue instead of overwriting each other; a project saved with
// another OpenCASCADE version warns once. Runs against the real server and frontend.
import { chromium } from 'playwright-core';
import { spawn } from 'node:child_process';
import { mkdtempSync, existsSync, readFileSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, resolve, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';
import assert from 'node:assert/strict';

const root = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const chrome = process.env.CHROME_BIN || ['/usr/bin/google-chrome', '/usr/bin/chromium', '/usr/bin/chromium-browser', '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome'].find((p) => existsSync(p));

function start(project) {
  const server = spawn(join(root, 'out', 'simoncad'),
    ['--no-browser', '--recording', join(root, 'testdata/ui/m2_four_holes.recording.json'), '--project', project],
    { cwd: root, stdio: ['ignore', 'pipe', 'inherit'] });
  const url = new Promise((res, rej) => {
    let buf = '';
    server.stdout.on('data', (d) => { buf += d; const m = /(http:\/\/\S+)/.exec(buf); if (m) res(m[1]); });
    server.on('exit', (c) => rej(new Error('server exited ' + c)));
    setTimeout(() => rej(new Error('server did not start')), 15000);
  });
  return { server, url };
}

const dir = mkdtempSync(join(tmpdir(), 'simon-res-'));
const { server, url: urlP } = start(join(dir, 'p.scadproj'));
const url = await urlP;
const browser = await chromium.launch({ executablePath: chrome, args: ['--use-gl=angle', '--use-angle=swiftshader', '--enable-unsafe-swiftshader', '--ignore-gpu-blocklist'] });
let failed = false;
try {
  const page = await (await browser.newContext({ bypassCSP: true, viewport: { width: 1440, height: 900 } })).newPage();
  const errors = [];
  page.on('pageerror', (e) => errors.push('pageerror: ' + e.message));

  // 1. With the event stream blocked the page says it is offline.
  await page.route('**/api/events*', (route) => route.abort());
  await page.goto(url);
  await page.waitForSelector('#tree');
  await page.waitForFunction(() => !document.querySelector('#conn').hidden, null, { timeout: 8000 });
  assert.match(await page.locator('#conn').textContent(), /Offline/);

  // 2. When the stream returns, the indicator goes away by itself (EventSource retries).
  await page.unroute('**/api/events*');
  await page.waitForFunction(() => document.querySelector('#conn').hidden, null, { timeout: 15000 });

  // 3. What happened while it was away is fetched on return: a change made through the API while
  //    the stream was down shows up without any user action.
  await page.route('**/api/events*', (route) => route.abort());
  await page.reload();
  await page.waitForFunction(() => !document.querySelector('#conn').hidden, null, { timeout: 8000 });
  const token = await page.evaluate(() => document.querySelector('meta[name="simon-token"]').content);
  const made = await fetch(url + 'api/level', { method: 'POST', headers: { 'X-Simon-Token': token, 'Content-Type': 'application/json' }, body: '{"level":"auto"}' });
  assert.ok(made.ok);
  const tool = await fetch(url + 'api/tool', { method: 'POST', headers: { 'X-Simon-Token': token, 'Content-Type': 'application/json' },
    body: JSON.stringify({ name: 'create_box', args: { id: 'while_away', dx: 10, dy: 10, dz: 10 } }) });
  assert.ok(tool.ok);
  assert.equal(await page.evaluate(() => window.simonViewport.bodies.size), 0, 'the page has not seen it yet');
  await page.unroute('**/api/events*');
  await page.waitForFunction(() => window.simonViewport.bodies.has('while_away'), null, { timeout: 15000 });

  // 4. Toasts queue: an informational one followed at once by another does not erase the first for
  //    at least the minimum time, while an error replaces a fresh informational one immediately.
  await page.evaluate(() => { window.__t = []; });
  const text = () => page.locator('#toast').textContent();
  await page.evaluate(() => window.simonToast('first', 'ok'));
  await page.evaluate(() => window.simonToast('second', 'ok'));
  assert.equal(await text(), 'first', 'a second informational toast waits for the first');
  await page.waitForFunction(() => document.querySelector('#toast').textContent === 'second', null, { timeout: 4000 });
  await page.evaluate(() => window.simonToast('info', 'ok'));
  await page.evaluate(() => window.simonToast('boom', 'bad'));
  assert.equal(await text(), 'boom', 'an error is shown at once');
  assert.equal(await page.locator('#toast').getAttribute('role'), 'alert', 'errors are announced');

  assert.deepEqual(errors, [], 'no browser errors: ' + errors.join(' | '));
  console.log('resilience e2e: OK');
} catch (e) {
  failed = true;
  console.error(e);
} finally {
  await browser.close();
  server.kill();
}

// 5. A project saved with another OpenCASCADE version opens with a one-time warning.
if (!failed) {
  const proj = join(dir, 'old.scadproj');
  // create it through a server, then rewrite its recorded kernel version
  const a = start(proj);
  const u = await a.url;
  const b2 = await chromium.launch({ executablePath: chrome, args: ['--use-gl=angle', '--use-angle=swiftshader', '--enable-unsafe-swiftshader'] });
  try {
    const pg = await (await b2.newContext({ bypassCSP: true, viewport: { width: 1280, height: 800 } })).newPage();
    await pg.goto(u);
    await pg.waitForSelector('#tree');
    const tok = await pg.evaluate(() => document.querySelector('meta[name="simon-token"]').content);
    const post = (p, body) => fetch(u + 'api/' + p, { method: 'POST', headers: { 'X-Simon-Token': tok, 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
    assert.ok((await post('project/save', { path: proj })).ok);
    a.server.kill();
    const pj = join(proj, 'project.json');
    writeFileSync(pj, readFileSync(pj, 'utf8').replace(/"occt_version": "[^"]*"/, '"occt_version": "6.0.0"'));
    const c = start(proj);
    const u2 = await c.url;
    const pg2 = await (await b2.newContext({ bypassCSP: true, viewport: { width: 1280, height: 800 } })).newPage();
    await pg2.goto(u2);
    await pg2.waitForSelector('#toast.warn:not([hidden])', { timeout: 10000 });
    assert.match(await pg2.locator('#toast').textContent(), /saved with OpenCASCADE 6\.0\.0/);
    c.server.kill();
    console.log('open-warning e2e: OK');
  } catch (e) {
    failed = true;
    console.error(e);
  } finally {
    await b2.close();
  }
}
process.exit(failed ? 1 : 0);
