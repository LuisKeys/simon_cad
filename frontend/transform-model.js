// The interactive transform state machine and its math (no DOM, no Three.js; unit tested).
// transform.js feeds it mouse rays and screen points and draws what it returns; it decides the
// mode, the constraint, the candidate motion and the tool call a confirmation commits.
//
//   IDLE ──begin('translate')──▶ TRANSLATING ──confirm / cancel──▶ IDLE
//   IDLE ──begin('rotate')─────▶ ROTATING ─────confirm / cancel──▶ IDLE
//
// Every update is computed from the state at Begin (the start ray, the start angle), never by
// adding per-frame steps, so the object follows the mouse without drift or jumps and snapping
// is deterministic. Lengths are mm and angles radians, in world coordinates.
import { snapMove, snapAngle } from './snap.js';

export const MODE = Object.freeze({ IDLE: 'IDLE', TRANSLATING: 'TRANSLATING', ROTATING: 'ROTATING' });
export const AXIS_NAMES = ['X', 'Y', 'Z'];
/** World axis indices of each plane's two in-plane axes and its normal (as GridService). */
export const PLANE_AXES = { XY: [0, 1, 2], XZ: [0, 2, 1], YZ: [1, 2, 0] };

// ---- vectors (plain [x, y, z] arrays) ---------------------------------------------------

export const add = (a, b) => [a[0] + b[0], a[1] + b[1], a[2] + b[2]];
export const sub = (a, b) => [a[0] - b[0], a[1] - b[1], a[2] - b[2]];
export const scale = (a, s) => [a[0] * s, a[1] * s, a[2] * s];
export const dot = (a, b) => a[0] * b[0] + a[1] * b[1] + a[2] * b[2];
export const cross = (a, b) => [a[1] * b[2] - a[2] * b[1], a[2] * b[0] - a[0] * b[2], a[0] * b[1] - a[1] * b[0]];
export const norm = (a) => Math.hypot(a[0], a[1], a[2]);
export const unit = (i) => [0, 1, 2].map((k) => (k === i ? 1 : 0));
const normalize = (a) => { const n = norm(a); return n > 0 ? scale(a, 1 / n) : a; };

// Below this |cos| between the view ray and a plane's normal the plane is seen (nearly) edge
// on: intersecting it would throw the object to infinity, so the drag uses a camera-facing
// plane through the anchor instead and keeps only the in-plane part of the motion.
export const EDGE_ON = 0.08;

/** Ray (origin, unit dir) ∩ plane through p0 with normal n, or null when parallel. */
export function rayPlane(ray, p0, n) {
  const d = dot(ray.dir, n);
  if (Math.abs(d) < 1e-9) return null;
  const t = dot(sub(p0, ray.origin), n) / d;
  return add(ray.origin, scale(ray.dir, t));
}

// ---- constraints ------------------------------------------------------------------------

/**
 * A move constraint: { kind: 'plane', normal } (move within the plane through the object with
 * that world axis as normal) or { kind: 'axis', axis } (move along one world axis). The default
 * is the active grid plane.
 */
export const planeConstraint = (gridPlane) => ({ kind: 'plane', normal: (PLANE_AXES[gridPlane] ?? PLANE_AXES.XY)[2], isDefault: true });

/** World axes a constraint lets move. */
export function freeAxes(c) {
  return [0, 1, 2].map((i) => (c.kind === 'axis' ? i === c.axis : i !== c.normal));
}

/** "X", "YZ plane", "XY plane (grid)": the constraint as the HUD names it. */
export function constraintLabel(c) {
  if (c.kind === 'axis') return `${AXIS_NAMES[c.axis]} axis`;
  const ax = [0, 1, 2].filter((i) => i !== c.normal).map((i) => AXIS_NAMES[i]).join('');
  return `${ax} plane${c.isDefault ? ' (grid)' : ''}`;
}

