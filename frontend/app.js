// Simon CAD UI: panels over the ui.Service API. Every mutation here is a call that ends in
// the tool registry (prompt, tool, parameter edit) or the ChangeSet lifecycle (commit,
// reject, undo, redo, restore); the UI has no other way to change the model.
import { api, apiBytes, subscribe, h, materialUrl } from './api.js';
import { toast } from './toast.js';
import { $ } from './dom.js';
import { showMenu } from './menu.js';
import { confirmBox, formDialog, ask } from './dialogs.js';
import { Viewport } from './viewport.js';
import { initVision } from './vision.js';
import { initVisualPanel } from './visualcmp.js';
import { MaterialLibrary } from './materials/library.js';
import { ThumbnailRenderer } from './materials/pbr.js';
import { materialEditor, materialMenu, DND_TYPE, noteRecentMaterial } from './materials/panel.js';
import { icon, featureIcon } from './icons.js';
import { selectionContext, actionsFor, groupRuns, ACTIONS, featureOf } from './actions.js';
import { initPalette } from './palette.js';
import { openSettings, loadTheme } from './settings.js';
import { initPanels, getPref, setPref } from './panels.js';
import { initComponents, openComponent, createComponent, replaceComponent, publishProject } from './components.js';
import { DimensionRenderer } from './dimensions.js';
import { ANCHORS, AXIS_LABEL, PANEL_ORDER } from './dimensions-model.js';
import { units, setUnits } from './units.js';
import { InteractiveTransformController } from './transform.js';
import { PathEditorController, viewToPath } from './path-editor.js';
import { sweepPreview } from './path-model.js';
import { initTracePanel } from './vectorize-panel.js';
import { initShortcuts } from './shortcuts.js';
import { isToggleKey } from './shortcuts-model.js';
import { initPrintSim } from './printsim-panel.js';

const S = { state: null, previewing: null, playing: false, paletteAll: [], busyScene: false, level: 'review', filter: 'auto',
  openNode: null, renaming: null, histSel: null, histOpen: getPref('history', true) };

// Icons are declared in the markup (data-icon) and drawn here, so there is one icon set.
function hydrateIcons(root = document) {
  for (const el of root.querySelectorAll('[data-icon]:not([data-ic])')) { el.dataset.ic = '1'; el.prepend(icon(el.dataset.icon)); }
}
hydrateIcons();
const panels = initPanels({ layout: $('layout'), toggles: { left: $('btn-panel-left'), right: $('btn-panel-right') } });

// ---- viewport wiring -----------------------------------------------------------------

// Visual materials: presentation only, resolved from the library at render time.
const library = new MaterialLibrary();
const textureUrl = (id, file) => materialUrl(id, file);

let dimOverlay = null; // the selected objects' editable dimensions (created right after the viewport)
const vp = new Viewport($('gl'), {
  library, textureUrl,
  onPick: async (p, mods) => {
    try {
      const mode = mods.shift || mods.ctrl ? 'add' : 'replace';
      const body = p ? { body: p.body, kind: p.kind, index: p.index ?? -1, triangle: p.triangle ?? -1, mode } : { mode };
      const sel = await api('pick', body);
      applySelection(sel);
    } catch (e) { toast(e.message, 'bad'); }
  },
  onBoxSelect: async (ids, mods) => {
    try {
      const picked = ids.map((id) => `body(feature=${id})`);
      const cur = S.selection?.selectors ?? [];
      const selectors = mods.shift || mods.ctrl ? [...new Set([...cur, ...picked])] : picked;
      applySelection(await api('select', { selectors }));
    } catch (e) { toast(e.message, 'bad'); }
  },
  onContext: async (p, at) => {
    try {
      // Right-clicking an object that is not part of the selection selects it first.
      if (p && !(S.selection?.selectors ?? []).some((x) => featureOf(x) === p.body)) {
        applySelection(await api('pick', { body: p.body, kind: p.kind, index: p.index ?? -1, triangle: p.triangle ?? -1, mode: 'replace' }));
      } else if (!p) applySelection(await api('select', { selectors: [] }));
    } catch (e) { toast(e.message, 'bad'); return; }
    const n = S.selection?.selectors?.length ?? 0;
    const ids = [...new Set((S.selection?.selectors ?? []).map(featureOf).filter(Boolean))];
    const hasMat = (S.state?.tree ?? []).some((t) => ids.includes(t.id) && t.appearance?.material_id);
    const bodySel = (S.selection?.selectors ?? []).every((x) => x.startsWith('body('));
    const movable = ids.length === 1 && (S.selection?.transforms ?? []).some((t) => t.object_id === ids[0]);
    showMenu(at, [
      ...(movable ? [{ head: 'Transform' },
        { label: 'Move', icon: 'move', key: 'G', data: { xform: 'move' }, run: () => xform.begin('translate', { at }) },
        { label: 'Rotate', icon: 'rotate', key: 'R', data: { xform: 'rotate' }, run: () => xform.begin('rotate', { at }) }, { sep: true }] : []),
      ...(ids.length ? [...materialMenu(materialCtx, ids, { hasMaterial: hasMat, openPanel: openMaterialPanel }), { sep: true }] : []),
      ...(ids.length && bodySel ? [{ label: 'Duplicate…', icon: 'duplicate', key: 'Ctrl+D', run: () => actionEnv.duplicate() },
        { label: 'Group…', icon: 'group', key: 'Ctrl+G', run: () => actionEnv.group() }, { sep: true }] : []),
      { label: n > 1 ? `Delete ${n} selected` : 'Delete', icon: 'trash', key: 'Del', danger: true, disabled: n === 0, run: () => deleteObjects() }]);
  },
  onHover: (p) => {
    $('hud').textContent = p ? `${p.kind} ${p.kind === 'body' ? p.body : '#' + p.index} on ${p.body}` : hudStats();
  },
  onMeasure: (m) => {
    const label = $('measure-label');
    if (!m) { label.hidden = true; $('hud').textContent = vp.measure.on ? 'Measure: click two points' : hudStats(); return; }
    const d = m.d;
    label.hidden = false;
    label.textContent = `${units.length(m.distance)}  (Δ ${[d.x, d.y, d.z].map((v) => units.length(v, { unit: false })).join(', ')})`;
    $('hud').textContent = `distance ${units.length(m.distance)}`;
    placeMeasureLabel(m.mid);
  },
  onFrame: () => {
    if (vp.measure.pts.length === 2) placeMeasureLabel(vp.measure.pts[0].clone().lerp(vp.measure.pts[1], 0.5));
    dimOverlay?.update();
  },
  onPaneMenu: (i, el) => showPaneMenu(i, el),
  // A click on a path curve opens it in the path editor; the editor follows scene updates.
  onPathPick: (pp) => { if (pathEditor.active || xform.active) return false; return startPathEdit(pp.pathId); },
  onBodyPathPick: (body) => { const t = S.state?.tree?.find((n) => n.id === body); return t?.type === 'sweep' && !pathEditor.active && startPathEdit(t.params?.path); },
  onPaths: (list) => pathEditor?.sync(list),
  onLayout: (st) => views.save(st),
});
window.simonViewport = vp;
window.simonToast = (...a) => toast(...a); // for the e2e flows
dimOverlay = new DimensionRenderer(vp, { onEdit: (id, axis, value) => editDimension(id, axis, value), onInvalid: (msg) => toast(msg, 'bad') });
window.simonDimensions = dimOverlay;

// Interactive Move (G) / Rotate (R): a modal operation previewed on the body's matrix and
// committed as one move_object / rotate_object call (the same TransformService the agent uses).
const xform = new InteractiveTransformController(vp, {
  selection: () => S.selection,
  workspace: () => S.state?.workspace,
  commit: async (cmds) => {
    try {
      let ok = true;
      for (const { name, args } of cmds) {
        const o = await api('tool', { name, args });
        if (!o.ok) { toast(o.error, 'bad'); ok = false; break; }
        if (!o.committed) toast(`${name === 'move_object' ? 'Move' : 'Rotate'} proposed: review it in the conversation`, 'ok');
      }
      await refresh();
      return ok;
    } catch (e) { toast(e.message, 'bad'); await refresh(); return false; }
  },
  // The editable dimensions would float at the old place: hidden while the object moves.
  onModal: (on) => {
    // Box select disables the camera controls: a Move/Rotate leaves it, or the view stays locked afterwards.
    if (on && vp.boxMode) toggleBoxSelect();
    dimOverlay?.set(on ? [] : S.selection?.dimensions ?? []);
    document.body.classList.toggle('transforming', on);
  },
  notify: (msg, kind) => toast(msg, kind),
});
window.simonTransform = xform;

// Path editor: draw a path point by point, or edit one (points, Bezier handles, width / height /
// twist). Every finished gesture is one create_path / update_path call (one undo step); while a
// point is dragged only the viewport changes, including a reduced preview of the sweeps that use it.
const pathEditor = new PathEditorController(vp, $('viewport'), {
  workspace: () => S.state?.workspace,
  preview: (id) => (id ? sweepPreview(S.state?.tree ?? [], id) : null),
  notify: (msg, kind) => toast(msg, kind),
  onModal: (on) => {
    if (on && vp.boxMode) toggleBoxSelect();
    if (on && vp.measure.on) toggleMeasure();
    if (on) $('empty').hidden = true;
    dimOverlay?.set(on ? [] : S.selection?.dimensions ?? []);
    document.body.classList.toggle('path-editing', on);
  },
  commit: async ({ name, args }) => {
    try {
      const o = await api('tool', { name, args });
      if (!o.ok) { toast(o.error, 'bad'); await refresh(); return false; }
      if (!o.committed) toast('Path change proposed: review it in the conversation', 'ok');
      await refresh();
      return true;
    } catch (e) { toast(e.message, 'bad'); await refresh(); return false; }
  },
});
window.simonPathEditor = pathEditor;

// Trace image: PNG / JPEG / SVG → sketches and paths with a live preview (vectorize-panel.js).
const tracePanel = initTracePanel({ toast: (m, k) => toast(m, k), afterCreate: () => refresh() });
window.simonTrace = tracePanel;

function startPathDraw(type) {
  if (xform.active) return;
  S.playing || S.printsim ? toast('Leave playback / print simulation first', 'bad') : pathEditor.begin('draw', { type });
}

function startPathEdit(id) {
  const view = vp.pathData?.get(id);
  if (!view || xform.active || S.playing || S.printsim) return false;
  pathEditor.begin('edit', { id, path: viewToPath(view) });
  return true;
}
// The viewport has keyboard focus for G/R when it was the last thing clicked or is under the mouse.
document.addEventListener('pointerdown', (e) => { S.vpFocus = $('viewport').contains(e.target); }, true);
const viewportFocused = () => S.vpFocus || $('viewport').matches(':hover');

// ---- materials: library, thumbnails, drag & drop --------------------------------------

