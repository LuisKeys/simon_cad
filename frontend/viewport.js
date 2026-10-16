// The Three.js viewport: draws bodies from the rendering API, picks faces/edges/vertices
// and reports them as (body, kind, index/triangle) for the server to turn into selectors,
// and draws selection, hover, preview ghost, section plane and measurement overlays.
// One renderer and one scene serve every view of the Single/Quad layout (viewlayout.js):
// each visible pane is drawn into its own rectangle of the canvas with its own camera.
import * as THREE from 'three';
import { RoomEnvironment } from 'three/addons/RoomEnvironment.js';
import { StudioLighting, studioEnvironment } from './lighting.js';
import { MaterialManager } from './materials/pbr.js';
import { WorldAxes } from './axes.js';
import { ViewportLayoutManager } from './viewlayout.js';
import { WorkspaceGrid } from './grid.js';
import { rectFrom, boundsOf, rectHit } from './boxselect-model.js';
import { renderParams } from './render-model.js';
import { PLANE_AXES, gridShown } from './grid-model.js';
import { buildBVH, raycastBVH } from './bvh.js';
import { decodeBody } from './mesh-codec.js';

const COLORS = { body: 0x9fb2c8, edge: 0x1b2330, hover: 0x8fd3ff, select: 0xff8a1f, ghost: 0x35d0c8, sketch: 0xf2a33c, removed: 0xef6a6a, path: 0x5bc0f8, pathHot: 0xff8a1f };
// Mesh.raycast replacement: walks a bounding-volume hierarchy (built on the first pick of the
// body) instead of testing every triangle. It reports what the stock raycast reports: hits
// in world space with distance, point and faceIndex, honouring the material's side.
const _inv = new THREE.Matrix4(), _ray = new THREE.Ray(), _hitPt = new THREE.Vector3();
const _sA = new THREE.Vector3(), _sC = new THREE.Vector3(), _sProj = new THREE.Vector3(); // scratch points for screen-space picking
function bvhRaycast(raycaster, intersects) {
  if (!this.material || this.material.side === THREE.BackSide) return THREE.Mesh.prototype.raycast.call(this, raycaster, intersects);
  const geo = this.geometry, index = geo.index;
  if (!index) return THREE.Mesh.prototype.raycast.call(this, raycaster, intersects);
  this.updateMatrixWorld();
  const bvh = this.userData.bvh ??= buildBVH(geo.getAttribute('position').array, index.array);
  _inv.copy(this.matrixWorld).invert();
  _ray.copy(raycaster.ray).applyMatrix4(_inv);
  const o = _ray.origin, d = _ray.direction;
  const hits = raycastBVH(bvh, [o.x, o.y, o.z], [d.x, d.y, d.z], this.material.side === THREE.FrontSide);
  for (const h of hits) {
    _hitPt.copy(d).multiplyScalar(h.t).add(o).applyMatrix4(this.matrixWorld);
    const distance = raycaster.ray.origin.distanceTo(_hitPt);
    if (distance < raycaster.near || distance > raycaster.far) continue;
    intersects.push({ distance, point: _hitPt.clone(), object: this, faceIndex: h.tri, face: null });
  }
}

const PICK_PX = 7;
const MAX_VERTEX_PICK = 4000;
const CUBE_MARGIN = 8;

export class Viewport {
  constructor(canvas, hooks) {
    this.hooks = hooks;
    this.canvas = canvas;
    this.renderer = new THREE.WebGLRenderer({ canvas, antialias: true, alpha: true, preserveDrawingBuffer: true });
    this.renderer.setClearColor(0x000000, 0); // let the CSS gradient show through
    this.renderer.setPixelRatio(Math.min(window.devicePixelRatio || 1, 2));
    this.renderer.localClippingEnabled = false;
    this.scene = new THREE.Scene();
    this.scene.up.set(0, 0, 1);
    // Image-based lighting gives metals something to reflect (a procedural studio, RoomEnvironment
    // if that fails); the key light adds soft shadows and the rest is fill.
    this.renderer.outputColorSpace = THREE.SRGBColorSpace;
    this.envMaps = {};          // name -> PMREM texture, built the first time it is chosen
    this.renderPrefs = renderParams();
    this._setEnvironment(this.renderPrefs.environment);
    this.lighting = new StudioLighting(this.scene, this.renderer);
    this.setRenderPrefs(this.renderPrefs);
    this._shadowStale = true;   // the shadow map is redrawn only when the model or the cut changes
    // The workspace grid (Settings → Workspace, or the agent's configure_grid): world-aligned,
    // with the project's spacing, extent, origin and plane.
    this.grid = new WorkspaceGrid();
    this.scene.add(this.grid);
    this.helperBox = new THREE.Box3();
    // Visual aids, not geometry: never in bounds(), picking, history or exports.
    this.axes = new WorldAxes();
    this.scene.add(this.axes);
    this.viewPrefs = { showAxes: true, showAxisLabels: true, axisOpacity: 70,
      showGizmo: true, gizmoSize: 'medium', gizmoPosition: 'bottom-left', showPlaybackStatus: true };

    this.appearance = {};       // body id -> {color, opacity, visible, material_id, texture_scale} from the CAD-IR
    this.materials = new MaterialManager(hooks.library, hooks.textureUrl, () => this.dirty());
    this.materials.maxAnisotropy = this.renderer.capabilities.getMaxAnisotropy();
    this.bodies = new Map();   // id -> { data, group, mesh, edges, material }
    this.ghost = new THREE.Group(); this.scene.add(this.ghost);
    this.hoverGroup = new THREE.Group(); this.scene.add(this.hoverGroup);
    this.selGroup = new THREE.Group(); this.scene.add(this.selGroup);
    this.measureGroup = new THREE.Group(); this.scene.add(this.measureGroup);
    this.fitOverlays = [];      // annotation groups Fit also frames (dimensions)
    this.paneHooks = [];        // called with each view just before it is drawn (per-view overlay visibility)
    this.filter = 'auto';
    this.measure = { on: false, pts: [] };
    this.section = { axis: '', t: 0.5 };
    this.ray = new THREE.Raycaster();
    this.hover = null;
    this.frames = 0;
    this._dirty = true;
    this.known = {};
    this.stats = { triangles: 0, bodies: 0 };
    // Print simulation (printsim.js) borrows this viewport: simMode hides the CAD scene and its helpers,
    // boundsOverride replaces what Fit and the shadow map frame, frameHooks run before every frame.
    this.simMode = false;
    this.boundsOverride = null;
    this.frameHooks = [];

    this._initCube();
    this.layout = new ViewportLayoutManager(canvas.parentElement, {
      dirty: () => this.dirty(),
      bounds: () => this.bounds(),
      onChange: (st) => hooks.onLayout?.(st),
      onPaneMenu: (i, el) => hooks.onPaneMenu?.(i, el),
      onActive: (pane) => hooks.onActive?.(pane),
    });
    for (const p of this.layout.panes) { this._bindPointer(p.surface); this._bindCube(p); }
    this.setViewPrefs(this.viewPrefs);
    new ResizeObserver(() => this.resize()).observe(canvas.parentElement);
    this.resize();
    const loop = (now) => {
      for (const f of this.frameHooks) f(now);
      if (this.layout.step()) this._dirty = true;
      if (this._dirty) { this._dirty = false; this.render(); }
      requestAnimationFrame(loop);
    };
    requestAnimationFrame(loop);
  }

