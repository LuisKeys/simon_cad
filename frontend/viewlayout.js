// ViewportLayoutManager: Single / Quad viewport layouts over ONE renderer and ONE scene.
// Each pane is a transparent DOM overlay with its own camera (perspective and orthographic),
// OrbitControls, axis gizmo, view-cube hit area and orientation label; the Viewport draws
// every visible pane into its rectangle of the shared canvas (scissor), so no geometry,
// material or project state is ever duplicated. Layout rules and the persisted form live in
// viewlayout-model.js.
import * as THREE from 'three';
import { OrbitControls } from 'three/addons/OrbitControls.js';
import { AxisGizmo, ISO_VIEW } from './axes.js';
import { icon } from './icons.js';
import {
  ORIENTATIONS, SPLIT_LIMITS, normalizeLayout, paneRects, paneAt, visiblePanes,
  toggledMode, toggledMaximize, withOrientation, orientationForDir,
} from './viewlayout-model.js';

const VIEW_ANIM_MS = 350;
const CUBE_PX = { single: 96, quad: 72 };
const reducedMotion = () => !!globalThis.matchMedia?.('(prefers-reduced-motion: reduce)').matches;
const v3 = (a) => new THREE.Vector3(...a);
const arr = (v) => [v.x, v.y, v.z];

/** One view: cameras, controls and its overlay elements. */
class ViewPane {
  constructor(index, config, mgr) {
    this.index = index;
    this.mgr = mgr;
    this.config = config;
    this.rect = null;
    this.anim = null;
    this.restored = false;          // camera came from a saved state: the first auto-fit leaves it alone
    this.lastPerspective = null;    // the perspective view to return to after an orthographic one

    this.el = document.createElement('div');
    this.el.className = 'vp-pane';
    this.el.dataset.pane = String(index);
    this.surface = document.createElement('div');
    this.surface.className = 'vp-surface';
    this.label = document.createElement('button');
    this.label.type = 'button';
    this.label.className = 'vp-label';
    this.label.setAttribute('aria-haspopup', 'menu');
    this.labelText = document.createElement('span');
    this.label.append(this.labelText, icon('down'));
    this.label.lastChild.classList.add('caret');
    this.cubeEl = document.createElement('div');
    this.cubeEl.className = 'vp-cube';
    this.cubeEl.title = 'View cube';
    this.el.append(this.surface, this.label, this.cubeEl);
    mgr.container.querySelector('.vp-panes').append(this.el);
    this.gizmo = new AxisGizmo(this.el, { onView: (dir) => mgr.viewFrom(this.index, dir) });

    this.persp = new THREE.PerspectiveCamera(40, 1, 0.1, 100000);
    this.persp.up.set(0, 0, 1);
    this.persp.position.set(120, -160, 110);
    this.ortho = new THREE.OrthographicCamera(-1, 1, 1, -1, 0.1, 100000);
    this.ortho.up.set(0, 0, 1);
    this.orthoH = 100;              // half the visible height at zoom 1, model units
    this.controls = new OrbitControls(this.persp, this.surface);
    this.controls.enableDamping = false;
    this.controls.addEventListener('change', () => mgr.dirty());
    this.controls.addEventListener('start', () => { this.anim = null; }); // the user takes over
    this.controls.addEventListener('end', () => mgr.changed());

    this._useProjection();
    this._syncLabel();
    const o = ORIENTATIONS[config.orientation];
    if (o.dir) this._setView({ target: new THREE.Vector3(), position: v3(o.dir).multiplyScalar(300), up: v3(o.up) });
  }

  get camera() { return this.config.projectionType === 'orthographic' ? this.ortho : this.persp; }
  get perspective() { return this.config.projectionType !== 'orthographic'; }

  /** Points the controls at the camera of the configured projection; orbit only in perspective. */
  _useProjection() {
    const c = this.controls;
    c.object = this.camera;
    const persp = this.perspective;
    c.enableRotate = persp;
    c.zoomToCursor = !persp;
    // Middle drag pans everywhere; right drag pans (as before); left drag orbits in
    // perspective and pans in the orthographic views, whose orientation is fixed.
    c.mouseButtons = { LEFT: persp ? THREE.MOUSE.ROTATE : THREE.MOUSE.PAN, MIDDLE: THREE.MOUSE.PAN, RIGHT: THREE.MOUSE.PAN };
    c.touches = { ONE: persp ? THREE.TOUCH.ROTATE : THREE.TOUCH.PAN, TWO: THREE.TOUCH.DOLLY_PAN };
    this.cubeEl.hidden = !persp;
    this.el.dataset.projection = this.config.projectionType;
  }

