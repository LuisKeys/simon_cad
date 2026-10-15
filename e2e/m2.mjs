// M2 browser end-to-end: palette → create plate → click a face → prompt → preview →
// commit → undo, against the real server, frontend and OCCT kernel (agent answers come
// from a recorded run so no network or API key is needed).
import { chromium } from 'playwright-core';
import { spawn } from 'node:child_process';
import { mkdtempSync, mkdirSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, resolve, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';
import assert from 'node:assert/strict';

const root = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const shots = join(root, 'out', 'e2e');
mkdirSync(shots, { recursive: true });
import { existsSync } from 'node:fs';
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
  const page = await (await browser.newContext({ bypassCSP: true, viewport: { width: 1440, height: 900 } })).newPage();
  const errors = [];
  page.on('pageerror', (e) => errors.push('pageerror: ' + e.message));
  page.on('console', (m) => { if (m.type() === 'error') errors.push('console: ' + m.text()); });
  await page.goto(url);
  await page.waitForSelector('#tree');

  // 1. Create the plate through the command palette (direct tool call, Review level → proposed).
  await page.keyboard.press('Control+k');
  await page.fill('#pal-input', 'create_box');
  await page.locator('#pal-list li').filter({ has: page.locator('.k', { hasText: /^tool$/i }), hasText: 'create_box' }).first().waitFor();
  await page.keyboard.press('Enter');
  await page.fill('#pal-args', JSON.stringify({ id: 'plate', name: 'Base plate', dx: 100, dy: 60, dz: 4, intent: 'Base plate' }));
  await page.click('#pal-run');
  const card = page.locator('.cs').first();
  await card.waitFor();
  await card.getByRole('button', { name: 'Commit' }).click();
  await page.waitForFunction(() => window.simonViewport.bodies.size === 1);
  await page.screenshot({ path: join(shots, '1-plate.png') });

  // 2. Click the top face (pick filter: Face).
  await page.click('#act-select-mode');
  await page.click('[data-filter="face"]');
  const target = await page.evaluate(() => {
    const vp = window.simonViewport;
    const b = vp.bodies.get('plate').data;
    const f = b.faces.find((f) => f.normal && f.normal[2] === 1);
    const s = vp.projectToPixels(new vp.camera.position.constructor(...f.centroid));
    const r = vp.canvas.getBoundingClientRect();
    return { x: r.left + s.x, y: r.top + s.y };
  });
  await page.mouse.click(target.x, target.y);
  await page.waitForSelector('.chip');
  assert.match(await page.locator('.chip').first().getAttribute('title'), /^face\(feature=plate,side=\+Z\)$/);
  assert.equal(await page.evaluate(() => window.simonViewport.selGroup.children.filter((c) => c.isMesh).length), 1, 'selected face is highlighted');

  // 3. Ask the agent; it sees the selection. At the Review level its step is applied as a
  //    draft the user keeps or discards.
  await page.fill('#prompt', 'Add four M3 holes here');
  await page.keyboard.press('Enter');
  const draft = page.locator('.cs').filter({ hasText: /applied as a draft/ }).first();
  await draft.waitFor();
  await page.waitForFunction(() => document.querySelectorAll('.node').length === 2);
  await page.waitForFunction(() => [...window.simonViewport.bodies.values()][0]?.data.faces.some((f) => f.surface === 'cylinder'));
  await page.screenshot({ path: join(shots, '2-draft.png') });

  // 4. Keep the draft: it becomes a semantic checkpoint.
  await draft.getByRole('button', { name: 'Keep' }).click();
  await page.waitForFunction(() => document.querySelectorAll('.cp').length >= 2);
  await page.screenshot({ path: join(shots, '3-committed.png') });
  assert.ok((await page.locator('.cp').count()) >= 2, 'semantic timeline has the checkpoints');

  // 5. Undo with the keyboard.
  await page.locator('#viewport').click({ position: { x: 5, y: 5 } }); // focus away from inputs
  await page.keyboard.press('Control+z');
  await page.waitForFunction(() => document.querySelectorAll('.node').length === 1);
  await page.screenshot({ path: join(shots, '4-undone.png') });

  // 6. Redo, then the remaining viewport tools: measurement, section plane, playback.
  await page.keyboard.press('Control+Shift+z');
  await page.waitForFunction(() => document.querySelectorAll('.node').length === 2);
  await page.click('#btn-measure');
  const pts = await page.evaluate(() => {
    const vp = window.simonViewport, r = vp.canvas.getBoundingClientRect();
    const P = vp.camera.position.constructor;
    const at = (x, y, z) => { const s = vp.projectToPixels(new P(x, y, z)); return { x: r.left + s.x, y: r.top + s.y }; };
    return [at(20, 30, 4), at(80, 30, 4)]; // two points on the top face, 60 mm apart in X
  });
  for (const p of pts) await page.mouse.click(p.x, p.y);
  const dist = await page.evaluate(() => window.simonViewport.measure.pts.length === 2 && window.simonViewport.measure.pts[0].distanceTo(window.simonViewport.measure.pts[1]));
  assert.ok(Math.abs(dist - 60) < 0.5, `measured ${dist} mm, expected 60`);
  assert.match(await page.locator('#measure-label').innerText(), /^60\.\d+ mm/);
  await page.click('#btn-measure');
  await page.keyboard.press('Escape'); // no selection: the bar offers Section and View again
  await page.click('#act-section');
  await page.click('li[data-axis="Y"]');
  assert.equal(await page.evaluate(() => window.simonViewport.renderer.clippingPlanes.length), 1, 'section plane active');
  await page.click('#act-section');
  await page.click('li[data-axis=""]');
  await page.click('#act-view');
  await page.click('li[data-view="play"]');
  // Playback replays the conversation too: the manual plate, then the prompt, the agent's call and its answer.
  await page.waitForFunction(() => document.querySelector('#chat.replaying .msg.system')?.textContent.startsWith('Manual edit'), null, { timeout: 15000 });
  await page.waitForFunction(() => document.querySelector('#chat.replaying .msg.user')?.textContent === 'Add four M3 holes here', null, { timeout: 15000 });
  await page.waitForFunction(() => document.querySelector('#hud').textContent.includes('2/2'), null, { timeout: 15000 });
  await page.waitForFunction(() => /^I proposed four/.test(document.querySelector('#chat.replaying .msg.agent')?.textContent ?? ''), null, { timeout: 15000 });
  await page.screenshot({ path: join(shots, '5-playback-chat.png') });
  await page.waitForFunction(() => !/\d\/\d|Playback/.test(document.querySelector('#hud').textContent) && window.simonViewport.bodies.size === 1, null, { timeout: 15000 });
  assert.equal(await page.locator('#chat.replaying').count(), 0, 'the real chat is back after playback');
  assert.ok((await page.locator('#chat .msg.tool').count()) >= 1, 'the real chat keeps its tool rows');
  await page.screenshot({ path: join(shots, '5-after-playback.png') });

  // Esc stops a playback midway.
  await page.click('#act-view');
  await page.click('li[data-view="play"]');
  await page.waitForSelector('#chat.replaying');
  await page.keyboard.press('Escape');
  await page.waitForFunction(() => !document.querySelector('#chat.replaying') && window.simonViewport.bodies.size === 1, null, { timeout: 15000 });

  assert.deepEqual(errors, [], 'no browser errors: ' + errors.join(' | '));
  console.log('e2e M2 flow OK; screenshots in out/e2e/');
} catch (e) {
  failed = true;
  console.error(e);
} finally {
  await browser.close();
  server.kill();
}
process.exit(failed ? 1 : 0);