  dirty() { this._dirty = true; }

  _setEnvironment(name) {
    try {
      if (!this.envMaps[name]) {
        const pmrem = new THREE.PMREMGenerator(this.renderer);
        try { this.envMaps[name] = (name === 'room' ? pmrem.fromScene(new RoomEnvironment(), 0.04) : pmrem.fromScene(studioEnvironment(), 0.03)).texture; }
        finally { pmrem.dispose(); }
      }
      this.envMap = this.scene.environment = this.envMaps[name];
    } catch (e) { console.warn('environment map unavailable', e); this.envMap = this.scene.environment = null; }
  }

  /** Settings → Rendering (see render-model.js): environment, exposure, tone mapping and shadows. */
  setRenderPrefs(r) {
    const p = this.renderPrefs = renderParams(r);
    this._setEnvironment(p.environment);
    this.scene.environmentIntensity = p.envFactor;
    const tone = { neutral: THREE.NeutralToneMapping, aces: THREE.ACESFilmicToneMapping, agx: THREE.AgXToneMapping, none: THREE.NoToneMapping }[p.toneMapping];
    this.renderer.toneMapping = tone;
    this.renderer.toneMappingExposure = p.exposureFactor;
    // Without an environment the direct lights have to carry the whole scene.
    this.lighting.hemi.intensity = this.envMap ? 0.2 : 1.1;
    this.lighting.key.intensity = this.envMap ? 1.4 : 2.6;
    this.lighting.setShadowSize(p.shadowMapSize);
    this._shadowsChanged();
  }

  /** The model's silhouette changed (bodies, visibility, cut, mesh view): redraw the shadow map. */
  _shadowsChanged() { this._shadowStale = true; this._dirty = true; }
  shadowsChanged() { this._shadowsChanged(); }

  /** The active view's camera and controls (tools, fit and the View menu act on the active view). */
  get camera() { return this.layout.active.camera; }
  get controls() { return this.layout.active.controls; }

  /** Centre of a body's bounding box in model (CAD) coordinates, or null. */
  bodyCenter(id) {
    const g = this.bodies.get(id)?.mesh.geometry;
    if (!g) return null;
    g.computeBoundingBox();
    const c = g.boundingBox.getCenter(new THREE.Vector3());
    return [c.x, c.y, c.z];
  }

  /** Size of a body's bounding box in model coordinates, or null. */
  bodySize(id) {
    const g = this.bodies.get(id)?.mesh.geometry;
    if (!g) return null;
    g.computeBoundingBox();
    const s = g.boundingBox.getSize(new THREE.Vector3());
    return [s.x, s.y, s.z];
  }

  resize() {
    const p = this.canvas.parentElement;
    const w = Math.max(1, p.clientWidth), h = Math.max(1, p.clientHeight);
    this.renderer.setSize(w, h, false);
    this.dirty();
  }

  // ---- scene -------------------------------------------------------------------------

  /** Applies a rendering.Delta: only changed bodies arrive; removed ones are dropped. */
  applyDelta(delta) {
    const hadBodies = this.bodies.size > 0;
    for (const id of delta.removed ?? []) this._dropBody(id);
    for (const d of delta.changed ?? []) { this._dropBody(d.id); this._addBody(d); }
    this.known = { ...(delta.keys ?? {}) };
    this._setSketches(delta.sketches ?? []);
    this._setPaths(delta.paths ?? []);
    // Appearance comes straight from the CAD-IR and is applied to existing meshes:
    // it never needs new geometry, so an appearance-only change arrives with no bodies.
    this.appearance = delta.appearance ?? {};
    for (const b of this.bodies.values()) this._applyAppearance(b);
    this.stats = { bodies: this.bodies.size, triangles: delta.stats?.triangles ?? 0 };
    this._shadowsChanged();
    this._updateSectionRange();
    if (!hadBodies && (this.bodies.size > 0 || (delta.sketches ?? []).length > 0 || (delta.paths ?? []).length > 0)) this.fitAll({ auto: true });
    this.dirty();
  }

  _addBody(d) {
    // The server packs the buffers (mesh-codec.js); the typed arrays go straight to the GPU
    // attributes and stay in b.data for picking, with no copy.
    decodeBody(d);
    const geo = new THREE.BufferGeometry();
    geo.setAttribute('position', new THREE.BufferAttribute(d.positions, 3));
    geo.setAttribute('normal', new THREE.BufferAttribute(d.normals, 3));
    geo.setIndex(new THREE.BufferAttribute(d.indices, 1));
    geo.computeBoundingSphere();
    const material = new THREE.MeshStandardMaterial({ color: COLORS.body, metalness: 0.25, roughness: 0.45, side: THREE.FrontSide });
    const mesh = new THREE.Mesh(geo, material); // placeholder until _applyAppearance assigns the shared material
    mesh.userData.body = d.id;
    mesh.raycast = bvhRaycast;
    mesh.castShadow = mesh.receiveShadow = true;

    const segs = [];
    const ep = d.edge_points ?? [];
    for (const e of d.edges ?? []) {
      for (let i = 0; i + 1 < e.pt_count; i++) {
        const a = (e.pt_start + i) * 3, b = (e.pt_start + i + 1) * 3;
        segs.push(ep[a], ep[a + 1], ep[a + 2], ep[b], ep[b + 1], ep[b + 2]);
      }
    }
    const egeo = new THREE.BufferGeometry();
    egeo.setAttribute('position', new THREE.BufferAttribute(Float32Array.from(segs), 3));
    const edges = new THREE.LineSegments(egeo, new THREE.LineBasicMaterial({ color: COLORS.edge }));
    const group = new THREE.Group();
    group.add(mesh, edges);
    this.scene.add(group);
    const b = { data: d, group, mesh, edges, material, matKey: null, uvTile: 0 };
    this.bodies.set(d.id, b);
    this._applyAppearance(b);
  }

