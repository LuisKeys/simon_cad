// Simon CAD UI: panels over the ui.Service API. Every mutation here is a call that ends in
// the tool registry (prompt, tool, parameter edit) or the ChangeSet lifecycle (commit,
// reject, undo, redo, restore); the UI has no other way to change the model.
import { api, subscribe, h, mm3 } from './api.js';
import { Viewport } from './viewport.js';

const $ = (id) => document.getElementById(id);
const S = { state: null, previewing: null, playing: false, paletteAll: [], busyScene: false };

// ---- viewport wiring -----------------------------------------------------------------

const vp = new Viewport($('gl'), $('cube'), {
  onPick: async (p, mods) => {
    try {
      const mode = mods.shift || mods.ctrl ? 'add' : 'replace';
      const body = p ? { body: p.body, kind: p.kind, index: p.index ?? -1, triangle: p.triangle ?? -1, mode } : { mode };
      const sel = await api('pick', body);
      applySelection(sel);
    } catch (e) { toast(e.message, 'bad'); }
  },
  onHover: (p) => {
    $('hud').textContent = p ? `${p.kind} ${p.kind === 'body' ? p.body : '#' + p.index} on ${p.body}` : hudStats();
  },
  onMeasure: (m) => {
    const label = $('measure-label');
    if (!m) { label.hidden = true; $('hud').textContent = vp.measure.on ? 'Measure: click two points' : hudStats(); return; }
    const d = m.d;
    label.hidden = false;
    label.textContent = `${m.distance.toFixed(3)} mm  (Δ ${d.x.toFixed(2)}, ${d.y.toFixed(2)}, ${d.z.toFixed(2)})`;
    $('hud').textContent = `distance ${m.distance.toFixed(3)} mm`;
    placeMeasureLabel(m.mid);
  },
  onFrame: () => { if (vp.measure.pts.length === 2) placeMeasureLabel(vp.measure.pts[0].clone().lerp(vp.measure.pts[1], 0.5)); },
});
window.simonViewport = vp; // for e2e tests and debugging

function placeMeasureLabel(mid) {
  const s = vp.projectToPixels(mid);
  const l = $('measure-label');
  l.style.left = s.x + 'px'; l.style.top = s.y + 'px';
}

function hudStats() {
  return vp.stats.bodies ? `${vp.stats.bodies} body · ${vp.stats.triangles.toLocaleString()} triangles` : '';
}

function applySelection(sel) {
  S.selection = sel;
  vp.setSelection(sel.highlights ?? []);
  renderSelection(sel);
}

// ---- data refresh --------------------------------------------------------------------

async function refreshScene() {
  if (S.playing) return;
  if (S.busyScene) { S.sceneAgain = true; return; }
  S.busyScene = true;
  try {
    do {
      S.sceneAgain = false;
      const delta = await api('scene', { known: vp.known });
      vp.applyDelta(delta);
      $('empty').hidden = vp.bodies.size > 0;
      $('hud').textContent = hudStats();
    } while (S.sceneAgain);
  } catch (e) { toast(e.message, 'bad'); } finally { S.busyScene = false; }
}

async function refresh() {
  try {
    const st = await api('state');
    S.state = st;
    render(st);
    await refreshScene();
    vp.setSelection(st.selection.highlights ?? []);
    if (S.previewing && !st.pending.some((c) => c.id === S.previewing)) clearPreview();
  } catch (e) { toast(e.message, 'bad'); }
}

let refreshTimer = null;
function scheduleRefresh() {
  clearTimeout(refreshTimer);
  refreshTimer = setTimeout(refresh, 60);
}

subscribe((n) => {
  if (n.kind === 'chat' && n.chat) { addChat(n.chat); return; }
  if (n.kind === 'selection') { api('state').then((st) => { S.state = st; applySelection(st.selection); }).catch(() => {}); return; }
  if (n.kind === 'state' || n.kind === 'agent_done') scheduleRefresh();
});

// ---- rendering of panels ---------------------------------------------------------------

