// Client-side view of the material library served by /api/materials: lookup, category
// filtering and search. Pure (no DOM, no Three.js).

export const FALLBACK = { id: 'generic_gray', name: 'Generic Gray', category: 'Generic', base_color: '#9AA0A6',
  baseColor: '#9AA0A6', metalness: 0.25, roughness: 0.45, textures: {}, textureScale: 1, tags: [] };

const tokens = (s) => String(s ?? '').toLowerCase().split(/[^a-z0-9]+/).filter(Boolean);

export class MaterialLibrary {
  constructor() { this.byId = new Map(); this.categories = []; this.warnings = []; this.defaults = {}; this.version = 0; }

  /** Replaces the contents from an /api/materials payload. */
  load(view) {
    this.byId = new Map((view?.materials ?? []).map((m) => [m.id, m]));
    this.categories = view?.categories ?? [];
    this.warnings = view?.warnings ?? [];
    this.defaults = view?.engineering_defaults ?? {};
    this.version++;
  }

  get(id) { return this.byId.get(id); }
  /** Always returns something drawable: unknown ids give the generic fallback. */
  resolve(id) { return this.byId.get(id) ?? FALLBACK; }
  list(category = '') {
    const all = [...this.byId.values()];
    return category ? all.filter((m) => m.category === category) : all;
  }

  /** Matches every query token against name, id, category and tags (prefix allowed). */
  search(query, category = '') {
    const q = tokens(query);
    const pool = this.list(category);
    if (!q.length) return pool;
    return pool.filter((m) => {
      const hay = tokens([m.name, m.id, m.category, ...(m.tags ?? [])].join(' '));
      return q.every((t) => hay.some((h) => h.startsWith(t)));
    });
  }
}
