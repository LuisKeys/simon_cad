// The interactive path editor: draw a path point by point, or edit one (drag points and Bezier
// handles, insert and delete points, close / reopen, change interpolation, set width / height /
// twist per point). It is a modal tool in the style of the Move / Rotate controller:
//
//   pointer / keys -> PathSession (path-editor-model.js, pure) -> preview in the viewport
//                                         \-> one create_path / update_path call per gesture
//
// While a point is dragged only the viewport changes: the working curve and a reduced-resolution
// tube of the sweeps that use the path are redrawn locally (path-model.js, the same math as the
// kernel). Releasing the point sends a single update_path; the server rebuilds the real solids
// and the scene delta that follows replaces the preview.
import * as THREE from 'three';
import { PathSession, DRAG } from './path-editor-model.js';
import * as P from './path-model.js';
import { snapContext } from './snap.js';
import { dragPoint } from './transform-model.js';
import { snapMove } from './snap.js';
import { h } from './api.js';

const PICK_PX = 12;
const COLOR = { curve: 0xff8a1f, point: 0xffffff, sel: 0xff8a1f, handle: 0x66bb6a, gizmo: 0x42a5f5, rubber: 0xffffff, tube: 0x35d0c8 };

export class PathEditorController {
  /**
   * hooks: workspace() settings, commit({name,args}) -> Promise<boolean>, notify(msg, kind),
   * onModal(on), preview(pathId) -> { radius, up } | null (how the sweeps using the path look).
   */
  constructor(vp, container, hooks) {
    this.vp = vp;
    this.hooks = hooks;
    this.session = null;
    this.busy = false;
    this.group = new THREE.Group();
    this.group.renderOrder = 40;
    vp.scene.add(this.group);
    this.panel = h('div', { class: 'path-hud', hidden: true, role: 'dialog', 'aria-label': 'Path editor' });
    container.appendChild(this.panel);
    this.mouse = null;
    this._onMove = (e) => this._pointerMove(e);
    this._onDown = (e) => this._pointerDown(e);
    this._onUp = (e) => this._pointerUp(e);
    this._onKey = (e) => this._key(e);
    this._onDbl = (e) => this._dblClick(e);
    window.addEventListener('pointermove', (e) => { this.mouse = { x: e.clientX, y: e.clientY }; }, true);
  }

  get active() { return !!this.session; }
  get path() { return this.session?.path ?? null; }

  // ---- lifecycle ---------------------------------------------------------------------------

  /** Starts drawing a new path (`type`), or editing path `id` whose data arrives as `path`. */
  begin(kind, { id = null, path = null, type = 'catmull_rom' } = {}) {
    if (this.session) this.end();
    const grid = this.hooks.workspace()?.grid?.plane ?? 'XY';
    this.session = new PathSession(kind, { id, path, gridPlane: grid, type });
    this.pane = this.vp.layout.active;
    this.vp.setPathHidden(id);
    window.addEventListener('pointermove', this._onMove, true);
    window.addEventListener('pointerdown', this._onDown, true);
    window.addEventListener('pointerup', this._onUp, true);
    window.addEventListener('keydown', this._onKey, true);
    window.addEventListener('dblclick', this._onDbl, true);
    for (const p of this.vp.layout.panes) p.surface.style.cursor = 'crosshair';
    this.hooks.onModal?.(true);
    this._renderPanel();
    this._redraw();
    return true;
  }

  end() {
    if (!this.session) return;
    this.session = null;
    window.removeEventListener('pointermove', this._onMove, true);
    window.removeEventListener('pointerdown', this._onDown, true);
    window.removeEventListener('pointerup', this._onUp, true);
    window.removeEventListener('keydown', this._onKey, true);
    window.removeEventListener('dblclick', this._onDbl, true);
    for (const p of this.vp.layout.panes) { p.surface.style.cursor = ''; p.controls.enabled = true; }
    this.vp.setPathHidden(null);
    this._clear();
    this.panel.hidden = true;
    this.hooks.onModal?.(false);
    this.vp.dirty();
  }