let thumbs = null;                 // lazy: needs the viewport's environment map
const thumbCache = new Map();      // id@version -> data URL promise
const materialCtx = {
  library,
  thumb: (def) => {
    const key = `${def.id}@${library.version}`;
    if (!thumbCache.has(key)) {
      thumbs ??= new ThumbnailRenderer(textureUrl);
      thumbCache.set(key, thumbs ? thumbs.render(def) : Promise.resolve(''));
    }
    return thumbCache.get(key);
  },
  apply: (tool, ids, extra) => applyAppearance(tool, ids, extra),
  // The picker can be open in the selection panel and in an expanded tree row; repaint both.
  rerender: () => { renderSelection(S.selection); if (S.state) renderTree(S.state); },
};

let materialsFor = null;
async function loadMaterials(path) {
  if (materialsFor === path && library.version) return;
  try {
    library.load(await api('materials'));
    materialsFor = path;
    thumbCache.clear();
    vp.reapplyMaterials();
    if (library.warnings.length) console.warn('materials:', library.warnings);
  } catch (e) { toast(e.message, 'bad'); }
}

const hasMat = (e) => [...(e.dataTransfer?.types ?? [])].includes(DND_TYPE);
async function dropMaterial(e, bodyId) {
  e.preventDefault();
  const id = e.dataTransfer.getData(DND_TYPE);
  if (!id || !bodyId) return;
  // Dropping on a selected object applies to the whole selection; otherwise just to that object.
  const sel = [...new Set((S.selection?.selectors ?? []).map(featureOf).filter(Boolean))];
  noteRecentMaterial(id);
  applyAppearance('set_material', sel.includes(bodyId) ? sel : [bodyId], { material_id: id });
}
$('viewport').addEventListener('dragover', (e) => { if (hasMat(e)) { e.preventDefault(); e.dataTransfer.dropEffect = 'copy'; } });
$('viewport').addEventListener('drop', (e) => { if (hasMat(e)) dropMaterial(e, vp.pick(e)?.body); });
$('tree').addEventListener('dragover', (e) => { if (hasMat(e) && e.target.closest?.('[data-id]')) { e.preventDefault(); e.dataTransfer.dropEffect = 'copy'; } });
$('tree').addEventListener('drop', (e) => { if (hasMat(e)) dropMaterial(e, e.target.closest?.('[data-id]')?.dataset.id); }); // for e2e tests and debugging

// ---- viewport layout: Single / Quad views ------------------------------------------------
// The layout, split and each view's orientation and camera are presentation state, saved per
// project in localStorage (never in the project file or its history).

const ORIENT_KEYS = ['perspective', 'top', 'bottom', 'front', 'back', 'left', 'right'];
const views = {
  key: null,
  timer: null,
  /** Restores the views saved for `project`; with none saved, the current views carry over. */
  load(project) {
    const key = `views.v1:${project}`;
    if (key === this.key) return;
    this.flush();
    this.key = key;
    const saved = getPref(key, null);
    if (saved) vp.layout.restore(saved); else this.save(vp.layout.serialize());
    renderCtxBar();
  },
  save(st) {
    this.pending = st;
    clearTimeout(this.timer);
    this.timer = setTimeout(() => this.flush(), 300);
  },
  flush() {
    clearTimeout(this.timer);
    if (!this.pending) return;
    if (this.key) setPref(this.key, this.pending);
    this.pending = null;
  },
};
addEventListener('pagehide', () => views.flush());

function setLayoutMode(mode) { vp.layout.setMode(mode); renderCtxBar(); }

function layoutMenuItems() {
  const quad = vp.layout.mode === 'quad';
  return [{ head: 'Viewport layout' },
    { label: 'Single View', checked: !quad, key: 'Ctrl+Alt+4', data: { layout: 'single' }, run: () => setLayoutMode('single') },
    { label: 'Quad View', checked: quad, key: 'Ctrl+Alt+4', data: { layout: 'quad' }, run: () => setLayoutMode('quad') }];
}

/** The menu of a view's label: its orientation, fit, maximize and the layout. */
function showPaneMenu(i, el) {
  const pane = vp.layout.panes[i];
  const lay = vp.layout;
  showMenu({ el }, [
    { head: 'View' },
    ...ORIENT_KEYS.map((o) => ({ label: o[0].toUpperCase() + o.slice(1), checked: pane.config.orientation === o, data: { orientation: o },
      run: () => lay.setOrientation(i, o) })),
    { sep: true },
    { label: 'Fit to scene', icon: 'fit', key: 'F', run: () => { lay.setActive(i); vp.fit(); } },
    lay.mode === 'quad' ? { label: lay.maximized === i ? 'Restore quad view' : 'Maximize view', icon: lay.maximized === i ? 'minimize' : 'maximize',
      key: 'Double-click', run: () => lay.toggleMaximize(i) } : null,
    { label: pane.config.showAxisGizmo !== false ? 'Hide axis gizmo' : 'Show axis gizmo', icon: 'view', run: () => lay.setGizmoVisible(i, pane.config.showAxisGizmo === false) },
    { sep: true },
    ...layoutMenuItems()]);
}

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
  if (S.state) renderTree(S.state);
  renderCtxBar();
  xform.validate();
}

// Runs one tool through the registry. A refused call (outcome not ok) and a failed request both show an
// error toast, except a refusal whose message matches `ignore` (one not worth telling the user about).
// Resolves to the outcome, or null when the request itself failed. Refreshing the page is up to the caller.
async function runTool(name, args, { ignore } = {}) {
  try {
    const o = await api('tool', { name, args });
    if (!o.ok && !(ignore && ignore.test(o.error ?? ''))) toast(o.error, 'bad');
    return o;
  } catch (err) { toast(err.message, 'bad'); return null; }
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
      xform.validate(); // an agent edit, undo or project switch may have replaced the target
      $('empty').hidden = vp.bodies.size > 0 || (delta.sketches ?? []).length > 0 || (delta.paths ?? []).length > 0 || pathEditor.active;
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
    printsim.onModelChanged();
    vp.setSelection(st.selection.highlights ?? []);
    if (S.previewing && !st.pending.some((c) => c.id === S.previewing)) clearPreview();
  } catch (e) { toast(e.message, 'bad'); }
}

let refreshTimer = null;
function scheduleRefresh() {
  clearTimeout(refreshTimer);
  refreshTimer = setTimeout(refresh, 60);
}

// Notices sent while the connection was down are lost, so the page is refetched when it returns.
let connLost = false;
const onConnection = (status) => {
  if (status === 'lost') { connLost = true; $('conn').hidden = false; return; }
  $('conn').hidden = true;
  if (connLost) { connLost = false; scheduleRefresh(); }
};

subscribe((n) => {
  visualPanel.onNotice(n);
  printsim.onNotice(n);
  if (n.kind === 'refine' && n.refine) { S.refineState = n.refine; renderAgentState(); return; }
  if (n.kind === 'visual') return;
  if (n.kind === 'chat' && n.chat) { addChat(n.chat); return; }
  if (n.kind === 'selection') {
    // The notice carries the selection; refetch the whole state only from a server that does not send it.
    if (n.selection) { if (S.state) S.state.selection = n.selection; applySelection(n.selection); }
    else api('state').then((st) => { S.state = st; applySelection(st.selection); }).catch(() => {});
    return;
  }
  if (n.kind === 'agent_state' && n.state) { S.agentState = n.state; renderAgentState(); return; }
  if (n.kind === 'model' && n.model) { S.model = n.model; renderModel(); return; }
  if (n.kind === 'state' || n.kind === 'agent_done') scheduleRefresh();
}, onConnection);

// ---- rendering of panels ---------------------------------------------------------------

// Remote fallback authorization. When the local primary model cannot serve a request and the fallback is a cloud
// model, the server refuses it (code "fallback_consent") until the user decides; `run` is sent again once allowed.
// Nothing is sent remotely on Cancel, and a remembered answer is saved in Settings (Fallback Model › Remote data).
async function withFallbackConsent(run) {
  try { return await run(); } catch (err) {
    if (err.code !== 'fallback_consent') throw err;
    const fb = err.data?.fallback ?? {};
    const remote = `${({ openai: 'OpenAI', claude: 'Anthropic' })[fb.provider] ?? fb.provider} · ${fb.model}`;
    const { ok, remember } = await confirmBox({ title: 'Send this request to a remote model?',
      body: `${err.data?.selected ?? 'The local model'} cannot handle it locally. To process it, SimonCAD will send the attachments `
        + `and the context the request needs to the fallback model, ${remote}, outside this computer.`,
      ok: 'Allow', okClass: 'primary', remember: true });
    if (ok || remember) await api('fallback/consent', { allow: ok, remember });
    if (!ok) { const e = new Error('Cancelled: nothing was sent to the remote model.'); e.cancelled = true; throw e; }
    return run();
  }
}

// Image-to-CAD: attachments, interpretation panel (all changes still go through tool calls).
const vision = initVision({
  $, toast: (...a) => toast(...a), getState: () => S.state, getSelection: () => S.selection,
  showPreview: (id) => showPreview(id), refresh: () => refresh(), withFallbackConsent,
});

// Visual comparison panel and closed-loop refinement (reference images stay constraints).
const visualPanel = initVisualPanel({
  $, toast: (...a) => toast(...a), getState: () => S.state, withFallbackConsent,
  attachmentIds: () => vision.attachmentIds(), clearAttachments: () => vision.clearAttachments(),
});

// Print simulation: a printer scene in this same viewport, a Print tab in the agent panel and a playback bar.
// It only reads the model (slicing runs on the server); the CAD scene is hidden, not changed.
const printsim = initPrintSim({
  vp, api, apiBytes, toast: (...a) => toast(...a), $, S,
  clearSelection: async () => { try { applySelection(await api('select', { selectors: [] })); } catch { /* nothing selected */ } },
  onChange: () => { renderCtxBar(); $('hud').textContent = hudStats(); },
});
window.simonPrintSim = printsim;

// Workspace settings of the project (units, grid, snapping): the grid redraws at once and every
// displayed length follows the display units. Values stay canonical millimetres underneath.
function applyWorkspace(ws) {
  if (!ws) return;
  vp.setWorkspace(ws);
  if (setUnits(ws)) {
    dimOverlay?.refresh();
    if (S.selection) renderSelection(S.selection);
  }
}

// Notes about the project as it was opened (e.g. saved with another OpenCASCADE version): once per project.
function showOpenWarnings(st) {
  const key = st.path || st.name;
  if (!st.open_warnings?.length || S.warnedOpen === key) return;
  S.warnedOpen = key;
  for (const w of st.open_warnings) toast(w, 'warn', 9000);
}

