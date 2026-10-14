// Decoding of the mesh buffers the server packs (internal/rendering/packed.go): the base64 of
// little-endian bytes, instead of a JSON array of numbers. A plain array is still accepted, so
// recordings and fixtures written before the change keep working.

function bytesOf(s) {
  // Native decoding is ~4x faster than the loop below (measured in Chrome: 11 ms of array
  // JSON vs 2.5 ms for a 120k-triangle body); older browsers fall back to atob.
  if (typeof Uint8Array.fromBase64 === 'function') return Uint8Array.fromBase64(s);
  const bin = atob(s);
  const out = new Uint8Array(bin.length);
  for (let i = 0; i < out.length; i++) out[i] = bin.charCodeAt(i);
  return out;
}

function typed(Type, v) {
  if (v == null || v === '') return new Type(0);
  if (typeof v === 'string') {
    const b = bytesOf(v);
    if (b.length % 4) throw new Error(`packed ${Type.name} buffer of ${b.length} bytes is not a multiple of 4`);
    // Float32Array/Uint32Array read the platform's byte order; every target is little-endian.
    return new Type(b.buffer, 0, b.length / 4);
  }
  return v instanceof Type ? v : Type.from(v);
}

/** A Float32Array from a packed string, a plain array or an existing Float32Array. */
export const f32 = (v) => typed(Float32Array, v);
/** A Uint32Array from a packed string, a plain array or an existing Uint32Array. */
export const u32 = (v) => typed(Uint32Array, v);

/** Decodes the buffers of a body mesh in place (idempotent) and returns it. */
export function decodeBody(d) {
  d.positions = f32(d.positions);
  d.normals = f32(d.normals);
  d.indices = u32(d.indices);
  d.edge_points = f32(d.edge_points);
  return d;
}
