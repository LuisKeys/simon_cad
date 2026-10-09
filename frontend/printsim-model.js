// Print simulation: the pure part (no Three.js, no DOM), so it is unit-tested with `node --test`.
// It decodes the toolpath buffer the Go backend sends (internal/printsim/toolpath/encode.go) and
// drives playback over it. The wire format is little endian:
//   header   16 B: "SCPS", version u32, nLayers u32, nSegments u32
//   summary  16 B: duration f32, filamentMM f32, filamentG f32, flags u32 (bit0 supports, bit1 preview)
//   bbox     24 B: 6 x f32
//   layers   16 B each: z f32, first u32, count u32, t0 f32
//   segments 40 B each: x0 y0 z0 x1 y1 z1 t0 dur width (9 x f32), kind u8, tool u8, pad u16

export const SPEEDS = [1, 10, 50, 100, 500];

export const KINDS = ['travel', 'perimeter', 'external_perimeter', 'infill', 'solid_infill', 'top_bottom', 'support', 'bridge', 'skirt', 'other'];
export const KIND_LABEL = {
  travel: 'Travel', perimeter: 'Inner wall', external_perimeter: 'Outer wall', infill: 'Infill', solid_infill: 'Solid infill',
  top_bottom: 'Top / bottom', support: 'Support', bridge: 'Bridge', skirt: 'Skirt / brim', other: 'Other',
};

// What the "show" toggles cover.
export const GROUPS = {
  travel: ['travel'],
  perimeters: ['perimeter', 'external_perimeter'],
  infill: ['infill', 'solid_infill', 'top_bottom', 'bridge'],
  support: ['support'],
  skirt: ['skirt'],
  other: ['other'],
};

export const HEADER = 56, LAYER_BYTES = 16, SEG_BYTES = 40, SEG_FLOATS = 10;
export const MAX_SEGMENTS = 8_000_000;

/**
 * Decodes the backend buffer. Segments stay in one Float32Array with a stride of SEG_FLOATS so the
 * GPU can use it as an interleaved instance buffer without a copy. `meta` holds [kind, layer]
 * per segment for the shader.
 */
export function decodeToolpath(buf) {
  if (!(buf instanceof ArrayBuffer) || buf.byteLength < HEADER) throw new Error('not a toolpath buffer');
  const dv = new DataView(buf);
  if (String.fromCharCode(dv.getUint8(0), dv.getUint8(1), dv.getUint8(2), dv.getUint8(3)) !== 'SCPS') throw new Error('not a toolpath buffer');
  if (dv.getUint32(4, true) !== 1) throw new Error(`unsupported toolpath version ${dv.getUint32(4, true)}`);
  const nl = dv.getUint32(8, true), ns = dv.getUint32(12, true);
  if (ns > MAX_SEGMENTS || nl > ns + 1 || buf.byteLength !== HEADER + nl * LAYER_BYTES + ns * SEG_BYTES) throw new Error('toolpath buffer has an inconsistent size');
  const flags = dv.getUint32(28, true);
  const layers = new Array(nl);
  let o = HEADER;
  for (let i = 0; i < nl; i++, o += LAYER_BYTES) {
    layers[i] = { z: dv.getFloat32(o, true), first: dv.getUint32(o + 4, true), count: dv.getUint32(o + 8, true), t0: dv.getFloat32(o + 12, true) };
  }
  const segStart = o;
  const seg = new Float32Array(buf, segStart, ns * SEG_FLOATS); // segStart is 4-byte aligned by construction
  const meta = new Float32Array(ns * 2);
  let next = 0;
  for (let li = 0; li < nl; li++) {
    const l = layers[li];
    if (l.first !== next || l.first + l.count > ns) throw new Error(`layer ${li} indexes segments out of range`);
    next = l.first + l.count;
    for (let i = l.first; i < next; i++) {
      const kind = dv.getUint8(segStart + i * SEG_BYTES + 36);
      if (kind >= KINDS.length) throw new Error(`segment ${i} has invalid kind ${kind}`);
      meta[2 * i] = kind;
      meta[2 * i + 1] = li;
    }
  }
  if (nl > 0 && next !== ns) throw new Error('layers do not cover every segment');
  return {
    duration: dv.getFloat32(16, true), filamentMM: dv.getFloat32(20, true), filamentG: dv.getFloat32(24, true),
    supports: !!(flags & 1), preview: !!(flags & 2),
    bbox: Array.from({ length: 6 }, (_, i) => dv.getFloat32(32 + 4 * i, true)),
    layers, layerCount: nl, segCount: ns, seg, meta, buffer: buf,
  };
}