function render(st) {
  showOpenWarnings(st);
  S.selection = st.selection;
  if (!st.refine_state) S.refineState = null;
  S.level = st.level;
  loadMaterials(st.path ?? st.name);
  views.load(st.path ?? st.name);
  applyWorkspace(st.workspace);
  // replaceChildren stringifies null into a "null" text node, so drop absent children first.
  $('project').replaceChildren(...[h('span', { class: 'pname' }, st.name), st.dirty ? h('span', { class: 'dirty', title: 'Unsaved changes' }, '●') : null, icon('down')].filter(Boolean));
  $('btn-undo').disabled = !st.can_undo || st.busy;
  $('btn-clear').disabled = st.busy || (st.chat.length === 0 && !(st.specs ?? []).length);
  $('btn-redo').disabled = !st.can_redo || st.busy;
  $('send').disabled = st.busy;
  $('send').classList.toggle('busy', !!st.busy);
  $('send').title = st.busy ? 'Working…' : 'Send (Enter)';
  renderTree(st);
  renderSelection(st.selection);
  renderTimeline(st);
  renderChat(st);
  renderUsage(st.usage);
  S.model = st.model ?? null; S.primary = st.primary ?? null;
  renderModel();
  renderCtxBar();
  components.render(st);
  vision.onState(st);
}

// Touch equivalent of right-click: press and hold. A click that follows a long-press is swallowed.
function onLongPress(el, fn, ms = 500) {
  let t = null, x0 = 0, y0 = 0;
  const cancel = () => { clearTimeout(t); t = null; };
  el.addEventListener('pointerdown', (e) => {
    if (e.pointerType === 'mouse') return;
    x0 = e.clientX; y0 = e.clientY; cancel();
    t = setTimeout(() => { t = null; el._lp = performance.now(); fn({ x: x0, y: y0 }); }, ms);
  });
  el.addEventListener('pointermove', (e) => { if (t && Math.hypot(e.clientX - x0, e.clientY - y0) > 8) cancel(); });
  for (const ev of ['pointerup', 'pointercancel', 'pointerleave']) el.addEventListener(ev, cancel);
}
const justLongPressed = (el) => performance.now() - (el._lp ?? -1e9) < 700;

function renderTree(st) {
  const el = $('tree');
  if (!st.tree.length) { el.replaceChildren(h('div', { class: 'muted' }, 'No features yet.')); return; }
  const sel = S.selection ?? st.selection;
  const selFeatures = new Set((sel.selectors ?? []).map(featureOf));
  const nodeEl = (n) => {
    const open = S.openNode === n.id;
    const selected = selFeatures.has(n.id);
    const hiddenApp = n.appearance?.visible === false;
    const renaming = S.renaming === n.id;
    const row = h('div', { class: 'row', role: 'treeitem', 'aria-selected': String(selected), 'aria-expanded': String(open), tabindex: '0',
      title: n.intent ? `${n.type} — ${n.intent}` : n.type, dataset: { id: n.id },
      oncontextmenu: (e) => { e.preventDefault(); if (!justLongPressed(row)) treeMenu(n, { x: e.clientX, y: e.clientY }); },
      onclick: (e) => { if (justLongPressed(row)) return; rowClick(n, e); },
      ondblclick: () => startRename(n),
      onkeydown: (e) => {
        if (e.target !== row) return;
        if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); rowClick(n, e); }
        else if (e.key === 'F2') { e.preventDefault(); startRename(n); }
        else if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
          e.preventDefault();
          const rows = [...el.querySelectorAll('.row')];
          rows[rows.indexOf(row) + (e.key === 'ArrowDown' ? 1 : -1)]?.focus();
        }
      } },
      n.visible ? h('button', { type: 'button', class: 'eye', 'aria-pressed': String(!hiddenApp),
        'aria-label': `${hiddenApp ? 'Show' : 'Hide'} ${n.name}`, title: hiddenApp ? 'Show' : 'Hide',
        onclick: (e) => { e.stopPropagation(); applyAppearance('set_visibility', [n.id], { visible: hiddenApp }); } },
        icon(hiddenApp ? 'eye-off' : 'eye')) : h('span', { style: 'width:34px;flex:none' }),
      h('span', { class: 'ficon' }, icon(featureIcon(n.type))),
      n.suppressed || n.status !== 'ok' ? h('span', { class: `dot ${n.suppressed ? 'suppressed' : n.status}`, title: n.suppressed ? 'suppressed' : n.status }) : null,
      renaming ? renameInput(n) : h('span', { class: 'name' }, n.name),
      n.component ? h('span', { class: `src component${n.out_of_date ? ' stale' : ''}`,
        title: n.out_of_date ? `references the component ${n.component}, which has changed since` : `references the component ${n.component}` },
        n.out_of_date ? 'Component ·' : 'Component') : null,
      n.created_by === 'agent' ? h('span', { class: 'src agent', title: n.capability ? `agent · ${n.capability}` : 'created by the agent' }, 'agent') : null,
      n.visible ? null : h('span', { class: 'src', title: 'Consumed by a later feature' }, 'input'),
      selected ? h('button', { type: 'button', class: 'more', 'aria-haspopup': 'menu', 'aria-label': `Actions for ${n.name}`,
        onclick: (e) => { e.stopPropagation(); treeMenu(n, { el: e.currentTarget }); } }, icon('more')) : null);
    onLongPress(row, (pt) => treeMenu(n, pt));
    const kids = [row];
    if (open) {
      kids.push(h('div', { class: 'detail' },
        n.intent ? h('div', { class: 'intent' }, n.intent) : null,
        h('div', { class: 'meta' }, h('span', {}, n.type), h('span', {}, '·'), h('span', {}, n.id), n.created_by ? h('span', {}, '· ' + n.created_by) : null),
        n.errors.length ? h('div', { class: 'errors' }, n.errors[0]) : null,
        n.visible ? appearanceEditor([n]) : null, paramEditor(n)));
    } else if (n.errors.length) kids.push(h('div', { class: 'detail' }, h('div', { class: 'errors' }, n.errors[0])));
    return h('div', { class: `node${selected ? ' sel' : ''}${n.suppressed || hiddenApp ? ' off' : ''}`, role: 'none', dataset: { type: n.type } }, ...kids);
  };
  // Grouped features are listed under their group, which sits where its first member is.
  const out = [];
  const done = new Set();
  for (const n of st.tree) {
    if (!n.group) { out.push(nodeEl(n)); continue; }
    if (done.has(n.group)) continue;
    done.add(n.group);
    const members = st.tree.filter((t) => t.group === n.group);
    out.push(groupEl(n.group, members, selFeatures, nodeEl));
  }
  el.replaceChildren(...out);
  if (S.renaming) el.querySelector('input.rename')?.focus();
}

// A design-tree group: organization only. Clicking it selects its visible bodies.
S.collapsed = new Set(getPref('collapsedGroups', []));
function groupEl(name, members, selFeatures, nodeEl) {
  const bodies = members.filter((m) => m.visible);
  const closed = S.collapsed.has(name);
  const allSel = bodies.length > 0 && bodies.every((m) => selFeatures.has(m.id));
  const hidden = bodies.length > 0 && bodies.every((m) => m.appearance?.visible === false);
  const fold = () => {
    if (closed) S.collapsed.delete(name); else S.collapsed.add(name);
    setPref('collapsedGroups', [...S.collapsed]);
    renderTree(S.state);
  };
  const select = async (e) => {
    const mode = e?.shiftKey || e?.ctrlKey || e?.metaKey ? 'add' : 'replace';
    const cur = mode === 'add' ? (S.selection?.selectors ?? []) : [];
    try { applySelection(await api('select', { selectors: [...new Set([...cur, ...bodies.map((m) => `body(feature=${m.id})`)])] })); } catch (err) { toast(err.message, 'bad'); }
  };
  const row = h('div', { class: 'row grp', role: 'treeitem', 'aria-expanded': String(!closed), 'aria-selected': String(allSel), tabindex: '0',
    title: `Group “${name}” · ${members.length} feature${members.length === 1 ? '' : 's'}`, dataset: { group: name },
    onclick: (e) => { if (!justLongPressed(row)) select(e); },
    oncontextmenu: (e) => { e.preventDefault(); if (!justLongPressed(row)) groupMenu(name, members, { x: e.clientX, y: e.clientY }); },
    onkeydown: (e) => {
      if (e.target !== row) return;
      if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); select(e); }
      else if (e.key === 'ArrowLeft' && !closed || e.key === 'ArrowRight' && closed) { e.preventDefault(); fold(); }
      else if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
        e.preventDefault();
        const rows = [...$('tree').querySelectorAll('.row')];
        rows[rows.indexOf(row) + (e.key === 'ArrowDown' ? 1 : -1)]?.focus();
      }
    } },
  h('button', { type: 'button', class: 'eye fold', 'aria-label': `${closed ? 'Expand' : 'Collapse'} ${name}`, title: closed ? 'Expand' : 'Collapse',
    onclick: (e) => { e.stopPropagation(); fold(); } }, icon(closed ? 'right' : 'down')),
  h('span', { class: 'ficon' }, icon('group')),
  h('span', { class: 'name' }, name),
  h('span', { class: 'src' }, String(bodies.length || members.length)),
  bodies.length ? h('button', { type: 'button', class: 'eye', 'aria-pressed': String(!hidden), 'aria-label': `${hidden ? 'Show' : 'Hide'} group ${name}`, title: hidden ? 'Show group' : 'Hide group',
    onclick: (e) => { e.stopPropagation(); applyAppearance('set_visibility', bodies.map((m) => m.id), { visible: hidden }); } }, icon(hidden ? 'eye-off' : 'eye')) : null,
  h('button', { type: 'button', class: 'more', 'aria-haspopup': 'menu', 'aria-label': `Actions for group ${name}`,
    onclick: (e) => { e.stopPropagation(); groupMenu(name, members, { el: e.currentTarget }); } }, icon('more')));
  onLongPress(row, (pt) => groupMenu(name, members, pt));
  return h('div', { class: `group${allSel ? ' sel' : ''}${closed ? ' closed' : ''}`, role: 'group', 'aria-label': `Group ${name}` },
    h('div', { class: `node${allSel ? ' sel' : ''}${hidden ? ' off' : ''}`, role: 'none' }, row),
    closed ? null : h('div', { class: 'members' }, ...members.map(nodeEl)));
}

function groupMenu(name, members, at) {
  const ids = members.map((m) => m.id);
  const bodies = members.filter((m) => m.visible).map((m) => m.id);
  showMenu(at, [
    { label: 'Rename group…', icon: 'pencil', run: async () => {
      const v = await formDialog({ title: `Rename group “${name}”`, ok: 'Rename', fields: [{ key: 'name', label: 'Name', text: true, value: name }] });
      if (v?.name?.trim() && v.name.trim() !== name) await callTool('group', { features: ids, name: v.name.trim() }, 'Rename group');
    } },
    { label: 'Duplicate group', icon: 'duplicate', disabled: !bodies.length, run: async () => {
      if (!bodies.length) return;
      applySelection(await api('select', { selectors: bodies.map((id) => `body(feature=${id})`) }));
      actionEnv.duplicate();
    } },
    { label: 'Ungroup', icon: 'ungroup', run: () => callTool('ungroup', { group: name }, 'Ungroup') },
    { sep: true },
    { label: `Delete group contents`, icon: 'trash', danger: true, disabled: !bodies.length, run: () => deleteObjects({ features: bodies }) },
  ]);
}

