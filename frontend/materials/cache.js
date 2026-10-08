// Reference-counted caches. A texture or material is created once, shared by every body
// that uses it, and disposed when the last user releases it (frees GPU memory).

export class RefCache {
  /** @param create (key, ...args) => value   @param dispose (value, key) => void */
  constructor(create, dispose = () => {}) {
    this.create = create; this.dispose = dispose;
    this.items = new Map(); // key -> { value, refs }
    this.created = 0;
    this.disposed = 0;
  }
  acquire(key, ...args) {
    let e = this.items.get(key);
    if (!e) {
      e = { value: this.create(key, ...args), refs: 0 };
      this.items.set(key, e);
      this.created++;
    }
    e.refs++;
    return e.value;
  }
  release(key) {
    const e = this.items.get(key);
    if (!e) return;
    if (--e.refs <= 0) {
      this.items.delete(key);
      this.dispose(e.value, key);
      this.disposed++;
    }
  }
  has(key) { return this.items.has(key); }
  refs(key) { return this.items.get(key)?.refs ?? 0; }
  get size() { return this.items.size; }
  clear() {
    for (const [k, e] of this.items) { this.dispose(e.value, k); this.disposed++; }
    this.items.clear();
  }
}

/** Cache key for a configured material: everything that changes how it looks. */
export function materialKey(def, ap) {
  return JSON.stringify([def.id, ap?.color ?? '', ap?.opacity ?? 1, ap?.texture_scale ?? 1, def.version ?? 0]);
}