function render(st) {
  $('project').replaceChildren(...[h('span', {}, st.name), st.dirty ? h('span', { class: 'dirty', title: 'Unsaved changes' }, ' ●') : null].filter(Boolean));
  $('btn-undo').disabled = !st.can_undo || st.busy;
  $('btn-clear').disabled = st.busy || st.chat.length === 0;
  $('btn-redo').disabled = !st.can_redo || st.busy;
  $('level').value = st.level;
  $('btn-play').disabled = st.frames === 0 || S.playing;
  $('send').disabled = st.busy;
  $('send').textContent = st.busy ? 'Working…' : 'Send';
  renderTree(st);
  renderSelection(st.selection);
  renderTimeline(st);
  renderChat(st);
}

function renderTree(st) {
  const el = $('tree');
  if (!st.tree.length) { el.replaceChildren(h('div', { class: 'muted' }, 'No features yet.')); return; }
  const selFeatures = new Set((st.selection.selectors ?? []).map(featureOf));
  el.replaceChildren(...st.tree.map((n) => {
    const open = S.openNode === n.id;
    const row = h('div', { class: 'row', role: 'treeitem', 'aria-expanded': String(open), tabindex: '0',
      onclick: () => { S.openNode = open ? null : n.id; renderTree(S.state); },
      onkeydown: (e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); row.click(); } } },
      h('span', { class: `dot ${n.status === 'ok' && !n.suppressed ? '' : n.suppressed ? 'suppressed' : n.status}`, title: n.suppressed ? 'suppressed' : n.status }),
      h('span', { class: 'name', title: n.id }, n.name),
      h('span', { class: 'type' }, n.type),
      h('span', { class: `badge ${n.created_by}`, title: n.capability ? `${n.created_by} · ${n.capability}` : n.created_by }, n.created_by || '—'),
      n.visible ? null : h('span', { class: 'badge hidden', title: 'Consumed by a later feature' }, 'input'));
    const kids = [row];
    if (n.intent) kids.push(h('div', { class: 'intent' }, n.intent));
    if (n.errors.length) kids.push(h('div', { class: 'errors' }, n.errors[0]));
    if (open) kids.push(paramEditor(n));
    return h('div', { class: `node${selFeatures.has(n.id) ? ' sel' : ''}`, role: 'none' }, ...kids);
  }));
}

function paramEditor(n) {
  const rows = [];
  for (const [k, v] of Object.entries(n.params ?? {})) {
    if (typeof v !== 'number' && typeof v !== 'string' && typeof v !== 'boolean') continue;
    const id = `p-${n.id}-${k}`;
    const input = h('input', { id, type: typeof v === 'number' ? 'number' : 'text', step: 'any', value: String(v),
      'aria-label': `${n.name} ${k}`,
      onchange: async (e) => {
        const raw = e.target.value;
        const val = typeof v === 'number' ? Number(raw) : typeof v === 'boolean' ? raw === 'true' : raw;
        try {
          const o = await api('param', { feature: n.id, parameter: k, value: val });
          if (!o.ok) toast(o.error, 'bad');
        } catch (err) { toast(err.message, 'bad'); }
        scheduleRefresh();
      } });
    rows.push(h('label', { for: id }, k), input);
  }
  return h('div', { class: 'params' }, ...rows);
}

function featureOf(sel) { const m = /feature=([^,)]+)/.exec(sel); return m ? m[1].trim() : ''; }

