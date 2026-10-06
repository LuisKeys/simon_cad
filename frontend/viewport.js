// The Three.js viewport: draws bodies from the rendering API, picks faces/edges/vertices
// and reports them as (body, kind, index/triangle) for the server to turn into selectors,
// and draws selection, hover, preview ghost, section plane and measurement overlays.
import * as THREE from 'three';
import { OrbitControls } from 'three/addons/OrbitControls.js';

const COLORS = { body: 0x9fb2c8, edge: 0x1b2330, hover: 0x8fd3ff, select: 0xffb347, ghost: 0x35d0c8, removed: 0xef6a6a };
const PICK_PX = 7;
const MAX_VERTEX_PICK = 4000;

export class Viewport {
  constructor(canvas, cubeCanvas, hooks) {
    this.hooks = hooks;
    this.canvas = canvas;
    this.renderer = new THREE.WebGLRenderer({ canvas, antialias: true, alpha: true, preserveDrawingBuffer: true });
    this.renderer.setClearColor(0x000000, 0); // let the CSS gradient show through
    this.renderer.setPixelRatio(Math.min(window.devicePixelRatio || 1, 2));
    this.renderer.localClippingEnabled = false;
    this.scene = new THREE.Scene();
    this.scene.up.set(0, 0, 1);
    this.camera = new THREE.PerspectiveCamera(40, 1, 0.1, 100000);
    this.camera.up.set(0, 0, 1);
    this.camera.position.set(120, -160, 110);
    this.controls = new OrbitControls(this.camera, canvas);
    this.controls.enableDamping = false;
    this.controls.addEventListener('change', () => this.dirty());
    this.scene.add(new THREE.HemisphereLight(0xffffff, 0x445066, 1.1));
    const key = new THREE.DirectionalLight(0xffffff, 1.6);
    key.position.set(1, -1.5, 2.5);
    this.scene.add(key);
    this.grid = new THREE.GridHelper(400, 40, 0x5a3a1a, 0x2a2018);
    this.grid.rotation.x = Math.PI / 2; // lie in XY, Z up
    this.grid.material.transparent = true; this.grid.material.opacity = 0.5;
    this.scene.add(this.grid);

    this.appearance = {};       // body id -> {color, opacity, visible} from the CAD-IR
    this.bodies = new Map();   // id -> { data, group, mesh, edges, material }
    this.ghost = new THREE.Group(); this.scene.add(this.ghost);
    this.hoverGroup = new THREE.Group(); this.scene.add(this.hoverGroup);
    this.selGroup = new THREE.Group(); this.scene.add(this.selGroup);
    this.measureGroup = new THREE.Group(); this.scene.add(this.measureGroup);
    this.filter = 'auto';
    this.measure = { on: false, pts: [] };
    this.section = { axis: '', t: 0.5 };
    this.ray = new THREE.Raycaster();
    this.hover = null;
    this.frames = 0;
    this._dirty = true;
    this.known = {};
    this.stats = { triangles: 0, bodies: 0 };

    this._bindPointer();
    this._initCube(cubeCanvas);
    new ResizeObserver(() => this.resize()).observe(canvas.parentElement);
    this.resize();
    const loop = () => { if (this._dirty) { this._dirty = false; this.render(); } requestAnimationFrame(loop); };
    requestAnimationFrame(loop);
  }

  dirty() { this._dirty = true; }

  resize() {
    const p = this.canvas.parentElement;
    const w = Math.max(1, p.clientWidth), h = Math.max(1, p.clientHeight);
    this.renderer.setSize(w, h, false);
    this.camera.aspect = w / h;
    this.camera.updateProjectionMatrix();
    this.dirty();
  }

  // ---- scene -------------------------------------------------------------------------

  /** Applies a rendering.Delta: only changed bodies arrive; removed ones are dropped. */
  applyDelta(delta) {
    const hadBodies = this.bodies.size > 0;
    for (const id of delta.removed ?? []) this._dropBody(id);
    for (const d of delta.changed ?? []) { this._dropBody(d.id); this._addBody(d); }
    this.known = { ...(delta.keys ?? {}) };
    // Appearance comes straight from the CAD-IR and is applied to existing meshes:
    // it never needs new geometry, so an appearance-only change arrives with no bodies.
    this.appearance = delta.appearance ?? {};
    for (const b of this.bodies.values()) this._applyAppearance(b);
    this.stats = { bodies: this.bodies.size, triangles: delta.stats?.triangles ?? 0 };
    this._updateSectionRange();
    if (!hadBodies && this.bodies.size > 0) this.fit();
    this.dirty();
  }

