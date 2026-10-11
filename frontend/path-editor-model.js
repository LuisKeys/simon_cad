// The path editor's state machine (no DOM, no Three.js; unit tested). A session either DRAWs a
// new path point by point or EDITs an existing one. While a point is dragged the working path
// changes locally only (the view previews it at frame rate, the server hears nothing); a finished
// gesture produces exactly one tool call (create_path / update_path), so one undo step.
import { PLANE_AXES, dragPoint, rayPlane, add, sub, scale, dot, cross, norm } from './transform-model.js';
import { snapMove, snapAngle } from './snap.js';
import * as P from './path-model.js';

export const MODE = Object.freeze({ IDLE: 'IDLE', DRAW: 'DRAW', EDIT: 'EDIT' });
export const DRAG = Object.freeze({ NONE: 'NONE', POINT: 'POINT', HANDLE: 'HANDLE', WIDTH: 'WIDTH', HEIGHT: 'HEIGHT', TWIST: 'TWIST' });

const unitV = (a) => { const n = norm(a); return n < 1e-12 ? [0, 0, 0] : scale(a, 1 / n); };
const round = (v) => Math.round(v * 1e9) / 1e9;
const r3 = (v) => v.map(round);

/** The args of a point as update_path / create_path expect: [x,y,z] when it has nothing else. */
export function pointArg(pt) {
  const extra = {};
  if (pt.in) extra.in = r3(pt.in);
  if (pt.out) extra.out = r3(pt.out);
  if (pt.w && pt.w !== 1) extra.w = round(pt.w);
  if (pt.h && pt.h !== 1) extra.h = round(pt.h);
  if (pt.twist) extra.twist = round(pt.twist);
  return Object.keys(extra).length ? { p: r3(pt.p), ...extra } : r3(pt.p);
}

export const pointsArg = (path) => path.points.map(pointArg);

export class PathSession {
  /**
   * kind 'draw': a new path of `type`. kind 'edit': the existing `path` named `id`.
   * gridPlane ('XY' | 'XZ' | 'YZ') is where clicks land and drags happen by default.
   */
  constructor(kind, { id = null, path = null, gridPlane = 'XY', type = 'catmull_rom' } = {}) {
    this.mode = kind === 'draw' ? MODE.DRAW : MODE.EDIT;
    this.id = id;
    this.gridPlane = gridPlane;
    this.normal = (PLANE_AXES[gridPlane] ?? PLANE_AXES.XY)[2];
    this.path = path ? P.clonePath(path) : { type, closed: false, points: [] };
    this.committed = P.clonePath(this.path);
    this.sel = -1;                // selected control point
    this.drag = { kind: DRAG.NONE };
    this.error = null;
  }

  get drawing() { return this.mode === MODE.DRAW; }
  get dragging() { return this.drag.kind !== DRAG.NONE; }
  get minPoints() { return this.path.closed ? 3 : 2; }

  // ---- drawing -------------------------------------------------------------------------

  /** Adds a point at a world position (a click on the working plane). */
  addPoint(pos) {
    if (!this.drawing) return false;
    const last = this.path.points[this.path.points.length - 1];
    if (last && norm(sub(last.p, pos)) < 1e-6) return false; // a double click is not two points
    this.path.points.push({ p: [...pos] });
    this.sel = this.path.points.length - 1;
    return true;
  }

  popPoint() {
    if (!this.drawing || !this.path.points.length) return false;
    this.path.points.pop();
    this.sel = this.path.points.length - 1;
    return true;
  }

  get canFinish() { return this.drawing && this.path.points.length >= this.minPoints; }

  /** The create_path call for what was drawn. Bezier points get smooth handles. */
  finishCommand() {
    if (!this.canFinish) return null;
    const path = P.clonePath(this.path);
    if (path.type === 'bezier') {
      path.points.forEach((pt, i) => { const h = P.autoHandles(path, i); pt.in = h.in; pt.out = h.out; });
      if (!path.closed) { delete path.points[0].in; delete path.points[path.points.length - 1].out; }
    }
    const args = { points: pointsArg(path), type: path.type };
    if (path.closed) args.closed = true;
    return { name: 'create_path', args };
  }

  // ---- shape-level settings (both modes) -------------------------------------------------

  setClosed(closed) {
    if (closed && this.path.points.length < 3) { this.error = 'A closed path needs at least 3 points'; return null; }
    this.error = null;
    this.path.closed = !!closed;
    return this.drawing ? null : this._cmd({ closed: !!closed });
  }

  setType(type) {
    if (!P.TYPES.includes(type)) return null;
    if (this.drawing) { this.path = { ...this.path, type }; return null; }
    this.path = P.convertType(this.path, type);
    return this._cmd({ type });
  }

  // ---- editing ---------------------------------------------------------------------------

  select(i) { this.sel = i >= 0 && i < this.path.points.length ? i : -1; }

  /** Starts dragging a point (kind POINT), a bezier handle (HANDLE with which 'in'|'out'), or
   *  a width / height / twist gizmo. */
  beginDrag(kind, index, opts = {}) {
    if (this.drawing || index < 0 || index >= this.path.points.length) return false;
    this.select(index);
    this.drag = { kind, index, ...opts, before: P.clonePath(this.path) };
    return true;
  }