function renderSelection(sel) {
  const list = $('sel-list'), chips = $('chips');
  const ents = sel?.entities ?? [];
  if (!ents.length) {
    list.replaceChildren(h('div', { class: 'muted' }, 'Click a face, edge or vertex in the viewport. Shift-click adds to the selection.'));
    chips.replaceChildren();
    return;
  }
  list.replaceChildren(...ents.map((e) => h('div', { class: 'sel-item' },
    h('code', {}, e.selector),
    h('div', { class: 'muted' }, [e.surface_type, e.area ? `${e.area.toFixed(1)} mm²` : null,
      `at (${e.centroid.map((v) => v.toFixed(1)).join(', ')})`].filter(Boolean).join(' · ')))));
  chips.replaceChildren(...ents.map((e) => h('span', { class: 'chip', title: e.selector },
    h('span', {}, shortSel(e.selector)),
    h('button', { type: 'button', 'aria-label': `Remove ${e.selector} from the selection`, onclick: async () => {
      const left = ents.map((x) => x.selector).filter((x) => x !== e.selector);
      try { applySelection(await api('select', { selectors: left })); } catch (err) { toast(err.message, 'bad'); }
    } }, '✕'))));
}

function shortSel(s) {
  const m = /^(\w+)\(feature=([^,)]+)(?:,(.*))?\)$/.exec(s);
  return m ? `${m[1]} ${m[2]}${m[3] ? ' ' + m[3] : ''}` : s;
}

// Conversation: messages, and ChangeSet cards for what is waiting for approval.
const chatSeen = new Set();
function addChat(m) {
  if (chatSeen.has(m.seq)) return;
  S.liveChat = true;
  scheduleRefresh();
}

function renderChat(st) {
  const el = $('chat');
  const stick = el.scrollTop + el.clientHeight >= el.scrollHeight - 40;
  const nodes = st.chat.map((m) => {
    chatSeen.add(m.seq);
    const cls = `msg ${m.role}${m.role === 'tool' && !m.ok ? ' bad' : ''}`;
    if (m.role === 'draft') return draftCard(m, st);
    if (m.role === 'tool') return h('div', { class: cls }, `${m.ok ? '✓' : '✗'} ${m.tool}${m.text ? ' — ' + m.text : ''}`);
    return h('div', { class: cls }, m.text);
  });
  for (const cs of st.pending) nodes.push(changeSetCard(cs));
  if (st.busy) nodes.push(h('div', { class: 'msg system' }, 'The agent is working…'));
  el.replaceChildren(...nodes);
  if (stick) el.scrollTop = el.scrollHeight;
}

function draftCard(m, st) {
  const open = (st.draft || []).length > 0 && m.seq === Math.max(...st.chat.filter((x) => x.role === 'draft').map((x) => x.seq));
  const act = (path) => async () => { try { await api(path, {}); } catch (err) { toast(err.message, 'bad'); } };
  return h('div', { class: 'cs' },
    h('div', { class: 'title' }, h('span', {}, m.text)),
    open ? h('div', { class: 'actions' },
      h('button', { class: 'primary', disabled: st.busy, onclick: act('draft/keep') }, 'Keep'),
      h('button', { disabled: st.busy, onclick: act('draft/discard') }, 'Discard')) : null);
}

function changeSetCard(cs) {
  const prev = S.previewing === cs.id;
  const delta = cs.volume_after_mm3 - cs.volume_before_mm3;
  const checks = cs.validation.map((v) => h('li', { class: v.ok ? 'ok' : v.severity === 'warning' ? 'warn' : 'bad' },
    `${v.ok ? '✓' : '✗'} ${v.check}${v.message ? ': ' + v.message : ''}`));
  return h('div', { class: `cs${prev ? ' previewing' : ''}`, 'data-cs': cs.id },
    h('div', { class: 'title' }, h('span', {}, cs.intent || cs.id), h('span', { class: 'muted' }, cs.id)),
    cs.explanation && cs.explanation !== cs.intent ? h('div', { class: 'muted' }, cs.explanation) : null,
    h('ul', {},
      cs.added.length ? h('li', {}, `adds ${cs.added.join(', ')}`) : null,
      cs.modified.length ? h('li', {}, `modifies ${cs.modified.join(', ')}`) : null,
      cs.removed.length ? h('li', {}, `removes ${cs.removed.join(', ')}`) : null,
      h('li', {}, `volume ${mm3(cs.volume_before_mm3)} → ${mm3(cs.volume_after_mm3)} (${delta >= 0 ? '+' : ''}${delta.toFixed(1)})`),
      ...cs.warnings.map((w) => h('li', { class: 'warn' }, '⚠ ' + w)),
      ...checks),
    h('div', { class: 'actions' },
      h('button', { onclick: () => (prev ? clearPreview() : showPreview(cs.id)) }, prev ? 'Hide preview' : 'Preview'),
      h('button', { class: 'primary', disabled: !cs.valid, onclick: () => lifecycle('commit', cs.id) }, 'Commit'),
      h('button', { class: 'danger', onclick: () => lifecycle('reject', cs.id) }, 'Reject')));
}