// Click selects the object (and shows its details); Shift adds, Ctrl/Cmd toggles; clicking
// the one selected object again folds its details away.
async function rowClick(n, e) {
  if (S.renaming) return;
  const redraw = () => { if (S.state) renderTree(S.state); };
  if (!n.visible) { S.openNode = S.openNode === n.id ? null : n.id; redraw(); return; } // consumed features cannot be picked, only inspected
  const sels = S.selection?.selectors ?? [];
  const mode = e.shiftKey ? 'add' : (e.ctrlKey || e.metaKey) ? 'toggle' : 'replace';
  if (mode === 'replace' && sels.length === 1 && /^body\(/.test(sels[0]) && featureOf(sels[0]) === n.id) { S.openNode = S.openNode === n.id ? null : n.id; redraw(); return; }
  S.openNode = mode === 'replace' ? n.id : null;
  try { applySelection(await api('pick', { body: n.id, kind: 'body', index: -1, triangle: -1, mode })); } catch (err) { toast(err.message, 'bad'); }
}

function renameInput(n) {
  let done = false;
  const finish = async (commit) => {
    if (done) return; done = true;
    const v = input.value.trim();
    S.renaming = null;
    if (S.state) renderTree(S.state);
    if (commit && v && v !== n.name) await callTool('rename_feature', { feature: n.id, name: v });
  };
  const input = h('input', { type: 'text', class: 'rename', value: n.name, 'aria-label': `Rename ${n.name}`,
    onclick: (e) => e.stopPropagation(), ondblclick: (e) => e.stopPropagation(),
    onkeydown: (e) => { e.stopPropagation(); if (e.key === 'Enter') { e.preventDefault(); finish(true); } else if (e.key === 'Escape') { e.preventDefault(); finish(false); } },
    onblur: () => finish(true) });
  setTimeout(() => input.select(), 0);
  return h('span', { class: 'name' }, input);
}
function startRename(n) { S.renaming = n.id; renderTree(S.state); }

function treeMenu(n, at) {
  const selIds = [...new Set((S.selection?.selectors ?? []).map(featureOf).filter(Boolean))];
  const ids = selIds.includes(n.id) && selIds.length > 1 ? selIds : [n.id];
  const hiddenApp = n.appearance?.visible === false;
  const nodes = ids.map((id) => S.state.tree.find((t) => t.id === id)).filter(Boolean);
  showMenu(at, [
    n.type === 'path' && { label: 'Edit path', icon: 'path', run: () => startPathEdit(n.id) },
    n.type === 'sweep' && n.params?.path && { label: `Edit path ${n.params.path}`, icon: 'path', run: () => startPathEdit(n.params.path) },
    n.visible && { label: hiddenApp ? 'Show' : 'Hide', icon: hiddenApp ? 'eye' : 'eye-off', run: () => applyAppearance('set_visibility', ids, { visible: hiddenApp }) },
    { label: 'Rename', icon: 'pencil', key: 'F2', disabled: ids.length > 1, run: () => startRename(n) },
    { label: n.suppressed ? 'Unsuppress' : 'Suppress', icon: 'ban', run: () => callTools(nodes.map((t) => ({ tool: 'suppress_feature', args: { feature: t.id, suppressed: !n.suppressed } })), n.suppressed ? 'Unsuppress' : 'Suppress') },
    { sep: true },
    // Component instances are references: they are opened, re-pinned, swapped or turned into
    // local geometry. Everything else can become a component.
    n.component && { label: 'Open component', icon: 'folder', run: () => openComponent(n.component) },
    n.component && n.out_of_date && { label: 'Update to latest', icon: 'restore', run: () => callTool('sync_component', { instances: [n.id] }, 'Update component') },
    n.component && { label: 'Replace component…', icon: 'component', run: () => replaceComponent(n) },
    n.component && { label: 'Detach component', icon: 'ungroup', run: () => callTool('detach_component', { instance: n.id }, 'Detach component') },
    !n.component && n.visible && { label: 'Create component…', icon: 'component', run: () => withSelected(ids, () => createComponent(ids)) },
    { sep: true },
    n.visible && { label: 'Duplicate…', icon: 'duplicate', key: 'Ctrl+D', run: () => withSelected(ids, actionEnv.duplicate) },
    { label: 'Group…', icon: 'group', key: 'Ctrl+G', run: () => withSelected(ids, actionEnv.group) },
    nodes.some((t) => t.group) && { label: 'Remove from group', icon: 'ungroup', run: () => callTool('ungroup', { features: nodes.filter((t) => t.group).map((t) => t.id) }, 'Ungroup') },
    { sep: true },
    { label: ids.length > 1 ? `Delete ${ids.length} objects` : `Delete ${n.name}`, icon: 'trash', key: 'Del', danger: true, run: () => deleteObjects({ features: ids }) },
  ]);
}

// Runs a selection-based action on `ids` (a tree menu may target rows that are not selected).
async function withSelected(ids, fn) {
  const cur = new Set(selIds());
  if (ids.length !== cur.size || ids.some((id) => !cur.has(id))) {
    try { applySelection(await api('select', { selectors: ids.map((id) => `body(feature=${id})`) })); } catch (e) { toast(e.message, 'bad'); return; }
  }
  fn();
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

// ---- delete ------------------------------------------------------------------------------
// Delete is a ChangeSet like any other (delete_objects → RemoveFeature commands, one set for
// the whole selection, undoable). The server first reports what would happen; the UI only
// asks the two questions it may come back with. B-Rep faces/edges/vertices are never edited:
// they resolve to their owning feature, which is offered instead.

async function deleteObjects(req = {}) {
  if (S.state?.busy) return;
  try {
    for (let round = 0; round < 3; round++) {
      const r = await api('delete', req);
      if (r.needs_confirm === 'owner') {
        const owners = [...new Set(r.analysis.sub_entities.map((s) => s.owner))];
        const what = [...new Set(r.analysis.sub_entities.map((s) => s.kind))].join('/');
        const yes = await confirmBox({ title: `A ${what} cannot be deleted directly`,
          body: `Faces, edges and vertices are derived from features. ${owners.length > 1 ? 'They belong' : 'It belongs'} to ${owners.join(', ')}. Delete ${owners.length > 1 ? 'those features' : 'that feature'} instead? (To change the shape, edit its parameters in the design tree.)`,
          ok: `Delete ${owners.join(', ')}` });
        if (!yes) return;
        req = { ...req, resolve_owner: true };
        continue;
      }
      if (r.needs_confirm === 'dependents') {
        const yes = await confirmBox({ title: 'Other features depend on this',
          body: `${r.analysis.dependents.join(', ')} depend${r.analysis.dependents.length > 1 ? '' : 's'} on ${r.analysis.targets.join(', ')}. Delete ${r.analysis.dependents.length > 1 ? 'them' : 'it'} too, in the same undoable change?`,
          ok: 'Delete all' });
        if (!yes) return;
        req = { ...req, cascade: true };
        continue;
      }
      if (!r.ok) toast(r.error, 'bad');
      else toast(`Deleted ${r.changeset.removed.join(', ')} · Ctrl+Z to undo`, 'ok');
      break;
    }
  } catch (e) { toast(e.message, 'bad'); }
  scheduleRefresh();
}

// ---- appearance (color / opacity / visibility) -----------------------------------------
// Edits go through the tool registry like any other change. They are presentation only:
// the viewport updates from the CAD-IR without a geometry rebuild.

async function applyAppearance(tool, ids, extra = {}) {
  await runTool(tool, { features: ids, ...extra }, { ignore: /already as requested/ });
  scheduleRefresh();
}

const HEX_RE = /^#?([0-9a-f]{6})$/i;
function parseColor(text) {
  const t = text.trim();
  const m = HEX_RE.exec(t);
  if (m) return '#' + m[1].toUpperCase();
  const rgb = /^rgb\(?\s*(\d{1,3})\s*[, ]\s*(\d{1,3})\s*[, ]\s*(\d{1,3})\s*\)?$/i.exec(t);
  if (rgb && [1, 2, 3].every((i) => Number(rgb[i]) <= 255)) {
    return '#' + [1, 2, 3].map((i) => Number(rgb[i]).toString(16).padStart(2, '0')).join('').toUpperCase();
  }
  return null;
}

/** Appearance controls for one or many tree nodes; differing values show as "mixed". */
function appearanceEditor(nodes) {
  const ids = nodes.map((n) => n.id);
  const uniq = (f) => { const v = new Set(nodes.map(f)); return v.size === 1 ? [...v][0] : undefined; };
  const color = uniq((n) => n.appearance?.color ?? '');  // color overrides the material's base color
  const opacity = uniq((n) => n.appearance?.opacity ?? 1);
  const visible = uniq((n) => n.appearance?.visible !== false);
  const key = ids.join('-');

  const picker = h('input', { id: `ap-c-${key}`, type: 'color', value: color || '#9FB2C8', 'aria-label': 'Color',
    onchange: (e) => applyAppearance('set_color', ids, { color: e.target.value.toUpperCase() }) });
  const hex = h('input', { id: `ap-h-${key}`, type: 'text', value: color ?? '', placeholder: color === undefined ? 'mixed' : 'default',
    'aria-label': 'Color as hex or rgb', spellcheck: 'false',
    onchange: (e) => {
      if (!e.target.value.trim()) { applyAppearance('reset_appearance', ids); return; }
      const c = parseColor(e.target.value);
      if (!c) { toast('Use #RRGGBB or rgb(r,g,b)', 'bad'); e.target.value = color ?? ''; return; }
      applyAppearance('set_color', ids, { color: c });
    } });
  const pct = h('output', {}, opacity === undefined ? 'mixed' : `${Math.round(opacity * 100)}%`);
  const slider = h('input', { id: `ap-o-${key}`, type: 'range', min: '0', max: '100', value: String(Math.round((opacity ?? 1) * 100)),
    'aria-label': 'Opacity',
    oninput: (e) => { pct.textContent = `${e.target.value}%`; },
    onchange: (e) => applyAppearance('set_opacity', ids, { opacity: Number(e.target.value) / 100 }) });
  const vis = h('input', { id: `ap-v-${key}`, type: 'checkbox', checked: visible !== false, 'aria-label': 'Visible',
    onchange: (e) => applyAppearance('set_visibility', ids, { visible: e.target.checked }) });
  if (visible === undefined) vis.indeterminate = true;
  const reset = h('button', { type: 'button', class: 'small', onclick: () => applyAppearance('reset_appearance', ids) }, 'Reset appearance');

  return h('div', { class: 'params appearance', role: 'group', 'aria-label': ids.length > 1 ? `Appearance of ${ids.length} objects` : 'Appearance' },
    h('label', { for: picker.id }, 'color'), h('span', { class: 'color-row' }, picker, hex),
    h('label', { for: slider.id }, 'opacity'), h('span', { class: 'color-row' }, slider, pct),
    h('label', { for: vis.id }, 'visible'), vis,
    ...materialEditor(materialCtx, nodes),
    h('span', {}), reset);
}


// ---- dimensions (informational + editable) ------------------------------------------------
// The server measures (DimensionService); a typed size is a set_object_dimension tool call,
// applied at once and undoable. The anchor is a per-user editing preference.

const dimAnchor = () => (ANCHORS.some(([v]) => v === getPref('dimAnchor', 'center')) ? getPref('dimAnchor', 'center') : 'center');

async function editDimension(id, axis, value) {
  await runTool('set_object_dimension', { object_id: id, axis, value, anchor: dimAnchor() });
  await refresh();
}

async function setLockProportions(id, enabled) {
  await runTool('set_object_proportional_scaling', { object_id: id, enabled });
  await refresh();
}

/** The Dimensions section of the object properties: width, height, depth, lock and anchor. */
function dimensionsEditor(d) {
  const rows = [];
  for (const axis of PANEL_ORDER) {
    const id = `dim-${axis}`;
    const input = h('input', { id, type: 'text', inputmode: 'decimal', value: units.compact(d[axis]), 'aria-label': `${AXIS_LABEL[axis]} (${units.symbol})`,
      onkeydown: (e) => { if (e.key === 'Escape') { e.stopPropagation(); e.target.value = units.compact(d[axis]); e.target.blur(); } else if (e.key === 'Enter') e.target.blur(); },
      onchange: (e) => {
        const r = units.parseLength(e.target.value);
        if (!r.ok) { toast(r.error, 'bad'); e.target.value = units.compact(d[axis]); return; }
        if (Math.abs(r.value - d[axis]) > 1e-9) editDimension(d.object_id, axis, r.value);
      } });
    rows.push(h('label', { for: id }, AXIS_LABEL[axis]), h('span', { class: 'dim-row' }, input, h('span', { class: 'muted' }, units.symbol)));
  }
  const lock = h('input', { id: 'dim-lock', type: 'checkbox', checked: !!d.lock_proportions, 'aria-label': 'Lock proportions',
    onchange: (e) => setLockProportions(d.object_id, e.target.checked) });
  const anchor = h('select', { id: 'dim-anchor', 'aria-label': 'Resize anchor', onchange: (e) => setPref('dimAnchor', e.target.value) },
    ...ANCHORS.map(([v, label]) => h('option', { value: v, selected: v === dimAnchor() }, label)));
  rows.push(h('label', { for: lock.id }, 'Lock proportions'), lock, h('label', { for: anchor.id }, 'Anchor'), anchor);
  return h('div', { class: 'params dimensions', role: 'group', 'aria-label': `Dimensions of ${d.object_id}` }, ...rows,
    d.rotated ? h('span', { class: 'muted dim-note' }, 'Measured along the object’s own (rotated) axes.') : null);
}

function renderSelection(sel) {
  const list = $('sel-list'), chips = $('chips');
  const ents = sel?.entities ?? [];
  dimOverlay?.set(sel?.dimensions ?? []);
  $('sel-section').hidden = !ents.length;
  if (!ents.length) { list.replaceChildren(); chips.replaceChildren(); return; }
  const selNodes = (S.state?.tree ?? []).filter((n) => n.visible && new Set(ents.map((e) => featureOf(e.selector))).has(n.id));
  const dims = sel?.dimensions ?? [];
  list.replaceChildren(...(dims.length === 1 ? [h('div', { class: 'sel-item' }, h('b', {}, `Dimensions · ${dims[0].object_id}`), dimensionsEditor(dims[0]))] : []),
    ...(selNodes.length ? [h('div', { class: 'sel-item' }, h('b', {}, `Appearance · ${selNodes.length} object${selNodes.length > 1 ? 's' : ''}`), appearanceEditor(selNodes))] : []), ...ents.map((e) => h('div', { class: 'sel-item' },
    h('code', {}, e.selector),
    h('div', { class: 'muted' }, [e.surface_type, e.area ? units.area(e.area) : null,
      `at (${e.centroid.map((v) => units.length(v, { unit: false })).join(', ')}) ${units.symbol}`].filter(Boolean).join(' · ')))));
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

// A failed tool call is hidden when the agent recovered within the same turn: the turn's last tool call succeeded
// (the same tool retried, or another tool reaching the goal). If the turn ends on a failure, every failure stays visible.
function retriedOk(chat, i) {
  const m = chat[i];
  if (m.role !== 'tool' || m.ok) return false;
  let last = null;
  for (let j = i + 1; j < chat.length && chat[j].role !== 'user'; j++) {
    if (chat[j].role === 'tool') last = chat[j];
  }
  return !!last && last.ok;
}

// An interpretation card stays where the chat was when it first appeared, so later messages follow it
// instead of it riding at the bottom for as long as the interpretation is unused.
const cardAnchor = new Map();

function renderChat(st) {
  const el = $('chat');
  const stick = el.scrollTop + el.clientHeight >= el.scrollHeight - 40;
  const lastSeq = st.chat.length ? st.chat[st.chat.length - 1].seq : 0;
  const cards = vision.proposalCards(st);
  for (const c of cards) if (!cardAnchor.has(c.id)) cardAnchor.set(c.id, lastSeq);
  const placed = (seq) => cards.filter((c) => cardAnchor.get(c.id) === seq).map((c) => c.node);
  const nodes = [];
  st.chat.forEach((m, i) => {
    chatSeen.add(m.seq);
    if (!retriedOk(st.chat, i)) {
      const cls = `msg ${m.role}${m.role === 'tool' && !m.ok ? ' bad' : ''}`;
      if (m.role === 'draft') nodes.push(draftCard(m, st));
      else if (m.role === 'tool') nodes.push(h('div', { class: cls }, `${m.ok ? '✓' : '✗'} ${m.tool}${m.text ? ' — ' + m.text : ''}`));
      else nodes.push(h('div', { class: cls }, m.text, vision.thumbs(m.images, m.docs)));
    }
    nodes.push(...placed(m.seq));
  });
  // anchored to a message that is gone (chat cleared, or the first card on an empty chat)
  const seqs = new Set(st.chat.map((m) => m.seq));
  nodes.push(...cards.filter((c) => !seqs.has(cardAnchor.get(c.id))).map((c) => c.node));
  for (const cs of st.pending) nodes.push(changeSetCard(cs));
  if (st.busy) nodes.push(h('div', { id: 'agent-state', class: 'msg system' }, st.refine_state ? refineText(st.refine_state) : agentStateText(st.agent_state)));
  el.replaceChildren(...nodes);
  if (stick) el.scrollTop = el.scrollHeight;
}

const PROVIDER_LABELS = { openai: 'OpenAI', claude: 'Anthropic', ollama: 'Ollama' };
const modelLabel = (m) => (m.provider ? `${PROVIDER_LABELS[m.provider] ?? m.provider} · ${m.model}` : m.model);

// Simon Agent header: the model serving the request as the router reports it ("model" notices), then the one that
// produced the last answer; before any request, the configured primary model, dimmed. A fallback says why on hover.
function renderModel() {
  const el = $('agent-model');
  const m = S.model ?? S.primary;
  if (!el) return;
  if (!m?.model) { el.hidden = true; el.replaceChildren(); return; }
  const label = modelLabel(m) + (m.fallback ? ' (Fallback)' : '');
  el.hidden = false;
  el.classList.toggle('fallback', !!m.fallback);
  el.classList.toggle('idle', !S.model);
  const where = m.local ? 'Local model' : 'Cloud model';
  el.title = m.fallback
    ? `${where} — fallback for ${m.requested || 'the primary model'}${m.reason ? `\n${m.reason}` : ''}`
    : `${where}${S.model ? '' : ' (primary; no request yet)'}`;
  el.replaceChildren(icon(m.local ? 'monitor' : 'cloud'), h('span', null, label));
}

// Token meter: the session total, with the last turn and the per-model split in the tooltip.
// Cost is only shown when prices are configured in Settings; otherwise tokens stand alone.
function renderUsage(u) {
  const el = $('usage');
  if (!el) return;
  if (!u || !u.session?.total_tokens) { el.hidden = true; el.replaceChildren(); return; }
  const n = (v) => v.toLocaleString();
  const one = (cur, a) => `${cur} ${a < 0.01 ? a.toFixed(4) : a.toFixed(2)}`;
  // Models priced in different currencies are listed side by side; no conversion is ever made.
  const amount = (c) => (c.amounts ? Object.keys(c.amounts).sort().map((k) => one(k, c.amounts[k])).join(' + ') : one(c.currency, c.amount));
  const parts = [`${n(u.session.total_tokens)} tokens`];
  if (u.session.cost.known) parts.push(`≈ ${amount(u.session.cost)}${u.session.cost.partial ? '*' : ''}`);
  else if (!u.session.cost.cloud_tokens && u.session.cost.local_tokens) parts.push('local');
  // Cached input is billed at a lower rate, so it is worth showing next to the raw counts.
  const cached = (c) => (c ? `, ${n(c)} cached` : '');
  const lines = [`Session (${u.turns} turn${u.turns === 1 ? '' : 's'}): ${n(u.session.input_tokens)} in${cached(u.session.cached_input_tokens)} / ${n(u.session.output_tokens)} out`];
  if (u.turn.total_tokens) lines.push(`Last turn: ${n(u.turn.input_tokens)} in${cached(u.turn.cached_input_tokens)} / ${n(u.turn.output_tokens)} out`);
  // Local models are counted, never costed: only cloud models are API spend.
  for (const m of u.session.models) lines.push(`${modelLabel(m)}${m.provider === 'ollama' ? ' (local, no cost)' : ''}: ${n(m.input_tokens)} in${cached(m.cached_input_tokens)} / ${n(m.output_tokens)} out (${m.calls} calls)`);
  const cost = u.session.cost;
  if (cost.local_tokens && cost.cloud_tokens) lines.push(`Local: ${n(cost.local_tokens)} tokens, no cost · Cloud: ${n(cost.cloud_tokens)} tokens`);
  if (!cost.known && cost.cloud_tokens) lines.push('No token prices configured for the cloud models — set them in Settings to estimate cost.');
  else if (!cost.cloud_tokens && cost.local_tokens) lines.push('Local models only — no API cost.');
  else if (u.session.cost.partial) lines.push(`* no price for: ${u.session.cost.unpriced.join(', ')}`);
  el.hidden = false;
  el.title = lines.join('\n');
  el.replaceChildren(h('span', { class: 'muted' }, parts.join('  ·  ')));
}

// Workflow progress: Planning… / Creating base… (2/5) / Validating model… / Correcting… / Done.
function agentStateText(ev) {
  if (!ev || !ev.message || ev.state === 'IDLE') return 'The agent is working…';
  let t = ev.message;
  if (ev.state === 'EXECUTING' && ev.step && ev.total) t += ` (${ev.step}/${ev.total})`;
  if (ev.state === 'CORRECTING' && ev.attempt) t += ` (attempt ${ev.attempt}/3)`;
  return t;
}

const refineText = (ev) => `Visual refinement — ${ev.message}${ev.scores?.overall !== undefined ? ` (overall ${Math.round(ev.scores.overall * 100)}%)` : ''}`;

function renderAgentState() {
  const el = document.getElementById('agent-state');
  if (el) el.textContent = S.refineState ? refineText(S.refineState) : agentStateText(S.agentState);
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
      h('li', {}, `volume ${units.volume(cs.volume_before_mm3)} → ${units.volume(cs.volume_after_mm3)} (${delta >= 0 ? '+' : ''}${units.volume(delta)})`),
      ...cs.warnings.map((w) => h('li', { class: 'warn' }, '⚠ ' + w)),
      ...checks),
    h('div', { class: 'actions' },
      h('button', { onclick: () => (prev ? clearPreview() : showPreview(cs.id)) }, prev ? 'Hide preview' : 'Preview'),
      h('button', { class: 'primary', disabled: !cs.valid, onclick: () => lifecycle('commit', cs.id) }, 'Commit'),
      h('button', { class: 'danger', onclick: () => lifecycle('reject', cs.id) }, 'Reject')));
}

function renderTimeline(st) {
  const el = $('timeline');
  const n = st.checkpoints.length;
  $('hist-title').textContent = n ? `History · ${n} step${n === 1 ? '' : 's'}` : 'History';
  const prev = el.scrollLeft, grew = n > (S.cpCount ?? 0);
  S.cpCount = n;
  if (S.histSel && !st.checkpoints.some((c) => c.id === S.histSel)) S.histSel = null;
  if (!n) el.replaceChildren(h('div', { class: 'none' }, 'Checkpoints appear here as the design evolves.'));
  else {
    el.replaceChildren(...st.checkpoints.map((c) => {
      const sel = S.histSel === c.id;
      return h('button', { type: 'button', class: `cp${c.current ? ' current' : ''}${sel ? ' sel' : ''}`, 'aria-pressed': String(sel), title: `${c.label}\n${c.time}`,
        onclick: () => { S.histSel = sel ? null : c.id; renderTimeline(S.state); } },
        h('div', { class: 'label' }, h('span', { class: 'n' }, c.seq), c.label),
        h('div', { class: 'sub' }, `${c.actor || 'user'} · ${c.changesets.length} change${c.changesets.length === 1 ? '' : 's'}`));
    }));
    el.scrollLeft = grew ? el.scrollWidth : prev;
  }
  // Restore / Compare exist only once a step is selected.
  const c = st.checkpoints.find((x) => x.id === S.histSel);
  const acts = $('hist-actions');
  acts.hidden = !c;
  if (c) acts.replaceChildren(
    h('button', { type: 'button', disabled: c.current || st.busy, onclick: () => restoreCheckpoint(c) }, icon('restore'), 'Restore'),
    h('button', { type: 'button', onclick: () => compareCheckpoint(c) }, icon('compare'), 'Compare'));
}

function applyHistoryOpen() {
  $('history').dataset.open = String(S.histOpen);
  $('hist-toggle').setAttribute('aria-expanded', String(S.histOpen));
}
$('hist-toggle').addEventListener('click', () => { S.histOpen = !S.histOpen; setPref('history', S.histOpen); applyHistoryOpen(); });
applyHistoryOpen();

// ---- ChangeSet lifecycle ----------------------------------------------------------------

async function showPreview(id) {
  try {
    const g = await api(`changeset/${id}/preview`);
    S.previewing = id;
    vp.setGhost(g);
    $('empty').hidden = true;
    const bar = $('ghost-bar');
    bar.hidden = false;
    bar.replaceChildren(h('span', {}, `Preview · ${g.changeset.intent || id}: +${g.added.length} / −${g.removed.length} bodies${(g.sketches ?? []).length ? ` · ${g.sketches.length} sketch outline(s)` : ''}`),
      h('button', { class: 'primary', onclick: () => lifecycle('commit', id) }, 'Commit'),
      h('button', { onclick: () => lifecycle('reject', id) }, 'Reject'),
      h('button', { onclick: clearPreview }, 'Hide'));
    renderChat(S.state);
  } catch (e) { toast(e.message, 'bad'); }
}

function clearPreview() {
  S.previewing = null;
  vp.setGhost(null);
  $('empty').hidden = vp.bodies.size > 0 || (vp.sketches?.children.length ?? 0) > 0;
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
    const lines = [`vs “${r.label_a}”: ${r.features_a} → ${r.features_b} features, ${units.volume(r.volume_a_mm3)} → ${units.volume(r.volume_b_mm3)}`];
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
  if (vision.uploading()) { toast('Wait until the attachments finish uploading.', 'bad'); return; }
  try {
    await withFallbackConsent(() => api('prompt', { text, thinking: $('thinking').checked, images: vision.attachmentIds() }));
    $('prompt').value = ''; autosize();
    vision.clearAttachments();
  } catch (err) { toast(err.message, 'bad'); }
});
try { $('thinking').checked = localStorage.getItem('thinking') !== 'off'; } catch {}
$('thinking').addEventListener('change', () => { try { localStorage.setItem('thinking', $('thinking').checked ? 'on' : 'off'); } catch {} });
$('btn-clear').addEventListener('click', async () => {
  try { await api('chat/clear', {}); chatSeen.clear(); } catch (err) { toast(err.message, 'bad'); }
});
$('prompt').addEventListener('keydown', (e) => {
  if (e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); $('composer').requestSubmit(); }
});
// The prompt grows with its content (52px minimum, 40% of the window at most).
function autosize() {
  const p = $('prompt');
  p.style.height = 'auto';
  p.style.height = Math.min(p.scrollHeight, innerHeight * 0.4) + 'px';
}
$('prompt').addEventListener('input', autosize);

