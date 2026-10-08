// Three.js side of the material system: turns library definitions + CAD-IR appearance into
// shared MeshStandardMaterials (MeshPhysicalMaterial when a definition asks for clearcoat, transmission...). Textures and materials are created once, reference counted
// and disposed from the GPU when unused (see cache.js). Presentation only: nothing here
// touches geometry except the optional `uv` attribute, which is derived from it.
import * as THREE from 'three';
import { RoomEnvironment } from 'three/addons/RoomEnvironment.js';
import { RefCache, materialKey } from './cache.js';
import { computeUVs } from './uv.js';
import { isPhysical, physicalProps, usesTransmission } from './physical.js';

export const DEFAULT_DEF = { id: '', name: 'Default', baseColor: '#9FB2C8', metalness: 0.25, roughness: 0.45, textures: {}, textureScale: 1 };

const MAPS = { baseColor: 'map', normal: 'normalMap', roughness: 'roughnessMap', metalness: 'metalnessMap', ao: 'aoMap' };

export class MaterialManager {
  /**
   * @param library MaterialLibrary
   * @param textureUrl (materialId, file) => url
   * @param onChange called when an async texture finishes loading (request a redraw)
   */
  constructor(library, textureUrl, onChange = () => {}) {
    this.library = library;
    this.textureUrl = textureUrl;
    this.onChange = onChange;
    this.loader = new THREE.TextureLoader();
    // key: "<kind>|<url>"; one GPU texture per file no matter how many bodies share it.
    this.textures = new RefCache((key) => this._loadTexture(key), (tex) => tex.dispose());
    this.materials = new RefCache((key, def, ap) => this._build(def, ap), (m) => this._disposeMaterial(m));
    this.owned = new WeakMap(); // material -> texture keys it holds
  }

  _loadTexture(key) {
    const [kind, url] = [key.slice(0, key.indexOf('|')), key.slice(key.indexOf('|') + 1)];
    const tex = this.loader.load(url, () => this.onChange(), undefined, () => { /* missing texture: scalars still apply */ });
    tex.wrapS = tex.wrapT = THREE.RepeatWrapping;
    tex.colorSpace = kind === 'baseColor' ? THREE.SRGBColorSpace : THREE.NoColorSpace;
    tex.anisotropy = 4;
    return tex;
  }

  _build(def, ap) {
    const base = new THREE.Color(ap?.color || def.baseColor);
    const op = (typeof def.opacity === 'number' ? def.opacity : 1) * (typeof ap?.opacity === 'number' ? ap.opacity : 1);
    const props = { color: base, metalness: def.metalness, roughness: def.roughness, side: THREE.FrontSide, ...physicalProps(def) };
    const m = isPhysical(def) ? new THREE.MeshPhysicalMaterial(props) : new THREE.MeshStandardMaterial(props);
    if (usesTransmission(def, op)) { // glass: refraction instead of alpha blending
      m.transparent = false; m.opacity = 1; m.depthWrite = true; m.attenuationColor = base.clone();
      m.attenuationDistance = Math.max(def.thickness ?? 1, 1) * 4;
    } else { m.transparent = op < 1; m.opacity = op; m.depthWrite = op >= 1; }
    const held = [];
    for (const [kind, file] of Object.entries(def.textures ?? {})) {
      if (!file || !MAPS[kind]) continue;
      const key = `${kind}|${this.textureUrl(def.id, file)}`;
      m[MAPS[kind]] = this.textures.acquire(key);
      held.push(key);
      // With a base-color texture, an override color tints it; otherwise the material color is used as is.
      if (kind === 'baseColor' && !ap?.color) m.color.set(0xffffff);
    }
    this.owned.set(m, held);
    return m;
  }

  _disposeMaterial(m) {
    for (const key of this.owned.get(m) ?? []) this.textures.release(key);
    this.owned.delete(m);
    m.dispose();
  }