  /** The scene changed (a commit, undo, or the agent): follow the server's version of the path. */
  sync(paths) {
    const s = this.session;
    if (!s || s.drawing) return;
    const cur = paths.find((p) => p.id === s.id);
    if (!cur) { this.hooks.notify?.(`Path ${s.id} no longer exists`, 'bad'); this.end(); return; }
    s.adopt(viewToPath(cur));
    this._renderPanel();
    this._redraw();
  }

  // ---- geometry helpers --------------------------------------------------------------------

  _pane(e) { return this.vp.layout.paneAt(e.clientX, e.clientY); }

  _ray(e, pane = this._pane(e)) {
    const n = this.vp._ndc(e, pane);
    this.vp.ray.setFromCamera({ x: n.x, y: n.y }, pane.camera);
    const o = this.vp.ray.ray.origin, d = this.vp.ray.ray.direction;
    return { origin: [o.x, o.y, o.z], dir: [d.x, d.y, d.z] };
  }

  _screen(p, pane) {
    const r = pane.surface.getBoundingClientRect();
    const v = new THREE.Vector3(...p).project(pane.camera);
    return { x: r.left + ((v.x + 1) / 2) * r.width, y: r.top + ((1 - v.y) / 2) * r.height, z: v.z };
  }

  _inViewport(e) { return this.vp.canvas.parentElement.contains(e.target) && !this.panel.contains(e.target); }

  /** Where a click lands while drawing: on the grid plane through the last point. */
  _planePoint(e) {
    const s = this.session, pane = this._pane(e);
    const ws = this.hooks.workspace();
    const last = s.path.points[s.path.points.length - 1];
    const anchor = last ? last.p : (ws?.grid?.origin ?? [0, 0, 0]);
    const snap = snapContext(ws, { bypass: e.ctrlKey || e.metaKey });
    const vertical = e.shiftKey && !!last;
    const c = vertical ? { kind: 'axis', axis: s.normal } : { kind: 'plane', normal: s.normal };
    const hit = dragPoint(this._ray(e, pane), anchor, c);
    if (!hit) return null;
    const free = vertical ? [0, 1, 2].map((i) => i === s.normal) : [0, 1, 2].map((i) => i !== s.normal);
    const d = snapMove(anchor, hit.map((v, i) => v - anchor[i]), free, snap).delta;
    return anchor.map((v, i) => v + d[i]);
  }

  /** Frames of the selected point for the gizmos. */
  _gizmo() {
    const s = this.session;
    if (!s || s.drawing || s.sel < 0 || s.path.points.length < 2) return null;
    const pa = P.normalizePath(s.path), cv = new P.Curve(pa);
    if (!(cv.length > 1e-9)) return null;
    const starts = cv.starts, L = cv.length;
    const sAt = s.sel < starts.length ? starts[s.sel] : L;
    const n = 96, pts = [], tans = [];
    for (let i = 0; i < n; i++) { const e = cv.at(i / (n - 1)); pts.push(e.p); tans.push(e.t); }
    const idx = Math.min(n - 1, Math.round((sAt / L) * (n - 1)));
    const info = this.hooks.preview?.(s.id);
    const radius = info?.radius ?? Math.max(L / 25, 0.5);
    const up = info?.up ?? [0, 0, 0];
    const base = P.frames(pts, tans, false, [], up)[idx];
    const full = P.frames(pts, tans, false, new Array(n).fill(0).map((_, i) => P.keyedAt(pa, cv, (i / (n - 1)) * L)[2]), up)[idx];
    const [w, hh] = P.scaleOf(pa.points[s.sel]);
    return { p: pa.points[s.sel].p, t: full.t, n: full.n, b: full.b, base: base.n, radius, w, h: hh };
  }

  // ---- pointer -----------------------------------------------------------------------------