function renderTimeline(st) {
  const el = $('timeline');
  if (!st.checkpoints.length) { el.replaceChildren(h('div', { class: 'none' }, 'Semantic timeline: checkpoints appear here as the design evolves.')); return; }
  el.replaceChildren(...st.checkpoints.map((c) => h('div', { class: `cp${c.current ? ' current' : ''}`, title: `${c.label}\n${c.time}` },
    h('div', { class: 'label' }, `${c.seq}. ${c.label}`),
    h('div', { class: 'muted', style: 'font-size:11px' }, `${c.actor || 'user'} · ${c.changesets.length} change${c.changesets.length === 1 ? '' : 's'}`),
    h('div', { class: 'acts' },
      h('button', { disabled: c.current || st.busy, onclick: () => restoreCheckpoint(c) }, 'Restore'),
      h('button', { onclick: () => compareCheckpoint(c) }, 'Compare')))));
}

// ---- ChangeSet lifecycle ----------------------------------------------------------------

async function showPreview(id) {
  try {
    const g = await api(`changeset/${id}/preview`);
    S.previewing = id;
    vp.setGhost(g);
    const bar = $('ghost-bar');
    bar.hidden = false;
    bar.replaceChildren(h('span', {}, `Preview · ${g.changeset.intent || id}: +${g.added.length} / −${g.removed.length} bodies`),
      h('button', { class: 'primary', onclick: () => lifecycle('commit', id) }, 'Commit'),
      h('button', { onclick: () => lifecycle('reject', id) }, 'Reject'),
      h('button', { onclick: clearPreview }, 'Hide'));
    renderChat(S.state);
  } catch (e) { toast(e.message, 'bad'); }
}

function clearPreview() {
  S.previewing = null;
  vp.setGhost(null);
  $('ghost-bar').hidden = true;
  if (S.state) renderChat(S.state);
}

async function lifecycle(action, id) {
  try {
    await api(`changeset/${id}/${action}`, {});
    clearPreview();
    toast(action === 'commit' ? `Committed ${id}` : `Rejected ${id}`, 'ok');
  } catch (e) { toast(e.message, 'bad'); }
  scheduleRefresh();
}

async function doUndo() { try { const c = await api('undo', {}); toast(`Undid ${c.id}`, 'ok'); } catch (e) { toast(e.message, 'bad'); } scheduleRefresh(); }
async function doRedo() { try { const c = await api('redo', {}); toast(`Redid ${c.id}`, 'ok'); } catch (e) { toast(e.message, 'bad'); } scheduleRefresh(); }

async function restoreCheckpoint(c) {
  try {
    const o = await api('restore', { checkpoint: c.id });
    if (!o.ok) { toast(o.error, 'bad'); return; }
    toast(`Restore to “${c.label}” proposed. Preview and commit it.`, 'ok');
  } catch (e) { toast(e.message, 'bad'); }
  scheduleRefresh();
}

async function compareCheckpoint(c) {
  try {
    const r = await api(`compare?a=${encodeURIComponent(c.id)}`);
    const lines = [`vs “${r.label_a}”: ${r.features_a} → ${r.features_b} features, ${r.volume_a_mm3.toFixed(1)} → ${r.volume_b_mm3.toFixed(1)} mm³`];
    if (r.diff.added?.length) lines.push('added: ' + r.diff.added.join(', '));
    if (r.diff.removed?.length) lines.push('removed: ' + r.diff.removed.join(', '));
    for (const ch of r.diff.changed ?? []) lines.push(`${ch.feature}.${ch.name}: ${ch.from} → ${ch.to}`);
    if (r.identical) lines.push('identical');
    toast(lines.join('\n'), 'ok', 9000);
  } catch (e) { toast(e.message, 'bad'); }
}

