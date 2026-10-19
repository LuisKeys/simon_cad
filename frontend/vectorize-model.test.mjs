import test from 'node:test';
import assert from 'node:assert/strict';
import { defaults, problems, traceArgs, createArgs, summary, warningTexts, debounce, latestOnly, MODES, stampArgs, createCall, plateView, stampProblems, TARGETS } from './vectorize-model.js';

const img = { kind: 'image', imageId: 'img_1', name: 'logo.png', width: 400, height: 300 };

test('a size is required to create but not to preview', () => {
  const s = { ...defaults(), source: img };
  assert.match(problems(s).join(), /real width/);
  assert.equal('width_mm' in traceArgs(s, { preview: true }), false, 'a preview may run in pixels');
  s.widthMm = 40;
  assert.deepEqual(problems(s), []);
  assert.equal(traceArgs(s).width_mm, 40);
  assert.match(problems({ ...defaults() }).join(), /Choose an image/);
});

test('threshold is only sent when it is manual and not adaptive', () => {
  const s = { ...defaults(), source: img, widthMm: 10 };
  assert.equal('threshold' in traceArgs(s), false);
  s.autoThreshold = false; s.threshold = 90.4;
  assert.equal(traceArgs(s).threshold, 90);
  s.adaptive = true;
  assert.equal('threshold' in traceArgs(s), false);
  assert.equal(traceArgs(s).adaptive, true);
  s.adaptive = false; s.threshold = 999;
  assert.equal(traceArgs(s).threshold, 255);
});

test('svg sources send markup, rasters an image id', () => {
  const svg = { ...defaults(), source: { kind: 'svg', svg: '<svg/>', name: 'a.svg' }, widthMm: 5 };
  const a = traceArgs(svg);
  assert.equal(a.svg, '<svg/>');
  assert.equal('image_id' in a, false);
  assert.equal(traceArgs({ ...defaults(), source: img }).image_id, 'img_1');
});

test('a dxf sends its text and needs no width: the file states its units', () => {
  const s = { ...defaults(), source: { kind: 'dxf', dxf: '0\nSECTION\n', name: 'p.dxf' } };
  const a = traceArgs(s);
  assert.equal(a.dxf, '0\nSECTION\n');
  assert.equal('image_id' in a || 'svg' in a || 'width_mm' in a, false);
  assert.deepEqual(problems(s), []);
  assert.equal(problems({ ...defaults(), source: img }).length, 1);
});

test('close gaps only applies to centerlines; placement args go on create', () => {
  const s = { ...defaults(), source: img, widthMm: 20, closeGaps: true, plane: 'XZ', center: false };
  assert.equal('close_gaps' in traceArgs(s), false);
  s.mode = 'centerline';
  assert.equal(traceArgs(s).close_gaps, true);
  const c = createArgs(s);
  assert.equal(c.name, 'logo');
  assert.equal(c.intent, 'Trace logo.png');
  assert.equal(c.plane, 'XZ');
  assert.equal('center' in c, false);
  assert.equal(createArgs({ ...s, plane: 'XY', center: true }).center, true);
  assert.equal('plane' in createArgs({ ...s, plane: 'XY' }), false);
});

test('summary and warnings read naturally', () => {
  assert.deepEqual(summary({ mode: 'filled_regions', regions: 2, holes: 1, segments: 30, units: 'mm', size_mm: [40, 30] }),
    ['2 regions, 1 hole', '40 × 30 mm', '30 curve segments']);
  assert.deepEqual(summary({ mode: 'centerline', strokes: 1, median_stroke_width: 1.234, segments: 5, units: 'mm' }),
    ['1 stroke', 'pen width ≈ 1.23 mm', '5 curve segments']);
  assert.deepEqual(summary({ mode: 'outline', outlines: 3, segments: 9, units: 'px' }), ['3 outlines', '9 curve segments']);
  assert.deepEqual(warningTexts({ warnings: ['FOREGROUND_MAJORITY: most of the image is foreground'] }), ['most of the image is foreground']);
  assert.deepEqual(summary(null), []);
});

test('modes are the three distinct ones', () => {
  assert.deepEqual(MODES.map((m) => m[0]), ['filled_regions', 'centerline', 'outline']);
});

test('debounce keeps only the last call', () => {
  const queue = [];
  const timers = { set: (f) => { queue.push(f); return queue.length; }, clear: (h) => { if (h) queue[h - 1] = null; } };
  const calls = [];
  const d = debounce((x) => calls.push(x), 100, timers);
  d(1); d(2); d(3);
  assert.equal(d.pending(), true);
  queue.splice(0).forEach((f) => f?.());
  assert.deepEqual(calls, [3]);
  d(4); d.cancel();
  queue.splice(0).forEach((f) => f?.());
  assert.deepEqual(calls, [3]);
});

