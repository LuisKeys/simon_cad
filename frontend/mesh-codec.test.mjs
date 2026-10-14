import test from 'node:test';
import assert from 'node:assert/strict';
import { f32, u32, decodeBody } from './mesh-codec.js';

// The same vectors are asserted by internal/rendering/packed_test.go.
test('decodes the golden float32 vector written by the server', () => {
  const a = f32('AACAPwAAIMDNzMw9');
  assert.ok(a instanceof Float32Array);
  assert.equal(a.length, 3);
  assert.equal(a[0], 1);
  assert.equal(a[1], -2.5);
  assert.equal(a[2], Math.fround(0.1));
});

test('decodes the golden uint32 vector, including the largest index', () => {
  assert.deepEqual([...u32('AAAAAAEAAAACAAAA/////w==')], [0, 1, 2, 4294967295]);
});

test('empty and missing buffers decode to empty arrays', () => {
  for (const v of ['', null, undefined]) {
    assert.equal(f32(v).length, 0);
    assert.equal(u32(v).length, 0);
  }
});

test('plain arrays (older recordings) and typed arrays are accepted', () => {
  assert.deepEqual([...f32([1, 2.5])], [1, 2.5]);
  assert.deepEqual([...u32([3, 4])], [3, 4]);
  const a = new Float32Array([9]);
  assert.equal(f32(a), a, 'an existing typed array is not copied');
});

test('a buffer that is not a whole number of 4-byte values is an error', () => {
  assert.throws(() => f32('AAAA'), /multiple of 4/);
});

test('decodeBody converts every buffer once and is idempotent', () => {
  const d = decodeBody({ positions: 'AACAPwAAIMDNzMw9', normals: [0, 0, 1], indices: 'AAAAAAEAAAACAAAA/////w==', edge_points: '' });
  assert.ok(d.positions instanceof Float32Array && d.normals instanceof Float32Array);
  assert.ok(d.indices instanceof Uint32Array && d.edge_points.length === 0);
  const p = d.positions;
  assert.equal(decodeBody(d).positions, p);
});

test('a large buffer round-trips through the same layout the server writes', () => {
  const n = 300000, src = new Float32Array(n);
  for (let i = 0; i < n; i++) src[i] = Math.fround(Math.sin(i) * 1000);
  const b64 = Buffer.from(src.buffer).toString('base64');
  const out = f32(b64);
  assert.equal(out.length, n);
  assert.ok(out.every((v, i) => v === src[i]));
});

test('the native decoder, when the browser has one, gives the same result as the fallback', () => {
  const src = Float32Array.from({ length: 1000 }, (_, i) => Math.fround(Math.cos(i) * 50));
  const b64 = Buffer.from(src.buffer).toString('base64');
  const slow = f32(b64);
  const had = Uint8Array.fromBase64;
  let called = 0;
  Uint8Array.fromBase64 = (s) => { called++; return new Uint8Array(Buffer.from(s, 'base64')); };
  try {
    const fast = f32(b64);
    assert.equal(called, 1, 'the native path is used when present');
    assert.deepEqual([...fast], [...slow]);
  } finally {
    if (had) Uint8Array.fromBase64 = had; else delete Uint8Array.fromBase64;
  }
});
