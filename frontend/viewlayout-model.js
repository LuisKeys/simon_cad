// Viewport layout model: the orientations a view can take, the per-view configuration
// (ViewportConfig), the Single/Quad layout state, pane rectangles and the persisted form.
// Pure data and functions (no DOM, no Three.js), so it is unit-tested on its own.
//
// SimonCAD's model coordinates are Z up. Directions point from the target toward the
// camera, the same convention as Viewport.viewFrom and the view cube.

export const ORIENTATIONS = {
  perspective: { label: 'Perspective', projection: 'perspective', dir: null, up: [0, 0, 1], grid: 'XY' },
  top: { label: 'Top', projection: 'orthographic', dir: [0, 0, 1], up: [0, 1, 0], grid: 'XY' },
  bottom: { label: 'Bottom', projection: 'orthographic', dir: [0, 0, -1], up: [0, 1, 0], grid: 'XY' },
  front: { label: 'Front', projection: 'orthographic', dir: [0, -1, 0], up: [0, 0, 1], grid: 'XZ' },
  back: { label: 'Back', projection: 'orthographic', dir: [0, 1, 0], up: [0, 0, 1], grid: 'XZ' },
  left: { label: 'Left', projection: 'orthographic', dir: [-1, 0, 0], up: [0, 0, 1], grid: 'YZ' },
  right: { label: 'Right', projection: 'orthographic', dir: [1, 0, 0], up: [0, 0, 1], grid: 'YZ' },
};
export const ORIENTATION_IDS = Object.keys(ORIENTATIONS);

/** The quad arrangement: TOP | PERSPECTIVE over FRONT | RIGHT. Single view shows the active one. */
export const QUAD_DEFAULT = ['top', 'perspective', 'front', 'right'];
export const PERSPECTIVE_PANE = 1;
export const SPLIT_LIMITS = [0.15, 0.85];

/** The orthographic orientation that looks along `dir`, or '' when `dir` is not a main axis. */
export function orientationForDir(dir) {
  const n = Math.hypot(...dir);
  if (!n) return '';
  for (const id of ORIENTATION_IDS) {
    const o = ORIENTATIONS[id];
    if (o.dir && o.dir.every((c, i) => Math.abs(c - dir[i] / n) < 1e-6)) return id;
  }
  return '';
}

/** A fresh ViewportConfig for pane `index` showing `orientation`. */
export function viewportConfig(index, orientation = QUAD_DEFAULT[index]) {
  const o = ORIENTATIONS[orientation] ?? ORIENTATIONS.perspective;
  const id = ORIENTATIONS[orientation] ? orientation : 'perspective';
  return { id: `vp${index}`, projectionType: o.projection, orientation: id, cameraState: null, gridPlane: o.grid, showAxisGizmo: true };
}

/** Returns `cfg` switched to `orientation` (projection and grid plane follow; the camera state is kept). */
export function withOrientation(cfg, orientation) {
  const o = ORIENTATIONS[orientation];
  if (!o) return cfg;
  return { ...cfg, orientation, projectionType: o.projection, gridPlane: o.grid };
}

export function defaultLayout() {
  return { v: 1, mode: 'single', active: PERSPECTIVE_PANE, maximized: -1, split: { x: 0.5, y: 0.5 },
    views: QUAD_DEFAULT.map((o, i) => viewportConfig(i, o)) };
}

const clamp = (v, lo, hi) => Math.max(lo, Math.min(hi, v));
const finite = (v) => typeof v === 'number' && Number.isFinite(v);
const vec3 = (v) => Array.isArray(v) && v.length === 3 && v.every(finite);

function cameraState(s) {
  if (!s || typeof s !== 'object' || !vec3(s.position) || !vec3(s.target) || !vec3(s.up)) return null;
  const out = { position: [...s.position], target: [...s.target], up: [...s.up] };
  if (finite(s.zoom) && s.zoom > 0) out.zoom = s.zoom;
  if (finite(s.height) && s.height > 0) out.height = s.height;
  if (s.lastPerspective) { const p = cameraState(s.lastPerspective); if (p) out.lastPerspective = p; }
  return out;
}

/**
 * Validates a stored layout (from localStorage, possibly old, partial or corrupt) and fills
 * in defaults. Never throws; anything unusable falls back to the default.
 */
export function normalizeLayout(saved) {
  const d = defaultLayout();
  if (!saved || typeof saved !== 'object') return d;
  const mode = saved.mode === 'quad' ? 'quad' : 'single';
  const idx = (v, dflt) => (Number.isInteger(v) && v >= 0 && v < 4 ? v : dflt);
  const split = {
    x: finite(saved.split?.x) ? clamp(saved.split.x, ...SPLIT_LIMITS) : 0.5,
    y: finite(saved.split?.y) ? clamp(saved.split.y, ...SPLIT_LIMITS) : 0.5,
  };
  const views = d.views.map((def, i) => {
    const s = Array.isArray(saved.views) ? saved.views[i] : null;
    if (!s || typeof s !== 'object') return def;
    const cfg = withOrientation(def, ORIENTATIONS[s.orientation] ? s.orientation : def.orientation);
    return { ...cfg, cameraState: cameraState(s.cameraState), showAxisGizmo: s.showAxisGizmo !== false };
  });
  const maximized = mode === 'quad' && Number.isInteger(saved.maximized) ? idx(saved.maximized, -1) : -1;
  return { v: 1, mode, active: idx(saved.active, d.active), maximized, split, views };
}

/** Panes shown for a layout state, in drawing order. */
export function visiblePanes(st) {
  if (st.mode === 'single') return [st.active];
  if (st.maximized >= 0) return [st.maximized];
  return [0, 1, 2, 3];
}

/**
 * Pixel rectangles {x, y, w, h} (top-left origin, CSS px) of each pane in a W×H area;
 * hidden panes get null. Quad panes share the split lines exactly, so they tile the area.
 */
export function paneRects(st, W, H) {
  const rects = [null, null, null, null];
  const shown = visiblePanes(st);
  if (shown.length === 1) { rects[shown[0]] = { x: 0, y: 0, w: W, h: H }; return rects; }
  const sx = Math.round(W * st.split.x), sy = Math.round(H * st.split.y);
  rects[0] = { x: 0, y: 0, w: sx, h: sy };
  rects[1] = { x: sx, y: 0, w: W - sx, h: sy };
  rects[2] = { x: 0, y: sy, w: sx, h: H - sy };
  rects[3] = { x: sx, y: sy, w: W - sx, h: H - sy };
  return rects;
}

/** Index of the pane containing point (px, py) (CSS px, relative to the area), or -1. */
export function paneAt(rects, px, py) {
  return rects.findIndex((r) => r && px >= r.x && px < r.x + r.w && py >= r.y && py < r.y + r.h);
}

/** Toggling the layout: Single ↔ Quad. Leaving Quad drops a temporary maximize. */
export function toggledMode(st) {
  return st.mode === 'quad' ? { ...st, mode: 'single', maximized: -1 } : { ...st, mode: 'quad', maximized: -1 };
}

/** Double-click on a pane label: maximize it in Quad, or restore the quad. A no-op in Single. */
export function toggledMaximize(st, i) {
  if (st.mode !== 'quad') return st;
  return { ...st, maximized: st.maximized === i ? -1 : i, active: i };
}
