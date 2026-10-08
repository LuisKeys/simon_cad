// Interactive Move (G) and Rotate (R) in the viewport, Blender style: a modal operation that
// follows the mouse until a left click / Enter confirms it or Esc / a right click cancels it.
//
//   Keyboard / context menu / action bar → InteractiveTransformController → move_object /
//   rotate_object (the TransformService tools the agent uses) → transform feature → rebuild
//
// While the operation runs nothing reaches the server: the preview only changes the body's
// matrix (no geometry is rebuilt, nothing is serialized, no history entry, no agent event).
// Confirming commits exactly one tool call (one ChangeSet, one undo step); cancelling restores
// the identity matrix, so the object is back exactly where it was.
import * as THREE from 'three';
import { TransformSession, MODE, AXIS_NAMES, constraintLabel, freeAxes } from './transform-model.js';
import { snapContext } from './snap.js';
import { units } from './units.js';

const AXIS_COLORS = [0xef5350, 0x66bb6a, 0x42a5f5];
const PIVOT_COLOR = 0xff8a1f;

/** Viewport feedback: constraint axes/planes, the pivot, the rotation ring and the HUD text. */
export class TransformPreview {
  constructor(vp, container) {
    this.vp = vp;
    this.group = new THREE.Group();
    this.group.renderOrder = 30;
    vp.scene.add(this.group);
    this.hud = document.createElement('div');
    this.hud.className = 'xform-hud';
    this.hud.hidden = true;
    this.hud.setAttribute('role', 'status');
    this.hud.setAttribute('aria-live', 'polite');
    container.appendChild(this.hud);
  }

  _clearScene() {
    for (const c of [...this.group.children]) { this.group.remove(c); c.geometry?.dispose(); c.material?.dispose(); }
  }

  _line(a, b, color, opacity = 1) {
    const g = new THREE.BufferGeometry().setFromPoints([new THREE.Vector3(...a), new THREE.Vector3(...b)]);
    const l = new THREE.Line(g, new THREE.LineBasicMaterial({ color, transparent: opacity < 1, opacity, depthTest: false }));
    l.renderOrder = 30;
    this.group.add(l);
  }

  _axisLine(through, axis, len, opacity = 1) {
    const a = [0, 0, 0]; a[axis] = len;
    this._line(through.map((v, i) => v - a[i]), through.map((v, i) => v + a[i]), AXIS_COLORS[axis], opacity);
  }

  /** Redraws the overlay for a session and its current result. */
  show(s, r, { size, snapOn }) {
    this._clearScene();
    const len = Math.max(size * 3, 50);
    if (s.translating) {
      const c = s.constraint, o = s.object.origin;
      if (c.kind === 'axis') this._axisLine(o, c.axis, len);
      else for (const i of [0, 1, 2]) if (i !== c.normal) this._axisLine(o, i, len, 0.45);
      const to = o.map((v, i) => v + r.delta[i]);
      if (r.delta.some((v) => v !== 0)) this._line(o, to, 0xffffff, 0.8);
    } else {
      const p = s.pivot, axis = s.axis;
      this._axisLine(p, axis, len);
      // A ring in the rotation plane, and the pivot marker.
      const ring = new THREE.EllipseCurve(0, 0, size * 0.75, size * 0.75).getPoints(64).map((q) => new THREE.Vector3(q.x, q.y, 0));
      const g = new THREE.BufferGeometry().setFromPoints(ring);
      const loop = new THREE.LineLoop(g, new THREE.LineBasicMaterial({ color: AXIS_COLORS[axis], transparent: true, opacity: 0.6, depthTest: false }));
      loop.quaternion.setFromUnitVectors(new THREE.Vector3(0, 0, 1), new THREE.Vector3(...[0, 1, 2].map((i) => (i === axis ? 1 : 0))));
      loop.position.set(...p);
      loop.renderOrder = 30;
      this.group.add(loop);
      const m = new THREE.Mesh(new THREE.SphereGeometry(Math.max(size * 0.025, 0.2), 14, 10), new THREE.MeshBasicMaterial({ color: PIVOT_COLOR, depthTest: false }));
      m.position.set(...p); m.renderOrder = 31;
      this.group.add(m);
    }
    this.hud.replaceChildren(...hudLines(s, r, units, snapOn).map((line, i) => {
      const el = document.createElement('div');
      el.className = i === 0 ? 'main' : 'hint';
      el.textContent = line;
      return el;
    }));
    this.hud.hidden = false;
    this.vp.dirty();
  }

