// Print simulation scene: a virtual printer in the SAME Three.js scene and renderer as the CAD
// viewport (viewport.js). Entering the mode hides the CAD bodies (their geometry stays loaded) and
// adds one Group; leaving removes it, disposes every GPU resource it made and restores the view.
// The deposited filament is a derived visualization of the slicer's toolpaths (one instanced draw
// call, see printsim-shader.js); the CAD model stays the authoritative geometry and is never touched.
import * as THREE from 'three';
import { Playback, decodeToolpath, kindMask, bedTicks, frameBox, SEG_FLOATS } from './printsim-model.js';
import { filamentUniforms, patchFilament, filamentDepthMaterial } from './printsim-shader.js';

// sRGB colour per kind, in printsim-model KINDS order:
// travel, perimeter, external_perimeter, infill, solid_infill, top_bottom, support, bridge, skirt, other
export const PALETTES = {
  realistic: [0x35d0c8, 0xff8a1f, 0xff7a00, 0xff8f2e, 0xff7a00, 0xff7a00, 0xd9d2c5, 0xff7a00, 0xb35200, 0xff7a00],
  technical: [0x35d0c8, 0xffd166, 0xff7a00, 0x4fc3f7, 0x7e57c2, 0x66bb6a, 0xef5350, 0xec407a, 0x9e9e9e, 0xcfd8dc],
  diagnostics: [0x35d0c8, 0x7b8594, 0xaab3c0, 0x7b8594, 0x7b8594, 0x7b8594, 0xef6a6a, 0xe8b04b, 0x5a626e, 0x7b8594],
};
const SURFACE = { realistic: { roughness: 0.5, metalness: 0 }, technical: { roughness: 0.85, metalness: 0 }, diagnostics: { roughness: 0.9, metalness: 0 } };

export const DEFAULT_OPTIONS = {
  mode: 'realistic',
  show: { travel: false, perimeters: true, infill: true, support: true, skirt: true, other: true },
  overlay: false,        // translucent original model on the plate
  heat: true,            // subtle nozzle / fresh-bead glow
  completedOnly: false,  // hide the bead being laid right now
  frame: 'part',         // what Fit frames: the part or the whole plate
};

// ---- the printer ------------------------------------------------------------------------------

/** A canvas texture of the plate: millimetre grid, 50 mm marks with labels, and the X/Y axes. */
function bedTexture(bx, by) {
  const ppm = Math.min(4, 2048 / Math.max(bx, by));
  const W = Math.max(2, Math.round(bx * ppm)), H = Math.max(2, Math.round(by * ppm));
  const c = document.createElement('canvas');
  c.width = W; c.height = H;
  const g = c.getContext('2d');
  g.fillStyle = '#20262e'; g.fillRect(0, 0, W, H);
  const X = (mm) => mm * ppm, Y = (mm) => H - mm * ppm; // canvas y grows downward, bed y grows up
  for (const t of bedTicks(bx)) { g.strokeStyle = t.major ? 'rgba(255,255,255,.26)' : 'rgba(255,255,255,.08)'; g.lineWidth = t.major ? 2 : 1; g.beginPath(); g.moveTo(X(t.pos), 0); g.lineTo(X(t.pos), H); g.stroke(); }
  for (const t of bedTicks(by)) { g.strokeStyle = t.major ? 'rgba(255,255,255,.26)' : 'rgba(255,255,255,.08)'; g.lineWidth = t.major ? 2 : 1; g.beginPath(); g.moveTo(0, Y(t.pos)); g.lineTo(W, Y(t.pos)); g.stroke(); }
  g.lineWidth = Math.max(3, ppm); // the axes, in the gizmo's colours
  g.strokeStyle = '#ef6a6a'; g.beginPath(); g.moveTo(0, Y(0) - g.lineWidth / 2); g.lineTo(X(Math.min(bx, 40)), Y(0) - g.lineWidth / 2); g.stroke();
  g.strokeStyle = '#4cc38a'; g.beginPath(); g.moveTo(g.lineWidth / 2, Y(0)); g.lineTo(g.lineWidth / 2, Y(Math.min(by, 40))); g.stroke();
  g.fillStyle = 'rgba(255,255,255,.62)';
  g.font = `${Math.round(5 * ppm)}px system-ui, sans-serif`;
  g.textBaseline = 'bottom'; g.textAlign = 'left';
  for (const t of bedTicks(bx)) if (t.major && t.pos > 0) g.fillText(String(t.pos), X(t.pos) + 2 * ppm, H - 1.5 * ppm);
  g.textBaseline = 'top';
  for (const t of bedTicks(by)) if (t.major && t.pos > 0) g.fillText(String(t.pos), 1.5 * ppm, Y(t.pos) + 1 * ppm);
  g.fillStyle = 'rgba(255,255,255,.4)';
  g.textAlign = 'right'; g.textBaseline = 'top';
  g.fillText('mm', W - 2 * ppm, 2 * ppm);
  g.strokeStyle = 'rgba(255,255,255,.5)'; g.lineWidth = 2; g.strokeRect(1, 1, W - 2, H - 2);
  const tex = new THREE.CanvasTexture(c);
  tex.colorSpace = THREE.SRGBColorSpace;
  tex.anisotropy = 8;
  return tex;
}