// ---- playback ------------------------------------------------------------------------

async function playDesign() {
  if (S.playing || !S.state?.frames) return;
  S.playing = true;
  clearPreview();
  vp.clear();
  try {
    for (let i = 0; i <= S.state.frames; i++) {
      const f = await api('playback', { index: i, known: vp.known });
      vp.applyDelta(f.scene);
      $('hud').textContent = i === 0 ? 'Playback…' : `${i}/${f.total} · ${f.changeset?.intent ?? ''}`;
      if (i > 0) vp.fitAll();
      await new Promise((r) => setTimeout(r, 375));
    }
  } catch (e) { toast(e.message, 'bad'); }
  S.playing = false;
  vp.clear();
  await refresh();
}

// ---- contextual action bar -------------------------------------------------------------
// A pure function of the selection (see actions.js): only actions valid right now are shown.
// It sits in a fixed strip above the viewport. Actions that do not fit move into "More".

const FILTERS = [['auto', 'Auto'], ['body', 'Body'], ['face', 'Face'], ['edge', 'Edge'], ['vertex', 'Vertex']];
const VIEWS = [['Front', [0, -1, 0]], ['Back', [0, 1, 0]], ['Left', [-1, 0, 0]], ['Right', [1, 0, 0]], ['Top', [0, 0, 1]], ['Bottom', [0, 0, -1]]];

