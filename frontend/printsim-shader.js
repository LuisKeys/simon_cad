// GPU side of the deposited filament. Every toolpath segment is one instance of a small
// cylinder; the vertex shader stretches it along the segment, so the whole print is ONE draw call
// and nothing is rebuilt while playing: time, layer range, kind mask and colours are uniforms.
//
// Instance attributes (an interleaved view of the buffer the backend sent, no copy):
//   aStart, aEnd  segment end points (bed mm)
//   aInfo         t0 (s), duration (s), bead width (mm)
//   aMeta         kind, layer
import * as THREE from 'three';

export const PRELUDE = /* glsl */ `
attribute vec3 aStart;
attribute vec3 aEnd;
attribute vec3 aInfo;
attribute vec2 aMeta;
uniform float uTime;
uniform float uLayerH;
uniform int uMask;
uniform vec2 uLayerRange;
uniform float uHeatOn;
uniform float uCool;
uniform float uCompletedOnly;
uniform vec3 uPal[10];
varying vec3 vBead;
varying float vHeat;

// Places a vertex of the unit bead (a cylinder along X, 1 mm across) on its segment.
void bead(in vec3 p, in vec3 n, out vec3 outPos, out vec3 outNrm) {
  int kind = int(aMeta.x + 0.5);
  float width = kind == 0 ? 0.14 : aInfo.z;          // travel moves are drawn as thin lines
  float frac = aInfo.y > 0.0 ? clamp((uTime - aInfo.x) / aInfo.y, 0.0, 1.0) : 1.0;
  vec3 a = aStart;
  vec3 b = aStart + (aEnd - aStart) * frac;          // the running segment is drawn only up to the nozzle
  float len = distance(a, b);
  bool hidden = ((uMask >> kind) & 1) == 0
    || aMeta.y < uLayerRange.x || aMeta.y > uLayerRange.y
    || aInfo.x > uTime || len < 1e-4 || width <= 0.0
    || (uCompletedOnly > 0.5 && frac < 1.0);
  vBead = uPal[kind];
  vHeat = 0.0;
  if (hidden) { outPos = vec3(0.0); outNrm = vec3(0.0, 0.0, 1.0); return; }
  vec3 d = (b - a) / len;
  vec3 up = abs(d.z) > 0.95 ? vec3(1.0, 0.0, 0.0) : vec3(0.0, 0.0, 1.0);
  vec3 s = normalize(cross(up, d));
  vec3 u = cross(d, s);
  float ext = width * 0.5;                           // overlap at the joints so corners close up
  vec3 center = 0.5 * (a + b) - vec3(0.0, 0.0, 0.5 * uLayerH); // the segment's Z is the layer top
  outPos = center + d * (p.x * (len + ext)) + s * (p.y * width) + u * (p.z * uLayerH);
  outNrm = normalize(s * (n.y / width) + u * (n.z / uLayerH) + d * (n.x / (len + ext)));
  vBead *= 0.93 + 0.07 * fract(sin(aMeta.y * 12.9898) * 43758.5453); // a little life from layer to layer
  float age = uTime - (aInfo.x + aInfo.y);
  vHeat = uHeatOn * (frac < 1.0 ? 1.0 : clamp(1.0 - age / uCool, 0.0, 1.0));
}
`;

/** The uniforms shared by the lit material and the shadow-depth material. */
export function filamentUniforms() {
  return {
    uTime: { value: 0 },
    uLayerH: { value: 0.2 },
    uMask: { value: 0x3ff },
    uLayerRange: { value: new THREE.Vector2(0, 1e9) },
    uHeatOn: { value: 1 },
    uCool: { value: 2.5 },
    uCompletedOnly: { value: 0 },
    uPal: { value: Array.from({ length: 10 }, () => new THREE.Vector3(1, 1, 1)) },
  };
}

/** Makes a MeshStandardMaterial place and colour its fragments as filament beads. */
export function patchFilament(material, uniforms) {
  material.onBeforeCompile = (shader) => {
    Object.assign(shader.uniforms, uniforms);
    shader.vertexShader = shader.vertexShader
      .replace('#include <common>', '#include <common>\n' + PRELUDE)
      .replace('#include <beginnormal_vertex>', 'vec3 beadPosition;\nvec3 objectNormal;\nbead(position, normal, beadPosition, objectNormal);')
      .replace('#include <begin_vertex>', 'vec3 transformed = beadPosition;');
    shader.fragmentShader = shader.fragmentShader
      .replace('#include <common>', '#include <common>\nvarying vec3 vBead;\nvarying float vHeat;')
      .replace('#include <color_fragment>', '#include <color_fragment>\n\tdiffuseColor.rgb *= vBead;')
      .replace('#include <emissivemap_fragment>', '#include <emissivemap_fragment>\n\ttotalEmissiveRadiance += vec3(1.0, 0.36, 0.08) * vHeat * 0.9;');
  };
  material.customProgramCacheKey = () => 'simon-filament-v1';
  return material;
}

/** Shadow pass material: the same instance placement, so the shadow is the filament's, not the unit bead's. */
export function filamentDepthMaterial(uniforms) {
  const m = new THREE.MeshDepthMaterial({ depthPacking: THREE.RGBADepthPacking });
  m.onBeforeCompile = (shader) => {
    Object.assign(shader.uniforms, uniforms);
    shader.vertexShader = shader.vertexShader
      .replace('#include <common>', '#include <common>\n' + PRELUDE)
      .replace('#include <begin_vertex>', 'vec3 beadPosition;\nvec3 beadNormal;\nbead(position, normal, beadPosition, beadNormal);\nvec3 transformed = beadPosition;');
  };
  m.customProgramCacheKey = () => 'simon-filament-depth-v1';
  return m;
}