const clamp = (v, lo, hi) => (v < lo ? lo : v > hi ? hi : v);

/**
 * Deterministic playback over a decoded toolpath. Time is simulated seconds; `step` advances it by
 * real elapsed seconds times the speed. Seeking never replays anything: segment lookup is a binary
 * search over start times.
 */
export class Playback {
  constructor(tp) {
    this.tp = tp;
    this.t = 0;
    this.playing = false;
    this.ended = false;
    this.speed = 10;
    this.layerMode = 'all'; // all | upto | single
    this.layer = 0;
    this.window = [0, tp.duration];
  }

  get duration() { return this.tp.duration; }

  play() {
    if (this.t >= this.window[1]) this.t = this.window[0];
    this.playing = true; this.ended = false;
  }
  pause() { this.playing = false; }
  toggle() { if (this.playing) this.pause(); else this.play(); }
  restart() { this.t = this.window[0]; this.ended = false; this.playing = true; }

  setSpeed(s) {
    if (!(s > 0) || !Number.isFinite(s)) throw new RangeError('speed must be a positive number');
    this.speed = s;
  }

  seek(t) {
    this.t = clamp(Number.isFinite(t) ? t : 0, this.window[0], this.window[1]);
    this.ended = this.t >= this.window[1] && this.window[1] > 0;
    if (this.ended) this.playing = false;
  }

  /** f in 0..1 over the current window. */
  seekFraction(f) { this.seek(this.window[0] + clamp(f, 0, 1) * (this.window[1] - this.window[0])); }

  /** Real seconds elapsed since the last frame. Returns true when time moved. */
  step(dtReal) {
    if (!this.playing || !(dtReal > 0)) return false;
    // a stalled tab must not skip minutes of print: cap one step
    this.seek(this.t + Math.min(dtReal, 0.25) * this.speed);
    return true;
  }

  layerEnd(l) { return l + 1 < this.tp.layerCount ? this.tp.layers[l + 1].t0 : this.tp.duration; }

  /** Selecting a layer: in 'all' mode the print jumps to the start of that layer; otherwise the window follows it. */
  seekLayer(l) {
    const L = clamp(Math.round(l), 0, Math.max(0, this.tp.layerCount - 1));
    this.layer = L;
    if (this.layerMode === 'all') { this.seek(this.tp.layers[L]?.t0 ?? 0); return; }
    this.setLayerMode(this.layerMode, L);
  }

  /** all: the whole print. upto: layers 0..L. single: only layer L (inspection). */
  setLayerMode(mode, l = this.layer) {
    if (!['all', 'upto', 'single'].includes(mode)) throw new RangeError(`unknown layer mode ${mode}`);
    const L = clamp(Math.round(l), 0, Math.max(0, this.tp.layerCount - 1));
    this.layerMode = mode; this.layer = L;
    if (mode === 'all') this.window = [0, this.duration];
    else if (mode === 'upto') this.window = [0, this.layerEnd(L)];
    else this.window = [this.tp.layers[L]?.t0 ?? 0, this.layerEnd(L)];
    // show the selected layer complete; Play replays it
    this.t = mode === 'all' ? clamp(this.t, 0, this.duration) : this.window[1];
    this.ended = mode !== 'all'; this.playing = false;
  }

  /** [min, max] layer indices the shader should show. */
  layerRange() {
    if (this.layerMode === 'upto') return [0, this.layer];
    if (this.layerMode === 'single') return [this.layer, this.layer];
    return [0, 1e9];
  }

  /** Index of the segment running at time t (clamped), or -1 for an empty print. */
  segmentAt(t = this.t) {
    const n = this.tp.segCount;
    if (n === 0) return -1;
    const s = this.tp.seg;
    let lo = 0, hi = n - 1;
    while (lo < hi) {
      const mid = (lo + hi + 1) >> 1;
      if (s[mid * SEG_FLOATS + 6] <= t) lo = mid; else hi = mid - 1;
    }
    return lo;
  }

  /** Number of segments that have started by time t: the instance count to draw. */
  visibleCount(t = this.t) {
    const n = this.tp.segCount;
    if (n === 0 || t < this.tp.seg[6]) return 0;
    return this.segmentAt(t) + 1;
  }

