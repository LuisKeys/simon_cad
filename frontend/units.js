// Display units of the open project (no DOM, no Three.js; unit tested). Every length the UI
// receives or sends is canonical millimetres and every angle degrees; this module only
// converts at the edge, for showing and for reading typed values. The conversion factors come
// from the server (state.workspace: mm_per_unit and the linear_units table of the Go
// UnitService), so there is one conversion table in the application, not two.

const DEFAULT = { units: { linear: 'mm', angular: 'deg', linear_precision: 2, angular_precision: 1 }, mm_per_unit: 1 };
const MAX_MM = 1e6;

/** Removes trailing zeros: "150.00" → "150", "12.50" → "12.5". */
const trim = (s) => (s.includes('.') ? s.replace(/\.?0+$/, '') : s);
/** toFixed without "-0.00". */
const fixed = (v, p) => { const s = v.toFixed(p); return /^-0\.?0*$/.test(s) ? s.slice(1) : s; };

/**
 * The display units of a workspace view (state.workspace). Lengths in and out are mm; angles
 * in and out are degrees (the canonical unit of tool arguments and settings).
 */
export function displayUnits(ws) {
  const u = { ...DEFAULT.units, ...(ws?.units ?? {}) };
  const k = ws?.mm_per_unit > 0 ? ws.mm_per_unit : 1;
  const lp = u.linear_precision, ap = u.angular_precision;
  const rad = u.angular === 'rad';
  const U = {
    linear: u.linear, angular: u.angular, symbol: u.linear, angleSymbol: rad ? 'rad' : '°', mmPerUnit: k,
    /** mm → the number shown (unrounded). */
    toDisplay: (mm) => mm / k,
    /** A number typed in the display unit → mm. */
    toMM: (v) => v * k,
    /** "3.94 in" (fixed decimals); unit: false drops the symbol. */
    length: (mm, { unit = true } = {}) => (Number.isFinite(mm) ? fixed(mm / k, lp) + (unit ? ` ${u.linear}` : '') : ''),
    /** A compact size for labels and fields: at most the configured decimals, no trailing zeros. */
    compact: (mm) => (Number.isFinite(mm) ? trim(fixed(mm / k, lp)) : ''),
    area: (mm2) => `${fixed(mm2 / (k * k), Math.max(lp - 1, 1))} ${u.linear}²`,
    volume: (mm3) => `${fixed((mm3 ?? 0) / (k * k * k), Math.max(lp - 1, 1))} ${u.linear}³`,
    /** Degrees → the number shown. */
    angleToDisplay: (deg) => (rad ? (deg * Math.PI) / 180 : deg),
    /** A number typed in the display angle unit → degrees. */
    angleToDeg: (v) => (rad ? (v * 180) / Math.PI : v),
    angle: (deg) => (rad ? `${fixed((deg * Math.PI) / 180, ap)} rad` : `${fixed(deg, ap)}°`),
  };
  /** "150", "150 in", "12,5" in the display unit → { ok, value (mm) } or { ok: false, error }. */
  U.parseLength = (text, { positive = true } = {}) => {
    const t = String(text ?? '').trim().replace(new RegExp(`\\s*${u.linear}$`, 'i'), '').replace(',', '.');
    if (!/^[+-]?(\d+\.?\d*|\.\d+)(e[+-]?\d+)?$/i.test(t)) return { ok: false, error: `Type a size in ${u.linear}, e.g. ${trim(fixed(150 / k, lp)) || 1}` };
    const mm = Number(t) * k;
    if (!Number.isFinite(mm) || (positive && mm <= 0)) return { ok: false, error: `The size must be greater than 0 ${u.linear}` };
    if (Math.abs(mm) > MAX_MM) return { ok: false, error: `The size must be at most ${trim(fixed(MAX_MM / k, lp))} ${u.linear}` };
    return { ok: true, value: mm };
  };
  return U;
}

/** The open project's display units; app.js updates them from every state. */
export let units = displayUnits(null);

/** Switches the display units (state.workspace). Returns true when they changed. */
export function setUnits(ws) {
  const next = displayUnits(ws);
  const changed = ['linear', 'angular', 'mmPerUnit'].some((k) => next[k] !== units[k])
    || next.length(1) !== units.length(1) || next.angle(1) !== units.angle(1);
  units = next;
  return changed;
}