  _syncLabel() {
    const name = ORIENTATIONS[this.config.orientation].label;
    this.labelText.textContent = name;
    this.label.title = `${name} view: click to change · double-click to maximize or restore`;
    this.label.setAttribute('aria-label', `${name} view. Change the view`);
    this.el.dataset.orientation = this.config.orientation;
  }

  /** Lays the pane out at `rect` (CSS px in the viewport) and fits the cameras to its aspect. */
  place(rect, mode, quadRect) {
    this.rect = rect;
    this.el.hidden = !rect;
    if (!rect) { // hidden: frame it for the size it will have in Quad
      if (quadRect) { this.aspect = quadRect.w / Math.max(quadRect.h, 1); this.persp.aspect = this.aspect; this.persp.updateProjectionMatrix(); this._orthoFrustum(); }
      return;
    }
    Object.assign(this.el.style, { left: `${rect.x}px`, top: `${rect.y}px`, width: `${rect.w}px`, height: `${rect.h}px` });
    const cube = CUBE_PX[mode === 'quad' ? 'quad' : 'single'];
    this.cubeSize = rect.w > cube * 2.4 && rect.h > cube * 1.8 ? cube : 0; // tiny panes: no cube
    Object.assign(this.cubeEl.style, { width: `${cube}px`, height: `${cube}px` });
    this.cubeEl.classList.toggle('off', !this.cubeSize);
    this.aspect = rect.w / Math.max(rect.h, 1); // kept while hidden, for fits and frustums
    this.persp.aspect = this.aspect;
    this.persp.updateProjectionMatrix();
    this._orthoFrustum();
  }

  _orthoFrustum() {
    const a = this.aspect ?? 1;
    Object.assign(this.ortho, { top: this.orthoH, bottom: -this.orthoH, left: -this.orthoH * a, right: this.orthoH * a });
    this.ortho.updateProjectionMatrix();
  }

  _setView({ target, position, up }) {
    const cam = this.camera;
    this.controls.target.copy(target);
    cam.position.copy(position);
    cam.up.copy(up);
    this.controls.update();
    this.mgr.dirty();
  }

  /** Moves to `end`, animated (slerped orientation) unless told otherwise or reduced motion. */
  animateTo(end, animate = true) {
    if (!animate || reducedMotion() || !this.rect) { this.anim = null; this._setView(end); return; }
    const cam = this.camera;
    const q1 = new THREE.Quaternion().setFromRotationMatrix(new THREE.Matrix4().lookAt(end.position, end.target, end.up));
    this.anim = { t0: performance.now(), q0: cam.quaternion.clone(), q1, from: this.controls.target.clone(),
      d0: cam.position.distanceTo(this.controls.target), d1: end.position.distanceTo(end.target), end };
    this.mgr.dirty();
  }

  /** Advances the view animation; true while it runs. */
  step(now) {
    const a = this.anim;
    if (!a) return false;
    const t = Math.min(1, (now - a.t0) / VIEW_ANIM_MS);
    if (t >= 1) { this.anim = null; this._setView(a.end); this.mgr.changed(); return true; }
    const k = t * t * (3 - 2 * t); // smoothstep
    const q = a.q0.clone().slerp(a.q1, k);
    const cam = this.camera;
    this.controls.target.lerpVectors(a.from, a.end.target, k);
    cam.quaternion.copy(q);
    cam.position.copy(this.controls.target).addScaledVector(new THREE.Vector3(0, 0, 1).applyQuaternion(q), a.d0 + (a.d1 - a.d0) * k);
    return true;
  }