  /** At the end of a layer window the print is still in that layer, not at the first point of the next one. */
  _lookup(t) {
    return this.layerMode !== 'all' && t >= this.window[1] && this.window[1] < this.duration ? this.window[1] - 1e-9 : t;
  }

  currentLayer(t = this.t) {
    const i = this.segmentAt(this._lookup(t));
    return i < 0 ? 0 : this.tp.meta[2 * i + 1];
  }

  /** Nozzle position at time t, interpolated within the running segment. */
  nozzle(t = this.t) {
    const i = this.segmentAt(this._lookup(t));
    if (i < 0) return null;
    const s = this.tp.seg, o = i * SEG_FLOATS;
    const t0 = s[o + 6], dur = s[o + 7];
    const f = dur > 0 ? clamp((this._lookup(t) - t0) / dur, 0, 1) : 1;
    const kind = this.tp.meta[2 * i];
    return {
      index: i, frac: f, kind, layer: this.tp.meta[2 * i + 1],
      x: s[o] + (s[o + 3] - s[o]) * f, y: s[o + 1] + (s[o + 4] - s[o + 1]) * f, z: s[o + 2] + (s[o + 5] - s[o + 2]) * f,
      depositing: kind !== 0 && f < 1,
    };
  }

  progress() { return this.duration > 0 ? clamp(this.t / this.duration, 0, 1) : 0; }
}

/** Bit mask of the kinds to draw, from the show toggles ({travel, perimeters, infill, support, skirt, other}). */
export function kindMask(show) {
  let m = 0;
  for (const [group, kinds] of Object.entries(GROUPS)) {
    if (show[group]) for (const k of kinds) m |= 1 << KINDS.indexOf(k);
  }
  return m;
}

/** "45s", "12m 05s", "1h 02m 03s". */
export function formatDuration(sec) {
  if (!Number.isFinite(sec) || sec < 0) return '–';
  const s = Math.round(sec);
  const h = Math.floor(s / 3600), m = Math.floor((s % 3600) / 60), r = s % 60;
  if (h > 0) return `${h}h ${String(m).padStart(2, '0')}m ${String(r).padStart(2, '0')}s`;
  if (m > 0) return `${m}m ${String(r).padStart(2, '0')}s`;
  return `${r}s`;
}

/** Playback clock: "0:45", "12:05", "1:02:03". */
export function formatClock(sec) {
  if (!Number.isFinite(sec) || sec < 0) return '–';
  const s = Math.floor(sec);
  const h = Math.floor(s / 3600), m = Math.floor((s % 3600) / 60), r = s % 60;
  const p = (n) => String(n).padStart(2, '0');
  return h > 0 ? `${h}:${p(m)}:${p(r)}` : `${m}:${p(r)}`;
}

/** Filament use as the user reads it: "3.4 g · 1.1 m". */
export function formatFilament(mm, g) {
  const len = mm >= 1000 ? `${(mm / 1000).toFixed(2)} m` : `${Math.round(mm)} mm`;
  return `${g.toFixed(g < 10 ? 2 : 1)} g · ${len}`;
}

/** Positions of the bed's measurement marks along one axis of `extent` mm. */
export function bedTicks(extent, minor = 10, major = 50) {
  const out = [];
  for (let p = 0; p <= extent + 1e-9; p += minor) out.push({ pos: p, major: Math.abs(p / major - Math.round(p / major)) < 1e-9 });
  return out;
}

/**
 * The box the camera frames and the shadow map covers: the part with a margin, so a small part on
 * a big bed fills the view. min/max are in bed coordinates.
 */
export function frameBox(partMin, partMax, bed) {
  const size = [0, 1, 2].map((a) => partMax[a] - partMin[a]);
  const margin = Math.max(20, 0.5 * Math.max(...size));
  const lo = [0, 1, 2].map((a) => Math.max(a < 2 ? 0 : 0, partMin[a] - margin));
  const hi = [0, 1, 2].map((a) => Math.min(a < 2 ? bed[a] : bed[2], partMax[a] + margin));
  lo[2] = 0; // the plate
  return { min: lo, max: hi };
}

/** A result is stale once the model's geometry revision moved on. */
export function isStale(summary, geomHash) {
  return !!summary && !!geomHash && summary.geom_hash !== geomHash;
}