function buildBed(bx, by, bz) {
  const group = new THREE.Group();
  const tex = bedTexture(bx, by);
  const top = new THREE.Mesh(new THREE.PlaneGeometry(bx, by),
    new THREE.MeshStandardMaterial({ map: tex, roughness: 0.55, metalness: 0.1 }));
  top.position.set(bx / 2, by / 2, -0.02);
  top.receiveShadow = true;
  const body = new THREE.Mesh(new THREE.BoxGeometry(bx + 8, by + 8, 8),
    new THREE.MeshStandardMaterial({ color: 0x2c333d, roughness: 0.5, metalness: 0.6 }));
  body.position.set(bx / 2, by / 2, -4.1);
  const volume = new THREE.LineSegments(new THREE.EdgesGeometry(new THREE.BoxGeometry(bx, by, bz)),
    new THREE.LineBasicMaterial({ color: 0x5b7a99, transparent: true, opacity: 0.35 }));
  volume.position.set(bx / 2, by / 2, bz / 2);
  group.add(top, body, volume);
  return group;
}

/** A hotend modelled from primitives: brass nozzle, heater block, heat break, fins and a fan duct. The tip is the origin. */
function buildNozzle() {
  const g = new THREE.Group();
  const brass = new THREE.MeshStandardMaterial({ color: 0xc8a24a, metalness: 0.9, roughness: 0.32, emissive: 0x000000 });
  const alu = new THREE.MeshStandardMaterial({ color: 0xaeb8c4, metalness: 0.85, roughness: 0.38 });
  const block = new THREE.MeshStandardMaterial({ color: 0xaeb8c4, metalness: 0.85, roughness: 0.38, emissive: 0x000000 });
  const dark = new THREE.MeshStandardMaterial({ color: 0x3a414c, metalness: 0.3, roughness: 0.55 });
  const accent = new THREE.MeshStandardMaterial({ color: 0xff7a00, metalness: 0.1, roughness: 0.5 });
  const add = (geo, mat, x, y, z, rot) => {
    const m = new THREE.Mesh(geo, mat);
    if (rot) geo.rotateX(Math.PI / 2); // CylinderGeometry stands along Y: lay it along Z
    m.position.set(x, y, z);
    g.add(m);
    return m;
  };
  add(new THREE.CylinderGeometry(2.4, 0.25, 3.2, 28), brass, 0, 0, 1.6, true);        // nozzle cone, tip at z=0
  add(new THREE.CylinderGeometry(3.4, 3.4, 3, 6), brass, 0, 0, 4.7, true);           // hex body
  add(new THREE.BoxGeometry(16, 20, 11.5), block, 0, 0, 12.0);                         // heater block
  add(new THREE.BoxGeometry(16.4, 6, 11.8), accent, 0, -5.2, 12.0);                    // silicone sock stripe
  add(new THREE.CylinderGeometry(3.6, 3.6, 7, 24), alu, 0, 0, 21.5, true);            // heat break
  add(new THREE.CylinderGeometry(4.4, 4.4, 20, 24), alu, 0, 0, 34, true);             // heat sink core
  for (let i = 0; i < 8; i++) add(new THREE.CylinderGeometry(10.5, 10.5, 0.9, 40), alu, 0, 0, 26 + i * 2.5, true);
  add(new THREE.BoxGeometry(22, 8, 20), dark, 0, 15.5, 33);                            // fan duct
  const ring = add(new THREE.TorusGeometry(7.5, 0.9, 10, 36), accent, 0, 19.7, 33);   // fan ring
  ring.rotation.x = Math.PI / 2;
  g.userData = { brass, block };
  return g;
}

// ---- the filament -----------------------------------------------------------------------------