  /** Looks at `box` from the configured orientation (or the current direction in perspective). */
  fit(box) {
    if (box.isEmpty()) return;
    this.anim = null;
    const c = box.getCenter(new THREE.Vector3());
    const size = box.getSize(new THREE.Vector3());
    const r = Math.max(size.length() / 2, 1);
    if (this.perspective) {
      const cam = this.persp;
      const dir = cam.position.clone().sub(this.controls.target).normalize();
      if (!isFinite(dir.x)) dir.set(0.5, -0.7, 0.55).normalize();
      this.controls.target.copy(c);
      cam.position.copy(c).addScaledVector(dir, r / Math.sin((cam.fov * Math.PI) / 360) * 1.1);
      cam.near = Math.max(r / 1000, 0.01); cam.far = r * 200;
      cam.updateProjectionMatrix();
      this.controls.update();
    } else {
      const o = ORIENTATIONS[this.config.orientation];
      const z = v3(o.dir), y = v3(o.up), x = new THREE.Vector3().crossVectors(y, z);
      const halfW = Math.abs(size.dot(x)) / 2, halfH = Math.abs(size.dot(y)) / 2;
      this.orthoH = Math.max(halfH, halfW / (this.aspect ?? 1), 0.5) * 1.15;
      const cam = this.ortho;
      cam.zoom = 1; cam.near = Math.max(r / 1000, 0.01); cam.far = r * 200;
      this._orthoFrustum();
      this._setView({ target: c, position: c.clone().addScaledVector(z, r * 4), up: y });
    }
    this.mgr.dirty();
  }

  /** Visible half-height at the target, in model units, whatever the projection. */
  _halfHeight() {
    if (!this.perspective) return this.orthoH / this.ortho.zoom;
    return this.persp.position.distanceTo(this.controls.target) * Math.tan((this.persp.fov * Math.PI) / 360);
  }

  /** The current camera pose, to hand back with restorePose (print simulation borrows the view). */
  pose() {
    const c = this.camera;
    return { perspective: this.perspective, position: c.position.clone(), target: this.controls.target.clone(), up: c.up.clone(),
      near: c.near, far: c.far, zoom: c.zoom, orthoH: this.orthoH };
  }

  restorePose(p) {
    if (!p || p.perspective !== this.perspective) return false;
    const c = this.camera;
    this.anim = null;
    c.near = p.near; c.far = p.far;
    if (this.perspective) c.updateProjectionMatrix();
    else { this.orthoH = p.orthoH; c.zoom = p.zoom; this._orthoFrustum(); }
    this._setView(p);
    return true;
  }

  _snapshot(cam = this.camera) {
    return { position: arr(cam.position), target: arr(this.controls.target), up: arr(cam.up) };
  }

  /**
   * Switches the orientation. Changing projection keeps what is on screen (same target and
   * visible size) and then turns to the new view, so the change reads as one motion.
   */
  setOrientation(orientation, { animate = true, dir = null } = {}) {
    const o = ORIENTATIONS[orientation];
    if (!o) return;
    const wasPersp = this.perspective;
    const from = this.camera;
    const target = this.controls.target.clone();
    const half = this._halfHeight();
    const q = from.quaternion.clone();
    if (wasPersp && o.projection === 'orthographic') this.lastPerspective = this._snapshot(this.persp);
    this.config = withOrientation(this.config, orientation);
    this._useProjection();
    this._syncLabel();
    const cam = this.camera;
    if (cam !== from) {
      const back = new THREE.Vector3(0, 0, 1).applyQuaternion(q);
      if (cam.isOrthographicCamera) {
        // Same visible size; the camera backs off far enough that nothing is clipped behind it.
        const depth = this.mgr.depth(target);
        this.orthoH = Math.max(half, 0.5);
        cam.zoom = 1; cam.near = depth.near; cam.far = depth.far;
        this._orthoFrustum();
        cam.position.copy(target).addScaledVector(back, Math.max(from.position.distanceTo(target), depth.dist));
      } else {
        cam.position.copy(target).addScaledVector(back, half / Math.tan((cam.fov * Math.PI) / 360));
      }
      cam.quaternion.copy(q);
      cam.up.copy(from.up);
      this.controls.target.copy(target);
    }
    let end;
    if (o.dir) {
      const dist = Math.max(cam.position.distanceTo(target), 1);
      end = { target, position: target.clone().addScaledVector(v3(o.dir), dist), up: v3(o.up) };
    } else if (dir || !this.lastPerspective) {
      // No perspective view to return to: frame the scene from that direction (isometric by default).
      const d = v3(dir ?? ISO_VIEW).normalize();
      const box = this.mgr.hooks.bounds?.();
      let c = target, dist = cam.position.distanceTo(target);
      if (box && !box.isEmpty()) {
        c = box.getCenter(new THREE.Vector3());
        const r = Math.max(box.getSize(new THREE.Vector3()).length() / 2, 1);
        dist = r / Math.sin((cam.fov * Math.PI) / 360) * 1.1;
        cam.near = Math.max(r / 1000, 0.01); cam.far = r * 200;
        cam.updateProjectionMatrix();
      }
      end = { target: c, position: c.clone().addScaledVector(d, dist), up: new THREE.Vector3(0, 0, 1) };
    } else {
      const s = this.lastPerspective;
      end = { target: v3(s.target), position: v3(s.position), up: v3(s.up) };
    }
    this.animateTo(end, animate);
    this.mgr.changed();
  }