function setFilter(f) { S.filter = f; vp.filter = f; renderCtxBar(); }
function toggleBoxSelect() {
  const on = !vp.boxMode;
  if (on && vp.measure.on) toggleMeasure();
  vp.setBoxMode(on);
  $('hud').textContent = on ? 'Box select: drag a rectangle' : hudStats();
  renderCtxBar();
}
function toggleMeasure() {
  if (!vp.measure.on && vp.boxMode) toggleBoxSelect();
  const on = !vp.measure.on;
  vp.setMeasure(on);
  $('measure-label').hidden = true;
  $('hud').textContent = on ? 'Measure: click two points' : hudStats();
  renderCtxBar();
}
function setSection(axis) { vp.setSection(axis, vp.section?.t ?? 0.5); renderCtxBar(); }
function toggleMesh() { vp.setMeshOnly(!vp.meshOnly); renderCtxBar(); }

let ctxKey = '';
function renderCtxBar() {
  const box = $('ctx-actions');
  const sel = selectionContext(S.selection?.selectors ?? [], groupOf);
  const actions = actionsFor(sel);
  // Modes that are switched on stay reachable whatever is selected.
  const keep = [vp.measure?.on && 'measure', vp.section?.axis && 'section', vp.meshOnly && 'view'].filter(Boolean);
  for (const id of keep) {
    if (actions.some((a) => a.id === id)) continue;
    const at = actions.findIndex((a) => a.group === 'destructive');
    actions.splice(at < 0 ? actions.length : at, 0, ACTIONS.find((a) => a.id === id));
  }
  const key = `${sel.ctx}:${actions.map((a) => a.id).join()}`;
  if (key !== ctxKey) { ctxKey = key; box.classList.remove('swap'); void box.offsetWidth; box.classList.add('swap'); }
  const hidden = new Set();
  const draw = () => {
    const shown = actions.filter((a) => !hidden.has(a.id));
    const more = hidden.size ? ctxMore(actions.filter((a) => hidden.has(a.id)), sel) : null;
    const groups = groupRuns(shown).map((run) => h('div', { class: `cgroup ${run.group}`, role: 'group', 'aria-label': run.group },
      ...run.items.map((a) => ctxButton(a, sel))));
    // "More" sits before the destructive group, which always stays last and set apart.
    if (more) { const i = groups.findIndex((g) => g.classList.contains('destructive')); groups.splice(i < 0 ? groups.length : i, 0, h('div', { class: 'cgroup' }, more)); }
    box.replaceChildren(...groups);
  };
  draw();
  // Overflow: move the least important actions into "More" until the bar fits. Non-primary
  // actions go first (from the end); then primary ones, but never the mode control, Delete or
  // a mode that is switched on.
  const room = () => $('ctxbar').clientWidth - 24;          // padding; the readout shrinks first
  while (box.scrollWidth > room() && hidden.size < actions.length) {
    const open = actions.filter((a) => !hidden.has(a.id) && a.group !== 'destructive' && a.group !== 'mode' && !keep.includes(a.id));
    const cand = [...open].reverse().find((a) => !a.primary) ?? open[open.length - 1];
    if (!cand) break;
    hidden.add(cand.id);
    draw();
  }
}
new ResizeObserver(() => { cancelAnimationFrame(S.ctxRaf); S.ctxRaf = requestAnimationFrame(renderCtxBar); }).observe($('ctxbar'));

