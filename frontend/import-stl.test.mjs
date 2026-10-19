import test from 'node:test';
import assert from 'node:assert/strict';
import { stlFiles, dragHasFiles, UNIT_CHOICES, importWithRepair } from './import-stl.js';

test('stlFiles keeps only .stl files, any case', () => {
  const files = [{ name: 'a.stl' }, { name: 'b.STL' }, { name: 'c.step' }, { name: 'd.stl.txt' }, {}];
  assert.deepEqual(stlFiles(files).map((f) => f.name), ['a.stl', 'b.STL']);
  assert.deepEqual(stlFiles(null), []);
});

test('only drags that carry files are taken', () => {
  assert.equal(dragHasFiles({ dataTransfer: { types: ['Files'] } }), true);
  assert.equal(dragHasFiles({ dataTransfer: { types: ['text/plain'] } }), false);
  assert.equal(dragHasFiles({}), false);
});

test('the unit choices match what the server accepts', () => {
  assert.deepEqual(UNIT_CHOICES.map((c) => c.value), ['mm', 'cm', 'm', 'in', 'ft']);
});

const holes = () => Object.assign(new Error('not watertight: 3 open edges'), { code: 'mesh_has_holes' });

test('a file refused only for holes is retried with the holes closed, if the user agrees', async () => {
  const calls = [];
  const uploadMesh = async (f, o) => { calls.push(o); if (!o.fillHoles) throw holes(); return { ok: true }; };
  const asked = [];
  const confirm = async (q) => { asked.push(q); return true; };
  const o = await importWithRepair({ file: { name: 'a.stl' }, units: 'm', uploadMesh, confirm });
  assert.deepEqual(o, { ok: true });
  assert.deepEqual(calls, [{ units: 'm' }, { units: 'm', fillHoles: true }]);
  assert.equal(asked.length, 1);
  assert.match(asked[0].body, /not watertight/);
});

test('declining to close the holes keeps the original refusal', async () => {
  const uploadMesh = async () => { throw holes(); };
  await assert.rejects(importWithRepair({ file: {}, units: 'mm', uploadMesh, confirm: async () => false }), /not watertight/);
});

test('any other refusal is not offered a repair', async () => {
  let asked = false;
  const uploadMesh = async () => { throw Object.assign(new Error('intersects itself'), { code: undefined }); };
  await assert.rejects(importWithRepair({ file: {}, units: 'mm', uploadMesh, confirm: async () => { asked = true; return true; } }), /intersects itself/);
  assert.equal(asked, false);
});

// A stand-in DOM-free harness for initImport: records what the busy dialog and toasts were told.
function harness({ upload, confirmAnswer = true }) {
  const log = [];
  const listeners = {};
  const el = () => ({ addEventListener: (t, f) => { listeners[t] = f; }, click() {}, disabled: false });
  const button = el(), input = { ...el(), files: [], value: '' }, viewport = el();
  const busy = {
    show: (t, s) => log.push(`show:${t ?? ''}|${s ?? ''}`), hide: () => log.push('hide'), close: () => log.push('close'),
  };
  return { log, listeners, button, input, viewport, busy,
    ask: async () => ({ value: 'mm' }), confirm: async () => { log.push('confirm'); return confirmAnswer; },
    toast: (m, k) => log.push(`toast:${k}:${m}`), uploadMesh: upload, refresh: () => log.push('refresh') };
}

test('the busy dialog covers the upload and closes before the result is shown', async () => {
  const { initImport } = await import('./import-stl.js');
  const h = harness({ upload: async () => ({ ok: true }) });
  const { importFiles } = initImport(h);
  await importFiles([{ name: 'a.stl' }]);
  assert.deepEqual(h.log.filter((l) => !l.startsWith('refresh')), ['show:Importing a.stl|Uploading the file', 'close', 'toast:ok:Imported a.stl', 'close']);
  assert.equal(h.button.disabled, false, 'the button is usable again');
});