class Filament {
  constructor(tp, material, depth) {
    // fewer sides when there are very many beads: the triangle count stays bounded
    const n = tp.segCount;
    const radial = n > 250000 ? 6 : n > 80000 ? 8 : 12;
    const base = new THREE.CylinderGeometry(0.5, 0.5, 1, radial, 1, false);
    base.rotateZ(Math.PI / 2); // axis along X, 1 mm across
    const geo = new THREE.InstancedBufferGeometry();
    geo.setIndex(base.index);
    geo.setAttribute('position', base.attributes.position);
    geo.setAttribute('normal', base.attributes.normal);
    const ib = new THREE.InstancedInterleavedBuffer(tp.seg, SEG_FLOATS, 1); // the backend's buffer, uploaded as is
    geo.setAttribute('aStart', new THREE.InterleavedBufferAttribute(ib, 3, 0));
    geo.setAttribute('aEnd', new THREE.InterleavedBufferAttribute(ib, 3, 3));
    geo.setAttribute('aInfo', new THREE.InterleavedBufferAttribute(ib, 3, 6));
    geo.setAttribute('aMeta', new THREE.InstancedBufferAttribute(tp.meta, 2));
    geo.instanceCount = 0;
    this.trianglesPerBead = (base.index.count / 3);
    this.base = base;
    this.mesh = new THREE.Mesh(geo, material);
    this.mesh.frustumCulled = false; // the base bead is 1 mm at the origin; the real extent is per instance
    this.mesh.castShadow = this.mesh.receiveShadow = true;
    this.mesh.customDepthMaterial = depth;
    this.mesh.renderOrder = 1;
  }

  dispose() { this.mesh.geometry.dispose(); this.base.dispose(); }
}

// ---- the mode ---------------------------------------------------------------------------------

export class PrintSim {
  constructor(vp) {
    this.vp = vp;
    this.active = false;
    this.opts = structuredClone(DEFAULT_OPTIONS);
    this.playback = null;
    this.tp = null;
    this.summary = null;
    this.onUpdate = null; // (playback, nozzle) after the scene changed
    this.u = filamentUniforms();
    this._hook = (now) => this._tick(now);
    this._shadowAt = 0;
    this._last = 0;
    this._layer = -1;
  }

  /** Switches the viewport to the printer scene. `bed` is the build volume {x, y, z} in mm. */
  enter(bed) {
    if (this.active) return;
    const vp = this.vp;
    this.active = true;
    this.bed = bed;
    this._saved = { panes: vp.layout.panes.map((p) => p.pose()),
      hidden: [vp.ghost, vp.hoverGroup, vp.selGroup, vp.measureGroup, vp.sketches, ...vp.fitOverlays].filter(Boolean).map((o) => [o, o.visible]) };
    for (const [o] of this._saved.hidden) o.visible = false;
    this.root = new THREE.Group();
    this.root.name = 'print-simulation';
    this.bedGroup = buildBed(bed.x, bed.y, bed.z);
    this.nozzle = buildNozzle();
    this.nozzle.visible = false;
    this.overlay = new THREE.Group();
    this.overlayMat = new THREE.MeshStandardMaterial({ color: 0x8fd3ff, transparent: true, opacity: 0.16, depthWrite: false, roughness: 0.6 });
    this.overlayLine = new THREE.LineBasicMaterial({ color: 0x8fd3ff, transparent: true, opacity: 0.35, depthWrite: false });
    this.overlay.visible = this.opts.overlay;
    this.root.add(this.bedGroup, this.nozzle, this.overlay);
    vp.scene.add(this.root);
    this.material = patchFilament(new THREE.MeshStandardMaterial({ color: 0xffffff }), this.u);
    this.depth = filamentDepthMaterial(this.u);
    vp.simMode = true;
    this._frame = this._frameBox();
    vp.boundsOverride = () => this._frame.clone();
    vp.frameHooks.push(this._hook);
    this.setOptions({});
    this.frameCamera({ direction: true });
  }

  /** Back to the CAD scene: every GPU resource made here is disposed and the user's view is restored. */
  exit() {
    if (!this.active) return;
    const vp = this.vp;
    this.clearToolpath();
    vp.frameHooks = vp.frameHooks.filter((f) => f !== this._hook);
    vp.boundsOverride = null;
    vp.simMode = false;
    vp.scene.remove(this.root);
    this.root.traverse((o) => { // shared CAD geometry (the overlay) is not ours to dispose
      if (o.parent === this.overlay) return;
      o.geometry?.dispose();
      for (const m of [].concat(o.material ?? [])) { m.map?.dispose(); m.dispose(); }
    });
    this.overlayMat.dispose(); this.overlayLine.dispose(); this.material.dispose(); this.depth.dispose();
    for (const [o, v] of this._saved.hidden) o.visible = v;
    const restored = vp.layout.panes.map((p, i) => p.restorePose(this._saved.panes[i]));
    this.active = false;
    this.root = null; this.playback = null; this.tp = null; this.summary = null;
    vp.reapplyMaterials(); // restores each body's visibility and redraws the CAD shadows
    if (restored.includes(false)) vp.fitAll(); // a view changed projection meanwhile: frame the CAD scene instead
    else if (!vp.bounds().isEmpty()) vp._updateHelpers(vp.bounds()); // axes and grid were sized for the print frame
    vp.dirty();
  }