function ctxMore(list, sel) {
  return h('button', { type: 'button', class: 'abtn', 'aria-haspopup': 'menu', title: 'More actions', onclick: (e) => {
    showMenu({ el: e.currentTarget }, list.map((a) => ({ label: a.labelFor?.(sel) ?? a.label, icon: a.icon, danger: a.destructive,
      run: () => triggerAction(a, sel, e.currentTarget) })));
  } }, icon('more'), h('span', { class: 'lbl' }, 'More'));
}

function ctxButton(a, sel) {
  const label = a.labelFor?.(sel) ?? a.label;
  const popup = ['pick-mode', 'menu', 'section', 'view'].includes(a.kind);
  let on = a.id === 'measure' ? vp.measure?.on : a.id === 'section' ? !!vp.section?.axis : a.id === 'view' ? !!vp.meshOnly : false;
  const lbl = a.kind === 'pick-mode' ? [h('span', { class: 'k' }, 'Select: '), h('b', {}, FILTERS.find(([k]) => k === S.filter)?.[1] ?? 'Auto')]
    : a.id === 'section' && vp.section?.axis ? [`Section · ${vp.section.axis}`] : [label];
  const btn = h('button', { type: 'button', id: a.id === 'measure' ? 'btn-measure' : `act-${a.id}`, class: `abtn${a.kind === 'pick-mode' ? ' mode' : ''}${a.destructive ? ' destructive' : ''}${on ? ' on' : ''}`,
    title: a.title ?? label, 'aria-haspopup': popup ? 'menu' : null, 'aria-pressed': a.kind === 'toggle' ? String(!!on) : null,
    onclick: (e) => triggerAction(a, sel, e.currentTarget) },
  icon(a.icon), h('span', { class: 'lbl' }, ...lbl), popup ? icon('down') : null);
  if (popup) btn.lastChild.classList.add('caret');
  if (a.id !== 'section' || !vp.section?.axis) return btn;
  // The section offset is part of the bar, next to its control (never floating over the model).
  const slider = h('input', { id: 'sec-off', type: 'range', min: '0', max: '1', step: '0.001', value: String(vp.section.t ?? 0.5), 'aria-label': 'Section offset',
    oninput: (e) => vp.setSection(vp.section.axis, Number(e.target.value)) });
  return h('span', { class: 'section-ctl' }, btn, slider);
}

function triggerAction(a, sel, anchor) {
  const at = { el: anchor };
  switch (a.kind) {
    case 'pick-mode':
      return showMenu(at, FILTERS.map(([k, label]) => ({ label, checked: S.filter === k, data: { filter: k }, run: () => setFilter(k) })));
    case 'menu':
      return showMenu(at, a.items.map(([tool, label, ic]) => ({ label, icon: ic, run: () => (tool.startsWith('@') ? actionEnv[tool.slice(1)]?.() : palette.openTool(tool)) })));
    case 'section':
      return showMenu(at, [{ label: 'Off', checked: !vp.section?.axis, data: { axis: '' }, run: () => setSection('') },
        ...['X', 'Y', 'Z'].map((ax) => ({ label: `${ax} plane`, checked: vp.section?.axis === ax, data: { axis: ax }, run: () => setSection(ax) }))]);
    case 'view':
      return showMenu(at, [
        { label: 'Mesh view', icon: 'mesh', checked: !!vp.meshOnly, data: { view: 'mesh' }, run: toggleMesh },
        { sep: true }, { head: 'Standard views' },
        ...VIEWS.map(([label, dir]) => ({ label, icon: 'view', run: () => vp.viewFrom(dir) })),
        { sep: true },
        ...layoutMenuItems(),
        { sep: true },
        { label: 'Play design', icon: 'play', data: { view: 'play' }, disabled: !S.state?.frames || S.playing, run: playDesign }]);
    default:
      return a.run?.(actionEnv, a, { currentTarget: anchor });
  }
}

// What the actions can do. Everything that changes the model ends in a tool call.
// The component library panel. It only browses and does library bookkeeping; everything that
// changes the model goes through callTool like every other mutation.
const components = initComponents({ callTool, toast, formDialog, confirm: (title, body) => confirmBox({ title, body, ok: 'Delete' }), refresh: scheduleRefresh });
$('comp-sync')?.addEventListener('click', () => callTool('sync_component', {}, 'Update components'));
$('comp-publish')?.addEventListener('click', (e) => publishProject(e.currentTarget.dataset.reusable !== 'true'));

const selIds = () => selectionContext(S.selection?.selectors ?? []).features;
const groupOf = (id) => S.state?.tree?.find((t) => t.id === id)?.group ?? '';
const nameOf = (ids) => (ids.length > 1 ? `${ids.length} objects` : ids[0]);

async function callTool(name, args, label = name) {
  try {
    const o = await api('tool', { name, args });
    toast(o.ok ? (o.committed ? `${label} applied` : o.changeset ? `${label} proposed: review it in the conversation` : `${label} ok`) : o.error, o.ok ? 'ok' : 'bad');
    scheduleRefresh();
    return o;
  } catch (err) { toast(err.message, 'bad'); scheduleRefresh(); return null; }
}

// Edits to several independent objects are one change: one undo step, applied completely or not at all
// (the batch tool). A single call goes through as itself. Do not use it for calls that depend on each other
// (e.g. two extrusions of faces of the same body): each is planned against the model as it is now.
function callTools(calls, label) {
  if (calls.length === 1) return callTool(calls[0].tool, calls[0].args, label);
  return callTool('batch', { calls, intent: `${label} ${calls.length} objects` }, label);
}

const actionEnv = {
  fit: () => vp.fit(),
  draw_path: () => startPathDraw('linear'),
  draw_curve: () => startPathDraw('catmull_rom'),
  trace_image: () => tracePanel.open(),
  createComponent: () => { const ids = selIds(); if (ids.length) createComponent(ids); },
  toggleMeasure,
  async transform(kind) {
    const ids = selIds();
    if (!ids.length) return;
    xform.begin(kind === 'move' ? 'translate' : 'rotate');
  },
  async boolean(op) {
    const ids = selIds();
    if (ids.length !== 2) return;
    let target = ids[0];
    if (op === 'subtract') {
      const v = await formDialog({ title: 'Subtract: which body keeps its volume?', ok: 'Subtract',
        fields: [{ key: 'target', label: 'Keep', options: ids, value: ids[0] }] });
      if (!v) return;
      target = v.target;
    }
    await callTool('boolean', { op, target, tool: ids.find((i) => i !== target) }, op[0].toUpperCase() + op.slice(1));
  },
  async edgeOp(tool) {
    const sels = S.selection?.selectors ?? [];
    const edges = sels.filter((x) => x.startsWith('edge('));
    const ids = edges.length ? [...new Set(edges.map(featureOf))] : selIds();
    if (!ids.length) return;
    const param = tool === 'fillet' ? 'radius' : 'distance';
    const v = await formDialog({ title: `${tool === 'fillet' ? 'Fillet' : 'Chamfer'} ${edges.length ? `${edges.length} edge${edges.length > 1 ? 's' : ''}` : 'all edges'} (${units.symbol})`,
      ok: tool === 'fillet' ? 'Fillet' : 'Chamfer', fields: [{ key: 'v', label: tool === 'fillet' ? 'Radius' : 'Distance', value: 1, min: 0.001, length: true }] });
    if (!v || !(v.v > 0)) return;
    for (const id of ids) {
      const args = { target: id, [param]: v.v };
      const mine = edges.filter((x) => featureOf(x) === id);
      if (mine.length) args.edges = mine;
      await callTool(tool, args, tool === 'fillet' ? 'Fillet' : 'Chamfer');
    }
  },
  async scale() {
    const ids = selIds();
    if (!ids.length) return;
    const v = await formDialog({ title: `Scale ${nameOf(ids)}`, ok: 'Scale', fields: [
      { key: 'f', label: 'Uniform factor', value: 2, min: 0.001 },
      { key: 'x', label: '× along X', value: 1, min: 0.001 }, { key: 'y', label: '× along Y', value: 1, min: 0.001 }, { key: 'z', label: '× along Z', value: 1, min: 0.001 },
      { key: 'about', label: 'Fixed point', value: 'center', options: [{ value: 'center', label: 'Centre' }, { value: 'base', label: 'Base (stays on the bed)' }, { value: 'origin', label: 'World origin' }] }] });
    if (!v) return;
    const [sx, sy, sz] = [v.f * v.x, v.f * v.y, v.f * v.z];
    if (![sx, sy, sz].every((s) => s > 0)) { toast('Scale factors must be greater than 0', 'bad'); return; }
    const factors = sx === sy && sy === sz ? { factor: sx } : { sx, sy, sz };
    await callTools(ids.map((id) => ({ tool: 'scale', args: { target: id, ...factors, about: v.about, intent: `Scale ${id}` } })), 'Scale');
  },
  async duplicate() {
    const ids = selIds();
    if (!ids.length) return;
    // Default: next to the selection along X, with a 5 mm gap.
    const w = Math.max(0, ...ids.map((id) => vp.bodySize(id)?.[0] ?? 0));
    const v = await formDialog({ title: `Duplicate ${nameOf(ids)}: offset (${units.symbol})`, ok: 'Duplicate',
      fields: [{ key: 'dx', label: 'X', value: Math.round((w + 5) * 100) / 100, length: true }, { key: 'dy', label: 'Y', value: 0, length: true }, { key: 'dz', label: 'Z', value: 0, length: true }] });
    if (!v) return;
    await callTool('duplicate', { features: ids, dx: v.dx, dy: v.dy, dz: v.dz, intent: `Duplicate ${ids.join(', ')}` }, 'Duplicate');
  },
  async align() {
    const ids = selIds();
    if (!ids.length) return;
    const multi = ids.length > 1;
    const v = await formDialog({ title: `Align ${nameOf(ids)}`, ok: 'Align', fields: [
      { key: 'axes', label: 'Along', value: multi ? 'x' : 'z', options: [{ value: 'x', label: 'X' }, { value: 'y', label: 'Y' }, { value: 'z', label: 'Z' }, { value: 'x,y', label: 'X and Y' }, { value: 'x,y,z', label: 'X, Y and Z' }] },
      { key: 'mode', label: 'Match', value: multi ? 'center' : 'min', options: [{ value: 'min', label: 'Minimum side' }, { value: 'center', label: 'Centre' }, { value: 'max', label: 'Maximum side' }] },
      { key: 'to', label: 'To', value: multi ? 'first' : 'origin', options: [
        ...(multi ? [{ value: 'first', label: `First selected (${ids[0]})` }, { value: 'selection', label: 'Bounds of the selection' }] : []),
        { value: 'origin', label: 'World origin (0)' }] }] });
    if (!v) return;
    await callTool('align', { features: ids, axes: v.axes.split(','), mode: v.mode, to: v.to }, 'Align');
  },
  async group() {
    const ids = selIds();
    if (!ids.length) return;
    const taken = new Set((S.state?.tree ?? []).map((t) => t.group).filter(Boolean));
    let n = taken.size + 1;
    while (taken.has(`Group ${n}`)) n++;
    const v = await formDialog({ title: `Group ${nameOf(ids)}`, ok: 'Group', fields: [{ key: 'name', label: 'Name', text: true, value: `Group ${n}` }] });
    if (!v?.name?.trim()) return;
    await callTool('group', { features: ids, name: v.name.trim() }, 'Group');
  },
  async ungroup() {
    const ids = selIds().filter((id) => groupOf(id));
    if (ids.length) await callTool('ungroup', { features: ids }, 'Ungroup');
  },
  async faceOp(tool) {
    // One face per body: each edit becomes a new feature on that body, so a second face of the
    // same body is picked again on the result.
    const all = (S.selection?.selectors ?? []).filter((x) => x.startsWith('face('));
    const faces = all.filter((x, i) => all.findIndex((y) => featureOf(y) === featureOf(x)) === i);
    if (!faces.length) return;
    if (faces.length < all.length) toast('Several faces of one body: only the first is used. Select the others again afterwards.', '', 6000);
    const what = `${faces.length > 1 ? faces.length + ' faces' : 'face'}`;
    const spec = {
      face_extrude: { title: `Extrude ${what} (${units.symbol})`, ok: 'Extrude', fields: [{ key: 'distance', label: 'Distance (+ out / − in)', value: 5, length: true }] },
      face_inset: { title: `Inset ${what} (${units.symbol})`, ok: 'Inset', fields: [{ key: 'inset', label: 'Inset from edges', value: 2, min: 0.001, length: true },
        { key: 'depth', label: 'Depth (0 = split only, + pad, − pocket)', value: 0, length: true }] },
      face_offset: { title: `Offset ${what} (${units.symbol})`, ok: 'Offset', fields: [{ key: 'distance', label: 'Distance (+ out of the material)', value: 1, length: true }] },
    }[tool];
    const v = await formDialog(spec);
    if (!v) return;
    if ('distance' in v && !v.distance) { toast('Distance must be non-zero', 'bad'); return; }
    const args = { ...v };
    if (tool === 'face_inset' && !args.depth) delete args.depth;
    for (const face of faces) await callTool(tool, { face, ...args }, spec.ok);
  },
  toolDialog: (name) => palette.openTool(name),
  materialMenu(ev) {
    const ids = selIds();
    const hasMaterial = (S.state?.tree ?? []).some((t) => ids.includes(t.id) && t.appearance?.material_id);
    showMenu({ el: ev.currentTarget }, materialMenu(materialCtx, ids, { hasMaterial, openPanel: openMaterialPanel }));
  },
  pickColor() {
    const ids = selIds();
    if (!ids.length) return;
    let i = $('color-pop');
    if (!i) { i = h('input', { id: 'color-pop', type: 'color', 'aria-label': 'Color', style: 'position:fixed;left:50%;top:40%;opacity:0;pointer-events:none;width:1px;height:1px' }); document.body.append(i); }
    i.value = '#9fb2c8';
    i.onchange = () => applyAppearance('set_color', ids, { color: i.value.toUpperCase() });
    i.click();
  },
  hide: () => applyAppearance('set_visibility', selIds(), { visible: false }),
  remove: () => deleteObjects(),
};

