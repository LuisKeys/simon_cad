// Pure helpers for the viewport box (marquee) selection.

/** Normalised rectangle from two corner points; `crossing` when dragged right → left. */
export function rectFrom(a, b) {
  return { x0: Math.min(a.x, b.x), y0: Math.min(a.y, b.y), x1: Math.max(a.x, b.x), y1: Math.max(a.y, b.y), crossing: b.x < a.x };
}

/** Screen-space bounds of projected points, or null when there are none. */
export function boundsOf(pts) {
  if (!pts.length) return null;
  let x0 = Infinity, y0 = Infinity, x1 = -Infinity, y1 = -Infinity;
  for (const p of pts) { x0 = Math.min(x0, p.x); y0 = Math.min(y0, p.y); x1 = Math.max(x1, p.x); y1 = Math.max(y1, p.y); }
  return { x0, y0, x1, y1 };
}

/** Window mode (left → right): the bounds lie fully inside. Crossing mode (right → left): they touch. */
export function rectHit(bounds, rect) {
  if (!bounds) return false;
  if (rect.crossing) return bounds.x1 >= rect.x0 && bounds.x0 <= rect.x1 && bounds.y1 >= rect.y0 && bounds.y0 <= rect.y1;
  return bounds.x0 >= rect.x0 && bounds.x1 <= rect.x1 && bounds.y0 >= rect.y0 && bounds.y1 <= rect.y1;
}