  // ---- toolpath ---------------------------------------------------------------------------------

  /** Shows a finished slice. `buffer` is the backend's binary toolpath, `summary` its JSON summary. */
  load(buffer, summary) {
    const tp = decodeToolpath(buffer);
    const first = !this.tp;
    this.clearToolpath();
    this.tp = tp; this.summary = summary;
    this.playback = new Playback(tp);
    this.playback.setSpeed(this._speed ?? 10);
    this.u.uLayerH.value = summary.layer_height ?? this._layerH ?? 0.2;
    this.filament = new Filament(tp, this.material, this.depth);
    this.root.add(this.filament.mesh);
    this._setOverlayPlacement(summary.placement);
    this._frame = this._frameBox();
    this.setOptions({});
    this._layer = -1;
    this._apply(true);
    if (first) this.frameCamera();
  }

  clearToolpath() {
    if (this.filament) { this.root?.remove(this.filament.mesh); this.filament.dispose(); this.filament = null; }
    if (this.nozzle) this.nozzle.visible = false;
    this.tp = null; this.playback = null; this.summary = null;
  }

  /** The slicer's layer height drives the bead's thickness. */
  setLayerHeight(h) { this._layerH = h; this.u.uLayerH.value = h; }

  // ---- options ----------------------------------------------------------------------------------

  setOptions(patch) {
    const o = this.opts;
    if (patch.show) Object.assign(o.show, patch.show);
    for (const k of ['mode', 'overlay', 'heat', 'completedOnly', 'frame']) if (k in patch) o[k] = patch[k];
    if (!this.active) return;
    const u = this.u;
    PALETTES[o.mode].forEach((hex, i) => { const c = new THREE.Color(hex); u.uPal.value[i].set(c.r, c.g, c.b); });
    u.uMask.value = kindMask(o.show);
    u.uHeatOn.value = o.heat ? 1 : 0;
    u.uCompletedOnly.value = o.completedOnly ? 1 : 0;
    Object.assign(this.material, SURFACE[o.mode]);
    this.overlay.visible = o.overlay;
    this.vp.shadowsChanged();
    this.vp.dirty();
  }

  /** Rebuilds the original-model overlay from the CAD bodies currently loaded (their geometry is shared, not copied). */
  rebuildOverlay() {
    if (!this.active) return;
    for (const c of [...this.overlay.children]) this.overlay.remove(c);
    for (const b of this.vp.bodies.values()) {
      if (this.vp.appearance?.[b.data.id]?.visible === false) continue;
      const m = new THREE.Mesh(b.mesh.geometry, this.overlayMat);
      m.renderOrder = 5;
      this.overlay.add(m, new THREE.LineSegments(b.edges.geometry, this.overlayLine));
    }
    this.vp.dirty();
  }

  _setOverlayPlacement(pl) {
    this.rebuildOverlay();
    if (pl) this.overlay.position.set(pl.DX, pl.DY, pl.DZ);
    this.overlay.visible = this.opts.overlay;
  }

  // ---- camera -----------------------------------------------------------------------------------

  _frameBox() {
    const bed = [this.bed.x, this.bed.y, this.bed.z];
    let box;
    if (this.opts.frame === 'bed') box = { min: [0, 0, 0], max: [bed[0], bed[1], Math.min(bed[2], 60)] };
    else if (this.tp) box = frameBox([this.tp.bbox[0], this.tp.bbox[1], this.tp.bbox[2]], [this.tp.bbox[3], this.tp.bbox[4], this.tp.bbox[5]], bed);
    else box = frameBox([bed[0] / 2 - 30, bed[1] / 2 - 30, 0], [bed[0] / 2 + 30, bed[1] / 2 + 30, 30], bed);
    return new THREE.Box3(new THREE.Vector3(...box.min), new THREE.Vector3(...box.max));
  }

  /** Frames the part (or the whole plate) in every view; `direction` also turns the active view to a 3/4 angle. */
  frameCamera({ direction = false } = {}) {
    const vp = this.vp;
    this._frame = this._frameBox();
    if (direction) {
      const pane = vp.layout.active;
      if (pane.perspective) {
        const c = this._frame.getCenter(new THREE.Vector3());
        pane.animateTo({ target: c, position: c.clone().add(new THREE.Vector3(0.62, -0.95, 0.62).multiplyScalar(300)), up: new THREE.Vector3(0, 0, 1) }, false);
      }
    }
    vp.fitAll();
    vp.shadowsChanged();
  }

