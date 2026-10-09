import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import {
  decodeToolpath, Playback, kindMask, KINDS, SPEEDS, HEADER, LAYER_BYTES, SEG_BYTES,
  formatDuration, formatClock, formatFilament, bedTicks, frameBox, isStale,
} from './printsim-model.js';

const near = (a, b, eps = 1e-5) => assert.ok(Math.abs(a - b) < eps, `${a} !~ ${b}`);

// The file the Go encoder wrote (internal/printsim/toolpath/testdata/sample.scps): 3 segments in 2 layers.
const golden = () => {
  const b = readFileSync(fileURLToPath(new URL('../internal/printsim/toolpath/testdata/sample.scps', import.meta.url)));
  return b.buffer.slice(b.byteOffset, b.byteOffset + b.byteLength);
};

// Builds a buffer in the wire format: layers = [[z, [[x0,y0,x1,y1,dur,kind], ...]], ...]
function build(layers) {
  const segs = layers.flatMap(([z, ss]) => ss.map((s) => ({ z, s })));
  const buf = new ArrayBuffer(HEADER + layers.length * LAYER_BYTES + segs.length * SEG_BYTES);
  const dv = new DataView(buf);
  'SCPS'.split('').forEach((c, i) => dv.setUint8(i, c.charCodeAt(0)));
  dv.setUint32(4, 1, true); dv.setUint32(8, layers.length, true); dv.setUint32(12, segs.length, true);
  let t = 0, o = HEADER, first = 0;
  const starts = [];
  for (const [z, ss] of layers) {
    dv.setFloat32(o, z, true); dv.setUint32(o + 4, first, true); dv.setUint32(o + 8, ss.length, true); dv.setFloat32(o + 12, t, true);
    o += LAYER_BYTES; first += ss.length;
    for (const s of ss) { starts.push(t); t += s[4]; }
  }
  dv.setFloat32(16, t, true);
  segs.forEach(({ z, s }, i) => {
    const p = o + i * SEG_BYTES;
    [s[0], s[1], z, s[2], s[3], z, starts[i], s[4], 0.4].forEach((v, k) => dv.setFloat32(p + 4 * k, v, true));
    dv.setUint8(p + 36, s[5]);
  });
  return buf;
}

// 2 layers, 4 segments, durations 1+2+1+4 = 8 s
const sample = () => decodeToolpath(build([
  [0.2, [[0, 0, 10, 0, 1, 2], [10, 0, 10, 10, 2, 3]]],
  [0.4, [[10, 10, 0, 10, 1, 0], [0, 10, 0, 0, 4, 1]]],
]));

test('decodes the buffer the Go encoder wrote', () => {
  const tp = decodeToolpath(golden());
  assert.equal(tp.segCount, 3);
  assert.equal(tp.layerCount, 2);
  assert.equal(tp.preview, true);
  assert.equal(tp.supports, true);
  near(tp.duration, 1.6);
  near(tp.layers[0].z, 0.2); near(tp.layers[1].z, 0.4);
  assert.deepEqual(tp.layers.map((l) => [l.first, l.count]), [[0, 2], [2, 1]]);
  assert.deepEqual([tp.meta[0], tp.meta[2], tp.meta[4]], [KINDS.indexOf('external_perimeter'), KINDS.indexOf('travel'), KINDS.indexOf('support')]);
  assert.deepEqual([tp.meta[1], tp.meta[3], tp.meta[5]], [0, 0, 1]);
  // segment 0: (0,0,.2) -> (10,0,.2) at t=0 for 1 s, 0.7 mm bead
  const s = tp.seg;
  assert.deepEqual([s[0], s[1], s[3], s[4]], [0, 0, 10, 0]);
  near(s[6], 0); near(s[7], 1);
  near(s[10 + 6], 1); // second segment starts when the first ends
  near(tp.filamentMM, 0.9, 1e-4);
  assert.equal(tp.seg.length, 30);
});

test('rejects garbage instead of guessing', () => {
  const ok = golden();
  assert.throws(() => decodeToolpath(new ArrayBuffer(3)), /not a toolpath/);
  assert.throws(() => decodeToolpath(ok.slice(0, ok.byteLength - 1)), /inconsistent/);
  const bad = ok.slice(0); new Uint8Array(bad)[0] = 0x58;
  assert.throws(() => decodeToolpath(bad), /not a toolpath/);
  const ver = ok.slice(0); new DataView(ver).setUint32(4, 9, true);
  assert.throws(() => decodeToolpath(ver), /unsupported/);
  const kind = ok.slice(0); new Uint8Array(kind)[HEADER + 2 * LAYER_BYTES + 36] = 77;
  assert.throws(() => decodeToolpath(kind), /invalid kind/);
  const layer = ok.slice(0); new DataView(layer).setUint32(HEADER + 4, 5, true);
  assert.throws(() => decodeToolpath(layer), /out of range/);
});