/**
 * The point under the mouse ray for a constrained drag of `anchor`: on the constraint plane, or
 * the closest point of the constraint axis. Null when the axis points straight at the camera
 * (there is no usable direction to drag along).
 */
export function dragPoint(ray, anchor, c) {
  if (c.kind === 'axis') {
    const a = unit(c.axis);
    // The plane containing the axis that faces the ray most squarely.
    const w = cross(a, cross(ray.dir, a));
    if (norm(w) < 1e-6) return null;
    const hit = rayPlane(ray, anchor, normalize(w));
    if (!hit) return null;
    return add(anchor, scale(a, dot(sub(hit, anchor), a)));
  }
  const n = unit(c.normal);
  if (Math.abs(dot(ray.dir, n)) >= EDGE_ON) return rayPlane(ray, anchor, n);
  const hit = rayPlane(ray, anchor, ray.dir); // camera-facing plane through the anchor
  if (!hit) return null;
  return sub(hit, scale(n, dot(sub(hit, anchor), n)));
}

// ---- rotation ---------------------------------------------------------------------------

/** Screen angle of point p around center c (pixels, y down) with y up, in radians. */
export const screenAngle = (c, p) => Math.atan2(-(p.y - c.y), p.x - c.x);

/** Wraps an angle difference into (-π, π]. */
export function wrap(a) {
  let r = a % (2 * Math.PI);
  if (r > Math.PI) r -= 2 * Math.PI;
  else if (r <= -Math.PI) r += 2 * Math.PI;
  return r;
}

/** Below this distance (px) from the pivot the mouse angle is too unstable to follow. */
export const PIVOT_DEAD_ZONE = 6;

// ---- the session ------------------------------------------------------------------------

/**
 * One interactive operation. `object` is the selected object's transform (selection.transforms:
 * object_id, origin, center); `gridPlane` the workspace grid plane.
 */
export class TransformSession {
  constructor(kind, object, { gridPlane = 'XY', pivot = 'origin' } = {}) {
    this.mode = kind === 'rotate' ? MODE.ROTATING : MODE.TRANSLATING;
    this.object = object;
    this.gridPlane = gridPlane;
    this.defaultAxis = (PLANE_AXES[gridPlane] ?? PLANE_AXES.XY)[2];
    this.constraint = planeConstraint(gridPlane);
    this.axis = this.defaultAxis;          // rotation axis (world index)
    this.pivotKind = pivot;
    this.start = null;                     // translate: { ray } · rotate: { screen, sign }
    this.turns = 0; this.lastRaw = 0;
    this.result = this.identity();
  }

  get translating() { return this.mode === MODE.TRANSLATING; }
  get rotating() { return this.mode === MODE.ROTATING; }
  /** The fixed point of a rotation (or the moved reference point of a translation). */
  get pivot() { return this.rotating && this.pivotKind === 'center' ? this.object.center : this.object.origin; }

  identity() {
    return this.translating ? { kind: 'translate', delta: [0, 0, 0], raw: [0, 0, 0], snapped: false }
      : { kind: 'rotate', angle: 0, raw: 0, axis: this.axis, pivot: this.pivot, snapped: false };
  }

  /**
   * Keyboard constraints. Translation: X/Y/Z → that axis, Shift+X/Y/Z → the plane normal to it
   * (Shift+Z = XY). Rotation: X/Y/Z → that axis. Pressing the active constraint again returns to
   * the default (the grid plane / its normal). Returns true when something changed.
   */
  constrain(key, shift = false) {
    const i = AXIS_NAMES.indexOf(String(key).toUpperCase());
    if (i < 0) return false;
    if (this.rotating) {
      this.axis = this.axis === i && i !== this.defaultAxis ? this.defaultAxis : i;
      this.rebase();
      return true;
    }
    const c = this.constraint;
    const same = shift ? c.kind === 'plane' && c.normal === i && !c.isDefault : c.kind === 'axis' && c.axis === i;
    this.constraint = same ? planeConstraint(this.gridPlane) : shift ? { kind: 'plane', normal: i } : { kind: 'axis', axis: i };
    return true;
  }