  _addBody(d) {
    const geo = new THREE.BufferGeometry();
    geo.setAttribute('position', new THREE.BufferAttribute(Float32Array.from(d.positions ?? []), 3));
    geo.setAttribute('normal', new THREE.BufferAttribute(Float32Array.from(d.normals ?? []), 3));
    geo.setIndex(new THREE.BufferAttribute(Uint32Array.from(d.indices ?? []), 1));
    geo.computeBoundingSphere();
    const material = new THREE.MeshStandardMaterial({ color: COLORS.body, metalness: 0.25, roughness: 0.45, side: THREE.FrontSide });
    const mesh = new THREE.Mesh(geo, material);
    mesh.userData.body = d.id;

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
    const b = { data: d, group, mesh, edges, material };
    this.bodies.set(d.id, b);
    this._applyAppearance(b);
  }

  /** Applies the body's CAD-IR appearance (color / opacity / visibility) to its material. */
  _applyAppearance(b) {
    const a = this.appearance?.[b.data.id] ?? {};
    const op = typeof a.opacity === 'number' ? a.opacity : 1;
    b.material.color.set(a.color || COLORS.body);
    b.material.transparent = op < 1;
    b.material.opacity = op;
    b.material.depthWrite = op >= 1;
    b.material.needsUpdate = true;
    b.group.visible = a.visible !== false;
  }

  _dropBody(id) {
    const b = this.bodies.get(id);
    if (!b) return;
    this.scene.remove(b.group);
    b.mesh.geometry.dispose(); b.edges.geometry.dispose(); b.material.dispose(); b.edges.material.dispose();
    this.bodies.delete(id);
  }

