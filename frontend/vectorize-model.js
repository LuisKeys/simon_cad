// The trace-image panel's state and rules (no DOM; unit tested). The panel turns an image or SVG
// into editable geometry with the same deterministic tools the agent uses: vectorize_image for the
// live preview, image_to_paths to create it.

export const MODES = [
  ['filled_regions', 'Filled regions', 'Solid areas with their holes: logos and silhouettes. Becomes a sketch you can extrude or emboss.'],
  ['centerline', 'Centerlines', 'The middle of each stroke, with its width: handwriting, line drawings, a flower. Becomes paths to sweep a profile along.'],
  ['outline', 'Outlines', 'Only the boundary curves of the shapes, as closed paths.'],
];

export const PLANES = ['XY', 'XZ', 'YZ'];

export const TARGETS = [
  ['geometry', 'Sketch / paths', 'Create the traced shapes as editable geometry'],
  ['stamp', 'Stamp', 'Make a printable stamp: plate, raised design, optional handle'],
];

export function stampDefaults() {
  return {
    shape: 'rectangle', width: null, height: null, diameter: null,
    thickness: 4, relief: 1.5, margin: 3, strokeWidth: 1.5,
    mirror: true, rounded: false, handle: false, handleHeight: 12,
    shrinkage: null,          // % of material shrinkage; blank = none (never assumed)
  };
}

export function defaults() {
  return {
    target: 'geometry',
    stamp: stampDefaults(),
    mode: 'filled_regions',
    autoThreshold: true, threshold: 128,
    adaptive: false, invert: false,
    minArea: 12, smooth: 0.7, tolerance: 0.8,
    closeGaps: false,
    widthMm: null,            // the real width of the whole image: required to create
    plane: 'XY', center: true,
    source: null,             // { kind: 'image'|'svg', imageId?, svg?, name, width, height }
  };
}

/** Problems that stop Create (the preview runs without a size). Empty when fine. */
export function problems(s) {
  const out = [];
  if (!s.source) out.push('Choose an image or an SVG file');
  if (s.minArea < 1) out.push('Minimum area must be at least 1 px');
  if (s.target === 'stamp') return out.concat(stampProblems(s.stamp));
  if (!(s.widthMm > 0)) out.push('Enter the real width of the image in mm: sizes are never guessed');
  return out;
}

/** The stamp's own requirements: its size is asked for, never assumed. */
export function stampProblems(t) {
  const out = [];
  if (t.shape === 'circle') {
    if (!(t.diameter > 0)) out.push('Enter the stamp diameter in mm');
  } else {
    if (!(t.width > 0)) out.push('Enter the stamp width in mm');
    if (!(t.height > 0)) out.push('Enter the stamp height in mm');
  }
  if (!(t.thickness > 0)) out.push('Plate thickness must be greater than 0');
  if (!(t.relief > 0)) out.push('Relief height must be greater than 0');
  const side = t.shape === 'circle' ? t.diameter : Math.min(t.width, t.height);
  if (side > 0 && t.margin * 2 >= side) out.push('The margin leaves no room for the design');
  if (t.shrinkage !== null && t.shrinkage !== '' && !(t.shrinkage >= 0 && t.shrinkage < 40)) out.push('Shrinkage must be between 0 and 40 %');
  return out;
}

/** The arguments shared by vectorize_image and image_to_paths. */
export function traceArgs(s, { preview = false } = {}) {
  if (!s.source) throw new Error('no source image');
  const a = { mode: s.mode };
  if (s.source.kind === 'svg') a.svg = s.source.svg; else a.image_id = s.source.imageId;
  if (!s.autoThreshold && !s.adaptive) a.threshold = Math.max(1, Math.min(255, Math.round(s.threshold)));
  if (s.adaptive) a.adaptive = true;
  if (s.invert) a.invert = true;
  if (s.closeGaps && s.mode === 'centerline') a.close_gaps = true;
  a.min_area = Math.max(1, Math.round(s.minArea));
  a.smooth = +s.smooth.toFixed(3);
  a.tolerance = +s.tolerance.toFixed(3);
  if (s.widthMm > 0 && s.target !== 'stamp') a.width_mm = s.widthMm; // a stamp is sized by its plate
  if (preview) a.preview = true;
  return a;
}

/** The image_to_paths call: placement on top of the shared arguments. */
export function createArgs(s) {
  const a = traceArgs(s);
  a.name = s.source.name.replace(/\.[^.]*$/, '') || 'trace';
  a.intent = `Trace ${s.source.name}`;
  if (s.plane !== 'XY') a.plane = s.plane;
  if (s.center) a.center = true;
  return a;
}