  /**
   * Applies the body's CAD-IR appearance: visual material, color override, opacity, scale
   * and visibility. Materials are shared and cached; no geometry is rebuilt (only the
   * derived `uv` attribute, and only when a textured material's tile size changes).
   */
  _applyAppearance(b) {
    const a = this.appearance?.[b.data.id] ?? {};
    b.group.visible = a.visible !== false;
    if (!b.group.visible && !b.matKey) return; // lazy: hidden bodies never load textures
    const next = this.materials.acquire(a);
    if (next.key === b.matKey) { this.materials.release(next.key); } // same look: drop the extra ref
    else {
      if (b.matKey) this.materials.release(b.matKey); else b.material.dispose(); // placeholder is unshared
      b.matKey = next.key;
      b.material = next.material;
      b.mesh.material = next.material;
    }
    if (MaterialManager.textured(next.def)) {
      const tile = this.materials.tileMM(next.def, a);
      if (b.uvTile !== tile) { this.materials.applyUVs(b.mesh, b.data, tile); b.uvTile = tile; }
    }
  }

  /** Re-resolves every body's material (after the library changed, e.g. project open or reload). */
  reapplyMaterials() {
    for (const b of this.bodies.values()) this._applyAppearance(b);
    this._shadowsChanged();
  }

  _dropBody(id) {
    const b = this.bodies.get(id);
    if (!b) return;
    this.scene.remove(b.group);
    b.mesh.geometry.dispose(); b.edges.geometry.dispose(); b.edges.material.dispose();
    b.outlineGeo?.dispose();
    if (b.wire) { b.wire.geometry.dispose(); b.wire.material.dispose(); }
    if (b.matKey) this.materials.release(b.matKey); else b.material.dispose();
    this.bodies.delete(id);
  }

  clear() {
    for (const id of [...this.bodies.keys()]) this._dropBody(id);
    this.known = {};
    this._shadowsChanged();
    this.setGhost(null);
    this.dirty();
  }

  /** Sketch outlines: planar 2D geometry that is not (yet) material. */
  _setSketches(list) {
    if (!this.sketches) { this.sketches = new THREE.Group(); this.scene.add(this.sketches); }
    this._clearGroup(this.sketches);
    for (const s of list) this.sketches.add(this._lineSet(s.lines, COLORS.sketch));
  }

  /** Path curves: editable 3D spines (no geometry). They stay visible so they can be picked and
   *  edited; the editor hides the one it is working on and draws its own live copy. */
  _setPaths(list) {
    if (!this.pathsGroup) { this.pathsGroup = new THREE.Group(); this.scene.add(this.pathsGroup); }
    this._clearGroup(this.pathsGroup);
    this.pathData = new Map();
    for (const pth of list) {
      this.pathData.set(pth.id, pth);
      const l = this._lineSet(pth.lines, pth.id === this.pathHidden ? COLORS.pathHot : COLORS.path);
      l.userData.pathId = pth.id;
      l.visible = pth.id !== this.pathHidden;
      this.pathsGroup.add(l);
    }
    this.hooks.onPaths?.(list);
    this.dirty();
  }

  /** Hides (or shows again with null) the committed curve of the path being edited. */
  setPathHidden(id) {
    this.pathHidden = id ?? null;
    for (const c of this.pathsGroup?.children ?? []) c.visible = c.userData.pathId !== this.pathHidden;
    this.dirty();
  }

  /** The path curve under the pointer (screen-space distance, like edge picking), or null. */
  pickPath(ev, px = 8) {
    if (!this.pathData?.size) return null;
    const pane = this.layout.paneAt(ev.clientX, ev.clientY);
    const cam = pane.camera, n = this._ndc(ev, pane);
    const a = new THREE.Vector3(), b = new THREE.Vector3();
    let best = null;
    for (const [id, pth] of this.pathData) {
      if (id === this.pathHidden) continue;
      const L = pth.lines;
      for (let i = 0; i + 5 < L.length; i += 6) {
        a.set(L[i], L[i + 1], L[i + 2]); b.set(L[i + 3], L[i + 4], L[i + 5]);
        const sa = this._toScreen(a, n, cam), sb = this._toScreen(b, n, cam);
        if (sa.z > 1 && sb.z > 1) continue;
        const dx = sb.x - sa.x, dy = sb.y - sa.y, l2 = dx * dx + dy * dy;
        const t = l2 > 0 ? Math.min(1, Math.max(0, ((n.px - sa.x) * dx + (n.py - sa.y) * dy) / l2)) : 0;
        const d = Math.hypot(n.px - (sa.x + t * dx), n.py - (sa.y + t * dy));
        if (d < px && (!best || d < best.distance)) best = { pathId: id, distance: d };
      }
    }
    return best;
  }

  _clearGroup(group) {
    for (const c of [...group.children]) {
      group.remove(c);
      c.traverse?.((o) => { o.geometry?.dispose(); o.material?.dispose(); });
    }
  }

  _lineSet(lines, color) {
    const g = new THREE.BufferGeometry();
    g.setAttribute('position', new THREE.BufferAttribute(Float32Array.from(lines ?? []), 3));
    return new THREE.LineSegments(g, new THREE.LineBasicMaterial({ color }));
  }

