// Coordinate-axis visual aids: the single definition of the X/Y/Z axes (direction, colour,
// label) and the views they lead to, shared by the axes drawn through the origin and the
// axis gizmo. All of it is presentation only: none of these objects
// is pickable, part of the model's bounds, exported or recorded in the project history.
import * as THREE from 'three';

/** The model axes, in the engine's own coordinates (Z up). */
export const AXES = [
  { name: 'X', color: '#E05252', dir: [1, 0, 0] },
  { name: 'Y', color: '#56B870', dir: [0, 1, 0] },
  { name: 'Z', color: '#4A90E2', dir: [0, 0, 1] },
];

/** The isometric view direction (front-right-top, from the camera toward the target). */
export const ISO_VIEW = [1, -1, 1];

const GIZMO_PX = { small: 64, medium: 80, large: 104 };
const noRaycast = () => {};

const labelCache = new Map();
/** A label texture (cached; shared by every user of the same text and colour). */
export function axisLabelTexture(text, color) {
  const key = text + color;
  if (!labelCache.has(key)) {
    const cv = document.createElement('canvas'); cv.width = cv.height = 64;
    const g = cv.getContext('2d');
    g.font = `bold ${text.length > 1 ? 34 : 44}px system-ui, sans-serif`;
    g.textAlign = 'center'; g.textBaseline = 'middle';
    g.lineWidth = 6; g.strokeStyle = 'rgba(0,0,0,.55)'; g.strokeText(text, 32, 34);
    g.fillStyle = color; g.fillText(text, 32, 34);
    const t = new THREE.CanvasTexture(cv); t.colorSpace = THREE.SRGBColorSpace;
    labelCache.set(key, t);
  }
  return labelCache.get(key);
}

function label(text, color, opacity = 1) {
  const s = new THREE.Sprite(new THREE.SpriteMaterial({ map: axisLabelTexture(text, color), transparent: true,
    opacity, depthWrite: false }));
  return s;
}

/**
 * The three axes through the origin (0, 0, 0), both directions, with labels at the positive
 * ends. The negative half is drawn fainter so the direction reads at a glance.
 */
export class WorldAxes extends THREE.Group {
  constructor() {
    super();
    this.name = 'coordinate-axes';
    this.userData.helper = true;
    this.renderOrder = 1;          // over the grid, which sits just under the model
    this.extent = 200;
    this.prefs = { opacity: 0.7, labels: true };
    this.halves = [];
    this.labels = [];
    for (const a of AXES) {
      for (const sign of [1, -1]) {
        const g = new THREE.BufferGeometry().setAttribute('position',
          new THREE.Float32BufferAttribute([0, 0, 0, ...a.dir.map((c) => c * sign)], 3));
        const l = new THREE.Line(g, new THREE.LineBasicMaterial({ color: a.color, transparent: true, depthWrite: false }));
        l.userData.negative = sign < 0;
        l.raycast = noRaycast;
        this.halves.push(l); this.add(l);
      }
      const s = label(a.name, a.color);
      s.userData.dir = a.dir;
      s.raycast = noRaycast;
      this.labels.push(s); this.add(s);
    }
    this.raycast = noRaycast;
    this.setExtent(this.extent);
  }

  /** Half-length of each axis, in model units. */
  setExtent(len) {
    this.extent = len;
    for (const l of this.halves) l.scale.setScalar(len);
    for (const s of this.labels) s.position.set(...s.userData.dir.map((c) => c * len * 1.03));
  }

  setPrefs({ opacity = this.prefs.opacity, labels = this.prefs.labels }) {
    this.prefs = { opacity, labels };
    for (const l of this.halves) l.material.opacity = opacity * (l.userData.negative ? 0.45 : 1);
    for (const s of this.labels) { s.visible = labels; s.material.opacity = Math.min(1, opacity + 0.2); }
  }

  /** Keeps the labels a constant on-screen size, in perspective and orthographic views. */
  update(camera, heightPx) {
    if (!this.prefs.labels) return;
    const px = 16 / Math.max(heightPx, 1);
    for (const s of this.labels) {
      let k;
      if (camera.isOrthographicCamera) k = px * (camera.top - camera.bottom) / camera.zoom;
      else k = px * 2 * Math.tan((camera.fov * Math.PI) / 360) * s.position.distanceTo(camera.position);
      s.scale.set(k, k, 1);
    }
  }
}

/**
 * The axis gizmo: a small, fixed-on-screen triad that turns with the camera. Clicking an
 * axis end looks along it, the centre returns to the isometric view. It is a plain 2D canvas
 * (no extra WebGL context) and redraws only when the camera's orientation changes.
 */
export class AxisGizmo {
  constructor(container, { onView }) {
    this.onView = onView;
    this.canvas = document.createElement('canvas');
    this.canvas.className = 'axis-gizmo';
    this.canvas.title = 'Click an axis to look along it · centre for isometric';
    this.canvas.setAttribute('role', 'img');
    this.canvas.setAttribute('aria-label', 'Axis orientation');
    container.appendChild(this.canvas);
    this.q = new THREE.Quaternion(0, 0, 0, 0); // nothing drawn yet
    this.items = [];
    this.hover = -1;
    this.setOptions({ visible: true, size: 'medium', position: 'bottom-left' });
    this.canvas.addEventListener('pointermove', (e) => this._hover(this._hit(e)));
    this.canvas.addEventListener('pointerleave', () => this._hover(-1));
    this.canvas.addEventListener('click', (e) => {
      const i = this._hit(e);
      if (i >= 0) this.onView?.(this.items[i].view);
    });
  }

