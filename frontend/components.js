// The Components panel. A component is a reusable project; an instance in the open project is
// a reference to it, never a copy. Everything here is browsing and library bookkeeping — the
// operations that change the model (create, insert, detach, sync) go through the tool registry
// like every other mutation.
import { api, h } from './api.js';
import { icon } from './icons.js';
import { units } from './units.js';

const $ = (id) => document.getElementById(id);

let state = { list: [], query: '', loading: false };
let deps = null; // { callTool, toast, formDialog, confirm, refresh }

export function initComponents(env) {
  deps = env;
  $('comp-search')?.addEventListener('input', (e) => {
    state.query = e.target.value;
    render();
    load();
  });
  $('btn-components')?.addEventListener('click', toggle);
  return { refresh: load, render: renderBadge };
}

function toggle() {
  const panel = $('components');
  if (!panel) return;
  panel.hidden = !panel.hidden;
  $('btn-components')?.setAttribute('aria-expanded', String(!panel.hidden));
  if (!panel.hidden) {
    load();
    $('comp-search')?.focus();
  }
}

export async function load() {
  state.loading = true;
  try {
    state.list = await api('components', { query: state.query });
  } catch (err) {
    deps?.toast(err.message, 'bad');
    state.list = [];
  }
  state.loading = false;
  render();
}

// renderBadge reflects the open project's component status in the header: whether it is itself
// a component, and how many of its instances have fallen behind.
export function renderBadge(st) {
  const btn = $('btn-components');
  if (!btn) return;
  btn.classList.toggle('stale', (st?.stale_components ?? 0) > 0);
  btn.title = st?.stale_components
    ? `${st.stale_components} component instance(s) are out of date`
    : 'Components';
  const sync = $('comp-sync');
  if (sync) {
    sync.hidden = !st?.stale_components;
    sync.textContent = `Update ${st?.stale_components ?? 0} out-of-date instance(s)`;
  }
  const pub = $('comp-publish');
  if (pub) {
    pub.textContent = st?.reusable ? 'Remove from library' : 'Publish this project as a component';
    pub.dataset.reusable = String(!!st?.reusable);
  }
  if (!$('components')?.hidden) load();
}

function render() {
  const list = $('comp-list');
  if (!list) return;
  if (state.loading && !state.list.length) {
    list.replaceChildren(h('div', { class: 'muted' }, 'Loading…'));
    return;
  }
  if (!state.list.length) {
    list.replaceChildren(h('div', { class: 'muted' },
      state.query ? `No component matches “${state.query}”.`
        : 'No components yet. Select objects and choose “Create component…” to make one.'));
    return;
  }
  list.replaceChildren(...state.list.map(row));
}

function row(c) {
  return h('div', { class: 'comp', dataset: { id: c.project_id } },
    h('span', { class: 'ficon' }, icon('component')),
    h('div', { class: 'comp-text' },
      h('div', { class: 'comp-name' }, c.name),
      c.description ? h('div', { class: 'comp-desc' }, c.description) : null,
      h('div', { class: 'comp-meta' },
        h('span', {}, `v${c.version || 1}`),
        ...(c.tags ?? []).map((t) => h('span', { class: 'tag' }, t)))),
    h('div', { class: 'comp-actions' },
      btn('Insert', 'create', () => insert(c)),
      btn('Open', 'folder', () => openComponent(c.project_id)),
      btn('Rename', 'pencil', () => rename(c)),
      btn('Delete', 'trash', () => remove(c), true)));
}

function btn(label, ic, run, danger = false) {
  return h('button', { type: 'button', class: `ghost${danger ? ' danger' : ''}`, title: label, 'aria-label': label,
    onclick: (e) => { e.stopPropagation(); run(); } }, icon(ic));
}

async function insert(c) {
  const v = await deps.formDialog({
    title: `Insert ${c.name}`, ok: 'Insert',
    fields: [{ key: 'name', label: 'Instance name', text: true, value: c.name },
      ...['X', 'Y', 'Z'].map((a) => ({ key: `d${a.toLowerCase()}`, label: `${a} (${units.symbol})`, value: 0, length: true }))],
  });
  if (!v) return;
  await deps.callTool('insert_component', { component: c.project_id, name: v.name, dx: v.dx, dy: v.dy, dz: v.dz },
    `Insert ${c.name}`);
}