  /** The preview overlay: proposed bodies in translucent cyan, replaced bodies faded. */
  setGhost(ghost) {
    for (const c of [...this.ghost.children]) {
      this.ghost.remove(c);
      c.traverse?.((o) => { o.geometry?.dispose(); o.material?.dispose(); });
    }
    for (const b of this.bodies.values()) { this._applyAppearance(b); b.edges.material.color.setHex(COLORS.edge); }
    this.ghostActive = !!ghost;
    if (ghost) {
      for (const id of ghost.removed ?? []) {
        const b = this.bodies.get(id);
        if (!b) continue;
        b.material.transparent = true; b.material.opacity = 0.14; b.material.depthWrite = false;
        b.edges.material.color.setHex(COLORS.removed);
      }
      for (const d of ghost.added ?? []) {
        decodeBody(d);
        const geo = new THREE.BufferGeometry();
        geo.setAttribute('position', new THREE.BufferAttribute(d.positions, 3));
        geo.setAttribute('normal', new THREE.BufferAttribute(d.normals, 3));
        geo.setIndex(new THREE.BufferAttribute(d.indices, 1));
        const m = new THREE.Mesh(geo, new THREE.MeshStandardMaterial({
          color: COLORS.ghost, transparent: true, opacity: 0.55, metalness: 0.1, roughness: 0.5, depthWrite: false }));
        const segs = [];
        for (const e of d.edges ?? []) for (let i = 0; i + 1 < e.pt_count; i++) {
          const a = (e.pt_start + i) * 3, c = (e.pt_start + i + 1) * 3;
          segs.push(d.edge_points[a], d.edge_points[a + 1], d.edge_points[a + 2], d.edge_points[c], d.edge_points[c + 1], d.edge_points[c + 2]);
        }
        const eg = new THREE.BufferGeometry();
        eg.setAttribute('position', new THREE.BufferAttribute(Float32Array.from(segs), 3));
        const g = new THREE.Group();
        g.add(m, new THREE.LineSegments(eg, new THREE.LineBasicMaterial({ color: COLORS.ghost })));
        this.ghost.add(g);
      }
      for (const sk of ghost.sketches ?? []) this.ghost.add(this._lineSet(sk.lines, COLORS.ghost));
    }
    this.dirty();
  }

  bounds() {
    const over = this.boundsOverride?.();
    if (over) return over;
    const box = new THREE.Box3();
    for (const b of this.bodies.values()) if (b.group.visible) box.expandByObject(b.group);
    if (this.sketches && this.sketches.children.length) box.expandByObject(this.sketches);
    if (this.pathsGroup && this.pathsGroup.children.length) box.expandByObject(this.pathsGroup);
    return box;
  }

  /** Fit to scene in every view at once (all four in multi-view). Overlays that
   *  annotate the model (the dimensions) are framed with it, never counted as model bounds. */
  fit() {
    const box = this.bounds();
    if (box.isEmpty()) return;
    this._updateHelpers(box);
    const framed = box.clone();
    for (const o of this.fitOverlays) if (o.visible && o.children.length) framed.expandByObject(o);
    for (const p of this.layout.panes) p.fit(framed);
    this.layout.changed();
  }

  /**
   * Fits every view, shown or not, so switching to Quad finds them framed. `auto` (the first
   * scene of a project) leaves views whose camera was restored from the saved layout alone.
   */
  fitAll({ auto = false } = {}) {
    const box = this.bounds();
    if (box.isEmpty()) return;
    this._updateHelpers(box);
    for (const p of this.layout.panes) {
      if (auto && p.restored) { p.restored = false; continue; }
      p.fit(box);
    }
    this.layout.changed();
  }

  /** Axis length follows the model (the grid has its own configured extent). */
  _updateHelpers(box) {
    const c = box.getCenter(new THREE.Vector3());
    const r = Math.max(box.getSize(new THREE.Vector3()).length() / 2, 1);
    const gs = Math.pow(10, Math.ceil(Math.log10(r * 4)));
    this.helperBox.copy(box);
    // The axes stay on the true origin and reach at least as far as the grid does.
    this.axes.setExtent(gs / 2 + Math.max(Math.abs(c.x), Math.abs(c.y), Math.abs(c.z), Math.abs(box.min.z)));
    this.dirty();
  }

  /**
   * Shows the workspace grid for one view. The perspective view shows the configured working
   * plane, through the grid origin; an orthographic view shows the plane it looks at (Top: XY,
   * Front: XZ, Right: YZ). The lines never move within their plane, so they stay on world
   * coordinates: only a nudge along the normal, away from the camera, keeps a face lying on
   * the grid plane from fighting it, and an orthographic grid is pushed behind the whole model
   * (which does not move it on screen) so it never draws over it.
   */
  _placeGrid(pane, visible) {
    const cfg = this.grid.config;
    if (!visible || !gridShown(cfg)) { this.grid.hide(); return; }
    const plane = pane.perspective ? cfg.plane : pane.config.gridPlane;
    const n = PLANE_AXES[plane][2], o = cfg.origin?.[n] ?? 0;
    const away = pane.camera.position.getComponent(n) >= o ? -1 : 1;
    const dist = pane.camera.position.distanceTo(pane.controls.target);
    const eps = Math.max(0.01, (pane.perspective ? dist : cfg.extent) * 1e-4);
    let behind = null;
    const b = this.helperBox;
    if (!pane.perspective && !b.isEmpty()) {
      const back = (away < 0 ? b.min.getComponent(n) : b.max.getComponent(n)) + away * eps - o;
      behind = away < 0 ? Math.min(back, -eps) : Math.max(back, eps);
    }
    this.grid.show(plane, { away, behind, eps });
  }

  /** The project's workspace settings (state.workspace): the grid follows them at once. */
  setWorkspace(ws) {
    this.grid.setConfig(ws?.grid);
    this.dirty();
  }

  /**
   * Looks at the model from direction `dir` (from the target toward the camera) in the active
   * view. Used by the view cube, the axis gizmo and the view menu; the transition is animated
   * unless `animate` is false or the user prefers reduced motion.
   */
  viewFrom(dir, { animate = true } = {}) {
    this.layout.viewFrom(this.layout.state.active, dir, { animate });
  }

  /**
   * Viewport display preferences (Settings → Viewport): coordinate axes, their labels and
   * opacity, and the axis gizmo.
   */
  setViewPrefs(p) {
    this.viewPrefs = { ...this.viewPrefs, ...p };
    const v = this.viewPrefs;
    this.axes.setPrefs({ opacity: Math.max(0, Math.min(100, v.axisOpacity)) / 100, labels: v.showAxisLabels });
    this.layout.setViewPrefs({ showGizmo: v.showGizmo, gizmoSize: v.gizmoSize, gizmoPosition: v.gizmoPosition });
    this.dirty();
  }

  // ---- section plane -------------------------------------------------------------------

  setSection(axis, t) {
    this.section = { axis, t };
    this._applySection();
  }

  _updateSectionRange() { if (this.section.axis) this._applySection(); }