  clear() {
    this._clearScene();
    this.hud.hidden = true;
    this.hud.replaceChildren();
    this.vp.dirty();
  }
}

/** The HUD text: what is moving, by how much, where to, the constraint and snapping. */
export function hudLines(s, r, u = units, snapOn = r.snapped) {
  const snap = snapOn ? 'Snap on' : 'Snap off';
  if (s.translating) {
    const free = freeAxes(s.constraint);
    const delta = [0, 1, 2].filter((i) => free[i]).map((i) => `Δ${AXIS_NAMES[i]} ${signed(u.length(r.delta[i]))}`).join('  ');
    const at = s.object.origin.map((v, i) => `${AXIS_NAMES[i]} ${u.length(v + r.delta[i], { unit: false })}`).join('  ');
    return [`Move · ${constraintLabel(s.constraint)} · ${delta} · at ${at} ${u.symbol} · ${snap}`,
      'X / Y / Z axis · Shift+X / Y / Z plane · Ctrl: no snap · Click or Enter: confirm · Esc or right-click: cancel'];
  }
  const deg = (r.angle * 180) / Math.PI;
  return [`Rotate · ${AXIS_NAMES[s.axis]} axis · ${signed(u.angle(deg))} · pivot: object ${s.pivotKind} · ${snap}`,
    'X / Y / Z axis · P: pivot origin/center · Ctrl: no snap · Click or Enter: confirm · Esc or right-click: cancel'];
}

const signed = (s) => (/^[0-9]/.test(s) && !/^0(\.0*)?(\s|°|$)/.test(s) ? '+' + s : s);

/**
 * The modal controller. Hooks: selection() (the selection view), workspace() (state.workspace),
 * commit({name, args}) → Promise<boolean> (runs the tool call and refreshes), onModal(active),
 * notify(message, kind).
 */
export class InteractiveTransformController {
  constructor(vp, hooks) {
    this.vp = vp;
    this.hooks = hooks;
    this.container = vp.canvas.parentElement;
    this.preview = new TransformPreview(vp, this.container);
    this.session = null;
    this.committing = false;
    this.mouse = null;          // last pointer position anywhere in the app (client px)
    this.ctrl = false;          // held: snapping is bypassed
    this.quietUntil = 0;        // swallow the contextmenu that follows a right-click cancel
    this._onMove = (e) => this._pointerMove(e);
    this._onDown = (e) => this._pointerDown(e);
    this._onKey = (e) => this._key(e);
    this._onKeyUp = (e) => { if (e.key === 'Control' && this.session) { this.ctrl = false; this._refresh(); } };
    // Always on: where the mouse is (to begin from it) and the context menu guard.
    window.addEventListener('pointermove', (e) => { this.mouse = { x: e.clientX, y: e.clientY }; }, true);
    window.addEventListener('contextmenu', (e) => {
      if (this.session || performance.now() < this.quietUntil) { e.preventDefault(); e.stopPropagation(); }
    }, true);
  }

  /** The current state of the lifecycle: IDLE, TRANSLATING or ROTATING. */
  get mode() { return this.session?.mode ?? MODE.IDLE; }
  get active() { return !!this.session; }

  /** Whether an operation of kind 'translate' | 'rotate' can start now, and why not. */
  check() {
    if (this.session || this.committing) return { ok: false, reason: 'A transform is already in progress' };
    const sel = this.hooks.selection();
    const ids = [...new Set((sel?.selectors ?? []).map(featureOf).filter(Boolean))];
    if (ids.length === 0) return { ok: false, reason: 'Select an object to move or rotate' };
    if (ids.length > 1) return { ok: false, reason: 'Interactive Move/Rotate works on one object at a time' };
    const t = (sel.transforms ?? []).find((x) => x.object_id === ids[0]);
    if (!t) return { ok: false, reason: `${ids[0]} is not a visible body: only visible bodies can be moved` };
    if (!t.transformable) return { ok: false, reason: `${ids[0]} cannot be moved: ${t.reason}` };
    if (!this.vp.bodies.get(ids[0])) return { ok: false, reason: `${ids[0]} is not shown in the viewport` };
    return { ok: true, object: t };
  }