  /** The draggable thing under the pointer: gizmo handles, then Bezier handles, then points. */
  _hit(e) {
    const s = this.session, pane = this._pane(e);
    let best = null;
    const consider = (kind, index, pos, extra = {}) => {
      const sp = this._screen(pos, pane);
      if (sp.z > 1) return;
      const d = Math.hypot(e.clientX - sp.x, e.clientY - sp.y);
      if (d < PICK_PX && (!best || d < best.d - 1e-9)) best = { kind, index, d, ...extra };
    };
    const g = this._gizmo();
    if (g) {
      consider(DRAG.WIDTH, s.sel, g.p.map((v, i) => v + g.n[i] * g.w * g.radius), { gizmo: g });
      consider(DRAG.HEIGHT, s.sel, g.p.map((v, i) => v + g.b[i] * g.h * g.radius), { gizmo: g });
      consider(DRAG.TWIST, s.sel, this._twistHandlePos(g), { gizmo: g });
    }
    s.path.points.forEach((pt, i) => {
      if (pt.in) consider(DRAG.HANDLE, i, pt.in, { which: 'in' });
      if (pt.out) consider(DRAG.HANDLE, i, pt.out, { which: 'out' });
    });
    s.path.points.forEach((pt, i) => consider(DRAG.POINT, i, pt.p));
    return best;
  }

  _twistHandlePos(g) {
    const tw = this.session.path.points[this.session.sel].twist ?? 0;
    const a = tw * Math.PI / 180, r = g.radius * 1.6;
    const bb = [g.t[1] * g.base[2] - g.t[2] * g.base[1], g.t[2] * g.base[0] - g.t[0] * g.base[2], g.t[0] * g.base[1] - g.t[1] * g.base[0]];
    return g.p.map((v, i) => v + (g.base[i] * Math.cos(a) + bb[i] * Math.sin(a)) * r);
  }

  _pointerDown(e) {
    const s = this.session;
    if (!s || e.button !== 0 || !this._inViewport(e)) return;
    this.down = { x: e.clientX, y: e.clientY };
    if (s.drawing) return; // a click without a drag adds a point on release
    const hit = this._hit(e);
    if (!hit) { this.pickedPoint = null; return; }
    e.stopPropagation(); e.preventDefault();
    if (s.beginDrag(hit.kind, hit.index, { which: hit.which })) {
      this.dragGizmo = hit.gizmo ?? null;
      for (const p of this.vp.layout.panes) p.controls.enabled = false;
      this._redraw();
      this._renderPanel();
    }
  }

  _pointerMove(e) {
    const s = this.session;
    if (!s) return;
    this.hover = e;
    if (s.dragging) {
      const ray = this._ray(e, this.pane);
      const snap = snapContext(this.hooks.workspace(), { bypass: e.ctrlKey || e.metaKey });
      s.dragTo(ray, snap, { vertical: e.shiftKey, smooth: !e.altKey, gizmo: this.dragGizmo });
      this._redraw();
      return;
    }
    if (s.drawing) { this.cursor = this._inViewport(e) ? this._planePoint(e) : null; this._redraw(); return; }
    if (this._inViewport(e)) {
      const hit = this._hit(e);
      for (const p of this.vp.layout.panes) p.surface.style.cursor = hit ? 'grab' : 'crosshair';
    }
  }

  async _pointerUp(e) {
    const s = this.session;
    if (!s || e.button !== 0) return;
    if (s.dragging) {
      this.pane = this._pane(e);
      const cmd = s.endDrag();
      this.dragGizmo = null;
      for (const p of this.vp.layout.panes) p.controls.enabled = true;
      e.stopPropagation();
      if (cmd) await this._commit(cmd); else { this._redraw(); this._renderPanel(); }
      return;
    }
    if (!this.down || !this._inViewport(e)) return;
    const moved = Math.hypot(e.clientX - this.down.x, e.clientY - this.down.y);
    this.down = null;
    if (moved > 4) return; // a drag orbited the view
    this.pane = this._pane(e);
    if (s.drawing) {
      const pos = this._planePoint(e);
      if (pos && s.addPoint(pos)) { this._renderPanel(); this._redraw(); }
      e.stopPropagation();
    } else {
      const hit = this._hit(e);
      if (hit && hit.kind === DRAG.POINT) { s.select(hit.index); this._renderPanel(); this._redraw(); }
    }
  }