  _applySection() {
    const { axis, t } = this.section;
    const on = !!axis;
    this.renderer.localClippingEnabled = on;
    this.renderer.clippingPlanes = [];
    if (on) {
      const box = this.bounds();
      const i = 'XYZ'.indexOf(axis);
      if (!box.isEmpty()) {
        const lo = box.min.getComponent(i), hi = box.max.getComponent(i);
        const n = new THREE.Vector3(); n.setComponent(i, -1);
        this.renderer.clippingPlanes = [new THREE.Plane(n, lo + (hi - lo) * t)];
        this.sectionValue = lo + (hi - lo) * t;
      }
    }
    this._shadowsChanged();
    for (const b of this.bodies.values()) { b.material.side = on ? THREE.DoubleSide : THREE.FrontSide; b.material.needsUpdate = true; }
    this.dirty();
  }

  // ---- picking -------------------------------------------------------------------------

  /** Pointer position in the pane's own space: NDC (x, y) and pixels (px, py of w × h). */
  _ndc(ev, pane) {
    const r = pane.surface.getBoundingClientRect();
    return { x: ((ev.clientX - r.left) / r.width) * 2 - 1, y: -((ev.clientY - r.top) / r.height) * 2 + 1, px: ev.clientX - r.left, py: ev.clientY - r.top, w: r.width, h: r.height };
  }

  _visibleMeshes() { return [...this.bodies.values()].filter((b) => b.group.visible).map((b) => b.mesh); }

  _clipped(point) {
    const ps = this.renderer.clippingPlanes;
    return ps?.length ? ps[0].distanceToPoint(point) < 0 : false;
  }

  _faceHit(n, cam) {
    this.ray.setFromCamera({ x: n.x, y: n.y }, cam);
    const hits = this.ray.intersectObjects(this._visibleMeshes(), false);
    for (const hit of hits) {
      if (this._clipped(hit.point)) continue;
      return hit;
    }
    return null;
  }

  _toScreen(v, n, cam = this.camera) {
    const p = _sProj.copy(v).project(cam);
    return { x: ((p.x + 1) / 2) * n.w, y: ((1 - p.y) / 2) * n.h, z: p.z };
  }

  _visibleAt(point, hit, cam) {
    // A vertex/edge point counts only when nothing opaque is in front of it. Distances are
    // measured from the ray origin: the eye in perspective, the near plane in orthographic.
    if (!hit) return true;
    const o = this.ray.ray.origin;
    const d = cam.isOrthographicCamera ? point.clone().sub(o).dot(this.ray.ray.direction) : point.distanceTo(o);
    const tol = Math.max(1e-3 * d, 0.02);
    return d <= hit.distance + tol;
  }

  /** Resolves the thing under the pointer, honouring the pick filter. The ray comes from the
   *  camera of the view the pointer is in. */
  pick(ev) {
    if (this.simMode || this.bodies.size === 0) return null;
    const pane = this.layout.paneAt(ev.clientX, ev.clientY);
    const cam = pane.camera;
    const n = this._ndc(ev, pane);
    const hit = this._faceHit(n, cam);
    const f = this.filter;
    const want = (k) => f === 'auto' || f === k;

    if (want('vertex')) {
      let best = null, bd = PICK_PX;
      for (const b of this.bodies.values()) {
        if (!b.group.visible) continue;
        const vs = b.data.vertices ?? [];
        if (vs.length > MAX_VERTEX_PICK) continue;
        for (const v of vs) {
          const w = _sA.set(v.pos[0], v.pos[1], v.pos[2]);
          if (this._clipped(w)) continue;
          const s = this._toScreen(w, n, cam);
          if (s.z < -1 || s.z > 1) continue;
          const d = Math.hypot(s.x - n.px, s.y - n.py);
          if (d < bd && this._visibleAt(w, hit, cam)) { bd = d; best = { body: b.data.id, kind: 'vertex', index: v.index, point: w.clone() }; }
        }
      }
      if (best) return best;
    }
    if (want('edge')) {
      let best = null, bd = PICK_PX - 1;
      for (const b of this.bodies.values()) {
        if (!b.group.visible) continue;
        const ep = b.data.edge_points ?? [];
        for (const e of b.data.edges ?? []) {
          for (let i = 0; i + 1 < e.pt_count; i++) {
            const a = (e.pt_start + i) * 3, c = (e.pt_start + i + 1) * 3;
            const A = _sA.set(ep[a], ep[a + 1], ep[a + 2]), C = _sC.set(ep[c], ep[c + 1], ep[c + 2]);
            const sa = this._toScreen(A, n, cam), sc = this._toScreen(C, n, cam);
            const dx = sc.x - sa.x, dy = sc.y - sa.y, L2 = dx * dx + dy * dy;
            let t = L2 ? ((n.px - sa.x) * dx + (n.py - sa.y) * dy) / L2 : 0;
            t = Math.max(0, Math.min(1, t));
            const d = Math.hypot(sa.x + t * dx - n.px, sa.y + t * dy - n.py);
            if (d < bd) {
              const P = A.clone().lerp(C, t);
              if (this._clipped(P) || !this._visibleAt(P, hit, cam)) continue;
              bd = d; best = { body: b.data.id, kind: 'edge', index: e.index, point: P };
            }
          }
        }
      }
      if (best) return best;
    }
    if (!hit) return null;
    const body = hit.object.userData.body;
    if (f === 'body') return { body, kind: 'body', index: -1, point: hit.point };
    if (!want('face')) return null;
    const faces = this.bodies.get(body).data.faces;
    return { body, kind: 'face', index: faceOfTriangle(faces, hit.faceIndex), triangle: hit.faceIndex, point: hit.point };
  }

  // ---- box selection ---------------------------------------------------------------------

  setBoxMode(on) {
    this.boxMode = on;
    for (const p of this.layout.panes) { p.controls.enabled = !on; p.surface.style.cursor = on ? 'crosshair' : ''; }
    if (!on) this._boxEl?.remove(), this._boxEl = null;
  }