// ---- prompt --------------------------------------------------------------------------

$('composer').addEventListener('submit', async (e) => {
  e.preventDefault();
  const text = $('prompt').value.trim();
  if (!text) return;
  try { await api('prompt', { text, thinking: $('thinking').checked }); $('prompt').value = ''; } catch (err) { toast(err.message, 'bad'); }
});
try { $('thinking').checked = localStorage.getItem('thinking') !== 'off'; } catch {}
$('thinking').addEventListener('change', () => { try { localStorage.setItem('thinking', $('thinking').checked ? 'on' : 'off'); } catch {} });
$('btn-clear').addEventListener('click', async () => {
  try { await api('chat/clear', {}); chatSeen.clear(); } catch (err) { toast(err.message, 'bad'); }
});
$('prompt').addEventListener('keydown', (e) => {
  if (e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); $('composer').requestSubmit(); }
});

// ---- playback ------------------------------------------------------------------------

$('btn-play').addEventListener('click', async () => {
  if (S.playing || !S.state?.frames) return;
  S.playing = true;
  $('btn-play').disabled = true;
  clearPreview();
  vp.clear();
  try {
    for (let i = 0; i <= S.state.frames; i++) {
      const f = await api('playback', { index: i, known: vp.known });
      vp.applyDelta(f.scene);
      $('hud').textContent = i === 0 ? 'Playback…' : `${i}/${f.total} · ${f.changeset?.intent ?? ''}`;
      if (i > 0) vp.fit();
      await new Promise((r) => setTimeout(r, 750));
    }
  } catch (e) { toast(e.message, 'bad'); }
  S.playing = false;
  vp.clear();
  await refresh();
});

// ---- toolbar -------------------------------------------------------------------------

for (const b of document.querySelectorAll('[data-filter]')) {
  b.addEventListener('click', () => {
    vp.filter = b.dataset.filter;
    document.querySelectorAll('[data-filter]').forEach((x) => x.classList.toggle('on', x === b));
  });
}
$('btn-measure').addEventListener('click', () => {
  const on = !vp.measure.on;
  $('btn-measure').classList.toggle('on', on);
  vp.setMeasure(on);
  $('measure-label').hidden = true;
  $('hud').textContent = on ? 'Measure: click two points' : hudStats();
});
$('btn-fit').addEventListener('click', () => vp.fit());
$('sec-axis').addEventListener('change', () => {
  const axis = $('sec-axis').value;
  $('sec-off').disabled = !axis;
  vp.setSection(axis, Number($('sec-off').value));
});
$('sec-off').addEventListener('input', () => vp.setSection($('sec-axis').value, Number($('sec-off').value)));
$('btn-undo').addEventListener('click', doUndo);
$('btn-redo').addEventListener('click', doRedo);
$('level').addEventListener('change', async () => {
  try { await api('level', { level: $('level').value }); } catch (e) { toast(e.message, 'bad'); }
});

// ---- project dialogs ---------------------------------------------------------------------

function ask({ title, value = '', format = false, ok = 'OK' }) {
  return new Promise((resolve) => {
    const d = $('dialog'), input = $('dialog-input'), fmt = $('dialog-format');
    $('dialog-title').textContent = title;
    input.value = value; fmt.hidden = !format; $('dialog-ok').textContent = ok;
    d.hidden = false; input.focus(); input.select();
    const close = (v) => { d.hidden = true; $('dialog-form').onsubmit = null; $('dialog-cancel').onclick = null; resolve(v); };
    $('dialog-form').onsubmit = (e) => { e.preventDefault(); close({ value: input.value.trim(), format: fmt.value }); };
    $('dialog-cancel').onclick = () => close(null);
  });
}