/** Human lines for a vectorize_image result. */
export function summary(r) {
  if (!r) return [];
  const u = r.units === 'mm' ? ' mm' : ' px';
  const out = [];
  if (r.mode === 'filled_regions') out.push(`${r.regions} region${r.regions === 1 ? '' : 's'}${r.holes ? `, ${r.holes} hole${r.holes === 1 ? '' : 's'}` : ''}`);
  else if (r.mode === 'outline') out.push(`${r.outlines} outline${r.outlines === 1 ? '' : 's'}`);
  else {
    out.push(`${r.strokes} stroke${r.strokes === 1 ? '' : 's'}`);
    if (r.median_stroke_width) out.push(`pen width ≈ ${+r.median_stroke_width.toFixed(2)}${u}`);
  }
  if (r.size_mm) out.push(`${+r.size_mm[0].toFixed(1)} × ${+r.size_mm[1].toFixed(1)} mm`);
  out.push(`${r.segments} curve segments`);
  return out;
}

/** Warnings as sentences (the server prefixes them with a code). */
export function warningTexts(r) {
  return (r?.warnings ?? []).map((w) => w.replace(/^[A-Z_]+:\s*/, ''));
}

/** Debounce with a trailing call; `cancel()` drops a pending one. Injectable timer for tests. */
export function debounce(fn, ms, timers = { set: (f, d) => setTimeout(f, d), clear: (t) => clearTimeout(t) }) {
  let h = null;
  const d = (...args) => { timers.clear(h); h = timers.set(() => { h = null; fn(...args); }, ms); };
  d.cancel = () => { timers.clear(h); h = null; };
  d.pending = () => h !== null;
  return d;
}

/** Keeps only the latest of overlapping async calls: older results are discarded (an outdated
 *  preview must never overwrite a newer one). */
export function latestOnly() {
  let n = 0;
  return async (fn) => {
    const mine = ++n;
    const v = await fn();
    return mine === n ? { value: v, current: true } : { current: false };
  };
}

/** The create_stamp call: the trace settings plus the plate and relief. */
export function stampArgs(s) {
  const t = s.stamp;
  const a = traceArgs({ ...s, widthMm: null });
  a.name = s.source.name.replace(/\.[^.]*$/, '') || 'stamp';
  a.intent = `Stamp from ${s.source.name}`;
  if (t.shape === 'circle') { a.shape = 'circle'; a.diameter = t.diameter; } else { a.width = t.width; a.height = t.height; }
  a.base_thickness = t.thickness;
  a.relief_height = t.relief;
  a.margin = t.margin;
  if (s.mode === 'centerline') a.stroke_width = t.strokeWidth;
  a.mirror = !!t.mirror;
  if (t.rounded) a.rounded_edges = true;
  if (t.handle) { a.handle_enabled = true; a.handle_height = t.handleHeight; }
  if (t.shrinkage !== null && t.shrinkage !== '' && t.shrinkage > 0) a.shrinkage_percent = t.shrinkage;
  delete a.preview;
  return a;
}

/** The call the Create button makes for the current target. */
export function createCall(s) {
  return s.target === 'stamp' ? { name: 'create_stamp', args: stampArgs(s) } : { name: 'image_to_paths', args: createArgs(s) };
}

/** CSS for the plate preview: its aspect, and how the design sits on it. The physical stamp shows
 *  the relief as built (mirrored when mirror is on); the impression is the mirror of that. */
export function plateView(t, view, designAspect) {
  const w = t.shape === 'circle' ? t.diameter : t.width, h = t.shape === 'circle' ? t.diameter : t.height;
  if (!(w > 0) || !(h > 0)) return null;
  const flip = view === 'physical' ? !!t.mirror : !t.mirror;
  const availW = t.shape === 'circle' ? (w - 2 * t.margin) / Math.SQRT2 : w - 2 * t.margin;
  const availH = t.shape === 'circle' ? (h - 2 * t.margin) / Math.SQRT2 : h - 2 * t.margin;
  const dw = Math.min(availW, availH / (designAspect || 1)), dh = dw * (designAspect || 1);
  return {
    aspect: w / h, round: t.shape === 'circle', flip,
    designWidthPct: Math.max(0, (dw / w) * 100), designHeightPct: Math.max(0, (dh / h) * 100),
  };
}