  /**
   * Updates the working path from the mouse ray. `snap` is a snapContext; `vertical` drags along
   * the grid plane's normal instead of within the plane; `smooth` (default) keeps Bezier
   * handles collinear. Returns the new path, or null when the ray cannot place the point.
   */
  dragTo(ray, snap, { vertical = false, smooth = true, gizmo = null } = {}) {
    const d = this.drag;
    if (d.kind === DRAG.NONE) return null;
    const pt = d.before.points[d.index];
    if (d.kind === DRAG.POINT || d.kind === DRAG.HANDLE) {
      const anchor = d.kind === DRAG.POINT ? pt.p : pt[d.which] ?? pt.p;
      const c = vertical ? { kind: 'axis', axis: this.normal } : { kind: 'plane', normal: this.normal };
      const hit = dragPoint(ray, anchor, c);
      if (!hit) return null;
      const free = vertical ? [0, 1, 2].map((i) => i === this.normal) : [0, 1, 2].map((i) => i !== this.normal);
      const target = add(anchor, snapMove(anchor, sub(hit, anchor), free, snap).delta);
      this.path = d.kind === DRAG.POINT ? P.movePoint(d.before, d.index, target) : this._moveHandle(d, target, smooth);
      return this.path;
    }
    if (!gizmo) return null;
    // Gizmo drags use the point's frame: gizmo = { p, t, n, b, radius }
    const out = P.clonePath(d.before), q = out.points[d.index];
    if (d.kind === DRAG.WIDTH || d.kind === DRAG.HEIGHT) {
      const axis = d.kind === DRAG.WIDTH ? gizmo.n : gizmo.b;
      const k = closestOnLine(ray, gizmo.p, axis);
      const f = Math.max(0.05, round(dot(sub(k, gizmo.p), axis) / gizmo.radius));
      const factor = snap?.rotationActive || snap?.translationActive ? Math.round(f * 20) / 20 : f;
      if (d.kind === DRAG.WIDTH) q.w = factor; else q.h = factor;
    } else {
      const hit = rayPlane(ray, gizmo.p, gizmo.t);
      if (!hit) return null;
      const v = sub(hit, gizmo.p);
      const base = gizmo.base; // the untwisted frame normal
      const ang = Math.atan2(dot(cross(base, v), gizmo.t), dot(base, v));
      q.twist = round(snapAngle(ang, snap).angle * 180 / Math.PI);
    }
    this.path = out;
    return this.path;
  }

  _moveHandle(d, target, smooth) {
    const out = P.clonePath(d.before), q = out.points[d.index];
    q[d.which] = target;
    const other = d.which === 'in' ? 'out' : 'in';
    if (smooth && q[other]) { // keep the tangent straight through the point, preserving the other length
      const dir = unitV(sub(q.p, target));
      if (norm(dir) > 0) q[other] = add(q.p, scale(dir, norm(sub(q[other], q.p))));
    }
    return out;
  }

  /** Ends the drag: the update_path call for the change, or null when nothing moved. */
  endDrag() {
    const d = this.drag;
    this.drag = { kind: DRAG.NONE };
    if (d.kind === DRAG.NONE) return null;
    if (JSON.stringify(P.normalizePath(this.path)) === JSON.stringify(P.normalizePath(d.before))) return null;
    return this._cmd({ points: pointsArg(this.path) });
  }

  cancelDrag() {
    if (this.drag.kind !== DRAG.NONE) this.path = this.drag.before;
    this.drag = { kind: DRAG.NONE };
  }

  /** Inserts a point on segment `seg` at curve position u (the shape does not change). */
  insert(seg, u) {
    if (this.drawing) return null;
    this.path = P.insertOnSegment(this.path, seg, u);
    this.select(seg + 1);
    return this._cmd({ points: pointsArg(this.path) });
  }

  /** Deletes the selected point (or `index`). Returns the call, or null with `error` set. */
  remove(index = this.sel) {
    const next = P.removePoint(this.path, index);
    if (!next) { this.error = `A ${this.path.closed ? 'closed' : 'open'} path needs at least ${this.minPoints} points`; return null; }
    this.error = null;
    this.path = next;
    this.sel = Math.min(this.sel, next.points.length - 1);
    return this.drawing ? null : this._cmd({ points: pointsArg(next) });
  }

  /** Sets width / height / twist of a point by number. */
  setPointProp(index, prop, value) {
    if (!['w', 'h', 'twist'].includes(prop) || !Number.isFinite(value)) return null;
    if ((prop === 'w' || prop === 'h') && !(value > 0)) { this.error = 'Width and height must be greater than 0'; return null; }
    this.error = null;
    const out = P.clonePath(this.path);
    out.points[index][prop] = value;
    this.path = out;
    return this.drawing ? null : this._cmd({ points: pointsArg(out) });
  }

  /** Moves a point to exact coordinates (typed in the panel). */
  setPointPosition(index, pos) {
    if (!pos.every(Number.isFinite)) return null;
    this.path = P.movePoint(this.path, index, pos);
    return this.drawing ? null : this._cmd({ points: pointsArg(this.path) });
  }

  /** The server accepted a command: the working path is now the committed one. */
  committed_() { this.committed = P.clonePath(this.path); }

  /** The server state changed under us (undo, agent): adopt it, keeping the selection. */
  adopt(path) {
    if (this.dragging) return;
    this.path = P.clonePath(path);
    this.committed = P.clonePath(path);
    if (this.sel >= this.path.points.length) this.sel = this.path.points.length - 1;
  }

  _cmd(args) { return { name: 'update_path', args: { path: this.id, ...args } }; }
}

function closestOnLine(ray, p, axis) {
  // closest point on the line p + s*axis to the ray
  const a = unitV(axis), d = ray.dir, w = sub(ray.origin, p);
  const b = dot(d, a), dd = dot(d, d), c = dot(a, a), dw = dot(d, w), aw = dot(a, w);
  const den = dd * c - b * b;
  const s = Math.abs(den) < 1e-12 ? 0 : (dd * aw - b * dw) / den;
  return add(p, scale(a, s));
}
