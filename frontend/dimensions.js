// Interactive dimensions in the viewport. Two parts, kept apart from the measurement (the
// server's DimensionService) and from the resize (the set_object_dimension tool):
//
//   DimensionRenderer draws the exterior dimensions of the selected objects: the lines live in
//     the one shared scene (so every view of the Single/Quad layout shows them) and each view
//     gets its own labels, placed with that view's camera.
//   DimensionEditor turns a clicked label into a number field: Enter confirms, Escape (or
//     leaving the field) cancels, and a bad value is refused in place without a round trip.
//
// Labels are only editable for the selected objects, which are the only ones dimensioned.
import * as THREE from 'three';
import { AXIS_LABEL, dimensionLines, labelVisible } from './dimensions-model.js';
import { units } from './units.js';

const LINE_COLOR = 0x35d0c8;

export class DimensionRenderer {
  /** @param {import('./viewport.js').Viewport} vp  @param {{ onEdit(id, axis, value): Promise<void>, onInvalid(msg): void }} hooks */
  constructor(vp, hooks) {
    this.vp = vp;
    this.group = new THREE.Group();
    this.group.renderOrder = 25;
    vp.scene.add(this.group);
    vp.fitOverlays.push(this.group); // Fit frames the dimensions with the object
    // The lines are shared by every view, but a dimension seen end-on (height from TOP) would
    // show only its extension lines and ticks as a stray oblique stroke: hide it in that view.
    this.beforePane = (pane) => {
      for (const it of this.items) it.obj.visible = labelVisible(...this._ends(it, pane));
    };
    vp.paneHooks.push(this.beforePane);
    this.items = [];          // { dim, line, labels: Map<paneIndex, element> }
    this.key = '';
    this.editor = new DimensionEditor(hooks);
  }

  /** Shows the dimensions of `list` (Dimensions from the server); [] clears them. */
  set(list) {
    const key = JSON.stringify(list ?? []);
    if (key === this.key) return;
    if (this.editor.active) { this.pending = list; return; } // never pull the field away while typing
    this.key = key;
    this._clear();
    for (const dim of list ?? []) {
      for (const line of dimensionLines(dim)) this.items.push({ dim, line, obj: this._drawLine(line), labels: new Map() });
    }
    this.vp.dirty();
  }

  /** Redraws the labels after the display units changed (the lines do not move). */
  refresh() {
    if (this.editor.active) return;
    const list = JSON.parse(this.key || '[]');
    this.key = '';
    this.set(list);
  }

  _clear() {
    for (const c of [...this.group.children]) { this.group.remove(c); c.geometry?.dispose(); c.material?.dispose(); }
    for (const it of this.items) for (const el of it.labels.values()) el.remove();
    this.items = [];
  }

  /** One dimension (line, extension lines, ticks) as one object, so each view can hide it. */
  _drawLine(l) {
    const pts = [l.a, l.b];
    for (const [p, q] of [...l.ext, ...l.ticks]) pts.push(p, q);
    const g = new THREE.BufferGeometry().setFromPoints(pts.map((p) => new THREE.Vector3(...p)));
    const seg = new THREE.LineSegments(g, new THREE.LineBasicMaterial({ color: LINE_COLOR, depthTest: false, transparent: true, opacity: 0.95 }));
    seg.renderOrder = 25;
    this.group.add(seg);
    return seg;
  }

  /** The dimension line's ends in a view's pixels (z: normalized depth). */
  _ends(it, pane) {
    const r = pane.rect ?? { w: 1, h: 1 };
    return [it.line.a, it.line.b].map((p) => {
      const v = new THREE.Vector3(...p).project(pane.camera);
      return { x: ((v.x + 1) / 2) * r.w, y: ((1 - v.y) / 2) * r.h, z: v.z };
    });
  }

