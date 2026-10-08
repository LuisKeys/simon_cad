import { test } from 'node:test';
import assert from 'node:assert/strict';
import * as THREE from '../vendor/three.module.js';
import { CHUNK_PATCHES, patchFragment, patchVertex } from './triplanar.js';

test('every chunk the triplanar patch rewrites still contains what it looks for (guards three.js upgrades)', () => {
  for (const [name, find] of CHUNK_PATCHES) assert.ok(THREE.ShaderChunk[name].includes(find), name);
});

test('the patch rewrites the fragment and vertex shaders of a physical material', () => {
  const frag = [THREE.ShaderLib.physical.fragmentShader, THREE.ShaderLib.physical.vertexShader];
  const f = patchFragment(frag[0], THREE.ShaderChunk), v = patchVertex(frag[1]);
  assert.match(f, /tpSample\( map, vMapUv \)/);
  assert.match(f, /tpNormalView\(\)/);
  assert.match(f, /uniform float tpScale/);
  assert.match(v, /attribute float tpFlag/);
  assert.match(v, /vTpPos = position/);
});
