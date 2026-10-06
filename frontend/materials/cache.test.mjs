import test from 'node:test';
import assert from 'node:assert/strict';
import { RefCache, materialKey } from './cache.js';
import { MaterialLibrary, FALLBACK } from './library.js';

test('textures are created once and shared', () => {
  const c = new RefCache((k) => ({ k }));
  const a = c.acquire('t.jpg'), b = c.acquire('t.jpg');
  assert.equal(a, b);
  assert.equal(c.created, 1);
  assert.equal(c.refs('t.jpg'), 2);
});

test('disposed on last release only', () => {
  const dead = [];
  const c = new RefCache((k) => ({ k }), (v) => dead.push(v.k));
  c.acquire('x'); c.acquire('x');
  c.release('x');
  assert.deepEqual(dead, []);
  c.release('x');
  assert.deepEqual(dead, ['x']);
  assert.equal(c.size, 0);
  c.release('x'); // over-release is harmless
  assert.equal(c.disposed, 1);
});

test('clear disposes everything', () => {
  const dead = [];
  const c = new RefCache((k) => k, (v) => dead.push(v));
  c.acquire('a'); c.acquire('b');
  c.clear();
  assert.deepEqual(dead.sort(), ['a', 'b']);
});

test('materialKey differs with overrides', () => {
  const d = { id: 'm' };
  assert.notEqual(materialKey(d, {}), materialKey(d, { color: '#FF0000' }));
  assert.notEqual(materialKey(d, {}), materialKey(d, { opacity: 0.5 }));
  assert.notEqual(materialKey(d, {}), materialKey(d, { texture_scale: 2 }));
  assert.equal(materialKey(d, undefined), materialKey(d, {}));
});

const view = { categories: ['Metals', 'Plastics'], materials: [
  { id: 'brushed_aluminum', name: 'Brushed Aluminum', category: 'Metals', tags: ['metal', 'silver'] },
  { id: 'black_abs', name: 'Black ABS', category: 'Plastics', tags: ['plastic', 'black'] },
] };

test('library search and fallback', () => {
  const l = new MaterialLibrary(); l.load(view);
  assert.deepEqual(l.search('alum').map((m) => m.id), ['brushed_aluminum']);
  assert.deepEqual(l.search('black', 'Plastics').map((m) => m.id), ['black_abs']);
  assert.deepEqual(l.search('black', 'Metals'), []);
  assert.equal(l.search('').length, 2);
  assert.equal(l.resolve('nope'), FALLBACK);
  assert.equal(l.resolve('black_abs').name, 'Black ABS');
});