  /** Ids of the visible bodies whose projected bounding box satisfies the rectangle (pane pixels). */
  _bodiesInRect(rect, pane) {
    const r = pane.surface.getBoundingClientRect();
    const n = { w: r.width, h: r.height };
    const ids = [];
    for (const b of this.bodies.values()) {
      if (!b.group.visible) continue;
      const geo = b.mesh.geometry;
      if (!geo.boundingBox) geo.computeBoundingBox();
      b.mesh.updateWorldMatrix(true, false);
      const { min, max } = geo.boundingBox;
      const pts = [];
      for (let i = 0; i < 8; i++) {
        const w = new THREE.Vector3(i & 1 ? max.x : min.x, i & 2 ? max.y : min.y, i & 4 ? max.z : min.z).applyMatrix4(b.mesh.matrixWorld);
        const s = this._toScreen(w, n, pane.camera);
        if (s.z > 1) continue; // behind the camera
        pts.push(s);
      }
      if (rectHit(boundsOf(pts), rect)) ids.push(b.data.id);
    }
    return ids;
  }

  _bindBox(c) {
    let start = null;
    c.addEventListener('pointerdown', (e) => {
      if (!this.boxMode || e.button !== 0 || !e.isPrimary) return;
      const pane = this.layout.paneAt(e.clientX, e.clientY);
      const r = pane.surface.getBoundingClientRect();
      start = { pane, r, x: e.clientX - r.left, y: e.clientY - r.top };
    });
    c.addEventListener('pointermove', (e) => {
      if (!start) return;
      const cur = { x: e.clientX - start.r.left, y: e.clientY - start.r.top };
      if (Math.hypot(cur.x - start.x, cur.y - start.y) <= 4 && !this._boxEl) return;
      const q = rectFrom(start, cur);
      if (!this._boxEl) { this._boxEl = document.createElement('div'); start.pane.surface.append(this._boxEl); }
      this._boxEl.className = `box-select${q.crossing ? ' crossing' : ''}`;
      Object.assign(this._boxEl.style, { left: `${q.x0}px`, top: `${q.y0}px`, width: `${q.x1 - q.x0}px`, height: `${q.y1 - q.y0}px` });
    });
    c.addEventListener('pointerup', (e) => {
      if (!start) return;
      const s = start; start = null;
      this._boxEl?.remove(); this._boxEl = null;
      const cur = { x: e.clientX - s.r.left, y: e.clientY - s.r.top };
      if (e.button !== 0 || Math.hypot(cur.x - s.x, cur.y - s.y) <= 4) return;
      const ids = this._bodiesInRect(rectFrom(s, cur), s.pane);
      this.hooks.onBoxSelect?.(ids, { shift: e.shiftKey, ctrl: e.ctrlKey || e.metaKey });
    });
    c.addEventListener('pointercancel', () => { start = null; this._boxEl?.remove(); this._boxEl = null; });
  }

  _bindPointer(c) {
    this._bindBox(c);
    let down = null;
    let rdown = null;
    let lp = null;                       // long-press timer (touch / pen): the touch equivalent of right-click
    const cancelLp = () => { clearTimeout(lp); lp = null; };
    c.addEventListener('pointerdown', (e) => {
      down = { x: e.clientX, y: e.clientY, t: performance.now() };
      if (e.button === 2) rdown = { x: e.clientX, y: e.clientY };
      cancelLp();
      if (e.pointerType !== 'mouse' && e.isPrimary && !this.measure.on) {
        const at = { clientX: e.clientX, clientY: e.clientY };
        lp = setTimeout(() => {
          lp = null;
          if (!down) return;
          down = null; this._lpAt = performance.now();
          this.hooks.onContext?.(this.pick(at), { x: at.clientX, y: at.clientY });
        }, 500);
      } else if (!e.isPrimary) cancelLp();
    });
    // Right-click without a drag opens the context menu; a right-drag is a pan.
    c.addEventListener('contextmenu', (e) => {
      e.preventDefault();
      if (performance.now() - (this._lpAt ?? -1e9) < 900) return; // already handled by the long-press
      const moved = rdown ? Math.hypot(e.clientX - rdown.x, e.clientY - rdown.y) : 0;
      rdown = null;
      if (moved > 4 || this.measure.on) return;
      this.hooks.onContext?.(this.pick(e), { x: e.clientX, y: e.clientY });
    });
    c.addEventListener('pointercancel', cancelLp);
    c.addEventListener('pointerup', (e) => {
      cancelLp();
      if (!down) return;
      const moved = Math.hypot(e.clientX - down.x, e.clientY - down.y);
      down = null;
      if (moved > 4 || e.button !== 0) return; // a drag is orbit/pan, not a pick
      const p = this.pick(e);
      if (this.measure.on) { if (p?.point) this._measureClick(p.point); return; }
      if (!this.boxMode) {
        // A click on a path curve opens it in the path editor. A swept solid hides its path
        // inside it, so Alt+click on the solid opens the path it follows instead.
        const pp = !p ? this.pickPath(e) : null;
        if (pp && this.hooks.onPathPick?.(pp)) return;
        if (p && e.altKey && this.hooks.onBodyPathPick?.(p.body)) return;
      }
      this.hooks.onPick?.(p, { shift: e.shiftKey, ctrl: e.ctrlKey || e.metaKey });
    });
    c.addEventListener('pointermove', (e) => {
      if (down && lp && Math.hypot(e.clientX - down.x, e.clientY - down.y) > 6) cancelLp();
      if (down || this.measure.on) return;
      // Picking walks edges and vertices, so do it at most once per frame, for the latest position.
      this._hoverAt = { clientX: e.clientX, clientY: e.clientY };
      if (this._hoverRaf) return;
      this._hoverRaf = requestAnimationFrame(() => {
        this._hoverRaf = 0;
        const at = this._hoverAt;
        if (!at || down || this.measure.on) return;
        const p = this.pick(at);
        const same = (!p && !this.hover) || (p && this.hover && p.body === this.hover.body && p.kind === this.hover.kind && p.index === this.hover.index);
        if (same) return;
        this.hover = p;
        this._drawOverlay(this.hoverGroup, p ? [p] : [], COLORS.hover, 0.4);
        this.hooks.onHover?.(p);
      });
    });
    c.addEventListener('pointerleave', () => {
      if (this._hoverRaf) { cancelAnimationFrame(this._hoverRaf); this._hoverRaf = 0; }
      this._hoverAt = null;
      if (this.hover) { this.hover = null; this._drawOverlay(this.hoverGroup, [], COLORS.hover); this.hooks.onHover?.(null); } });
  }

  // ---- highlight overlays ----------------------------------------------------------------

  setSelection(picks) { this._drawOverlay(this.selGroup, picks, COLORS.select, 0.22, true); }

