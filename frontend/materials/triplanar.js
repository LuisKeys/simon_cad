// Triplanar texturing for curved faces. CAD meshes carry procedural UVs (uv.js): exact for
// planes, cylinders and cones, but a sphere, torus or free-form face has no distortion-free
// UV, and picking one axis per vertex smears the texture where the axes change. Those faces
// get a per-vertex `tpFlag` of 1 instead, and the material samples its maps along the three
// object axes and blends them by the surface normal. Everything else keeps its UVs.
//
// Bodies are drawn untransformed (object space = world space = CAD mm), so the object-space
// position divided by the tile length is the texture coordinate.

const DECLS = `
attribute float tpFlag;
varying float vTpFlag;
varying vec3 vTpPos;
varying vec3 vTpNrm;
`;
const FRAG_DECLS = `
varying float vTpFlag;
varying vec3 vTpPos;
varying vec3 vTpNrm;
uniform float tpScale;
`;
// After the sampler declarations (the helpers use normalMap).
const HELPERS = `
vec3 tpWeights() {
  vec3 w = pow( abs( normalize( vTpNrm ) ), vec3( 4.0 ) );
  return w / ( w.x + w.y + w.z );
}
vec4 tpSample( sampler2D t, vec2 uv ) {
  vec3 w = tpWeights();
  vec3 p = vTpPos * tpScale;
  vec4 tri = texture2D( t, p.yz ) * w.x + texture2D( t, p.xz ) * w.y + texture2D( t, p.xy ) * w.z;
  return mix( texture2D( t, uv ), tri, step( 0.5, vTpFlag ) );
}
#ifdef USE_NORMALMAP
vec3 tpNormalView() {
  vec3 w = tpWeights();
  vec3 p = vTpPos * tpScale;
  vec3 n = normalize( vTpNrm );
  vec2 tx = texture2D( normalMap, p.yz ).xy * 2.0 - 1.0;
  vec2 ty = texture2D( normalMap, p.xz ).xy * 2.0 - 1.0;
  vec2 tz = texture2D( normalMap, p.xy ).xy * 2.0 - 1.0;
  vec3 nx = vec3( n.x, n.y + tx.x, n.z + tx.y );
  vec3 ny = vec3( n.x + ty.x, n.y, n.z + ty.y );
  vec3 nz = vec3( n.x + tz.x, n.y + tz.y, n.z );
  vec3 nw = normalize( nx * w.x + ny * w.y + nz * w.z );
  return normalize( ( viewMatrix * vec4( nw, 0.0 ) ).xyz );
}
#endif
`;

/** [chunk name, text to find inside it, replacement]. Checked against the vendored three.js in tests. */
export const CHUNK_PATCHES = [
  ['map_fragment', 'texture2D( map, vMapUv )', 'tpSample( map, vMapUv )'],
  ['roughnessmap_fragment', 'texture2D( roughnessMap, vRoughnessMapUv )', 'tpSample( roughnessMap, vRoughnessMapUv )'],
  ['normal_fragment_maps', 'normal = normalize( tbn * mapN );', 'normal = normalize( mix( tbn * mapN, tpNormalView(), step( 0.5, vTpFlag ) ) );'],
];

/** Rewrites a fragment shader's chunk includes with the triplanar variants. */
export function patchFragment(src, chunks) {
  let out = src.replace('#include <common>', '#include <common>\n' + FRAG_DECLS)
    .replace('#include <normalmap_pars_fragment>', '#include <normalmap_pars_fragment>\n' + HELPERS);
  for (const [name, find, repl] of CHUNK_PATCHES) {
    if (!chunks[name]?.includes(find)) throw new Error(`triplanar: three.js chunk ${name} changed`);
    out = out.replace(`#include <${name}>`, chunks[name].replace(find, repl));
  }
  return out;
}

export function patchVertex(src) {
  return src.replace('#include <common>', '#include <common>\n' + DECLS)
    .replace('#include <begin_vertex>', '#include <begin_vertex>\nvTpFlag = tpFlag; vTpPos = position; vTpNrm = normal;');
}

/** Makes `material` sample its maps triplanarly on flagged vertices; `tile` is mm per texture repeat and `chunks` is THREE.ShaderChunk. */
export function makeTriplanar(material, tile, chunks) {
  material.onBeforeCompile = (shader) => {
    shader.uniforms.tpScale = { value: 1 / Math.max(tile, 1e-6) };
    shader.vertexShader = patchVertex(shader.vertexShader);
    shader.fragmentShader = patchFragment(shader.fragmentShader, chunks);
  };
  material.customProgramCacheKey = () => 'triplanar-v1';
  material.needsUpdate = true;
}