  setOptions({ visible, size, position }) {
    this.visible = visible !== false;
    this.size = GIZMO_PX[size] ?? GIZMO_PX.medium;
    const dpr = Math.min(window.devicePixelRatio || 1, 2);
    this.canvas.width = this.canvas.height = Math.round(this.size * dpr);
    this.canvas.style.width = this.canvas.style.height = `${this.size}px`;
    this.canvas.dataset.position = ['bottom-left', 'bottom-right', 'top-left'].includes(position) ? position : 'bottom-left';
    this.canvas.hidden = !this.visible;
    this.q.set(0, 0, 0, 0); // force a redraw
  }

  /** Redraws for the camera's orientation, if it changed. */
  update(camera) {
    if (!this.visible || this.q.equals(camera.quaternion)) return;
    this.q.copy(camera.quaternion);
    this._layout();
    this._draw();
  }

  _layout() {
    const inv = this.q.clone().invert();
    const c = this.size / 2, R = c - 13;
    this.items = [{ x: c, y: c, z: 0, view: ISO_VIEW, center: true }];
    for (const a of AXES) {
      const v = new THREE.Vector3(...a.dir).applyQuaternion(inv); // view space: x right, y up, z toward the eye
      for (const sign of [1, -1]) {
        this.items.push({ axis: a, neg: sign < 0, x: c + sign * v.x * R, y: c - sign * v.y * R, z: sign * v.z,
          view: a.dir.map((k) => k * sign) });
      }
    }
  }

  _draw() {
    const g = this.canvas.getContext('2d');
    const dpr = this.canvas.width / this.size, c = this.size / 2;
    g.setTransform(dpr, 0, 0, dpr, 0, 0);
    g.clearRect(0, 0, this.size, this.size);
    g.lineCap = 'round';
    const order = this.items.map((it, i) => i).filter((i) => !this.items[i].center)
      .sort((i, j) => this.items[i].z - this.items[j].z); // far first
    const ci = 0;
    let centerDrawn = false;
    const drawCenter = () => {
      centerDrawn = true;
      g.globalAlpha = 1;
      g.beginPath(); g.arc(c, c, this.hover === ci ? 5 : 3.5, 0, Math.PI * 2);
      g.fillStyle = this.hover === ci ? '#e4e8ee' : '#8b939e'; g.fill();
    };
    for (const i of order) {
      const it = this.items[i], hot = this.hover === i;
      if (!centerDrawn && it.z > 0) drawCenter(); // the origin sits between the far and near halves
      const dx = it.x - c, dy = it.y - c, len = Math.hypot(dx, dy);
      g.strokeStyle = g.fillStyle = it.axis.color;
      if (it.neg) {
        g.globalAlpha = hot ? 0.9 : 0.4;
        g.lineWidth = 1.5;
        g.beginPath(); g.moveTo(c, c); g.lineTo(c + dx * 0.55, c + dy * 0.55); g.stroke();
        g.beginPath(); g.arc(it.x, it.y, hot ? 6 : 4.5, 0, Math.PI * 2); g.fill();
        continue;
      }
      g.globalAlpha = 1;
      g.lineWidth = hot ? 2.5 : 2;
      const ux = len ? dx / len : 0, uy = len ? dy / len : 0;
      const head = Math.min(7, len * 0.4);
      g.beginPath(); g.moveTo(c, c); g.lineTo(it.x - ux * head, it.y - uy * head); g.stroke();
      if (len > 3) { // arrowhead (skipped when the axis points straight at the eye)
        const w = head * 0.55;
        g.beginPath(); g.moveTo(it.x, it.y);
        g.lineTo(it.x - ux * head - uy * w, it.y - uy * head + ux * w);
        g.lineTo(it.x - ux * head + uy * w, it.y - uy * head - ux * w);
        g.closePath(); g.fill();
      }
      const lx = it.x + ux * 7, ly = it.y + uy * 7;
      g.font = `bold ${hot ? 12 : 11}px system-ui, sans-serif`;
      g.textAlign = 'center'; g.textBaseline = 'middle';
      g.lineWidth = 3; g.strokeStyle = 'rgba(0,0,0,.5)'; g.strokeText(it.axis.name, lx, ly);
      g.fillText(it.axis.name, lx, ly);
    }
    if (!centerDrawn) drawCenter();
    g.globalAlpha = 1;
  }

  _hit(e) {
    const b = this.canvas.getBoundingClientRect();
    const x = (e.clientX - b.left) * (this.size / b.width), y = (e.clientY - b.top) * (this.size / b.height);
    // The centre wins over an axis pointing straight at (or away from) the eye: that axis is
    // the current view anyway, and the isometric view must stay reachable.
    if (Math.hypot(this.size / 2 - x, this.size / 2 - y) < 7) return 0;
    let best = -1, bd = 10, bz = -Infinity;
    this.items.forEach((it, i) => {
      if (it.center) return;
      const d = Math.hypot(it.x - x, it.y - y);
      // Overlapping targets: the one nearest the eye wins.
      if (d < 10 && (d < bd - 2 || (d <= bd + 2 && it.z > bz))) { best = i; bd = d; bz = it.z; }
    });
    return best;
  }

  _hover(i) {
    if (i === this.hover) return;
    this.hover = i;
    this.canvas.style.cursor = i >= 0 ? 'pointer' : '';
    if (this.visible && this.items.length) this._draw();
  }
}