  // ---- per frame --------------------------------------------------------------------------------

  _tick(now) {
    if (!this.active) return;
    const dt = this._last ? (now - this._last) / 1000 : 0;
    this._last = now;
    const pb = this.playback;
    if (!pb) return;
    if (pb.step(dt)) this._apply();
    if (this.opts.heat && this.nozzle.visible && pb.playing) { // a gentle pulse on the hot parts
      const n = this._nozzleState;
      const k = 0.18 + (n?.depositing ? 0.3 : 0.05) + 0.04 * Math.sin(now / 180);
      this.nozzle.userData.brass.emissive.setRGB(1, 0.35, 0.08).multiplyScalar(k);
      this.nozzle.userData.block.emissive.setRGB(1, 0.3, 0.05).multiplyScalar(k * 0.3);
      this.nozzle.userData.glowing = true;
      this.vp.dirty();
    } else if (this.nozzle.userData.glowing) { // idle or heat off: the hotend stops glowing
      this.nozzle.userData.brass.emissive.setRGB(0, 0, 0);
      this.nozzle.userData.block.emissive.setRGB(0, 0, 0);
      this.nozzle.userData.glowing = false;
      this.vp.dirty();
    }
  }

  /** Pushes the playback state to the GPU: no geometry is rebuilt, only uniforms and the instance count. */
  _apply(force = false) {
    const pb = this.playback;
    if (!pb || !this.filament) return;
    const u = this.u;
    u.uTime.value = pb.t;
    const [lo, hi] = pb.layerRange();
    u.uLayerRange.value.set(lo, hi);
    this.filament.mesh.geometry.instanceCount = pb.visibleCount();
    const n = pb.nozzle();
    this._nozzleState = n;
    if (n) { this.nozzle.position.set(n.x, n.y, n.z); this.nozzle.visible = true; }
    // the shadow map follows the print at a gentler rate than the camera
    const now = performance.now();
    if (force || n?.layer !== this._layer || now - this._shadowAt > 250) {
      this._layer = n?.layer ?? -1; this._shadowAt = now;
      this.vp.shadowsChanged();
    }
    this.vp.dirty();
    this.onUpdate?.(pb, n);
  }

  /** After a seek or option change made outside the clock (sliders, buttons). */
  refresh() { this._apply(true); }

  setSpeed(s) { this._speed = s; this.playback?.setSpeed(s); }

  /**
   * Plays the print for `ms` at `speed`x and reports frame intervals. Frame rate depends on the GPU, so run it
   * on the target machine (`simonPrintSim.sim.benchmark()` in the console) rather than trusting a number from another.
   */
  async benchmark({ ms = 4000, speed = 100 } = {}) {
    const pb = this.playback;
    if (!pb) throw new Error('slice something first');
    const prev = { speed: pb.speed, t: pb.t };
    pb.setSpeed(speed); pb.seek(0); pb.play();
    const times = [];
    const start = performance.now();
    let last = start;
    await new Promise((done) => {
      const frame = (now) => {
        times.push(now - last); last = now;
        this.vp.dirty();
        if (now - start < ms && pb.playing) requestAnimationFrame(frame); else done();
      };
      requestAnimationFrame(frame);
    });
    pb.pause(); pb.setSpeed(prev.speed); pb.seek(prev.t); this.refresh();
    times.shift(); // the first interval includes the wait for the first frame
    const sorted = [...times].sort((a, b) => a - b);
    const mean = times.reduce((a, b) => a + b, 0) / Math.max(times.length, 1);
    return { frames: times.length, fps: 1000 / mean, meanMs: mean, p95Ms: sorted[Math.floor(sorted.length * 0.95)] ?? 0, worstMs: sorted.at(-1) ?? 0, ...this.stats() };
  }

  /** What a test or a benchmark can observe without reading pixels. */
  stats() {
    const info = this.vp.renderer.info;
    return {
      segments: this.tp?.segCount ?? 0, drawnBeads: this.filament?.mesh.geometry.instanceCount ?? 0,
      trianglesPerBead: this.filament?.trianglesPerBead ?? 0, drawCalls: info.render.calls, triangles: info.render.triangles,
      geometries: info.memory.geometries, textures: info.memory.textures, t: this.playback?.t ?? 0,
      nozzle: this.nozzle?.visible ? this.nozzle.position.toArray() : null,
    };
  }
}