  clear() {
    for (const id of [...this.bodies.keys()]) this._dropBody(id);
    this.known = {};
    this.setGhost(null);
    this.dirty();
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
        const geo = new THREE.BufferGeometry();
        geo.setAttribute('position', new THREE.BufferAttribute(Float32Array.from(d.positions), 3));
        geo.setAttribute('normal', new THREE.BufferAttribute(Float32Array.from(d.normals), 3));
        geo.setIndex(new THREE.BufferAttribute(Uint32Array.from(d.indices), 1));
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
    }
    this.dirty();
  }

  bounds() {
    const box = new THREE.Box3();
    for (const b of this.bodies.values()) if (b.group.visible) box.expandByObject(b.group);
    return box;
  }

  fit() {
    const box = this.bounds();
    if (box.isEmpty()) return;
    const c = box.getCenter(new THREE.Vector3());
    const r = Math.max(box.getSize(new THREE.Vector3()).length() / 2, 1);
    const dir = this.camera.position.clone().sub(this.controls.target).normalize();
    if (!isFinite(dir.x)) dir.set(0.5, -0.7, 0.55).normalize();
    this.controls.target.copy(c);
    this.camera.position.copy(c).addScaledVector(dir, r / Math.sin((this.camera.fov * Math.PI) / 360) * 1.1);
    this.camera.near = Math.max(r / 1000, 0.01); this.camera.far = r * 200;
    this.camera.updateProjectionMatrix();
    this.controls.update();
    const gs = Math.pow(10, Math.ceil(Math.log10(r * 4)));
    this.grid.scale.setScalar(gs / 400);
    this.grid.position.set(c.x, c.y, box.min.z - 0.01);
    this.dirty();
  }

  viewFrom(dir) {
    const box = this.bounds();
    const c = box.isEmpty() ? this.controls.target.clone() : box.getCenter(new THREE.Vector3());
    const dist = this.camera.position.distanceTo(this.controls.target);
    this.controls.target.copy(c);
    const d = new THREE.Vector3(...dir).normalize();
    this.camera.position.copy(c).addScaledVector(d, dist);
    // Looking straight down/up the Z axis, Y becomes the screen's up direction.
    if (Math.abs(d.z) > 0.99) this.camera.up.set(0, 1, 0); else this.camera.up.set(0, 0, 1);
    this.controls.update();
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
    for (const b of this.bodies.values()) { b.material.side = on ? THREE.DoubleSide : THREE.FrontSide; b.material.needsUpdate = true; }
    this.dirty();
  }

  // ---- picking -------------------------------------------------------------------------

  _ndc(ev) {
    const r = this.canvas.getBoundingClientRect();
    return { x: ((ev.clientX - r.left) / r.width) * 2 - 1, y: -((ev.clientY - r.top) / r.height) * 2 + 1, px: ev.clientX - r.left, py: ev.clientY - r.top, w: r.width, h: r.height };
  }

  _visibleMeshes() { return [...this.bodies.values()].filter((b) => b.group.visible).map((b) => b.mesh); }

  _clipped(point) {
    const ps = this.renderer.clippingPlanes;
    return ps?.length ? ps[0].distanceToPoint(point) < 0 : false;
  }

  _faceHit(n) {
    this.ray.setFromCamera({ x: n.x, y: n.y }, this.camera);
    const hits = this.ray.intersectObjects(this._visibleMeshes(), false);
    for (const hit of hits) {
      if (this._clipped(hit.point)) continue;
      return hit;
    }
    return null;
  }

  _toScreen(v, n) {
    const p = v.clone().project(this.camera);
    return { x: ((p.x + 1) / 2) * n.w, y: ((1 - p.y) / 2) * n.h, z: p.z };
  }

  _visibleAt(point, hit) {
    // A vertex/edge point counts only when nothing opaque is in front of it.
    if (!hit) return true;
    const d = point.distanceTo(this.camera.position);
    const tol = Math.max(1e-3 * d, 0.02);
    return d <= hit.distance + tol;
  }

  /** Resolves the thing under the pointer, honouring the pick filter. */
  pick(ev) {
    if (this.bodies.size === 0) return null;
    const n = this._ndc(ev);
    const hit = this._faceHit(n);
    const f = this.filter;
    const want = (k) => f === 'auto' || f === k;

    if (want('vertex')) {
      let best = null, bd = PICK_PX;
      for (const b of this.bodies.values()) {
        if (!b.group.visible) continue;
        const vs = b.data.vertices ?? [];
        if (vs.length > MAX_VERTEX_PICK) continue;
        for (const v of vs) {
          const w = new THREE.Vector3(...v.pos);
          if (this._clipped(w)) continue;
          const s = this._toScreen(w, n);
          if (s.z < -1 || s.z > 1) continue;
          const d = Math.hypot(s.x - n.px, s.y - n.py);
          if (d < bd && this._visibleAt(w, hit)) { bd = d; best = { body: b.data.id, kind: 'vertex', index: v.index, point: w }; }
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
            const A = new THREE.Vector3(ep[a], ep[a + 1], ep[a + 2]), C = new THREE.Vector3(ep[c], ep[c + 1], ep[c + 2]);
            const sa = this._toScreen(A, n), sc = this._toScreen(C, n);
            const dx = sc.x - sa.x, dy = sc.y - sa.y, L2 = dx * dx + dy * dy;
            let t = L2 ? ((n.px - sa.x) * dx + (n.py - sa.y) * dy) / L2 : 0;
            t = Math.max(0, Math.min(1, t));
            const d = Math.hypot(sa.x + t * dx - n.px, sa.y + t * dy - n.py);
            if (d < bd) {
              const P = A.clone().lerp(C, t);
              if (this._clipped(P) || !this._visibleAt(P, hit)) continue;
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

  _bindPointer() {
    let down = null;
    const c = this.canvas;
    let rdown = null;
    c.addEventListener('pointerdown', (e) => {
      down = { x: e.clientX, y: e.clientY, t: performance.now() };
      if (e.button === 2) rdown = { x: e.clientX, y: e.clientY };
    });
    // Right-click without a drag opens the context menu; a right-drag is a pan.
    c.addEventListener('contextmenu', (e) => {
      e.preventDefault();
      const moved = rdown ? Math.hypot(e.clientX - rdown.x, e.clientY - rdown.y) : 0;
      rdown = null;
      if (moved > 4 || this.measure.on) return;
      this.hooks.onContext?.(this.pick(e), { x: e.clientX, y: e.clientY });
    });
    c.addEventListener('pointerup', (e) => {
      if (!down) return;
      const moved = Math.hypot(e.clientX - down.x, e.clientY - down.y);
      down = null;
      if (moved > 4 || e.button !== 0) return; // a drag is orbit/pan, not a pick
      const p = this.pick(e);
      if (this.measure.on) { if (p?.point) this._measureClick(p.point); return; }
      this.hooks.onPick?.(p, { shift: e.shiftKey, ctrl: e.ctrlKey || e.metaKey });
    });
    c.addEventListener('pointermove', (e) => {
      if (down || this.measure.on) return;
      const p = this.pick(e);
      const same = (!p && !this.hover) || (p && this.hover && p.body === this.hover.body && p.kind === this.hover.kind && p.index === this.hover.index);
      if (same) return;
      this.hover = p;
      this._drawOverlay(this.hoverGroup, p ? [p] : [], COLORS.hover, 0.4);
      this.hooks.onHover?.(p);
    });
    c.addEventListener('pointerleave', () => { if (this.hover) { this.hover = null; this._drawOverlay(this.hoverGroup, [], COLORS.hover); this.hooks.onHover?.(null); } });
  }

  // ---- highlight overlays ----------------------------------------------------------------

  setSelection(picks) { this._drawOverlay(this.selGroup, picks, COLORS.select, 0.6); }

  _drawOverlay(group, picks, color, opacity = 0.5) {
    for (const c of [...group.children]) { group.remove(c); c.geometry?.dispose(); c.material?.dispose(); }
    for (const p of picks) {
      const b = this.bodies.get(p.body);
      if (!b) continue;
      const kind = p.kind;
      if (kind === 'face' || kind === 'body') {
        const idx = b.data.indices;
        let slice = idx;
        if (kind === 'face') {
          const f = b.data.faces[p.index];
          if (!f) continue;
          slice = idx.slice(f.tri_start * 3, (f.tri_start + f.tri_count) * 3);
        }
        const g = new THREE.BufferGeometry();
        g.setAttribute('position', b.mesh.geometry.getAttribute('position'));
        g.setAttribute('normal', b.mesh.geometry.getAttribute('normal'));
        g.setIndex(new THREE.BufferAttribute(Uint32Array.from(slice), 1));
        const m = new THREE.Mesh(g, new THREE.MeshBasicMaterial({ color, transparent: true, opacity, depthWrite: false,
          polygonOffset: true, polygonOffsetFactor: -2, polygonOffsetUnits: -2, side: THREE.DoubleSide }));
        group.add(m);
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
    this.canvas.style.cursor = on ? 'crosshair' : '';
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

  projectToPixels(v) {
    const n = { w: this.canvas.clientWidth, h: this.canvas.clientHeight };
    return this._toScreen(v, n);
  }

  // ---- view cube -----------------------------------------------------------------------

  _initCube(canvas) {
    this.cube = { canvas };
    const r = new THREE.WebGLRenderer({ canvas, alpha: true, antialias: true });
    r.setPixelRatio(Math.min(window.devicePixelRatio || 1, 2));
    r.setSize(canvas.width, canvas.height, false);
    const scene = new THREE.Scene();
    const cam = new THREE.PerspectiveCamera(30, 1, 0.1, 50);
    cam.up.set(0, 0, 1);
    const face = (txt, rot) => {
      const cv = document.createElement('canvas'); cv.width = cv.height = 128;
      const g = cv.getContext('2d');
      g.fillStyle = '#1f1f1f'; g.fillRect(0, 0, 128, 128);
      g.strokeStyle = '#ff7a00'; g.lineWidth = 6; g.strokeRect(3, 3, 122, 122);
      g.fillStyle = '#e4e8ee'; g.font = 'bold 26px system-ui, sans-serif'; g.textAlign = 'center'; g.textBaseline = 'middle';
      g.translate(64, 64); g.rotate(rot); g.fillText(txt, 0, 2);
      const t = new THREE.CanvasTexture(cv); t.colorSpace = THREE.SRGBColorSpace;
      return new THREE.MeshBasicMaterial({ map: t });
    };
    // BoxGeometry material order: +X, -X, +Y, -Y, +Z, -Z
    const names = ['RIGHT', 'LEFT', 'BACK', 'FRONT', 'TOP', 'BOTTOM'];
    // BoxGeometry UVs assume Y-up; rotate each label so its up is world +Z (TOP/BOTTOM: +Y)
    const rots = [-Math.PI / 2, Math.PI / 2, Math.PI, 0, 0, Math.PI];
    const mesh = new THREE.Mesh(new THREE.BoxGeometry(1.6, 1.6, 1.6), names.map((n, i) => face(n, rots[i])));
    mesh.userData.dirs = [[1, 0, 0], [-1, 0, 0], [0, 1, 0], [0, -1, 0], [0, 0, 1], [0, 0, -1]];
    scene.add(mesh);
    Object.assign(this.cube, { r, scene, cam, mesh });
    canvas.addEventListener('click', (e) => {
      const b = canvas.getBoundingClientRect();
      const ray = new THREE.Raycaster();
      ray.setFromCamera({ x: ((e.clientX - b.left) / b.width) * 2 - 1, y: -((e.clientY - b.top) / b.height) * 2 + 1 }, cam);
      const hit = ray.intersectObject(mesh)[0];
      if (hit) this.viewFrom(mesh.userData.dirs[Math.floor(hit.faceIndex / 2)]);
    });
  }

  render() {
    this.renderer.render(this.scene, this.camera);
    const { r, scene, cam } = this.cube;
    const d = this.camera.position.clone().sub(this.controls.target).normalize().multiplyScalar(6);
    cam.position.copy(d); cam.up.copy(this.camera.up); cam.lookAt(0, 0, 0);
    r.render(scene, cam);
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