  _dblClick(e) {
    const s = this.session;
    if (!s || s.drawing || !this._inViewport(e) || this.busy) return;
    const pane = this._pane(e), pa = P.normalizePath(s.path);
    let best = null;
    P.segments(pa).forEach((seg, si) => {
      const steps = 40;
      for (let k = 0; k <= steps; k++) {
        const q = P.segAt(seg, k / steps), sp = this._screen(q, pane);
        const d = Math.hypot(e.clientX - sp.x, e.clientY - sp.y);
        if (d < PICK_PX && (!best || d < best.d)) best = { d, seg: si, u: k / steps };
      }
    });
    if (!best || best.u <= 0.02 || best.u >= 0.98) return; // on an end point: that is a click, not an insert
    e.stopPropagation(); e.preventDefault();
    this._commit(s.insert(best.seg, best.u));
  }

  _key(e) {
    const s = this.session;
    if (!s) return;
    const typing = /^(input|select|textarea)$/i.test(e.target?.tagName ?? '');
    if (e.key === 'Escape') {
      e.preventDefault(); e.stopPropagation();
      if (s.dragging) { s.cancelDrag(); this.dragGizmo = null; for (const p of this.vp.layout.panes) p.controls.enabled = true; this._redraw(); } else this.end();
      return;
    }
    if (typing) return;
    if (e.key === 'Enter') { e.preventDefault(); e.stopPropagation(); if (s.drawing) this.finish(); else this.end(); return; }
    if (e.key === 'Delete' || e.key === 'Backspace') {
      e.preventDefault(); e.stopPropagation();
      if (s.drawing) { s.popPoint(); this._renderPanel(); this._redraw(); } else if (s.sel >= 0) this._commit(s.remove());
      return;
    }
    if (e.key === 'c' && !e.ctrlKey && !e.metaKey) { e.preventDefault(); e.stopPropagation(); this.toggleClosed(); return; }
    const t = P.TYPES[Number(e.key) - 1];
    if (t && !e.ctrlKey && !e.metaKey) { e.preventDefault(); e.stopPropagation(); this.setType(t); }
  }

  // ---- commands ----------------------------------------------------------------------------

  async _commit(cmd) {
    const s = this.session;
    if (!cmd) { this._warn(); this._renderPanel(); this._redraw(); return false; }
    if (this.busy) return false;
    this.busy = true;
    try {
      const ok = await this.hooks.commit(cmd);
      if (!ok && s === this.session && s) { s.path = P.clonePath(s.committed); this._redraw(); }
      else if (ok && s === this.session && s) s.committed_();
      return ok;
    } finally { this.busy = false; this._renderPanel(); }
  }

  _warn() { if (this.session?.error) this.hooks.notify?.(this.session.error, 'bad'); }

  async finish() {
    const s = this.session;
    const cmd = s?.finishCommand();
    if (!cmd) { this.hooks.notify?.(`Add at least ${s?.minPoints ?? 2} points first`, 'bad'); return; }
    this.busy = true;
    try {
      const ok = await this.hooks.commit(cmd);
      if (ok) this.end();
    } finally { this.busy = false; }
  }

  toggleClosed() {
    const s = this.session;
    if (!s) return;
    const cmd = s.setClosed(!s.path.closed);
    if (s.error) this._warn();
    this._renderPanel(); this._redraw();
    if (cmd) this._commit(cmd);
  }

  setType(type) {
    const s = this.session;
    if (!s || s.path.type === type) return;
    const cmd = s.setType(type);
    this._renderPanel(); this._redraw();
    if (cmd) this._commit(cmd);
  }

  // ---- drawing the overlay -----------------------------------------------------------------

  _clear() {
    for (const c of [...this.group.children]) { this.group.remove(c); c.geometry?.dispose(); c.material?.dispose(); }
  }