  _drawOverlay(group, picks, color, opacity = 0.5, outline = false) {
    for (const c of [...group.children]) {
      group.remove(c);
      // A highlight borrows the body's vertex (and, for a whole body, index) buffers. Detach
      // them first: dispose() would delete the GPU copies the body is still drawn from and
      // force a re-upload on the next frame, on every hover change.
      if (c.userData.borrowed) {
        c.geometry.deleteAttribute('position'); c.geometry.deleteAttribute('normal');
        if (c.userData.borrowedIndex) c.geometry.index = null;
      }
      if (!c.userData.cachedGeometry) c.geometry?.dispose();
      c.material?.dispose();
    }
    for (const p of picks) {
      const b = this.bodies.get(p.body);
      if (!b) continue;
      const kind = p.kind;
      if (kind === 'face' || kind === 'body') {
        const whole = kind === 'body';
        let slice = null;
        if (!whole) {
          const f = b.data.faces[p.index];
          if (!f) continue;
          slice = b.data.indices.slice(f.tri_start * 3, (f.tri_start + f.tri_count) * 3);
        }
        const g = new THREE.BufferGeometry();
        g.setAttribute('position', b.mesh.geometry.getAttribute('position'));
        g.setAttribute('normal', b.mesh.geometry.getAttribute('normal'));
        // A whole body shares its own index buffer: no per-redraw copy of every triangle.
        g.setIndex(whole ? b.mesh.geometry.index : new THREE.BufferAttribute(Uint32Array.from(slice), 1));
        const m = new THREE.Mesh(g, new THREE.MeshBasicMaterial({ color, transparent: true, opacity, depthWrite: false,
          polygonOffset: true, polygonOffsetFactor: -2, polygonOffsetUnits: -2, side: THREE.DoubleSide }));
        m.userData.borrowed = true;
        m.userData.borrowedIndex = whole;
        group.add(m);
        if (outline) {
          // Selected geometry reads as an orange outline over a light tint, not a solid fill.
          // A whole body's outline never changes, so it is computed once per body (EdgesGeometry
          // walks every triangle) and freed with the body.
          const eg = whole ? (b.outlineGeo ??= new THREE.EdgesGeometry(g, 25)) : new THREE.EdgesGeometry(g, 25);
          const ol = new THREE.LineSegments(eg,
            new THREE.LineBasicMaterial({ color, transparent: true, opacity: 0.95, depthTest: false }));
          ol.userData.cachedGeometry = whole;
          ol.renderOrder = 9;
          group.add(ol);
        }
      } else if (kind === 'edge') {
        const e = b.data.edges[p.index];
        if (!e) continue;
        const pts = [];
        for (let i = 0; i < e.pt_count; i++) { const o = (e.pt_start + i) * 3; pts.push(new THREE.Vector3(b.data.edge_points[o], b.data.edge_points[o + 1], b.data.edge_points[o + 2])); }
        const g = new THREE.BufferGeometry().setFromPoints(pts);
        const l = new THREE.Line(g, new THREE.LineBasicMaterial({ color, depthTest: false }));
        l.renderOrder = 10;
        group.add(l);
        // Lines are 1px in WebGL; a translucent tube makes the highlighted edge easy to see.
        if (pts.length > 1) {
          const curve = new THREE.CatmullRomCurve3(pts, false, 'catmullrom', 0);
          const r = Math.max(b.mesh.geometry.boundingSphere.radius * 0.006, 0.05);
          const t = new THREE.Mesh(new THREE.TubeGeometry(curve, Math.max(pts.length * 2, 2), r, 6, false),
            new THREE.MeshBasicMaterial({ color, depthTest: false, transparent: true, opacity: 0.8 }));
          t.renderOrder = 11;
          group.add(t);
        }
      } else if (kind === 'vertex') {
        const v = b.data.vertices[p.index];
        if (!v) continue;
        const r = Math.max(b.mesh.geometry.boundingSphere.radius * 0.015, 0.1);
        const s = new THREE.Mesh(new THREE.SphereGeometry(r, 12, 8), new THREE.MeshBasicMaterial({ color, depthTest: false }));
        s.position.set(...v.pos); s.renderOrder = 12;
        group.add(s);
      }
    }
    this.dirty();
  }

  // ---- measurement ---------------------------------------------------------------------

  setMeasure(on) {
    this.measure = { on, pts: [] };
    this._drawMeasure();
    for (const p of this.layout.panes) p.surface.style.cursor = on ? 'crosshair' : '';
  }

  _measureClick(p) {
    if (this.measure.pts.length >= 2) this.measure.pts = [];
    this.measure.pts.push(p.clone());
    this._drawMeasure();
  }

  _drawMeasure() {
    for (const c of [...this.measureGroup.children]) { this.measureGroup.remove(c); c.geometry?.dispose(); c.material?.dispose(); }
    const pts = this.measure.pts;
    const r = Math.max(this.bounds().getSize(new THREE.Vector3()).length() * 0.006, 0.1);
    for (const p of pts) {
      const s = new THREE.Mesh(new THREE.SphereGeometry(r, 10, 8), new THREE.MeshBasicMaterial({ color: 0xffffff, depthTest: false }));
      s.position.copy(p); s.renderOrder = 20; this.measureGroup.add(s);
    }
    if (pts.length === 2) {
      const l = new THREE.Line(new THREE.BufferGeometry().setFromPoints(pts), new THREE.LineBasicMaterial({ color: 0xffffff, depthTest: false }));
      l.renderOrder = 20; this.measureGroup.add(l);
    }
    this.hooks.onMeasure?.(pts.length === 2 ? { distance: pts[0].distanceTo(pts[1]), mid: pts[0].clone().lerp(pts[1], 0.5),
      d: pts[1].clone().sub(pts[0]) } : null);
    this.dirty();
  }

  /** Projects a model point with the active view's camera to pixels relative to the canvas. */
  projectToPixels(v) {
    const pane = this.layout.active, r = pane.rect ?? { x: 0, y: 0, w: this.canvas.clientWidth, h: this.canvas.clientHeight };
    const s = this._toScreen(v, { w: r.w, h: r.h }, pane.camera);
    return { x: s.x + r.x, y: s.y + r.y, z: s.z };
  }

  // ---- view cube -----------------------------------------------------------------------