test('the busy dialog steps aside while the user is asked about holes, then returns', async () => {
  const { initImport } = await import('./import-stl.js');
  let n = 0;
  const h = harness({ upload: async (f, o) => { if (!o.fillHoles) throw Object.assign(new Error('holes'), { code: 'mesh_has_holes' }); n++; return { ok: true }; } });
  await initImport(h).importFiles([{ name: 'b.stl' }]);
  const order = h.log.filter((l) => /^(show|hide|confirm|close)/.test(l));
  assert.deepEqual(order.slice(0, 4), ['show:Importing b.stl|Uploading the file', 'hide', 'confirm', 'show:|']);
  assert.equal(n, 1);
});

test('a refused file closes the dialog and says why', async () => {
  const { initImport } = await import('./import-stl.js');
  const h = harness({ upload: async () => { throw new Error('b.stl was not imported: not watertight'); } });
  await initImport(h).importFiles([{ name: 'b.stl' }]);
  assert.ok(h.log.includes('toast:bad:b.stl was not imported: not watertight'));
  assert.equal(h.log.at(-1), 'close', 'and it is closed again at the very end');
  assert.ok(h.log.indexOf('close') < h.log.indexOf('toast:bad:b.stl was not imported: not watertight'));
});

test('stepFiles keeps .step and .stp files, any case', async () => {
  const { stepFiles } = await import('./import-stl.js');
  const files = [{ name: 'a.step' }, { name: 'b.STP' }, { name: 'c.stl' }, { name: 'd.step.txt' }, {}];
  assert.deepEqual(stepFiles(files).map((f) => f.name), ['a.step', 'b.STP']);
  assert.deepEqual(stepFiles(null), []);
});

test('a STEP file is uploaded without asking for units, and an STL beside it still is', async () => {
  const { initImport } = await import('./import-stl.js');
  const sent = [];
  const h = harness({ upload: async (f) => { sent.push('mesh:' + f.name); return { ok: true }; } });
  let asked = 0;
  h.ask = async () => { asked++; return { value: 'cm' }; };
  h.uploadStep = async (f) => { sent.push('step:' + f.name); return { ok: true }; };
  const { importFiles } = initImport(h);
  await importFiles([{ name: 'a.step' }]);
  assert.equal(asked, 0, 'STEP declares its own units');
  await importFiles([{ name: 'b.STP' }, { name: 'c.stl' }, { name: 'notes.txt' }]);
  assert.equal(asked, 1);
  assert.deepEqual(sent, ['step:a.step', 'step:b.STP', 'mesh:c.stl']);
});

test('a refused STEP file says why and closes the dialog', async () => {
  const { initImport } = await import('./import-stl.js');
  const h = harness({ upload: async () => ({ ok: true }) });
  h.uploadStep = async () => { throw new Error('x.step was not imported: the file has no solid'); };
  await initImport(h).importFiles([{ name: 'x.step' }]);
  assert.ok(h.log.includes('toast:bad:x.step was not imported: the file has no solid'));
  assert.equal(h.log.at(-1), 'close');
});

test('threeMFFiles keeps .3mf files, any case', async () => {
  const { threeMFFiles } = await import('./import-stl.js');
  const files = [{ name: 'a.3mf' }, { name: 'b.3MF' }, { name: 'c.stl' }];
  assert.deepEqual(threeMFFiles(files).map((f) => f.name), ['a.3mf', 'b.3MF']);
});

test('objFiles keeps .obj files, any case, and OBJ asks for units like an STL', async () => {
  const { objFiles, initImport } = await import('./import-stl.js');
  assert.deepEqual(objFiles([{ name: 'a.obj' }, { name: 'b.OBJ' }, { name: 'c.stl' }]).map((f) => f.name), ['a.obj', 'b.OBJ']);
  const sent = [];
  const h = harness({ upload: async (f, o) => { sent.push(f.name + ':' + o.units); return { ok: true }; } });
  let asked = 0;
  h.ask = async () => { asked++; return { value: 'in' }; };
  await initImport(h).importFiles([{ name: 'p.obj' }]);
  assert.equal(asked, 1);
  assert.deepEqual(sent, ['p.obj:in']);
});
