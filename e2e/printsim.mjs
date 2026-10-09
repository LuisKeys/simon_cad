// Print simulation end-to-end: enter the mode from a real model, slice with the built-in preview slicer,
// play / pause / seek / layer inspection, the three visualization presets, the original-model overlay,
// and leaving: the CAD scene and its undo history are untouched and every GPU resource is released.
// Screenshots go to out/e2e/printsim-*.png.
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
const chrome = process.env.CHROME_BIN || ['/usr/bin/google-chrome', '/usr/bin/chromium', '/usr/bin/chromium-browser'].find((p) => existsSync(p));

const server = spawn(join(root, 'out', 'simoncad'),
  ['--no-browser', '--recording', join(root, 'testdata/ui/m2_four_holes.recording.json'), '--project', join(mkdtempSync(join(tmpdir(), 'simon-')), 'p.scadproj')],
  { cwd: root, stdio: ['ignore', 'pipe', 'inherit'], env: { ...process.env, SIMONCAD_PRUSASLICER: '/nonexistent/prusa-slicer' } });
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
  page.on('console', (m) => { if (m.type() === 'error' || m.type() === 'warning') errors.push(`console ${m.type()}: ` + m.text()); });
  const KNOWN_NOISE = /favicon|PCFSoftShadowMap has been removed|GPU stall|GL Driver Message/i; // pre-existing, not ours
  // the built-in preview slicer keeps this test independent of any installed slicer
  await page.addInitScript(() => { try { localStorage.setItem('simon.printsim.provider', '"builtin"'); } catch { /* ignore */ } });
  await page.goto(url);
  await page.waitForSelector('#tree');

  const api = (path, body) => page.evaluate(async ([p, b]) => {
    const r = await fetch('/api/' + p, { method: b === undefined ? 'GET' : 'POST', headers: { 'X-Simon-Token': document.querySelector('meta[name=simon-token]').content, 'Content-Type': 'application/json' }, body: b === undefined ? undefined : JSON.stringify(b) });
    return r.json();
  }, [path, body]);
  const sim = (fn, arg) => page.evaluate(`(() => { const ps = window.simonPrintSim, sim = ps.sim, vp = window.simonViewport; return (${fn})(${JSON.stringify(arg ?? null)}); })()`);
  // The viewport draws only when something changed, so ask for frames before waiting for them.
  const frames = (n = 2) => page.evaluate(async (k) => {
    const vp = window.simonViewport, f0 = vp.frames, t0 = performance.now();
    vp.dirty();
    while (vp.frames < f0 + k && performance.now() - t0 < 15000) { await new Promise((r) => setTimeout(r, 30)); vp.dirty(); }
  }, n);

  await api('level', { level: 'auto' });
  await api('tool', { name: 'create_box', args: { id: 'plate', dx: 40, dy: 30, dz: 8 } });
  await api('tool', { name: 'add_hole', args: { target: 'plate', diameter: 10, positions: [[20, 15]] } });
  await page.waitForFunction(() => window.simonViewport.bodies.size === 1);
  await frames();

  const before = await page.evaluate(() => ({ geos: window.simonViewport.renderer.info.memory.geometries, texs: window.simonViewport.renderer.info.memory.textures, programs: window.simonViewport.renderer.info.programs?.length ?? 0 }));
  const stateBefore = await api('state');
  const hashBefore = (await api('printsim/info')).geom_hash;
  assert.ok(hashBefore, 'geometry revision');

  // 1. Enter: the printer scene replaces the CAD scene in the same viewport.
  await page.click('#btn-printsim');
  await page.waitForFunction(() => !!window.simonPrintSim.sim.playback, null, { timeout: 60000 });
  await frames();
  assert.equal(await page.evaluate(() => document.body.dataset.printsim), 'on');
  assert.equal(await page.locator('#printbar').isVisible(), true);
  assert.equal(await page.locator('#history').isVisible(), false, 'the history bar gives way to the playback bar');
  assert.equal(await page.locator('#print-panel').isVisible(), true);
  assert.equal(await sim(`() => [...vp.bodies.values()][0].group.visible`), false, 'CAD bodies are hidden, not removed');
  assert.equal(await sim(`() => vp.bodies.size`), 1, 'CAD geometry stays loaded');
  assert.ok(await sim(`() => !!vp.scene.getObjectByName('print-simulation')`));
  assert.match(await page.locator('.ps-badge').innerText(), /not verified hardware/);
  assert.match(await page.locator('.ps-summary').innerText(), /Preview slicer/);
  const sum = await sim(`() => ps.state.summary`);
  assert.ok(sum.layers >= 38 && sum.layers <= 42, `40 layers of 0.2 mm for an 8 mm part, got ${sum.layers}`);
  assert.equal(sum.preview, true);
  assert.equal(sum.geom_hash, hashBefore);
  assert.deepEqual(sum.build, [220, 220, 250]);
  assert.ok(sum.duration_s > 60 && sum.filament_g > 0.5);
  assert.equal(await sim(`() => sim.playback.playing`), true, 'the print starts by itself');
  assert.doesNotMatch(await page.locator('.ps-summary').innerText(), /null/);
  await sim(`() => sim.playback.pause()`);
  await page.screenshot({ path: join(shots, 'printsim-00-start.png') });

  // 2. Seeking works without replaying: halfway through the print.
  const total = await sim(`() => sim.playback.duration`);
  await sim(`() => { sim.playback.seekFraction(0.5); sim.refresh(); }`);
  await frames();
  const half = await sim(`() => ({ s: sim.stats(), t: sim.playback.t, n: sim.playback.nozzle(), layer: sim.playback.currentLayer() })`);
  assert.ok(Math.abs(half.t - total / 2) < 0.01);
  assert.ok(half.layer > 10 && half.layer < 30, `mid-print layer ${half.layer}`);
  assert.ok(half.s.drawnBeads > 100 && half.s.drawnBeads < half.s.segments, 'only the printed part is drawn');
  // the nozzle is over the plate, near the part (centred on the 220 mm bed)
  assert.ok(half.n.x > 80 && half.n.x < 140 && half.n.y > 85 && half.n.y < 135, `nozzle at ${half.n.x},${half.n.y}`);
  assert.deepEqual(half.s.nozzle.map((v) => Math.round(v * 1000)), [half.n.x, half.n.y, half.n.z].map((v) => Math.round(v * 1000)), 'the nozzle model follows the toolpath');
  assert.ok(half.s.drawCalls < 80, `a handful of draw calls, not one per segment: ${half.s.drawCalls}`);
  await page.screenshot({ path: join(shots, 'printsim-01-realistic-half.png') });

  // 3. Seek backward, then play at 500x and pause.
  await sim(`() => { sim.playback.seek(1); sim.refresh(); }`);
  assert.equal((await sim(`() => sim.stats()`)).t, 1);
  await page.click('.ps-speeds button:has-text("500x")');
  assert.equal(await sim(`() => sim.playback.speed`), 500);
  await page.click('.printbar .ibtn[aria-label="Play"]');
  assert.equal(await sim(`() => sim.playback.playing`), true);
  await page.waitForFunction(() => window.simonPrintSim.sim.playback.t > 20, null, { timeout: 20000 });
  // a real click takes time between mouse down and up; the button must survive frames redrawn in between
  const pbox = await page.locator('.printbar .ibtn[aria-label="Pause"]').boundingBox();
  await page.mouse.move(pbox.x + pbox.width / 2, pbox.y + pbox.height / 2);
  await page.mouse.down(); await page.waitForTimeout(250); await page.mouse.up();
  assert.equal(await sim(`() => sim.playback.playing`), false, 'Pause works on a slow click while playing');
  const tp = await sim(`() => sim.playback.t`);
  await page.waitForTimeout(300);
  assert.equal(await sim(`() => sim.playback.t`), tp, 'pause holds the time');
  await page.click('.printbar .ibtn[aria-label="Play"]'); // resume
  await page.waitForFunction((t) => window.simonPrintSim.sim.playback.t > t, tp, { timeout: 20000 });
  await page.click('.printbar .ibtn[aria-label="Pause"]');
  await sim(`() => sim.setSpeed(1)`); // at 500x time would run away between the click and the read
  await page.click('.printbar .ibtn[aria-label="Restart"]');
  assert.ok((await sim(`() => sim.playback.t`)) < 5, 'restart goes back to the start');
  await page.click('.printbar .ibtn[aria-label="Pause"]');

  // 4. Layer inspection.
  await page.locator('.ps-layer').evaluate((el) => { el.value = '10'; el.dispatchEvent(new Event('input', { bubbles: true })); });
  assert.equal(await sim(`() => sim.playback.layer`), 9, 'jump to layer 10');
  await page.selectOption('.printbar select', 'single');
  await frames();
  const single = await sim(`() => ({ range: sim.playback.layerRange(), mode: sim.playback.layerMode, mask: sim.u.uLayerRange.value.toArray() })`);
  assert.deepEqual(single.range, [9, 9]);
  assert.deepEqual(single.mask, [9, 9]);
  await page.screenshot({ path: join(shots, 'printsim-02-single-layer.png') });
  await page.selectOption('.printbar select', 'all');

  // 5. Presets switch without re-slicing.
  const jobBefore = await sim(`() => ps.state.summary.job`);
  await sim(`() => { sim.playback.seekFraction(0.7); sim.refresh(); }`);
  await page.click('.ps-seg button:has-text("Technical")');
  await frames();
  assert.equal(await sim(`() => sim.opts.mode`), 'technical');
  assert.match(await page.locator('.ps-legend').innerText(), /Outer wall[\s\S]*Infill/);
  await page.screenshot({ path: join(shots, 'printsim-03-technical.png') });
  await page.click('.ps-seg button:has-text("Diagnostics")');
  await frames();
  await page.screenshot({ path: join(shots, 'printsim-04-diagnostics.png') });
  await page.click('.ps-seg button:has-text("Realistic")');
  assert.equal(await sim(`() => ps.state.summary.job`), jobBefore, 'switching presets must not slice again');

  // 6. Filters and the original-model overlay.
  const mask0 = await sim(`() => sim.u.uMask.value`);
  await page.locator('input[data-show=infill]').uncheck();
  assert.notEqual(await sim(`() => sim.u.uMask.value`), mask0);
  await page.locator('input[data-show=infill]').check();
  await page.locator('input[data-show=travel]').check();
  assert.ok((await sim(`() => sim.u.uMask.value & 1`)) === 1, 'travel moves can be shown');
  await page.locator('input[data-show=travel]').uncheck();
  await page.locator('input[data-opt=overlay]').check();
  assert.equal(await sim(`() => sim.overlay.visible && sim.overlay.children.length > 0`), true);
  const shared = await sim(`() => sim.overlay.children[0].geometry === [...vp.bodies.values()][0].mesh.geometry`);
  assert.equal(shared, true, 'the overlay reuses the CAD geometry instead of copying it');
  // the part (40 x 30, corner at the origin) is centred on the 220 mm plate: offset (110-20, 110-15, 0)
  assert.deepEqual(await sim(`() => sim.overlay.position.toArray().map((v) => Math.round(v))`), [90, 95, 0]);
  await frames();
  await page.screenshot({ path: join(shots, 'printsim-05-overlay.png') });
  await page.locator('input[data-opt=overlay]').uncheck();

  // 6b. The built-in benchmark reports frame statistics (software rendering here, so the numbers only prove it runs).
  const bench = await page.evaluate(() => window.simonPrintSim.sim.benchmark({ ms: 1500, speed: 100 }));
  assert.ok(bench.frames > 2 && bench.fps > 0 && bench.p95Ms >= bench.meanMs * 0.5, JSON.stringify(bench));
  console.log(`benchmark (SwiftShader, ${bench.segments} segments): ${bench.fps.toFixed(1)} fps, p95 ${bench.p95Ms.toFixed(0)} ms, ${bench.drawCalls} draw calls`);

  // 7. Orbiting works during the simulation (the camera moves, the mode stays).
  const cam0 = await sim(`() => vp.camera.position.toArray()`);
  await page.mouse.move(700, 400); await page.mouse.down(); await page.mouse.move(820, 440, { steps: 6 }); await page.mouse.up();
  const cam1 = await sim(`() => vp.camera.position.toArray()`);
  assert.notDeepEqual(cam0.map((v) => Math.round(v)), cam1.map((v) => Math.round(v)), 'the view orbits');

  // 8. Nothing so far touched the model; editing it marks the result stale (the revision moved on).
  assert.equal((await api('state')).frames, stateBefore.frames, 'the simulation added nothing to the history');
  assert.equal((await api('printsim/info')).geom_hash, hashBefore, 'and did not change the geometry');
  assert.equal(await sim(`() => ps.state.stale`), false);
  await api('param', { feature: 'plate', parameter: 'dz', value: 10 });
  await page.waitForFunction(() => window.simonPrintSim.state.stale === true, null, { timeout: 15000 });
  assert.equal(await page.locator('.ps-note.warn:not([hidden])').filter({ hasText: 'model changed' }).count(), 1, 'the panel says the slice is stale');
  await frames();
  await page.screenshot({ path: join(shots, 'printsim-05b-stale.png') });

  // 9. Leave: CAD scene restored, history untouched, GPU resources released.
  await page.click('#btn-printsim');
  await frames();
  assert.equal(await page.evaluate(() => document.body.dataset.printsim ?? ''), '');
  assert.equal(await page.locator('#history').isVisible(), true);
  assert.equal(await page.locator('#printbar').isVisible(), false);
  assert.equal(await sim(`() => [...vp.bodies.values()][0].group.visible`), true, 'the CAD model is back');
  assert.equal(await sim(`() => !!vp.scene.getObjectByName('print-simulation')`), false);
  assert.equal(await sim(`() => vp.simMode`), false);
  assert.equal(await sim(`() => vp.frameHooks.length`), 0);
  const after = await page.evaluate(() => ({ geos: window.simonViewport.renderer.info.memory.geometries, texs: window.simonViewport.renderer.info.memory.textures }));
  assert.equal(after.geos, before.geos, `geometries released (${before.geos} -> ${after.geos})`);
  assert.equal(after.texs, before.texs, `textures released (${before.texs} -> ${after.texs})`);
  await page.screenshot({ path: join(shots, 'printsim-06-back-to-cad.png') });

  assert.equal((await api('state')).frames, stateBefore.frames + 1, 'only the user\'s own edit is in the history');
  assert.deepEqual(errors.filter((e) => !KNOWN_NOISE.test(e)), [], 'no console errors or shader failures');
  console.log('printsim e2e ok');
} catch (e) {
  failed = true;
  console.error(e);
} finally {
  await browser.close();
  server.kill();
}
process.exit(failed ? 1 : 0);
