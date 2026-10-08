// The workspace grid in the 3D scene: minor lines, major lines and the two centre lines in
// the colours of the axes they run along. Lines come from grid-model.js (world space, exact
// multiples of the spacing from the grid origin). Presentation only: never pickable, never in
// the model's bounds, never exported.
import * as THREE from 'three';
import { AXES } from './axes.js';
import { DEFAULT_GRID, PLANE_AXES, gridKey, gridLines } from './grid-model.js';

const THEMES = { dark: { minor: 0x2c2c2c, major: 0x474747 }, light: { minor: 0xcdd1d9, major: 0xa3a9b4 } };
const noRaycast = () => {};

export class WorkspaceGrid extends THREE.Group {
  constructor() {
    super();
    this.name = 'workspace-grid';
    this.userData.helper = true;
    this.renderOrder = -1; // under everything else; it never writes depth
    this.config = { ...DEFAULT_GRID };
    this.theme = 'dark';
    this.planes = {}; // plane -> { group, key }
    this.mats = {
      minor: new THREE.LineBasicMaterial({ transparent: true, opacity: 0.5, depthWrite: false }),
      major: new THREE.LineBasicMaterial({ transparent: true, opacity: 0.75, depthWrite: false }),
      axis: AXES.map((a) => new THREE.LineBasicMaterial({ color: a.color, transparent: true, opacity: 0.55, depthWrite: false })),
    };
    this.setTheme('dark');
  }

  /** New grid settings (canonical mm). Lines are rebuilt lazily, only when they change. */
  setConfig(grid) {
    this.config = { ...DEFAULT_GRID, ...(grid ?? {}) };
  }

  setTheme(theme) {
    this.theme = theme === 'light' ? 'light' : 'dark';
    const c = THEMES[this.theme];
    this.mats.minor.color.setHex(c.minor);
    this.mats.major.color.setHex(c.major);
  }

  /** The grid group of a plane for the current settings (built or rebuilt on demand). */
  plane(plane) {
    const key = gridKey(this.config);
    let p = this.planes[plane];
    if (p?.key === key) return p.group;
    if (p) { this.remove(p.group); p.group.traverse((o) => o.geometry?.dispose()); }
    const lines = gridLines(this.config, plane);
    const group = new THREE.Group();
    const add = (positions, mat, order) => {
      if (!positions.length) return;
      const seg = new THREE.LineSegments(new THREE.BufferGeometry().setAttribute('position', new THREE.Float32BufferAttribute(positions, 3)), mat);
      seg.raycast = noRaycast; seg.renderOrder = order; seg.frustumCulled = false;
      group.add(seg);
    };
    add(lines.minor, this.mats.minor, -3);
    add(lines.major, this.mats.major, -2);
    for (const a of lines.axes) add(a.positions, this.mats.axis[a.axis], -1);
    group.visible = false;
    this.add(group);
    this.planes[plane] = p = { group, key };
    return group;
  }

  /**
   * Shows the grid of `plane` for one view, hiding the others. `away` is the side of the plane
   * (+1/−1 along its normal) facing away from the camera: the grid is nudged a hair that way so
   * a face lying exactly on the grid plane never fights it. In an orthographic view `behind`
   * pushes the whole grid behind the model instead, which does not move it on screen.
   */
  show(plane, { away = -1, behind = null, eps = 0.01 } = {}) {
    this.hide();
    const g = this.plane(plane);
    const n = PLANE_AXES[plane][2];
    g.position.set(0, 0, 0);
    g.position.setComponent(n, behind ?? away * eps);
    g.visible = true;
  }

  hide() { for (const p of Object.values(this.planes)) p.group.visible = false; }
}
