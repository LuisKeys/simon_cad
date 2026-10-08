// DOM for the Material panel: current material, a searchable/categorised thumbnail picker,
// texture scale and "remove". Every change is a tool call made through ctx.apply, so it
// is a ChangeSet with undo/redo like any other edit; nothing here touches geometry.
import { h } from '../api.js';

export const DND_TYPE = 'application/x-simon-material';

const PICK = { open: false, query: '', category: '' }; // survives panel re-renders

const recents = (() => {
  const KEY = 'simon.recentMaterials';
  return {
    get() { try { return JSON.parse(localStorage.getItem(KEY) ?? '[]'); } catch { return []; } },
    push(id) { try { localStorage.setItem(KEY, JSON.stringify([id, ...this.get().filter((x) => x !== id)].slice(0, 6))); } catch { /* storage unavailable */ } },
  };
})();
export const recentMaterials = () => recents.get();
export const noteRecentMaterial = (id) => recents.push(id);

/**
 * @param ctx { library, thumb(def)->Promise<string>, apply(tool, ids, extra), rerender() }
 * @param nodes tree nodes the panel edits (one or many; differing values show "mixed")
 */
export function materialEditor(ctx, nodes) {
  const ids = nodes.map((n) => n.id);
  const uniq = (f) => { const v = new Set(nodes.map(f)); return v.size === 1 ? [...v][0] : undefined; };
  const cur = uniq((n) => n.appearance?.material_id ?? '');
  const scale = uniq((n) => n.appearance?.texture_scale ?? 1);
  const def = cur ? ctx.library.get(cur) : null;
  const label = cur === undefined ? 'mixed' : !cur ? 'none' : def ? def.name : `${cur} (missing)`;

  const choose = (id) => { noteRecentMaterial(id); ctx.apply('set_material', ids, { material_id: id }); };

  const head = h('span', { class: 'color-row' },
    cur && def ? thumb(ctx, def) : null,
    h('output', { class: 'mat-name' }, label),
    h('button', { type: 'button', class: 'small', 'aria-expanded': String(PICK.open),
      onclick: () => { PICK.open = !PICK.open; ctx.rerender(); } }, PICK.open ? 'Close' : 'Choose…'));

  const grid = h('div', { class: 'mat-grid', role: 'listbox', 'aria-label': 'Materials' });
  const fill = () => {
    const found = ctx.library.search(PICK.query, PICK.category);
    grid.replaceChildren(...(found.length ? found.map((m) => h('button', { type: 'button', class: `mat${m.id === cur ? ' on' : ''}`, role: 'option',
      'aria-selected': String(m.id === cur), title: `${m.name} · ${m.category}`, draggable: 'true',
      ondragstart: (e) => { e.dataTransfer.setData(DND_TYPE, m.id); e.dataTransfer.effectAllowed = 'copy'; },
      onclick: () => choose(m.id) }, thumb(ctx, m), h('span', {}, m.name)))
      : [h('div', { class: 'muted' }, 'No materials match.')]));
  };
  fill();

  const search = h('input', { type: 'search', placeholder: 'Search materials (e.g. steel, black, glass)', value: PICK.query, 'aria-label': 'Search materials',
    oninput: (e) => { PICK.query = e.target.value; fill(); } });
  const cats = h('div', { class: 'mat-cats', role: 'group', 'aria-label': 'Material categories' },
    ['', ...ctx.library.categories].map((c) => h('button', { type: 'button', class: `chip${PICK.category === c ? ' on' : ''}`, 'aria-pressed': String(PICK.category === c),
      onclick: () => { PICK.category = c; ctx.rerender(); } }, c || 'All')));

  const sc = h('input', { type: 'number', min: '0.1', max: '100', step: '0.1', value: scale === undefined ? '' : String(scale),
    placeholder: scale === undefined ? 'mixed' : '1', 'aria-label': 'Texture scale', disabled: cur === '',
    onchange: (e) => { const v = Number(e.target.value); if (v > 0) ctx.apply('set_material_scale', ids, { scale: v }); } });
  const remove = h('button', { type: 'button', class: 'small', disabled: cur === '',
    onclick: () => ctx.apply('remove_material', ids) }, 'Remove material');

  return [
    h('label', {}, 'material'), head,
    PICK.open ? h('div', { class: 'mat-picker' }, search, cats, grid) : null,
    h('label', {}, 'texture scale'), sc,
    h('span', {}), remove,
  ];
}

/** A thumbnail <img>; the rendered sphere arrives asynchronously and is cached by the caller. */
function thumb(ctx, def) {
  const img = h('img', { class: 'mat-thumb', alt: '', width: '40', height: '40', style: `background:${def.base_color ?? def.baseColor}` });
  Promise.resolve(ctx.thumb(def)).then((url) => { if (url) { img.src = url; img.style.background = ''; } }).catch(() => {});
  return img;
}

/** Quick menu entries for the context menu: recents, a browse command and remove. */
export function materialMenu(ctx, ids, { hasMaterial, openPanel }) {
  const items = recentMaterials().map((id) => ctx.library.get(id)).filter(Boolean)
    .map((m) => ({ label: `Material: ${m.name}`, run: () => { noteRecentMaterial(m.id); ctx.apply('set_material', ids, { material_id: m.id }); } }));
  items.push({ label: 'Material…', run: () => { PICK.open = true; openPanel(); } });
  if (hasMaterial) items.push({ label: 'Remove material', run: () => ctx.apply('remove_material', ids) });
  return items;
}
