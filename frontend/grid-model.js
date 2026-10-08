// The workspace grid as lines in world space (no DOM, no Three.js; unit tested). The grid
// is a visual reference only: it is never geometry, picked, measured or exported. Its
// settings (state.workspace.grid) are canonical millimetres.

/** World axis indices (0 = X, 1 = Y, 2 = Z) of each plane's u, v and normal, as GridService. */
export const PLANE_AXES = { XY: [0, 1, 2], XZ: [0, 2, 1], YZ: [1, 2, 0] };

/** The factory grid, used until the project's settings arrive. */
export const DEFAULT_GRID = { enabled: true, visible: true, plane: 'XY', spacing: 10, major_interval: 10, extent: 1000, origin: [0, 0, 0] };

/**
 * Grid lines of `grid` in `plane` (default: the configured plane), as flat [x, y, z, …]
 * segment arrays: minor lines, major lines (every major_interval cells from the origin) and
 * the two centre lines through the grid origin, tagged with the world axis they run along.
 * Lines sit at origin + k·spacing exactly, so the grid stays aligned with world coordinates.
 */
export function gridLines(grid, plane = grid.plane) {
  const ax = PLANE_AXES[plane] ?? PLANE_AXES.XY;
  const s = grid.spacing, o = grid.origin ?? [0, 0, 0];
  const n = Math.floor(grid.extent / 2 / s + 1e-9);
  const span = n > 0 ? n * s : grid.extent / 2; // end on a line, so the border is a grid line
  const major = Math.max(1, Math.round(grid.major_interval ?? 10));
  const pt = (u, v) => { const p = [o[0], o[1], o[2]]; p[ax[0]] += u; p[ax[1]] += v; return p; };
  const out = { minor: [], major: [], axes: [{ axis: ax[0], positions: [] }, { axis: ax[1], positions: [] }], normal: ax[2], cells: 2 * n };
  for (let k = -n; k <= n; k++) {
    const c = k * s;
    const alongV = [...pt(c, -span), ...pt(c, span)]; // u = c, runs along v
    const alongU = [...pt(-span, c), ...pt(span, c)]; // v = c, runs along u
    if (k === 0) { out.axes[0].positions.push(...alongU); out.axes[1].positions.push(...alongV); } else if (k % major === 0) out.major.push(...alongV, ...alongU);
    else out.minor.push(...alongV, ...alongU);
  }
  return out;
}

/** Whether the grid is drawn at all: enabled (it exists) and visible. */
export const gridShown = (grid) => !!(grid?.enabled && grid?.visible);

/** A key that changes whenever the lines would (the geometry is rebuilt only then). */
export const gridKey = (grid) => JSON.stringify([grid.spacing, grid.major_interval, grid.extent, grid.origin]);
