// Studio lighting for the viewport: a procedural softbox environment for reflections (no image
// assets needed, so it works offline), a soft key light that casts shadows, a fill, and a
// ground plane that only shows the model's shadow.
import * as THREE from 'three';
import { shadowFrame } from './lighting-model.js';

/** Softboxes in a dark room, in world (Z-up) directions: what metals and glossy plastics reflect. */
export function studioEnvironment() {
  const scene = new THREE.Scene();
  // A sky-to-ground gradient dome: flat metal faces reflect one direction of it, so the
  // dome must be mid-bright everywhere or polished parts come out pure black or pure white.
  const dome = new THREE.SphereGeometry(30, 32, 16);
  const pos = dome.attributes.position, col = [];
  const top = new THREE.Color(0xe4ebf5).multiplyScalar(1.1), mid = new THREE.Color(0x9aa3b0), low = new THREE.Color(0x4c5058);
  for (let i = 0; i < pos.count; i++) {
    const t = pos.getZ(i) / 30; // -1 (down) .. 1 (up)
    col.push(...(t > 0 ? mid.clone().lerp(top, t) : mid.clone().lerp(low, -t * 1.2 > 1 ? 1 : -t * 1.2)).toArray());
  }
  dome.setAttribute('color', new THREE.Float32BufferAttribute(col, 3));
  scene.add(new THREE.Mesh(dome, new THREE.MeshBasicMaterial({ vertexColors: true, side: THREE.BackSide })));
  const panel = (w, h, intensity, pos, tint = 0xffffff) => {
    const m = new THREE.Mesh(new THREE.PlaneGeometry(w, h),
      new THREE.MeshBasicMaterial({ color: new THREE.Color(tint).multiplyScalar(intensity), side: THREE.DoubleSide }));
    m.position.set(...pos);
    m.lookAt(0, 0, 0);
    scene.add(m);
  };
  panel(16, 12, 3.2, [10, -12, 14]);            // key softbox, front-right and above
  panel(6, 18, 2.2, [-16, -4, 6], 0xdfe9ff);    // cool strip on the left
  panel(6, 16, 2, [16, 6, 5], 0xfff1dd);        // warm strip on the right
  panel(18, 18, 1.6, [0, 0, 19]);               // overhead
  panel(20, 5, 2.4, [0, 17, 9]);                // rim light behind
  return scene;
}

export class StudioLighting {
  constructor(scene, renderer) {
    this.key = new THREE.DirectionalLight(0xfff6ea, 1.4);
    this.key.castShadow = true;
    this.key.shadow.mapSize.set(2048, 2048);
    this.key.shadow.bias = -0.0004;
    this.fill = new THREE.DirectionalLight(0xdfe9ff, 0.5);
    this.fill.position.set(-2, 1.5, 1);
    this.hemi = new THREE.HemisphereLight(0xffffff, 0x445066, 0.2);
    this.floor = new THREE.Mesh(new THREE.PlaneGeometry(1, 1),
      new THREE.ShadowMaterial({ opacity: 0.3, depthWrite: false, polygonOffset: true, polygonOffsetFactor: 2, polygonOffsetUnits: 2 }));
    this.floor.receiveShadow = true;
    this.floor.renderOrder = -1;
    this.floor.visible = false;
    this.framed = false;
    scene.add(this.key, this.key.target, this.fill, this.fill.target, this.hemi, this.floor);
    renderer.shadowMap.enabled = true;
    renderer.shadowMap.type = THREE.PCFSoftShadowMap;
    renderer.shadowMap.autoUpdate = false; // lights are fixed in the world: redraw only when the model changes
  }

  /** Frames the key light's shadow camera and the ground catcher on the model's bounds. */
  fitTo(box) {
    const f = box.isEmpty() ? null : shadowFrame(box.min.toArray(), box.max.toArray());
    this.framed = !!f;
    if (!f) { this.floor.visible = false; return; }
    const cam = this.key.shadow.camera;
    cam.left = -f.half; cam.right = f.half; cam.top = f.half; cam.bottom = -f.half;
    cam.near = f.near; cam.far = f.far;
    cam.updateProjectionMatrix();
    this.key.shadow.normalBias = f.normalBias;
    this.key.position.set(...f.position);
    this.key.target.position.set(...f.center);
    this.fill.position.set(f.center[0] - 2 * f.radius, f.center[1] + 1.5 * f.radius, f.center[2] + f.radius);
    this.fill.target.position.set(...f.center);
    this.floor.scale.setScalar(f.floorSize);
    this.floor.position.set(f.center[0], f.center[1], f.floorZ);
  }
}