test('latestOnly discards outdated results', async () => {
  const run = latestOnly();
  let release1;
  const first = run(() => new Promise((r) => { release1 = () => r('old'); }));
  const second = await run(async () => 'new');
  release1();
  assert.deepEqual(second, { value: 'new', current: true });
  assert.deepEqual(await first, { current: false });
});

const stampState = (over = {}, stamp = {}) => ({ ...defaults(), source: img, target: 'stamp', stamp: { ...defaults().stamp, width: 60, height: 40, ...stamp }, ...over });

test('a stamp asks for its size and never assumes shrinkage', () => {
  assert.match(problems({ ...defaults(), source: img, target: 'stamp' }).join(), /stamp width/);
  assert.deepEqual(problems(stampState()), []);
  const a = stampArgs(stampState());
  assert.equal(a.width, 60); assert.equal(a.height, 40);
  assert.equal(a.mirror, true);
  assert.equal('shrinkage_percent' in a, false, 'no shrinkage unless the user types one');
  assert.equal('width_mm' in a, false, 'the plate size, not the image size, drives the scale');
  assert.equal('preview' in a, false);
  assert.equal(stampArgs(stampState({}, { shrinkage: 12 })).shrinkage_percent, 12);
  assert.equal('shrinkage_percent' in stampArgs(stampState({}, { shrinkage: 0 })), false);
  assert.match(stampProblems({ ...defaults().stamp, width: 60, height: 40, shrinkage: 55 }).join(), /Shrinkage/);
});

test('a stamp preview ignores the image width typed earlier', () => {
  const s = stampState({ widthMm: 80 });
  assert.equal('width_mm' in traceArgs(s, { preview: true }), false);
  assert.equal(traceArgs({ ...s, target: 'geometry' }).width_mm, 80);
});

test('circle, handle, rounded edges and stroke width map to create_stamp arguments', () => {
  const c = stampArgs(stampState({}, { shape: 'circle', diameter: 50, width: null, height: null, handle: true, handleHeight: 15, rounded: true, mirror: false }));
  assert.equal(c.shape, 'circle'); assert.equal(c.diameter, 50);
  assert.equal('width' in c, false);
  assert.equal(c.handle_enabled, true); assert.equal(c.handle_height, 15);
  assert.equal(c.rounded_edges, true); assert.equal(c.mirror, false);
  assert.equal('stroke_width' in stampArgs(stampState()), false);
  assert.equal(stampArgs(stampState({ mode: 'centerline' }, { strokeWidth: 2 })).stroke_width, 2);
  assert.match(stampProblems({ ...defaults().stamp, width: 10, height: 10, margin: 6 }).join(), /margin/);
});

test('the Create button calls the tool for the target', () => {
  assert.equal(createCall({ ...defaults(), source: img, widthMm: 40 }).name, 'image_to_paths');
  assert.equal(createCall(stampState()).name, 'create_stamp');
  assert.deepEqual(TARGETS.map((t) => t[0]), ['geometry', 'stamp']);
});

test('plateView: the physical stamp is mirrored when the relief is, the impression is not', () => {
  const t = { ...defaults().stamp, width: 60, height: 40, margin: 4, mirror: true };
  const phys = plateView(t, 'physical', 4 / 3), imp = plateView(t, 'impression', 4 / 3);
  assert.equal(phys.flip, true); assert.equal(imp.flip, false);
  assert.ok(Math.abs(phys.aspect - 1.5) < 1e-12);
  // 4:3 design in a 52 x 32 free area: limited by height, 32/(4/3) = 24 wide
  assert.ok(Math.abs(phys.designWidthPct - 40) < 1e-9 && Math.abs(phys.designHeightPct - 80) < 1e-9, JSON.stringify(phys));
  assert.equal(plateView({ ...t, mirror: false }, 'physical', 1).flip, false);
  assert.equal(plateView({ ...t, mirror: false }, 'impression', 1).flip, true);
  assert.equal(plateView({ ...t, width: null }, 'physical', 1), null);
  const round = plateView({ ...t, shape: 'circle', diameter: 50, width: null, height: null }, 'physical', 1);
  assert.equal(round.round, true);
  assert.ok(Math.abs(round.designWidthPct - ((50 - 8) / Math.SQRT2 / 50) * 100) < 1e-9);
});
