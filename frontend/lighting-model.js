// Pure geometry for the studio lighting (no three.js, so node can test it): where the key
// light sits and how big its shadow frustum and the ground shadow catcher must be so the
// shadow map covers the model, whatever its size (the CAD scale is millimetres).

export const KEY_DIR = normalize([1, -1.5, 2.5]); // from the model towards the key light (Z is up)

function normalize(v) {
  const l = Math.hypot(v[0], v[1], v[2]) || 1;
  return [v[0] / l, v[1] / l, v[2] / l];
}

/** `min`/`max` are the model's bounding-box corners; returns null for an empty or invalid box. */
export function shadowFrame(min, max) {
  if (![...min, ...max].every(Number.isFinite) || max.some((v, i) => v < min[i])) return null;
  const center = min.map((v, i) => (v + max[i]) / 2);
  const radius = Math.max(Math.hypot(max[0] - min[0], max[1] - min[1], max[2] - min[2]) / 2, 1e-3);
  const distance = radius * 4;
  return {
    center,
    radius,
    position: center.map((c, i) => c + KEY_DIR[i] * distance),
    half: radius * 1.15,          // orthographic half-extent of the shadow camera
    near: distance - radius * 1.5,
    far: distance + radius * 3,   // far enough to reach the ground under a tall model
    normalBias: radius * 0.003,   // keeps flat CAD faces from shadowing themselves
    floorZ: min[2],
    floorSize: radius * 12,
  };
}
