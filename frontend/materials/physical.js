// Optional physically based extras of a library material (clearcoat, transmission, sheen...).
// Pure data mapping, no three.js, so node can test it. A material only becomes a
// MeshPhysicalMaterial when it asks for something MeshStandardMaterial cannot do.

const PHYSICAL_ONLY = ['clearcoat', 'clearcoatRoughness', 'transmission', 'ior', 'thickness', 'sheen', 'anisotropy'];
const SHARED = ['envMapIntensity']; // standard materials have it too

const num = (v) => (typeof v === 'number' && Number.isFinite(v) ? v : undefined);

/** True when the definition needs MeshPhysicalMaterial. */
export function isPhysical(def) {
  return PHYSICAL_ONLY.some((k) => num(def?.[k]) !== undefined);
}

/** The extra material properties a definition asks for (only the ones it sets). */
export function physicalProps(def) {
  const out = {};
  for (const k of [...(isPhysical(def) ? PHYSICAL_ONLY : []), ...SHARED]) {
    const v = num(def?.[k]);
    if (v !== undefined) out[k] = v;
  }
  if (typeof def?.emissive === 'string' && /^#[0-9a-f]{6}$/i.test(def.emissive)) out.emissive = def.emissive;
  if (out.sheen) out.sheenRoughness = 0.5; // soft cloth/rubber sheen, tinted by sheenColor
  if (out.clearcoat && out.clearcoatRoughness === undefined) out.clearcoatRoughness = 0.1;
  return out;
}

/** Transmission replaces alpha blending: the body stays opaque to the depth buffer. */
export function usesTransmission(def, opacity = 1) {
  return (num(def?.transmission) ?? 0) > 0 && opacity >= 1;
}
