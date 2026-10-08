// Studio lighting for the viewport: a procedural softbox environment for reflections (no image
// assets needed, so it works offline), a soft key light that casts shadows, a fill, and a
// ground plane that only shows the model's shadow.
import * as THREE from 'three';
import { shadowFrame } from './lighting-model.js';

/** Softboxes in a dark room, in world (Z-up) directions: what metals and glossy plastics reflect. */
export function studioEnvironment() {
  const scene = new THREE.Scene();
  const room = new THREE.Mesh(new THREE.BoxGeometry(40, 40, 24),
    new THREE.MeshBasicMaterial({ color: new THREE.Color(0x4a505a), side: THREE.BackSide }));
  room.position.z = 8;
  scene.add(room);
  const panel = (w, h, intensity, pos, tint = 0xffffff) => {
    const m = new THREE.Mesh(new THREE.PlaneGeometry(w, h),
      new THREE.MeshBasicMaterial({ color: new THREE.Color(tint).multiplyScalar(intensity), side: THREE.DoubleSide }));
    m.position.set(...pos);
    m.lookAt(0, 0, 0);
    scene.add(m);
  };
  panel(14, 10, 9, [10, -12, 14]);              // key softbox, front-right and above
  panel(5, 16, 4, [-16, -4, 6], 0xdfe9ff);      // cool strip on the left
  panel(5, 14, 3, [16, 6, 5], 0xfff1dd);        // warm strip on the right
  panel(16, 16, 3.5, [0, 0, 19]);               // overhead
  panel(18, 4, 5, [0, 17, 9]);                  // rim light behind
  const floor = new THREE.Mesh(new THREE.PlaneGeometry(40, 40),
    new THREE.MeshBasicMaterial({ color: new THREE.Color(0x8b9099) }));
  floor.position.z = -3.9;
  scene.add(floor);                             // bounce from below, so undersides are not black
  return scene;
}

export class StudioLighting {
  constructor(scene, renderer) {
    this.key = new THREE.DirectionalLight(0xfff6ea, 2.2);
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
