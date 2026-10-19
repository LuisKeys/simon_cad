// The arithmetic of the "please wait" dialog: how far along a stage looks, and how to say how long
// it has taken. Pure, so it is tested without a DOM.

/**
 * Fraction (0..0.95) of a stage that has run for `elapsedMs`, given a rough estimate `etaMs`.
 * It tracks the estimate up to 90%, then creeps towards 95% when the stage overruns, so the bar
 * never claims to be done before the server says so, and never sits still while work goes on.
 * Without an estimate it returns null: the stage is of unknown length.
 */
export function stageFraction(elapsedMs, etaMs) {
  if (!(etaMs > 0)) return null;
  const f = Math.max(0, elapsedMs) / etaMs;
  return f <= 1 ? 0.9 * f : 0.9 + 0.05 * (1 - 1 / f);
}

/** "3 s", "1 min 05 s". */
export function elapsedText(ms) {
  const s = Math.max(0, Math.floor(ms / 1000));
  return s < 60 ? `${s} s` : `${Math.floor(s / 60)} min ${String(s % 60).padStart(2, '0')} s`;
}

/** The estimate as shown next to the elapsed time: "about 12 s", or '' when there is none. */
export function etaText(etaMs) {
  if (!(etaMs > 0)) return '';
  const s = Math.max(1, Math.round(etaMs / 1000));
  return `about ${elapsedText(s * 1000)}`;
}