  _redraw() {
    const s = this.session;
    this._clear();
    if (!s) return;
    const add = (o) => { o.renderOrder = 40; this.group.add(o); return o; };
    const lineMat = (color, extra = {}) => new THREE.LineBasicMaterial({ color, depthTest: false, ...extra });
    const pa = P.normalizePath(s.path);
    // live curve
    if (pa.points.length >= 2) {
      const pts = P.flatten(pa, Math.max(0.02, new P.Curve(pa).length / 1500));
      const v = pts.map((p) => new THREE.Vector3(...p));
      if (pa.closed && v.length) v.push(v[0].clone());
      add(new THREE.Line(new THREE.BufferGeometry().setFromPoints(v), lineMat(COLOR.curve)));
    }
    // rubber band to the cursor while drawing
    if (s.drawing && this.cursor && pa.points.length) {
      const last = pa.points[pa.points.length - 1].p;
      add(new THREE.Line(new THREE.BufferGeometry().setFromPoints([new THREE.Vector3(...last), new THREE.Vector3(...this.cursor)]),
        lineMat(COLOR.rubber, { transparent: true, opacity: 0.5 })));
      add(this._points([this.cursor], COLOR.rubber, 7));
    }
    // swept preview of the solids that use this path
    if (!s.drawing) {
      const info = this.hooks.preview?.(s.id);
      if (info) {
        const m = P.previewTube(pa, { radius: info.radius, up: info.up ?? [0, 0, 0], sides: 14, samples: 56 });
        if (m) {
          const g = new THREE.BufferGeometry();
          g.setAttribute('position', new THREE.BufferAttribute(Float32Array.from(m.positions), 3));
          g.setAttribute('normal', new THREE.BufferAttribute(Float32Array.from(m.normals), 3));
          g.setIndex(m.indices);
          add(new THREE.Mesh(g, new THREE.MeshStandardMaterial({ color: COLOR.tube, transparent: true, opacity: 0.4, roughness: 0.6, metalness: 0.1, depthWrite: false, side: THREE.DoubleSide })));
        }
      }
    }
    // control points, handles, gizmos
    pa.points.forEach((pt, i) => {
      add(this._points([pt.p], i === s.sel ? COLOR.sel : COLOR.point, i === s.sel ? 11 : 8));
      for (const w of ['in', 'out']) {
        if (!pt[w]) continue;
        add(new THREE.Line(new THREE.BufferGeometry().setFromPoints([new THREE.Vector3(...pt.p), new THREE.Vector3(...pt[w])]), lineMat(COLOR.handle, { transparent: true, opacity: 0.8 })));
        add(this._points([pt[w]], COLOR.handle, 7));
      }
    });
    const g = this._gizmo();
    if (g) {
      const wp = g.p.map((v, i) => v + g.n[i] * g.w * g.radius), hp = g.p.map((v, i) => v + g.b[i] * g.h * g.radius);
      for (const [pos] of [[wp], [hp]]) add(new THREE.Line(new THREE.BufferGeometry().setFromPoints([new THREE.Vector3(...g.p), new THREE.Vector3(...pos)]), lineMat(COLOR.gizmo, { transparent: true, opacity: 0.7 })));
      add(this._points([wp, hp], COLOR.gizmo, 9));
      // twist ring around the tangent, with its handle
      const ring = [], r = g.radius * 1.6;
      const bb = [g.t[1] * g.base[2] - g.t[2] * g.base[1], g.t[2] * g.base[0] - g.t[0] * g.base[2], g.t[0] * g.base[1] - g.t[1] * g.base[0]];
      for (let k = 0; k <= 48; k++) {
        const a = (k / 48) * 2 * Math.PI;
        ring.push(new THREE.Vector3(...g.p.map((v, i) => v + (g.base[i] * Math.cos(a) + bb[i] * Math.sin(a)) * r)));
      }
      add(new THREE.Line(new THREE.BufferGeometry().setFromPoints(ring), lineMat(COLOR.gizmo, { transparent: true, opacity: 0.35 })));
      add(this._points([this._twistHandlePos(g)], COLOR.gizmo, 9));
    }
    this.vp.dirty();
  }

  _points(list, color, size) {
    const g = new THREE.BufferGeometry().setFromPoints(list.map((p) => new THREE.Vector3(...p)));
    return new THREE.Points(g, new THREE.PointsMaterial({ color, size, sizeAttenuation: false, depthTest: false }));
  }

  // ---- panel -------------------------------------------------------------------------------

  _renderPanel() {
    // the focused input is replaced and its blur can fire a change that renders again: no nesting
    if (this._rendering) { this._renderAgain = true; return; }
    this._rendering = true;
    try { this._paintPanel(); } finally { this._rendering = false; }
    if (this._renderAgain) { this._renderAgain = false; this._renderPanel(); }
  }