  /** The persisted camera state of this pane (ViewportConfig.cameraState). */
  cameraState() {
    const end = this.anim?.end;
    const s = end ? { position: arr(end.position), target: arr(end.target), up: arr(end.up) } : this._snapshot();
    if (!this.perspective) { s.zoom = this.ortho.zoom; s.height = this.orthoH; }
    if (this.lastPerspective && !this.perspective) s.lastPerspective = this.lastPerspective;
    return s;
  }

  restoreCamera(s) {
    if (!s) return;
    this.anim = null;
    const cam = this.camera;
    if (!this.perspective) {
      this.orthoH = s.height ?? this.orthoH;
      cam.zoom = s.zoom ?? 1;
      this._orthoFrustum();
    }
    this.lastPerspective = s.lastPerspective ?? null;
    const target = v3(s.target), position = v3(s.position);
    const r = Math.max(position.distanceTo(target), 1);
    cam.near = Math.max(r / 2000, 0.01); cam.far = Math.max(r * 400, 1000);
    cam.updateProjectionMatrix();
    this._setView({ target, position, up: v3(s.up) });
    this.restored = true;
  }

  setGizmo(prefs) {
    this.gizmo.setOptions({ visible: prefs.showGizmo && this.config.showAxisGizmo !== false,
      size: prefs.gizmoSize, position: prefs.gizmoPosition });
  }
}

export class ViewportLayoutManager {
  /**
   * `container` is the #viewport element. Hooks: dirty() (redraw), onChange(state) (layout or
   * a camera changed: persist), onPaneMenu(index, anchor) (the orientation menu), bounds().
   */
  constructor(container, hooks) {
    this.container = container;
    this.hooks = hooks;
    this.state = normalizeLayout(null);
    this.viewPrefs = { showGizmo: true, gizmoSize: 'medium', gizmoPosition: 'bottom-left' };
    const host = document.createElement('div');
    host.className = 'vp-panes';
    container.insertBefore(host, container.querySelector('#gl')?.nextSibling ?? null);
    this.panes = this.state.views.map((cfg, i) => new ViewPane(i, cfg, this));
    for (const p of this.panes) this._bindPane(p);
    this._initDividers(host);
    new ResizeObserver(() => this.layout()).observe(container);
    this.layout();
  }

  dirty() { this.hooks.dirty?.(); }

  /** Something worth persisting changed (layout, orientation or a camera). */
  changed() { this.hooks.onChange?.(this.serialize()); }

  get active() { return this.panes[this.state.active]; }
  get mode() { return this.state.mode; }
  get maximized() { return this.state.maximized; }
  visible() { return visiblePanes(this.state).map((i) => this.panes[i]); }

  layout() {
    const W = Math.max(1, this.container.clientWidth), H = Math.max(1, this.container.clientHeight);
    this.size = { w: W, h: H };
    const rects = paneRects(this.state, W, H);
    const quadRects = paneRects({ ...this.state, mode: 'quad', maximized: -1 }, W, H);
    const quad = this.state.mode === 'quad' && this.state.maximized < 0;
    this.container.dataset.layout = this.state.mode === 'single' ? 'single' : quad ? 'quad' : 'maximized';
    this.panes.forEach((p, i) => {
      p.place(rects[i], quad ? 'quad' : 'single', quadRects[i]);
      p.el.classList.toggle('active', i === this.state.active);
    });
    const { x, y } = this.state.split;
    this.dividers.v.style.left = `${Math.round(W * x)}px`;
    this.dividers.h.style.top = `${Math.round(H * y)}px`;
    Object.assign(this.dividers.c.style, { left: `${Math.round(W * x)}px`, top: `${Math.round(H * y)}px` });
    for (const d of Object.values(this.dividers)) d.hidden = !quad;
    this.dirty();
  }