  /** Rotation pivot: 'origin' (default) or 'center'. */
  setPivot(kind) {
    if (!this.rotating || (kind !== 'origin' && kind !== 'center')) return false;
    this.pivotKind = kind;
    this.rebase();
    return true;
  }

  /** A new axis or pivot keeps the mouse reference (the angle carries over, no jump); only the
   *  turn direction is re-read for the new axis on the next update. */
  rebase() {
    if (this.start) this.start.sign = 0;
    this.result = { ...this.result, axis: this.axis, pivot: this.pivot };
  }

  /**
   * Moves the candidate. Translate input: { ray, snap }. Rotate input: { screen: {x, y},
   * pivotScreen: {x, y}, toCamera: [x, y, z], snap }. `snap` is a snap.js context.
   * The first update after Begin (or after the axis/pivot changed) only records the reference.
   */
  update(input) {
    return this.translating ? this._translate(input) : this._rotate(input);
  }

  _translate({ ray, snap }) {
    if (!this.start) { this.start = { ray }; return this.result; }
    const anchor = this.object.origin;
    // The start point is recomputed with the current constraint, so switching constraints mid
    // drag keeps the reference where the mouse began instead of jumping.
    const a = dragPoint(this.start.ray, anchor, this.constraint), b = dragPoint(ray, anchor, this.constraint);
    if (!a || !b) return this.result;          // the axis points at the camera: hold still
    const free = freeAxes(this.constraint);
    const raw = sub(b, a).map((v, i) => (free[i] ? v : 0));
    const s = snap ? snapMove(anchor, raw, free, snap) : { delta: raw, snapped: false };
    this.result = { kind: 'translate', delta: s.delta, raw, snapped: s.snapped };
    return this.result;
  }

  _rotate({ screen, pivotScreen, toCamera, snap }) {
    if (Math.hypot(screen.x - pivotScreen.x, screen.y - pivotScreen.y) < PIVOT_DEAD_ZONE) return this.result;
    const ang = screenAngle(pivotScreen, screen);
    // Looking down the axis, counter-clockwise on screen is a positive turn; from behind it is
    // negative. The sign is read once per axis so it never flips mid-drag.
    const sign = () => (dot(unit(this.axis), toCamera) >= 0 ? 1 : -1);
    if (!this.start) { this.start = { angle: ang, sign: sign() }; return this.result; }
    if (!this.start.sign) this.start.sign = sign();
    let raw = wrap(ang - this.start.angle);
    // Absolute from the start, plus whole turns: crossing ±180° continues smoothly.
    if (raw - this.lastRaw > Math.PI) this.turns--;
    else if (raw - this.lastRaw < -Math.PI) this.turns++;
    this.lastRaw = raw;
    raw = (raw + this.turns * 2 * Math.PI) * this.start.sign;
    const s = snap ? snapAngle(raw, snap) : { angle: raw, snapped: false };
    this.result = { kind: 'rotate', angle: s.angle, raw, axis: this.axis, pivot: this.pivot, snapped: s.snapped };
    return this.result;
  }

  /**
   * The tool call that commits the current candidate, or null when it changes nothing (then a
   * confirmation is a cancellation: no history entry). Values are already snapped.
   */
  command() {
    const r = this.result, id = this.object.object_id;
    if (r.kind === 'translate') {
      if (r.delta.every((v) => Math.abs(v) < 1e-9)) return null;
      const [dx, dy, dz] = r.delta;
      return { name: 'move_object', args: { object_id: id, dx, dy, dz, intent: `Move ${id}` } };
    }
    const deg = (r.angle * 180) / Math.PI;
    if (Math.abs(wrap(r.angle)) < 1e-12) return null;
    return { name: 'rotate_object', args: { object_id: id, angle: deg, axis: AXIS_NAMES[r.axis].toLowerCase(), pivot: this.pivotKind, intent: `Rotate ${id}` } };
  }
}