  /**
   * Begin: records the original transform (the body's identity matrix), the view and the mouse
   * reference. `at` picks the view (the right-click point of the context menu); the reference
   * is where the mouse is now, so the object never jumps on the first move.
   */
  begin(kind, { at = null } = {}) {
    const c = this.check();
    if (!c.ok) { this.hooks.notify?.(c.reason, 'bad'); return false; }
    const body = this.vp.bodies.get(c.object.object_id);
    const where = at ?? this.mouse;
    const pane = where ? this.vp.layout.paneAt(where.x, where.y) : this.vp.layout.active;
    const r = pane.surface.getBoundingClientRect();
    this.pointer = this.mouse ?? { x: r.left + r.width / 2, y: r.top + r.height / 2 };
    const ws = this.hooks.workspace() ?? {};
    this.session = new TransformSession(kind, c.object, { gridPlane: ws.grid?.plane ?? 'XY' });
    this.pane = pane;
    this.body = body;
    this.size = Math.max(new THREE.Box3().setFromObject(body.group).getSize(new THREE.Vector3()).length(), 1);
    this.ctrl = false;
    body.group.matrixAutoUpdate = false;
    this.vp.selGroup.matrixAutoUpdate = false;
    this.vp.hover = null;
    this.vp._drawOverlay(this.vp.hoverGroup, [], 0);
    window.addEventListener('pointermove', this._onMove, true);
    window.addEventListener('pointerdown', this._onDown, true);
    window.addEventListener('keydown', this._onKey, true);
    window.addEventListener('keyup', this._onKeyUp, true);
    for (const p of this.vp.layout.panes) p.surface.style.cursor = kind === 'rotate' ? 'alias' : 'move';
    this.hooks.onModal?.(true);
    this._refresh(); // records the reference: the object does not jump
    return true;
  }

  _input() {
    const s = this.session, p = this.pointer, pane = this.pane;
    const ws = this.hooks.workspace();
    const snap = snapContext(ws, { bypass: this.ctrl });
    const cam = pane.camera;
    if (s.translating) {
      const n = this.vp._ndc({ clientX: p.x, clientY: p.y }, pane);
      this.vp.ray.setFromCamera({ x: n.x, y: n.y }, cam);
      const o = this.vp.ray.ray.origin, d = this.vp.ray.ray.direction;
      return { ray: { origin: [o.x, o.y, o.z], dir: [d.x, d.y, d.z] }, snap };
    }
    const r = pane.surface.getBoundingClientRect();
    const pv = new THREE.Vector3(...s.pivot);
    const sp = this.vp._toScreen(pv, { w: r.width, h: r.height }, cam);
    const toCam = cam.isOrthographicCamera ? cam.getWorldDirection(new THREE.Vector3()).negate() : cam.position.clone().sub(pv).normalize();
    return { screen: p, pivotScreen: { x: sp.x + r.left, y: sp.y + r.top }, toCamera: [toCam.x, toCam.y, toCam.z], snap };
  }

  /** Update: recomputes the candidate from the current mouse and redraws the preview. */
  _refresh() {
    const s = this.session;
    if (!s) return;
    const input = this._input();
    const r = s.update(input);
    const m = new THREE.Matrix4();
    if (r.kind === 'translate') m.makeTranslation(r.delta[0], r.delta[1], r.delta[2]);
    else if (r.angle !== 0) {
      const p = new THREE.Vector3(...r.pivot);
      const axis = new THREE.Vector3(...[0, 1, 2].map((i) => (i === r.axis ? 1 : 0)));
      m.makeTranslation(p.x, p.y, p.z).multiply(new THREE.Matrix4().makeRotationAxis(axis, r.angle)).multiply(new THREE.Matrix4().makeTranslation(-p.x, -p.y, -p.z));
    }
    this._applyMatrix(m);
    const snapOn = s.translating ? input.snap.gridActive || input.snap.translationActive : input.snap.rotationActive;
    this.preview.show(s, r, { size: this.size, snapOn });
  }

  _applyMatrix(m) {
    for (const g of [this.body.group, this.vp.selGroup]) { g.matrix.copy(m); g.matrixWorldNeedsUpdate = true; }
    this.vp.dirty();
  }