  // The cube is drawn by the main renderer into a corner of each perspective view (no second
  // WebGL context); every view has its own hit area for clicks.
  _initCube() {
    const scene = new THREE.Scene();
    const cam = new THREE.PerspectiveCamera(30, 1, 0.1, 50);
    cam.up.set(0, 0, 1);
    const face = (txt, rot) => {
      const cv = document.createElement('canvas'); cv.width = cv.height = 128;
      const g = cv.getContext('2d');
      g.fillStyle = '#242424'; g.fillRect(0, 0, 128, 128);
      g.strokeStyle = '#4a4a4a'; g.lineWidth = 4; g.strokeRect(2, 2, 124, 124);
      g.fillStyle = '#e4e8ee'; g.font = 'bold 26px system-ui, sans-serif'; g.textAlign = 'center'; g.textBaseline = 'middle';
      g.translate(64, 64); g.rotate(rot); g.fillText(txt, 0, 2);
      const t = new THREE.CanvasTexture(cv); t.colorSpace = THREE.SRGBColorSpace;
      return new THREE.MeshBasicMaterial({ map: t, toneMapped: false });
    };
    // BoxGeometry material order: +X, -X, +Y, -Y, +Z, -Z
    const names = ['RIGHT', 'LEFT', 'BACK', 'FRONT', 'TOP', 'BOTTOM'];
    // BoxGeometry UVs assume Y-up; rotate each label so its up is world +Z (TOP/BOTTOM: +Y)
    const rots = [-Math.PI / 2, Math.PI / 2, Math.PI, 0, 0, Math.PI];
    const mesh = new THREE.Mesh(new THREE.BoxGeometry(1.6, 1.6, 1.6), names.map((n, i) => face(n, rots[i])));
    mesh.userData.dirs = [[1, 0, 0], [-1, 0, 0], [0, 1, 0], [0, -1, 0], [0, 0, 1], [0, 0, -1]];
    scene.add(mesh);
    this.cube = { scene, cam, mesh };
  }

  _orientCube(camera) {
    const { cam } = this.cube;
    cam.quaternion.copy(camera.quaternion);
    cam.position.set(0, 0, 6).applyQuaternion(cam.quaternion);
    cam.updateMatrixWorld();
  }

  _bindCube(pane) {
    pane.cubeEl.addEventListener('click', (e) => {
      const b = pane.cubeEl.getBoundingClientRect();
      this._orientCube(pane.camera);
      const ray = new THREE.Raycaster();
      ray.setFromCamera({ x: ((e.clientX - b.left) / b.width) * 2 - 1, y: -((e.clientY - b.top) / b.height) * 2 + 1 }, this.cube.cam);
      const hit = ray.intersectObject(this.cube.mesh)[0];
      if (hit) this.layout.viewFrom(pane.index, this.cube.mesh.userData.dirs[Math.floor(hit.faceIndex / 2)]);
    });
  }

  /** Recolors the grid for the UI theme (the canvas is transparent over the CSS background). */
  setTheme(theme) {
    this.grid.setTheme(theme);
    this.dirty();
  }

  /** Mesh view: shows each body's triangle mesh and hides the grid, B-rep edges and sketch outlines. */
  setMeshOnly(on) {
    this.meshOnly = !!on;
    this._shadowsChanged();
    this.dirty();
  }

  render() {
    const mo = !!this.meshOnly, sim = this.simMode;
    this.axes.visible = !mo && !sim && this.viewPrefs.showAxes;
    if (this.sketches) this.sketches.visible = !mo && !sim;
    for (const b of this.bodies.values()) {
      if (sim) { b.group.visible = false; continue; } // the printer scene replaces the CAD scene; geometry stays loaded
      b.edges.visible = !mo;
      b.mesh.visible = !mo; // picking raycasts the mesh regardless of visibility
      if (mo && !b.wire) { // built lazily, once per body
        b.wire = new THREE.LineSegments(new THREE.WireframeGeometry(b.mesh.geometry),
          new THREE.LineBasicMaterial({ color: 0x8fb4d8, transparent: true, opacity: 0.6 }));
        b.group.add(b.wire);
      }
      if (b.wire) b.wire.visible = mo;
    }
    const r = this.renderer;
    if (this._shadowStale) {
      this._shadowStale = false;
      this.lighting.fitTo(this.bounds());
      r.shadowMap.needsUpdate = true;
    }
    this.lighting.floor.visible = this.lighting.framed && this.lighting.shadowsOn && !mo && !sim; // the mesh view is a bare wireframe
    const H = this.canvas.clientHeight || 1, W = this.canvas.clientWidth || 1;
    r.setScissorTest(true);
    r.setViewport(0, 0, W, H); r.setScissor(0, 0, W, H);
    r.clear();
    for (const pane of this.layout.visible()) {
      const { x, y, w, h } = pane.rect;
      if (w < 1 || h < 1) continue;
      const by = H - y - h; // WebGL rectangles start at the bottom left
      r.setViewport(x, by, w, h); r.setScissor(x, by, w, h);
      this._placeGrid(pane, !mo && !sim);
      for (const hook of this.paneHooks) hook(pane);
      if (this.axes.visible) this.axes.update(pane.camera, h);
      r.render(this.scene, pane.camera);
      // The cube and the gizmo take the view's camera orientation as is: one source of truth,
      // so they always agree with each other and with the axes in the scene.
      if (pane.perspective && pane.cubeSize) {
        const s = pane.cubeSize, cx = x + w - CUBE_MARGIN - s, cy = H - (y + CUBE_MARGIN) - s;
        this._orientCube(pane.camera);
        const clip = r.clippingPlanes;
        r.clippingPlanes = []; // the section plane cuts the model, not the cube
        r.setViewport(cx, cy, s, s); r.setScissor(cx, cy, s, s);
        r.autoClear = false; r.clearDepth();
        r.render(this.cube.scene, this.cube.cam);
        r.autoClear = true;
        r.clippingPlanes = clip;
      }
      pane.gizmo.update(pane.camera);
    }
    r.setScissorTest(false);
    this.frames++;
    this.hooks.onFrame?.();
  }
}

/** Triangle index → face index (faces are contiguous triangle ranges, sorted). */
export function faceOfTriangle(faces, tri) {
  let lo = 0, hi = faces.length - 1;
  while (lo < hi) {
    const mid = (lo + hi) >> 1;
    if (faces[mid].tri_start + faces[mid].tri_count > tri) hi = mid; else lo = mid + 1;
  }
  return lo;
}
