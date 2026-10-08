// Settings → Rendering as the viewport consumes them (no three.js, so node can test it):
// stored percentages become factors, and names become the choices the viewport maps onto
// three.js constants. Unknown or out-of-range values fall back to the defaults.

export const RENDER_DEFAULTS = { environment: 'studio', envIntensity: 100, exposure: 100, toneMapping: 'neutral', shadows: 'standard' };

export const ENVIRONMENTS = [['studio', 'Studio softboxes'], ['room', 'Plain room']];
export const TONE_MAPPINGS = [['neutral', 'Neutral (true colors)'], ['aces', 'ACES filmic'], ['agx', 'AgX filmic'], ['none', 'None']];
export const SHADOW_QUALITIES = [['off', 'Off'], ['standard', 'Standard'], ['high', 'High']];

const pick = (v, list, def) => (list.some(([k]) => k === v) ? v : def);
const range = (v, lo, hi, def) => (Number.isFinite(v) && v >= lo && v <= hi ? v : def);

export function normalizeRender(r = {}) {
  return {
    environment: pick(r.environment, ENVIRONMENTS, RENDER_DEFAULTS.environment),
    envIntensity: range(r.envIntensity, 0, 300, RENDER_DEFAULTS.envIntensity),
    exposure: range(r.exposure, 25, 300, RENDER_DEFAULTS.exposure),
    toneMapping: pick(r.toneMapping, TONE_MAPPINGS, RENDER_DEFAULTS.toneMapping),
    shadows: pick(r.shadows, SHADOW_QUALITIES, RENDER_DEFAULTS.shadows),
  };
}

/** Numeric form: factors instead of percents, and the shadow map resolution (0 = no shadows). */
export function renderParams(r) {
  const n = normalizeRender(r);
  return { ...n, envFactor: n.envIntensity / 100, exposureFactor: n.exposure / 100,
    shadowMapSize: { off: 0, standard: 2048, high: 4096 }[n.shadows] };
}