test('segment lookup is a binary search over start times', () => {
  const pb = new Playback(sample());
  const at = (t) => pb.segmentAt(t);
  assert.deepEqual([-1, 0, 0.99, 1, 2.99, 3, 3.5, 4, 7.99, 8, 100].map(at), [0, 0, 0, 1, 1, 2, 2, 3, 3, 3, 3]);
  assert.equal(new Playback(decodeToolpath(build([]))).segmentAt(0), -1);
});

test('visible count is the instance count to draw', () => {
  const pb = new Playback(sample());
  assert.equal(pb.visibleCount(0), 1);
  assert.equal(pb.visibleCount(1), 2);
  assert.equal(pb.visibleCount(8), 4);
});

test('the nozzle is interpolated inside the running segment', () => {
  const pb = new Playback(sample());
  let n = pb.nozzle(0.5);
  near(n.x, 5); near(n.y, 0); near(n.z, 0.2); near(n.frac, 0.5);
  assert.equal(n.depositing, true); assert.equal(n.layer, 0);
  n = pb.nozzle(2); // halfway down the second segment
  near(n.x, 10); near(n.y, 5); assert.equal(n.index, 1);
  n = pb.nozzle(3.5); // travel in layer 1
  assert.equal(n.layer, 1); assert.equal(n.depositing, false); near(n.z, 0.4);
  n = pb.nozzle(1e6);
  near(n.x, 0); near(n.y, 0); near(n.frac, 1);
  assert.equal(n.depositing, false);
});

test('play, pause, resume and restart', () => {
  const pb = new Playback(sample());
  assert.equal(pb.step(1), false, 'paused time does not move');
  pb.setSpeed(1); pb.play();
  assert.equal(pb.step(0.25), true); pb.step(0.25); near(pb.t, 0.5);
  pb.pause(); pb.step(10); near(pb.t, 0.5);
  pb.play(); pb.step(0.25); near(pb.t, 0.75); // resume continues
  pb.restart(); assert.equal(pb.t, 0); assert.equal(pb.playing, true);
});

test('speed multiplies real time and rejects nonsense', () => {
  const pb = new Playback(sample());
  for (const s of SPEEDS) { pb.t = 0; pb.setSpeed(s); pb.play(); pb.step(0.01); near(pb.t, 0.01 * s, 1e-9); }
  assert.throws(() => pb.setSpeed(0), RangeError);
  assert.throws(() => pb.setSpeed(NaN), RangeError);
  assert.throws(() => pb.setSpeed(-5), RangeError);
});

test('a stalled frame cannot skip the print', () => {
  const pb = new Playback(sample()); pb.setSpeed(10); pb.play();
  pb.step(60); // a minute-long hiccup counts as 0.25 s
  near(pb.t, 2.5);
});

test('reaching the end stops playback and Play restarts from the start', () => {
  const pb = new Playback(sample()); pb.setSpeed(500); pb.play();
  for (let i = 0; i < 4; i++) pb.step(0.25); // 500x: 125 s of print per frame, capped at the end
  assert.equal(pb.t, 8); assert.equal(pb.playing, false); assert.equal(pb.ended, true);
  pb.play(); assert.equal(pb.t, 0); assert.equal(pb.playing, true); assert.equal(pb.ended, false);
});

test('seeking works forward and backward without replaying', () => {
  const pb = new Playback(sample());
  pb.seek(6); assert.equal(pb.segmentAt(), 3); assert.equal(pb.currentLayer(), 1);
  pb.seek(0.2); assert.equal(pb.segmentAt(), 0); assert.equal(pb.currentLayer(), 0);
  pb.seek(-5); assert.equal(pb.t, 0);
  pb.seek(99); assert.equal(pb.t, 8); assert.equal(pb.ended, true); assert.equal(pb.playing, false);
  pb.seek(NaN); assert.equal(pb.t, 0);
  pb.seekFraction(0.5); near(pb.t, 4);
  pb.seekFraction(2); near(pb.t, 8);
  near(pb.progress(), 1);
});

test('seeking is O(log n): a million segments seek instantly', () => {
  const n = 1_000_000;
  const seg = new Float32Array(n * 10);
  for (let i = 0; i < n; i++) { seg[i * 10 + 6] = i * 0.01; seg[i * 10 + 7] = 0.01; }
  const tp = { duration: n * 0.01, segCount: n, layerCount: 1, layers: [{ z: 0.2, first: 0, count: n, t0: 0 }], seg, meta: new Float32Array(n * 2) };
  const pb = new Playback(tp);
  const t0 = performance.now();
  for (let i = 0; i < 20000; i++) pb.segmentAt((i * 7919 % n) * 0.01);
  assert.ok(performance.now() - t0 < 200, 'binary search should take well under 10 µs per seek');
  assert.equal(pb.segmentAt(5000.0), 500000);
});

test('jump to layer starts that layer in all mode', () => {
  const pb = new Playback(sample());
  pb.seekLayer(1); near(pb.t, 3); assert.equal(pb.layer, 1);
  pb.seekLayer(0); assert.equal(pb.t, 0);
  pb.seekLayer(99); assert.equal(pb.layer, 1, 'clamped to the last layer');
  pb.seekLayer(-3); assert.equal(pb.layer, 0);
});