  /** The pane under a client point (or the active pane when the point is outside every pane). */
  paneAt(clientX, clientY) {
    const r = this.container.getBoundingClientRect();
    const i = paneAt(this.panes.map((p) => p.rect), clientX - r.left, clientY - r.top);
    return this.panes[i] ?? this.active;
  }

  setActive(i) {
    if (i === this.state.active || !this.panes[i]) return;
    this.state = { ...this.state, active: i };
    this.panes.forEach((p, k) => p.el.classList.toggle('active', k === i));
    this.hooks.onActive?.(this.panes[i]);
    this.changed();
  }

  setMode(mode) {
    if (mode !== 'single' && mode !== 'quad') return;
    if (mode === this.state.mode) { if (this.state.maximized >= 0) this.toggleMaximize(this.state.maximized); return; }
    this.state = toggledMode(this.state);
    this.layout();
    this.changed();
  }

  toggleMode() { this.setMode(this.state.mode === 'quad' ? 'single' : 'quad'); }

  toggleMaximize(i) {
    const next = toggledMaximize(this.state, i);
    if (next === this.state) return;
    this.state = next;
    this.layout();
    this.changed();
  }

  setSplit(x, y) {
    const c = (v) => Math.max(SPLIT_LIMITS[0], Math.min(SPLIT_LIMITS[1], v));
    this.state = { ...this.state, split: { x: c(x), y: c(y) } };
    this.layout();
  }

  setOrientation(i, orientation, opts) { this.panes[i]?.setOrientation(orientation, opts); }

  setGizmoVisible(i, on) {
    const p = this.panes[i];
    if (!p) return;
    p.config = { ...p.config, showAxisGizmo: !!on };
    p.setGizmo(this.viewPrefs);
    this.dirty();
    this.changed();
  }

  /**
   * Looks along `dir` in pane `i` (view cube, axis gizmo, the View menu). A perspective pane
   * turns its camera; an orthographic pane switches to the matching fixed view, and the
   * isometric direction brings it back to perspective.
   */
  viewFrom(i, dir, { animate = true } = {}) {
    const p = this.panes[i];
    if (!p) return;
    this.setActive(i);
    if (!p.perspective) {
      const o = orientationForDir(dir);
      p.setOrientation(o || 'perspective', { animate, dir: o ? null : dir });
      return;
    }
    const box = this.hooks.bounds?.() ?? new THREE.Box3();
    const c = box.isEmpty() ? p.controls.target.clone() : box.getCenter(new THREE.Vector3());
    const dist = p.camera.position.distanceTo(p.controls.target);
    const d = v3(dir).normalize();
    // Looking straight down/up the Z axis, Y becomes the screen's up direction.
    const up = Math.abs(d.z) > 0.99 ? new THREE.Vector3(0, 1, 0) : new THREE.Vector3(0, 0, 1);
    p.animateTo({ target: c, position: c.clone().addScaledVector(d, dist), up }, animate);
  }

  /** Camera distance and clip range that keep the whole scene in front of an orthographic view of `target`. */
  depth(target) {
    const box = this.hooks.bounds?.();
    if (!box || box.isEmpty()) return { dist: 300, near: 0.1, far: 100000 };
    const c = box.getCenter(new THREE.Vector3());
    const r = Math.max(box.getSize(new THREE.Vector3()).length() / 2, 1);
    const dist = c.distanceTo(target) + r * 3;
    return { dist, near: Math.max(r / 1000, 0.01), far: dist + r * 200 };
  }

  /** Advances view animations; true when something moved. */
  step() {
    const now = performance.now();
    let moved = false;
    for (const p of this.panes) moved = p.step(now) || moved;
    return moved;
  }

  setViewPrefs(prefs) {
    this.viewPrefs = { ...this.viewPrefs, ...prefs };
    for (const p of this.panes) p.setGizmo(this.viewPrefs);
  }