  /** Places every label in every visible view; called after each frame. */
  update() {
    if (this.pending && !this.editor.active) { const p = this.pending; this.pending = null; this.set(p); }
    const visible = new Set(this.vp.layout.visible());
    for (const it of this.items) {
      for (const pane of this.vp.layout.panes) {
        let el = it.labels.get(pane.index);
        if (!visible.has(pane) || !pane.rect) { if (el) el.hidden = true; continue; }
        const r = pane.rect, cam = pane.camera;
        const s = (p) => { const v = new THREE.Vector3(...p).project(cam); return { x: ((v.x + 1) / 2) * r.w, y: ((1 - v.y) / 2) * r.h, z: v.z }; };
        const a = s(it.line.a), b = s(it.line.b), m = s(it.line.mid);
        const show = labelVisible(a, b) && m.x >= 0 && m.y >= 0 && m.x <= r.w && m.y <= r.h;
        if (!el) {
          if (!show) continue;
          el = this._label(it, pane);
          it.labels.set(pane.index, el);
        }
        el.hidden = !show && !this.editor.owns(el);
        if (!el.hidden) { el.style.left = `${m.x}px`; el.style.top = `${m.y}px`; }
      }
    }
  }

  _label(it, pane) {
    const { dim, line } = it;
    const el = document.createElement('button');
    el.type = 'button';
    el.className = 'dim-label';
    el.dataset.object = dim.object_id;
    el.dataset.axis = line.axis;
    el.textContent = units.compact(line.value);
    const what = `${AXIS_LABEL[line.axis]} of ${dim.object_id}: ${units.compact(line.value)} ${units.symbol}`;
    el.title = `${what}${dim.rotated ? ' (along its own axes)' : ''}. Click to type a new size${dim.lock_proportions ? '; proportions are locked' : ''}.`;
    el.setAttribute('aria-label', `${what}. Edit`);
    // The view underneath must not see the press (no orbit, no pick).
    el.addEventListener('pointerdown', (e) => e.stopPropagation());
    el.addEventListener('click', (e) => { e.stopPropagation(); this.editor.open(el, dim, line); });
    pane.el.append(el);
    return el;
  }

  dispose() {
    this._clear();
    this.vp.scene.remove(this.group);
    this.vp.paneHooks = this.vp.paneHooks.filter((h) => h !== this.beforePane);
    this.vp.fitOverlays = this.vp.fitOverlays.filter((o) => o !== this.group);
  }
}

/** Numeric editing of one dimension label at a time. */
export class DimensionEditor {
  constructor(hooks) { this.hooks = hooks; this.active = null; }

  owns(el) { return this.active?.label === el; }

  open(label, dim, line) {
    if (this.active) this.close(false);
    const input = document.createElement('input');
    input.type = 'text';
    input.inputMode = 'decimal';
    input.className = 'dim-input';
    input.value = units.compact(line.value);
    input.setAttribute('aria-label', `New ${line.axis} of ${dim.object_id} in ${units.symbol}`);
    input.spellcheck = false;
    label.replaceChildren(input);
    label.classList.add('editing');
    this.active = { label, dim, line, input };
    input.addEventListener('pointerdown', (e) => e.stopPropagation());
    input.addEventListener('click', (e) => e.stopPropagation());
    input.addEventListener('keydown', (e) => {
      e.stopPropagation(); // Escape must not also clear the selection, Delete must not delete
      if (e.key === 'Enter') { e.preventDefault(); this.close(true); }
      else if (e.key === 'Escape') { e.preventDefault(); this.close(false); }
    });
    input.addEventListener('blur', () => { if (this.active?.input === input) this.close(false); });
    input.focus();
    input.select();
  }

  /** Confirms (Enter) or cancels (Escape, blur). A value that does not parse keeps the field open. */
  async close(confirm) {
    const a = this.active;
    if (!a) return;
    if (confirm) {
      const r = units.parseLength(a.input.value);
      if (!r.ok) { a.input.setAttribute('aria-invalid', 'true'); this.hooks.onInvalid?.(r.error); a.input.select(); return; }
      if (Math.abs(r.value - a.line.value) < 1e-9) { confirm = false; } // unchanged: nothing to do
      else {
        this.active = null;
        a.label.classList.remove('editing');
        a.label.classList.add('busy');
        a.label.textContent = units.compact(r.value);
        // On success the new dimensions replace this label; on failure it shows the old value.
        try { await this.hooks.onEdit(a.dim.object_id, a.line.axis, r.value); } finally {
          a.label.classList.remove('busy');
          a.label.textContent = units.compact(a.line.value);
        }
        return;
      }
    }
    this.active = null;
    a.label.classList.remove('editing');
    a.label.textContent = units.compact(a.line.value);
  }
}