  _pointerMove(e) {
    this.pointer = { x: e.clientX, y: e.clientY };
    if (e.ctrlKey !== this.ctrl) this.ctrl = e.ctrlKey;
    if (this.container.contains(e.target)) e.stopPropagation(); // no hover picking meanwhile
    this._refresh();
  }

  _pointerDown(e) {
    const inside = this.container.contains(e.target);
    // On Linux the contextmenu event follows this pointerdown; on Windows the pointerup.
    if (e.button === 2) { this._stop(e); this.quietUntil = performance.now() + 400; this.cancel(); return; }
    if (e.button !== 0) return;      // the middle button still navigates the camera
    if (inside) { this._stop(e); this.pointer = { x: e.clientX, y: e.clientY }; this._refresh(); this.confirm(); }
    else this.cancel();              // clicking elsewhere in the app abandons the operation
  }

  _stop(e) { e.preventDefault(); e.stopPropagation(); }

  _key(e) {
    if (!this.session) return;
    const k = e.key;
    if (['Shift', 'Alt', 'Meta'].includes(k)) return;
    if (k === 'Control') { this.ctrl = true; this._refresh(); return; }
    this._stop(e); // while transforming, no other shortcut runs (Delete, F, M, Ctrl+Z…)
    if (k === 'Escape') this.cancel();
    else if (k === 'Enter' || k === ' ') this.confirm();
    else if (!e.ctrlKey && !e.metaKey && /^[xyz]$/i.test(k)) { this.session.constrain(k, e.shiftKey); this._refresh(); }
    else if (!e.ctrlKey && !e.metaKey && /^p$/i.test(k) && this.session.rotating) {
      this.session.setPivot(this.session.pivotKind === 'origin' ? 'center' : 'origin');
      this._refresh();
    }
  }

  /** Confirm: commits one tool call; a candidate that changes nothing is a cancel. */
  async confirm() {
    const s = this.session;
    if (!s) return;
    const cmd = s.command();
    if (!cmd) { this.cancel(); return; }
    const body = this.body;
    this._end();
    this.committing = true;
    let ok = false;
    try { ok = await this.hooks.commit(cmd); } finally { this.committing = false; }
    // On success the refreshed scene has replaced the body; whatever is left (an error, or the
    // body was not replaced) goes back to where the model says it is. The selection overlay is
    // redrawn from the model either way, so it never keeps the preview's matrix.
    this._reset(this.vp.bodies.get(body.data.id)?.group === body.group || !ok ? body.group : null);
  }

  /** Cancel: restores the original transform exactly; nothing was sent, nothing to undo. */
  cancel(reason = '') {
    if (!this.session) return;
    const body = this.body;
    this._end();
    this._reset(body.group);
    if (reason) this.hooks.notify?.(reason, 'bad');
  }

  _reset(group) {
    for (const g of [group, this.vp.selGroup]) {
      if (!g) continue;
      g.matrix.identity(); g.matrixAutoUpdate = true; g.matrixWorldNeedsUpdate = true;
    }
    this.vp.dirty();
  }

  _end() {
    this.session = null;
    window.removeEventListener('pointermove', this._onMove, true);
    window.removeEventListener('pointerdown', this._onDown, true);
    window.removeEventListener('keydown', this._onKey, true);
    window.removeEventListener('keyup', this._onKeyUp, true);
    for (const p of this.vp.layout.panes) p.surface.style.cursor = '';
    this.preview.clear();
    this.hooks.onModal?.(false);
  }

  /**
   * Called after the scene or the selection changed underneath an active operation (an agent
   * edit, undo, a project switch): the operation is cancelled safely if its target is gone,
   * was rebuilt or is no longer selected. Snapping settings changes need nothing: they are read
   * on every update.
   */
  validate() {
    if (!this.session) return;
    const id = this.session.object.object_id;
    const sel = this.hooks.selection();
    if (this.vp.bodies.get(id)?.group !== this.body.group) {
      this.cancel(`${id} changed while it was being transformed; the transform was cancelled`);
    } else if (!(sel?.selectors ?? []).some((x) => featureOf(x) === id)) {
      this.cancel('The selection changed; the transform was cancelled');
    }
  }
}

function featureOf(sel) { const m = /feature=([^,)]+)/.exec(sel); return m ? m[1].trim() : ''; }