async function rename(c) {
  const v = await deps.formDialog({
    title: `Rename ${c.name}`, ok: 'Save',
    fields: [{ key: 'name', label: 'Name', text: true, value: c.name },
      { key: 'description', label: 'Description', text: true, value: c.description ?? '' },
      { key: 'tags', label: 'Tags (comma separated)', text: true, value: (c.tags ?? []).join(', ') }],
  });
  if (!v) return;
  const tags = v.tags.split(',').map((t) => t.trim()).filter(Boolean);
  try {
    const o = await api('components/rename', { component: c.project_id, name: v.name, description: v.description, tags });
    if (!o.ok) { deps.toast(o.error, 'bad'); return; }
    deps.toast('Component renamed', 'ok');
    load();
  } catch (err) { deps.toast(err.message, 'bad'); }
}

async function remove(c) {
  if (!await deps.confirm(`Delete the component “${c.name}”?`,
    'Its project is removed from disk. Projects that still reference it will fail to rebuild.')) return;
  try {
    const o = await api('components/delete', { component: c.project_id });
    if (!o.ok) { deps.toast(o.error, 'bad'); return; }
    deps.toast('Component deleted', 'ok');
    load();
  } catch (err) { deps.toast(err.message, 'bad'); }
}

// Opening a component opens its project: there is no separate component editor. Changes to it
// reach the projects that use it when they are next synced.
export async function openComponent(projectID) {
  try {
    const o = await api('components/open', { component: projectID });
    if (!o.ok) { deps.toast(o.error, 'bad'); return; }
    deps.toast('Opened the component project', 'ok');
    deps.refresh();
  } catch (err) { deps.toast(err.message, 'bad'); }
}

// createComponent turns the current selection into a reusable project and replaces it in place
// with one instance.
export async function createComponent(ids) {
  const v = await deps.formDialog({
    title: ids.length > 1 ? `Create a component from ${ids.length} objects` : `Create a component from ${ids[0]}`,
    ok: 'Create',
    fields: [{ key: 'name', label: 'Name', text: true, value: '' },
      { key: 'description', label: 'Description', text: true, value: '' },
      { key: 'tags', label: 'Tags (comma separated)', text: true, value: '' }],
  });
  if (!v || !v.name.trim()) return;
  const tags = v.tags.split(',').map((t) => t.trim()).filter(Boolean);
  const o = await deps.callTool('create_component',
    { name: v.name.trim(), description: v.description, tags, features: ids }, 'Create component');
  if (o?.ok) load();
}

// replaceComponent swaps which component an instance points at, keeping its placement.
export async function replaceComponent(node) {
  if (!state.list.length) await load();
  const options = state.list.filter((c) => c.project_id !== node.component)
    .map((c) => ({ value: c.project_id, label: c.name }));
  if (!options.length) { deps.toast('There is no other component to swap to', 'bad'); return; }
  const v = await deps.formDialog({
    title: `Replace the component used by ${node.name}`, ok: 'Replace',
    fields: [{ key: 'component', label: 'Component', options, value: options[0].value }],
  });
  if (!v) return;
  // A swap is "place the new one where the old one stood, then remove the old one". Both are
  // ordinary ChangeSets, so the whole thing undoes in two steps.
  const p = node.params ?? {};
  const placement = {};
  for (const k of ['dx', 'dy', 'dz', 'angle_deg', 'ax', 'ay', 'az', 'ox', 'oy', 'oz', 'sx', 'sy', 'sz']) {
    if (p[k] !== undefined) placement[k] = p[k];
  }
  const o = await deps.callTool('insert_component',
    { component: v.component, name: node.name, ...placement }, 'Replace component');
  if (o?.ok) await deps.callTool('delete_objects', { features: [node.id] }, 'Remove the old instance');
  load();
}

// publishProject marks the open project as reusable, so it shows up in the library itself.
export async function publishProject(reusable) {
  try {
    const o = await api('components/publish', { reusable, description: '', tags: [] });
    if (!o.ok) { deps.toast(o.error, 'bad'); return; }
    deps.toast(reusable ? 'This project is now a component — save it to publish' : 'Removed from the library', 'ok');
    deps.refresh();
  } catch (err) { deps.toast(err.message, 'bad'); }
}