  /** True when the definition uses any texture map (then the mesh needs UVs). */
  static textured(def) { return Object.values(def.textures ?? {}).some(Boolean); }

  /** Acquires the shared material for (library material, appearance); pair with release(key). */
  acquire(ap) {
    const def = ap?.material_id ? this.library.resolve(ap.material_id) : DEFAULT_DEF;
    const key = materialKey({ ...def, version: this.library.version }, ap);
    return { key, def, material: this.materials.acquire(key, def, ap) };
  }
  release(key) { if (key) this.materials.release(key); }

  /** Tile length in mm for a body: the material's natural size times the per-object multiplier. */
  tileMM(def, ap) { return Math.max(0.001, (def.textureScale || 1) * (ap?.texture_scale || 1)); }

  /** Writes the `uv` attribute for a body mesh from its tessellation. */
  applyUVs(mesh, data, tile) {
    const uv = computeUVs(data.positions ?? [], data.normals ?? [], data.indices ?? [], data.faces ?? [], tile);
    mesh.geometry.setAttribute('uv', new THREE.BufferAttribute(uv, 2));
    mesh.geometry.setAttribute('uv1', new THREE.BufferAttribute(uv, 2)); // aoMap channel
  }

  dispose() { this.materials.clear(); this.textures.clear(); }
}

/** Renders a small sphere preview of a material to a data URL (cached by the caller). */
export class ThumbnailRenderer {
  constructor(textureUrl, size = 96) {
    this.size = size; this.textureUrl = textureUrl;
    this.canvas = document.createElement('canvas'); this.canvas.width = this.canvas.height = size;
    this.renderer = new THREE.WebGLRenderer({ canvas: this.canvas, alpha: true, antialias: true, preserveDrawingBuffer: true });
    this.renderer.setPixelRatio(1); this.renderer.setSize(size, size, false);
    this.renderer.toneMapping = THREE.NeutralToneMapping;
    this.scene = new THREE.Scene();
    // GL textures cannot cross contexts, so the thumbnail renderer builds its own environment.
    const pmrem = new THREE.PMREMGenerator(this.renderer);
    this.scene.environment = pmrem.fromScene(new RoomEnvironment(), 0.04).texture;
    pmrem.dispose();
    this.cam = new THREE.PerspectiveCamera(30, 1, 0.1, 10); this.cam.position.set(0, 0, 3.2);
    const l = new THREE.DirectionalLight(0xffffff, 1.4); l.position.set(1, 1, 2); this.scene.add(l);
    this.mesh = new THREE.Mesh(new THREE.SphereGeometry(1, 48, 32), new THREE.MeshStandardMaterial());
    this.scene.add(this.mesh);
    this.loader = new THREE.TextureLoader();
  }

  /** @returns {Promise<string>} PNG data URL */
  async render(def) {
    const props = { color: def.baseColor, metalness: def.metalness, roughness: def.roughness, ...physicalProps(def) };
    const m = isPhysical(def) ? new THREE.MeshPhysicalMaterial(props) : new THREE.MeshStandardMaterial(props);
    const op = typeof def.opacity === 'number' ? def.opacity : 1;
    m.transparent = op < 1; m.opacity = Math.max(op, 0.35);
    if (usesTransmission(def)) { m.transmission = 0; m.transparent = true; m.opacity = 0.4; } // no transmission pass in a 96px swatch
    const base = def.textures?.baseColor;
    if (base) {
      try {
        m.map = await this.loader.loadAsync(this.textureUrl(def.id, base));
        m.map.colorSpace = THREE.SRGBColorSpace; m.map.wrapS = m.map.wrapT = THREE.RepeatWrapping; m.map.repeat.set(2, 2);
        m.color.set(0xffffff);
      } catch { /* scalars only */ }
    }
    this.mesh.material = m;
    this.renderer.render(this.scene, this.cam);
    const url = this.canvas.toDataURL('image/png');
    m.map?.dispose(); m.dispose();
    return url;
  }
}