  /** The persisted layout: mode, split, maximized/active pane and every ViewportConfig. */
  serialize() {
    return { ...this.state, views: this.panes.map((p) => ({ ...p.config, cameraState: p.cameraState() })) };
  }

  /** Restores a stored layout (normalised; cameras only where one was saved). */
  restore(saved) {
    const { views, ...st } = normalizeLayout(saved);
    this.state = st;
    this.panes.forEach((p, i) => {
      const cfg = views[i];
      if (cfg.orientation !== p.config.orientation) { p.config = withOrientation(p.config, cfg.orientation); p._useProjection(); p._syncLabel(); }
      p.config = { ...p.config, showAxisGizmo: cfg.showAxisGizmo };
      p.setGizmo(this.viewPrefs);
      p.restored = false;
      if (cfg.cameraState) p.restoreCamera(cfg.cameraState);
    });
    this.layout();
  }

  // ---- DOM wiring ------------------------------------------------------------------------

  _bindPane(p) {
    // Any press inside a pane (surface, label, gizmo, cube) makes it the active one.
    p.el.addEventListener('pointerdown', () => this.setActive(p.index), true);
    let timer = null;
    p.label.addEventListener('click', (e) => {
      clearTimeout(timer);
      if (e.detail > 1) return;
      const open = () => this.hooks.onPaneMenu?.(p.index, p.label);
      // Wait a moment so a double-click maximizes without flashing the menu; keyboard opens at once.
      if (e.detail === 0) open(); else timer = setTimeout(open, 220);
    });
    p.label.addEventListener('dblclick', (e) => { e.preventDefault(); clearTimeout(timer); this.toggleMaximize(p.index); });
  }

  _initDividers(host) {
    const mk = (cls, orient, label) => {
      const d = document.createElement('div');
      d.className = `vp-split ${cls}`;
      d.setAttribute('role', 'separator');
      if (orient) d.setAttribute('aria-orientation', orient);
      d.setAttribute('aria-label', label);
      d.tabIndex = 0;
      d.title = 'Drag to resize the views · double-click to reset';
      host.append(d);
      return d;
    };
    this.dividers = {
      v: mk('v', 'vertical', 'Resize the left and right views'),
      h: mk('h', 'horizontal', 'Resize the top and bottom views'),
      c: mk('c', null, 'Resize all four views'),
    };
    const drag = (el, ax) => {
      el.addEventListener('pointerdown', (e) => {
        e.preventDefault(); e.stopPropagation();
        el.setPointerCapture(e.pointerId);
        el.classList.add('drag');
        const box = this.container.getBoundingClientRect();
        const move = (ev) => {
          const { x, y } = this.state.split;
          this.setSplit(ax.includes('x') ? (ev.clientX - box.left) / box.width : x, ax.includes('y') ? (ev.clientY - box.top) / box.height : y);
        };
        const up = () => {
          el.classList.remove('drag');
          el.removeEventListener('pointermove', move); el.removeEventListener('pointerup', up); el.removeEventListener('pointercancel', up);
          this.changed();
        };
        el.addEventListener('pointermove', move); el.addEventListener('pointerup', up); el.addEventListener('pointercancel', up);
      });
      el.addEventListener('dblclick', () => {
        const { x, y } = this.state.split;
        this.setSplit(ax.includes('x') ? 0.5 : x, ax.includes('y') ? 0.5 : y);
        this.changed();
      });
      el.addEventListener('keydown', (e) => {
        const dx = e.key === 'ArrowLeft' ? -1 : e.key === 'ArrowRight' ? 1 : 0;
        const dy = e.key === 'ArrowUp' ? -1 : e.key === 'ArrowDown' ? 1 : 0;
        if (!(ax.includes('x') && dx) && !(ax.includes('y') && dy)) return;
        e.preventDefault();
        const { x, y } = this.state.split;
        this.setSplit(ax.includes('x') ? x + dx * 0.02 : x, ax.includes('y') ? y + dy * 0.02 : y);
        this.changed();
      });
    };
    drag(this.dividers.v, 'x');
    drag(this.dividers.h, 'y');
    drag(this.dividers.c, 'xy');
  }
}