  _paintPanel() {
    const s = this.session;
    if (!s) return;
    this.panel.hidden = false;
    const pa = s.path;
    const row = (label, ...kids) => h('label', { class: 'path-row' }, h('span', null, label), ...kids);
    const num = (value, onChange, { step = 'any', min } = {}) => h('input', {
      type: 'number', step, min, value: Number.isFinite(value) ? +value.toFixed(4) : '',
      onchange: (ev) => { const v = parseFloat(ev.target.value); if (Number.isFinite(v)) onChange(v); },
      onkeydown: (ev) => ev.stopPropagation() });
    const typeSel = h('select', { 'aria-label': 'Interpolation', onchange: (ev) => this.setType(ev.target.value) },
      ...P.TYPES.map((t, i) => h('option', { value: t, selected: t === pa.type }, `${i + 1} · ${P.TYPE_LABELS[t]}`)));
    const closed = h('input', { type: 'checkbox', checked: pa.closed, onchange: () => this.toggleClosed() });
    const sel = !s.drawing && s.sel >= 0 ? pa.points[s.sel] : null;
    const point = sel ? h('div', { class: 'path-point' },
      h('div', { class: 'path-sub' }, `Point ${s.sel + 1} of ${pa.points.length}`),
      h('div', { class: 'path-xyz' }, ...['X', 'Y', 'Z'].map((ax, k) => row(ax, num(sel.p[k], (v) => { const p = [...sel.p]; p[k] = v; this._commit(s.setPointPosition(s.sel, p)); })))),
      h('div', { class: 'path-xyz' },
        row('Width ×', num(sel.w ?? 1, (v) => this._commit(s.setPointProp(s.sel, 'w', v)), { step: 0.1, min: 0.01 })),
        row('Height ×', num(sel.h ?? 1, (v) => this._commit(s.setPointProp(s.sel, 'h', v)), { step: 0.1, min: 0.01 })),
        row('Twist °', num(sel.twist ?? 0, (v) => this._commit(s.setPointProp(s.sel, 'twist', v)), { step: 5 }))),
      h('div', { class: 'path-actions' },
        h('button', { type: 'button', onclick: () => this._commit(s.remove()), title: 'Delete the selected point (Del)' }, 'Delete point'))) : null;
    const hint = s.drawing
      ? 'Click to add points · Shift: vertical · Ctrl: no snap · Backspace: undo point · Enter: finish · Esc: cancel'
      : 'Drag points and handles · double-click the curve to insert · Del: delete · C: close/open · 1–4: type · Esc: done';
    this.panel.replaceChildren(
      h('div', { class: 'path-title' }, s.drawing ? 'Draw path' : `Edit path ${s.id}`,
        h('span', { class: 'path-count' }, `${pa.points.length} point${pa.points.length === 1 ? '' : 's'}`)),
      h('div', { class: 'path-row-line' }, row('Type', typeSel), row('Closed', closed)),
      point,
      s.error ? h('div', { class: 'path-error', role: 'alert' }, s.error) : null,
      h('div', { class: 'path-hint' }, hint),
      h('div', { class: 'path-actions' },
        s.drawing ? h('button', { type: 'button', class: 'primary', disabled: !s.canFinish || this.busy, onclick: () => this.finish() }, 'Create path') : null,
        h('button', { type: 'button', onclick: () => this.end() }, s.drawing ? 'Cancel' : 'Done')));
  }
}

/** A rendering.PathLines (the scene delta's path) as a path-model path. */
export function viewToPath(view) {
  return P.normalizePath({ type: view.type, closed: view.closed, points: view.points.map(fromView) });
}

/** A rendering.PathPointView back to the path-model point shape. */
function fromView(v) {
  const pt = { p: [...v.p] };
  if (v.in) pt.in = [...v.in];
  if (v.out) pt.out = [...v.out];
  if (v.w) pt.w = v.w;
  if (v.h) pt.h = v.h;
  if (v.twist) pt.twist = v.twist;
  return pt;
}
