import { test } from 'node:test';
import assert from 'node:assert/strict';
import { isPhysical, physicalProps, usesTransmission } from './physical.js';

test('plain definitions stay standard materials', () => {
  assert.equal(isPhysical({ metalness: 1, roughness: 0.3 }), false);
  assert.deepEqual(physicalProps({ metalness: 1 }), {});
  assert.deepEqual(physicalProps({ envMapIntensity: 1.5 }), {}); // reflections are scaled globally (Settings → Rendering)
});

test('clearcoat, transmission and friends need a physical material', () => {
  for (const k of ['clearcoat', 'transmission', 'ior', 'sheen', 'anisotropy', 'thickness']) assert.equal(isPhysical({ [k]: 0.5 }), true, k);
});

test('physicalProps fills sensible companions and ignores junk', () => {
  const p = physicalProps({ clearcoat: 1, sheen: 0.4, emissive: '#102030', ior: 'x', transmission: NaN });
  assert.deepEqual(p, { clearcoat: 1, clearcoatRoughness: 0.1, sheen: 0.4, sheenRoughness: 0.5, emissive: '#102030' });
  assert.deepEqual(physicalProps({ emissive: 'red' }), {});
});

test('transmission only applies while the body is not user-faded', () => {
  assert.equal(usesTransmission({ transmission: 0.9 }, 1), true);
  assert.equal(usesTransmission({ transmission: 0.9 }, 0.5), false);
  assert.equal(usesTransmission({}, 1), false);
});