test('single-layer inspection windows time to one layer', () => {
  const pb = new Playback(sample());
  pb.setLayerMode('single', 1);
  assert.deepEqual(pb.window, [3, 8]); assert.equal(pb.t, 8, 'the layer is shown complete');
  assert.deepEqual(pb.layerRange(), [1, 1]);
  pb.play(); assert.equal(pb.t, 3, 'Play replays the layer');
  pb.setSpeed(1); pb.step(0.25); near(pb.t, 3.25);
  pb.seek(0); assert.equal(pb.t, 3, 'cannot leave the window');
  pb.seek(100); assert.equal(pb.t, 8);
  pb.seekLayer(0); assert.deepEqual(pb.window, [0, 3]); assert.deepEqual(pb.layerRange(), [0, 0]);
  pb.setLayerMode('all'); assert.deepEqual(pb.window, [0, 8]); assert.deepEqual(pb.layerRange()[0], 0);
  assert.throws(() => pb.setLayerMode('weird'), RangeError);
});

test('up-to-layer mode shows the print so far', () => {
  const pb = new Playback(sample());
  pb.setLayerMode('upto', 0);
  assert.deepEqual(pb.window, [0, 3]); assert.deepEqual(pb.layerRange(), [0, 0]);
  pb.setLayerMode('upto', 1);
  assert.deepEqual(pb.window, [0, 8]); assert.deepEqual(pb.layerRange(), [0, 1]);
});

test('empty toolpaths are safe', () => {
  const pb = new Playback(decodeToolpath(build([])));
  assert.equal(pb.nozzle(), null); assert.equal(pb.visibleCount(), 0); assert.equal(pb.progress(), 0);
  pb.play(); pb.step(1); pb.seek(5); pb.seekLayer(3); pb.setLayerMode('single', 0);
});

test('kind mask follows the show toggles', () => {
  const all = { travel: true, perimeters: true, infill: true, support: true, skirt: true, other: true };
  assert.equal(kindMask(all), (1 << KINDS.length) - 1);
  assert.equal(kindMask({}), 0);
  const m = kindMask({ ...all, travel: false });
  assert.equal(m & 1, 0, 'travel bit cleared');
  assert.equal(m & (1 << KINDS.indexOf('infill')), 1 << KINDS.indexOf('infill'));
  const noSupport = kindMask({ ...all, support: false });
  assert.equal(noSupport & (1 << KINDS.indexOf('support')), 0);
  assert.equal(kindMask({ perimeters: true }), (1 << 1) | (1 << 2));
});

test('formatting', () => {
  assert.equal(formatDuration(45), '45s'); assert.equal(formatDuration(725), '12m 05s'); assert.equal(formatDuration(3723), '1h 02m 03s');
  assert.equal(formatDuration(-1), '–'); assert.equal(formatDuration(NaN), '–');
  assert.equal(formatClock(45), '0:45'); assert.equal(formatClock(725), '12:05'); assert.equal(formatClock(3723), '1:02:03');
  assert.equal(formatFilament(1125, 3.35), '3.35 g · 1.13 m'); assert.equal(formatFilament(420, 12.5), '12.5 g · 420 mm');
});

test('bed measurement marks', () => {
  const t = bedTicks(220);
  assert.equal(t.length, 23); assert.equal(t[0].pos, 0); assert.equal(t[22].pos, 220);
  assert.deepEqual(t.filter((x) => x.major).map((x) => x.pos), [0, 50, 100, 150, 200]);
  assert.equal(bedTicks(35).at(-1).pos, 30);
});

test('the frame box keeps a small part large in view and inside the build volume', () => {
  const f = frameBox([100, 100, 0], [120, 120, 4], [220, 220, 250]);
  assert.deepEqual(f.min, [80, 80, 0]); assert.deepEqual(f.max, [140, 140, 24]);
  const edge = frameBox([0, 0, 0], [10, 10, 10], [220, 220, 250]);
  assert.deepEqual(edge.min, [0, 0, 0], 'never frames beyond the plate');
  const big = frameBox([10, 10, 0], [210, 210, 240], [220, 220, 250]);
  assert.deepEqual(big.max, [220, 220, 250]);
});

test('staleness compares geometry revisions', () => {
  assert.equal(isStale({ geom_hash: 'a' }, 'a'), false);
  assert.equal(isStale({ geom_hash: 'a' }, 'b'), true);
  assert.equal(isStale(null, 'b'), false);
  assert.equal(isStale({ geom_hash: 'a' }, ''), false);
});

test('at the end of a layer window the nozzle is still in that layer', () => {
  const pb = new Playback(sample());
  pb.setLayerMode('single', 0); // window [0,3]; t = 3 is where layer 1 begins
  assert.equal(pb.t, 3);
  assert.equal(pb.nozzle().layer, 0); assert.equal(pb.currentLayer(), 0);
  near(pb.nozzle().z, 0.2);
  pb.setLayerMode('upto', 0);
  assert.equal(pb.nozzle().layer, 0);
  pb.setLayerMode('all'); pb.seek(3);
  assert.equal(pb.nozzle().layer, 1, 'in all-layers mode t=3 really is the start of layer 1');
});