$('btn-new').addEventListener('click', async () => {
  const r = await ask({ title: 'New project name', value: 'untitled', ok: 'Create' });
  if (!r) return;
  try { await api('project/new', { name: r.value }); clearPreview(); vp.clear(); } catch (e) { toast(e.message, 'bad'); }
  scheduleRefresh();
});
$('btn-open').addEventListener('click', async () => {
  const r = await ask({ title: 'Path of the .scadproj directory', ok: 'Open' });
  if (!r?.value) return;
  try { await api('project/open', { path: r.value }); clearPreview(); vp.clear(); } catch (e) { toast(e.message, 'bad'); }
  scheduleRefresh();
});
async function save() {
  try {
    let path = S.state?.path;
    if (!path) {
      const r = await ask({ title: 'Save project to (a .scadproj directory)', value: `${S.state?.name || 'project'}.scadproj`, ok: 'Save' });
      if (!r?.value) return;
      path = r.value;
    }
    const res = await api('project/save', { path });
    toast(`Saved ${res.path}`, 'ok');
  } catch (e) { toast(e.message, 'bad'); }
  scheduleRefresh();
}
$('btn-save').addEventListener('click', save);
$('btn-export').addEventListener('click', async () => {
  const r = await ask({ title: 'Export to (path)', value: `out/${S.state?.name || 'model'}.step`, format: true, ok: 'Export' });
  if (!r?.value) return;
  let path = r.value;
  const ext = r.format === 'stl' ? '.stl' : '.step';
  if (!path.toLowerCase().endsWith(ext)) path = path.replace(/\.(step|stl)$/i, '') + ext;
  try {
    const o = await api('project/export', { format: r.format, path });
    toast(o.ok ? `Exported ${path} (${o.data?.bytes ?? '?'} bytes, valid solid)` : o.error, o.ok ? 'ok' : 'bad');
  } catch (e) { toast(e.message, 'bad'); }
});

// ---- command palette -------------------------------------------------------------------

const pal = { items: [], idx: 0 };
async function openPalette() {
  $('palette').hidden = false;
  $('pal-form').hidden = true; $('pal-list').hidden = false;
  $('pal-input').value = '';
  $('pal-input').focus();
  if (!S.paletteAll.length) S.paletteAll = await api('palette');
  filterPalette($('pal-input').value); // the user may already be typing
}
function closePalette() { $('palette').hidden = true; }

function filterPalette(q) {
  const t = q.trim().toLowerCase();
  const hits = S.paletteAll.filter((e) => !t || e.title.toLowerCase().includes(t) || e.id.toLowerCase().includes(t) || e.description.toLowerCase().includes(t));
  pal.items = hits.slice(0, 40);
  if (t) pal.items.push({ kind: 'ask', id: 'ask', title: `Ask the agent: “${q.trim()}”`, description: 'Natural language through the agent', template: q.trim() });
  pal.idx = 0;
  drawPalette();
}
function drawPalette() {
  $('pal-list').replaceChildren(...pal.items.map((e, i) => h('li', { class: i === pal.idx ? 'on' : '', onclick: () => runPalette(e), onmousemove: () => { pal.idx = i; drawPalette(); } },
    h('span', { class: 'k' }, e.kind), h('span', {}, e.title), h('span', { class: 'd' }, e.description))));
  $('pal-list').children[pal.idx]?.scrollIntoView({ block: 'nearest' });
}

