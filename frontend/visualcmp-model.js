// Pure helpers of the Visual Comparison panel (no DOM), tested with node --test.

/** Score rows in display order: [metric, label]. */
export const SCORE_ROWS = [
  ['side_silhouette', 'Side silhouette'],
  ['front_silhouette', 'Front silhouette'],
  ['top_silhouette', 'Top silhouette'],
  ['proportions', 'Proportions'],
  ['landmark_alignment', 'Landmarks'],
  ['wheel_placement', 'Wheel placement'],
  ['semantic_visual_score', 'Visual similarity'],
  ['overall', 'Overall'],
];

export const pct = (v) => `${Math.round((v ?? 0) * 100)}%`;

/**
 * Rows for one iteration: measured metrics with the stage threshold (if any) and whether
 * they pass. Unmeasured metrics are left out.
 */
export function scoreRows(iteration, stages) {
  const stage = (stages ?? []).find((s) => s.name === iteration?.stage);
  const scores = iteration?.scores ?? {};
  return SCORE_ROWS.filter(([k]) => typeof scores[k] === 'number').map(([k, label]) => {
    const threshold = stage?.thresholds?.[k];
    return { key: k, label, value: scores[k], threshold, pass: threshold === undefined ? null : scores[k] + 1e-9 >= threshold };
  });
}

/** The iteration the model currently reflects: the latest one not reverted. */
export function currentIteration(history) {
  for (let i = (history ?? []).length - 1; i >= 0; i--) if (!history[i].reverted) return history[i];
  return null;
}

/**
 * Views available for comparison in an iteration: each reference (keyed "ref:<id>", with
 * its calibrated view label) first, then the standard views.
 */
export function comparisonViews(iteration, references) {
  const renders = iteration?.renders ?? {};
  const out = [];
  for (const r of references ?? []) {
    const key = `ref:${r.image_id}`;
    if (renders[key]) out.push({ key, ref: r.image_id, label: `${r.calibration?.view ?? 'reference'} · ${r.image_id.slice(0, 10)}` });
  }
  for (const v of ['left', 'front', 'top', 'front_3q', 'rear_3q', 'right', 'rear']) {
    if (renders[v]) out.push({ key: v, ref: null, label: v.replace('_3q', ' 3/4') });
  }
  return out;
}

/** Parameter changes of an iteration: [{name, before, after}] for values that changed. */
export function parameterChanges(iteration) {
  const b = iteration?.parameters_before ?? {}, a = iteration?.parameters_after ?? {};
  return Object.keys({ ...b, ...a }).sort().filter((k) => b[k] !== a[k]).map((k) => ({ name: k, before: b[k], after: a[k] }));
}

/** The critic's errors of an iteration (parsed critique JSON, most severe first). */
export function critiqueErrors(iteration) {
  const c = iteration?.critique;
  const list = (c && typeof c === 'object' ? c.largest_errors : null) ?? [];
  return [...list].sort((x, y) => (y.severity ?? 0) - (x.severity ?? 0));
}