// The material picker lives in the Selection panel: make sure it is on screen.
function openMaterialPanel() { panels.show('left'); materialCtx.rerender(); }

// Tools without a dedicated form reuse the command palette's argument form, prefilled from
// the selection.
// ---- project dialogs ---------------------------------------------------------------------

async function newProject() {
  const r = await ask({ title: 'New project name', value: 'untitled', ok: 'Create' });
  if (!r) return;
  try { await api('project/new', { name: r.value }); clearPreview(); vp.clear(); } catch (e) { toast(e.message, 'bad'); }
  scheduleRefresh();
}
async function openProject() {
  let projects = [];
  try { projects = await api('project/list'); } catch { /* fall back to typing a path */ }
  const choices = projects.map((p) => ({ label: p.name, value: p.path, detail: new Date(p.modified).toLocaleString() }));
  const r = await ask({ title: 'Open a project (click one, or type a path)', ok: 'Open', choices });
  if (!r?.value) return;
  try { await api('project/open', { path: r.value }); clearPreview(); vp.clear(); } catch (e) { toast(e.message, 'bad'); }
  scheduleRefresh();
}
// File actions live in the project-name menu; the rest of the bar is document-level.
$('project').addEventListener('click', (e) => showMenu({ el: e.currentTarget }, [
  { label: 'New project…', icon: 'file-plus', run: newProject },
  { label: 'Open project…', icon: 'folder', run: openProject }]));
$('btn-settings').addEventListener('click', () => openSettings({ toast, onTheme: (t) => vp.setTheme(t), onViewport: (v) => vp.setViewPrefs(v), onRender: (r) => vp.setRenderPrefs(r),
  workspace: S.state?.workspace, onWorkspace: (ws) => vp.setWorkspace(ws), onSaved: () => refresh() }));
$('btn-more').addEventListener('click', (e) => showMenu({ el: e.currentTarget }, [
  { label: 'Command palette', icon: 'command', key: 'Ctrl+K', run: () => palette.open() },
  { label: 'Keyboard shortcuts', icon: 'keyboard', key: '?', run: () => shortcuts.open() },
  { sep: true }, { head: 'Agent level' },
  { label: 'Review — preview every change', checked: S.level === 'review', data: { level: 'review' }, run: () => setLevel('review') },
  { label: 'Auto — commit low-risk changes', checked: S.level === 'auto', data: { level: 'auto' }, run: () => setLevel('auto') }]));
async function setLevel(level) {
  try { await api('level', { level }); S.level = level; } catch (e) { toast(e.message, 'bad'); }
  scheduleRefresh();
}
async function save() {
  try {
    let path = S.state?.path;
    if (!path) {
      const r = await ask({ title: 'Save project as (relative names go to projects_output)', value: `${S.state?.name || 'project'}.scadproj`, ok: 'Save' });
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

// What the palette runs for an app command (everything else it does itself).
function paletteCommand(id) {
  switch (id) {
    case 'undo': return doUndo();
    case 'redo': return doRedo();
    case 'save': return save();
    case 'export_step': case 'export_stl': return $('btn-export').click();
    case 'playback': return playDesign();
    case 'print_simulation': return printsim.toggle();
    case 'new': return newProject();
  }
}
const palette = initPalette({ S, api, toast, callTool: (...a) => callTool(...a), autosize: () => autosize(), command: paletteCommand });

const shortcuts = initShortcuts($('shortcuts'));
$('btn-shortcuts').addEventListener('click', () => shortcuts.toggle());

document.addEventListener('keydown', (e) => {
  const mod = e.ctrlKey || e.metaKey;
  const typing = ['INPUT', 'TEXTAREA', 'SELECT'].includes(document.activeElement?.tagName);
  if (shortcuts.isOpen()) { // the panel is modal: only its own keys work while it is open
    if (e.key === 'Escape' || isToggleKey(e, typing)) { e.preventDefault(); shortcuts.close(); }
    return;
  }
  const overlay = !$('palette').hidden || !$('dialog').hidden || !$('confirm').hidden || !$('formdlg').hidden;
  if (isToggleKey(e, typing) && !overlay && !S.playing && $('visual').hidden && $('settings').hidden && $('interp').hidden) { e.preventDefault(); $('ctxmenu').hidden = true; shortcuts.open(); }
  else if ((e.key === 'Delete' || e.key === 'Backspace') && !typing && !mod && !overlay && !S.playing && !S.printsim && S.selection?.selectors?.length) {
    e.preventDefault(); // Backspace must not navigate back
    deleteObjects();
  } else if (mod && e.key.toLowerCase() === 'k') { e.preventDefault(); palette.toggle(); }
  else if (e.key === 'Escape') {
    if (!$('ctxmenu').hidden) $('ctxmenu').hidden = true;
    else if (!$('confirm').hidden) $('confirm-cancel').click();
    else if (!$('formdlg').hidden) $('fd-cancel').click();
    else if (!$('palette').hidden) palette.close();
    else if (!$('dialog').hidden) $('dialog-cancel').click();
    else if (!$('interp').hidden) $('interp').querySelector('[aria-label=Close]')?.click();
    else if (vp.boxMode) toggleBoxSelect();
    else if (vp.measure.on) toggleMeasure();
    else if (S.selection?.selectors?.length) api('select', { selectors: [] }).then(applySelection).catch(() => {});
  } else if (mod && e.key.toLowerCase() === 's') { e.preventDefault(); save(); }
  else if (mod && !typing && !overlay && e.key.toLowerCase() === 'd' && selIds().length) { e.preventDefault(); actionEnv.duplicate(); }
  else if (mod && !typing && !overlay && e.key.toLowerCase() === 'g' && selIds().length) { e.preventDefault(); e.shiftKey ? actionEnv.ungroup() : actionEnv.group(); }
  else if (mod && e.key.toLowerCase() === 'z' && !typing) { e.preventDefault(); e.shiftKey ? doRedo() : doUndo(); }
  else if (e.ctrlKey && e.altKey && !e.shiftKey && (e.code === 'Digit4' || e.code === 'Numpad4') && !typing && !overlay) { e.preventDefault(); vp.layout.toggleMode(); renderCtxBar(); }
  else if (pathEditor.active && !mod) { /* the path editor owns the plain keys */ }
  else if (!typing && !mod && !overlay && !e.altKey && (e.key === 'g' || e.key === 'r') && !S.playing && !S.printsim && viewportFocused()) {
    e.preventDefault();
    xform.begin(e.key === 'g' ? 'translate' : 'rotate');
  } else if (!typing && !mod && !overlay && e.key === 'f') vp.fit();
  else if (!typing && !mod && !overlay && !S.printsim && e.key === 'm') toggleMeasure();
  else if (!typing && !mod && !overlay && !e.altKey && !S.printsim && e.key === 'b') toggleBoxSelect();
});

// ---- toast ---------------------------------------------------------------------------


loadTheme((t) => vp.setTheme(t), (v) => vp.setViewPrefs(v), (r) => vp.setRenderPrefs(r));
refresh();
api('palette').then((p) => { S.paletteAll = p; }).catch(() => {}); // warm the palette so Ctrl+K is instant