async function runPalette(e) {
  if (e.kind === 'ask') { closePalette(); $('prompt').value = e.template; $('composer').requestSubmit(); return; }
  if (e.kind === 'intent') { closePalette(); $('prompt').value = e.template; $('prompt').focus(); return; }
  if (e.kind === 'tool') {
    $('pal-list').hidden = true; $('pal-form').hidden = false;
    $('pal-form-title').textContent = `${e.title} — ${e.description}`;
    $('pal-args').value = JSON.stringify(withSelection(JSON.parse(e.template || '{}'), e), null, 2);
    $('pal-run').onclick = async () => {
      let args;
      try { args = JSON.parse($('pal-args').value || '{}'); } catch (err) { toast('Arguments are not valid JSON: ' + err.message, 'bad'); return; }
      closePalette();
      try {
        const o = await api('tool', { name: e.id, args });
        toast(o.ok ? (o.committed ? `${e.id} applied` : o.changeset ? `${e.id} proposed: review it in the conversation` : `${e.id} ok`) : o.error, o.ok ? 'ok' : 'bad');
      } catch (err) { toast(err.message, 'bad'); }
      scheduleRefresh();
    };
    $('pal-back').onclick = () => { $('pal-form').hidden = true; $('pal-list').hidden = false; $('pal-input').focus(); };
    $('pal-args').focus();
    return;
  }
  closePalette();
  switch (e.id) {
    case 'undo': return doUndo();
    case 'redo': return doRedo();
    case 'save': return save();
    case 'export_step': case 'export_stl': return $('btn-export').click();
    case 'playback': return $('btn-play').click();
    case 'new': return $('btn-new').click();
  }
}

// A tool that takes a face selector gets the current selection prefilled.
function withSelection(args, e) {
  const sels = S.selection?.selectors ?? [];
  const props = e.schema?.properties ?? {};
  const face = sels.find((x) => x.startsWith('face('));
  if (face && props.face !== undefined && 'face' in args) args.face = face;
  // fillet/chamfer: the selected edges, and the feature they belong to.
  const edges = sels.filter((x) => x.startsWith('edge('));
  if (edges.length && props.edges !== undefined) {
    args.edges = edges;
    args.target = featureOf(edges[0]);
  }
  return args;
}

$('btn-palette').addEventListener('click', openPalette);
$('pal-input').addEventListener('input', (e) => filterPalette(e.target.value));
$('pal-input').addEventListener('keydown', (e) => {
  if (e.key === 'ArrowDown') { e.preventDefault(); pal.idx = Math.min(pal.idx + 1, pal.items.length - 1); drawPalette(); }
  else if (e.key === 'ArrowUp') { e.preventDefault(); pal.idx = Math.max(pal.idx - 1, 0); drawPalette(); }
  else if (e.key === 'Enter') { e.preventDefault(); if (pal.items[pal.idx]) runPalette(pal.items[pal.idx]); }
});
$('palette').addEventListener('mousedown', (e) => { if (e.target === $('palette')) closePalette(); });

document.addEventListener('keydown', (e) => {
  const mod = e.ctrlKey || e.metaKey;
  const typing = ['INPUT', 'TEXTAREA', 'SELECT'].includes(document.activeElement?.tagName);
  if (mod && e.key.toLowerCase() === 'k') { e.preventDefault(); $('palette').hidden ? openPalette() : closePalette(); }
  else if (e.key === 'Escape') {
    if (!$('palette').hidden) closePalette();
    else if (!$('dialog').hidden) $('dialog-cancel').click();
    else if (vp.measure.on) $('btn-measure').click();
    else if (S.selection?.selectors?.length) api('select', { selectors: [] }).then(applySelection).catch(() => {});
  } else if (mod && e.key.toLowerCase() === 's') { e.preventDefault(); save(); }
  else if (mod && e.key.toLowerCase() === 'z' && !typing) { e.preventDefault(); e.shiftKey ? doRedo() : doUndo(); }
  else if (!typing && e.key === 'f') vp.fit();
});

// ---- toast ---------------------------------------------------------------------------

let toastTimer = null;
function toast(msg, kind = '', ms = 4500) {
  const t = $('toast');
  t.textContent = msg; t.className = `toast ${kind}`; t.hidden = false; t.style.whiteSpace = 'pre-line';
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => { t.hidden = true; }, ms);
}

refresh();
api('palette').then((p) => { S.paletteAll = p; }).catch(() => {}); // warm the palette so Ctrl+K is instant
