// The Print Simulation workspace: the printer scene in the main viewport (printsim.js), a Print tab in
// the right panel (slicer, profile, results, view options) and the playback bar under the viewport.
// Slicing runs on the backend; progress arrives as "printsim" notices and the finished toolpath is
// fetched as one binary buffer. Nothing here changes the CAD model.
import { h } from './api.js';
import { icon } from './icons.js';
import { PrintSim, PALETTES } from './printsim.js';
import { SPEEDS, KINDS, KIND_LABEL, formatDuration, formatClock, formatFilament, isStale } from './printsim-model.js';
import { getPref, setPref } from './panels.js';

const BUSY = new Set(['queued', 'preparing', 'slicing', 'parsing']);
const LAYER_HEIGHTS = [0.08, 0.1, 0.12, 0.15, 0.2, 0.24, 0.28, 0.32];
const SLIDER_STEPS = 10000;

const SECTIONS = [
  { title: 'Process', fields: [
    { key: 'layer_height', label: 'Layer height', type: 'select', unit: 'mm' },
    { key: 'walls', label: 'Walls', type: 'number', min: 1, max: 8, step: 1 },
    { key: 'infill_pct', label: 'Infill', type: 'range', min: 0, max: 100, step: 5, unit: '%' },
    { key: 'infill_pattern', label: 'Infill pattern', type: 'select', options: ['rectilinear', 'grid', 'gyroid', 'honeycomb'] },
    { key: 'top_bottom_layers', label: 'Top / bottom layers', type: 'number', min: 0, max: 10, step: 1 },
    { key: 'supports', label: 'Supports', type: 'check' },
    { key: 'adhesion', label: 'Plate adhesion', type: 'select', options: ['none', 'skirt', 'brim'] },
  ] },
  { title: 'Material and speed', fields: [
    { key: 'print_temp', label: 'Nozzle temp', type: 'number', min: 150, max: 450, step: 5, unit: '°C' },
    { key: 'bed_temp', label: 'Bed temp', type: 'number', min: 0, max: 150, step: 5, unit: '°C' },
    { key: 'print_speed', label: 'Print speed', type: 'number', min: 5, max: 500, step: 5, unit: 'mm/s' },
    { key: 'travel_speed', label: 'Travel speed', type: 'number', min: 10, max: 1000, step: 10, unit: 'mm/s' },
  ] },
  { title: 'Machine', collapsed: true, fields: [
    { key: 'nozzle_diameter', label: 'Nozzle', type: 'select', options: [0.2, 0.25, 0.4, 0.6, 0.8], unit: 'mm' },
    { key: 'filament_diameter', label: 'Filament', type: 'select', options: [1.75, 2.85], unit: 'mm' },
    { key: 'build_x', label: 'Build X', type: 'number', min: 10, max: 2000, step: 10, unit: 'mm' },
    { key: 'build_y', label: 'Build Y', type: 'number', min: 10, max: 2000, step: 10, unit: 'mm' },
    { key: 'build_z', label: 'Build Z', type: 'number', min: 10, max: 2000, step: 10, unit: 'mm' },
  ] },
];

const GROUP_LABEL = { travel: 'Travel moves', perimeters: 'Walls', infill: 'Infill and skins', support: 'Supports', skirt: 'Skirt / brim', other: 'Other' };

const hex = (n) => '#' + n.toString(16).padStart(6, '0');

export function initPrintSim({ vp, api, apiBytes, toast, $, S, clearSelection, onChange }) {
  const sim = new PrintSim(vp);
  const st = {
    active: false, info: null, defaults: null, profile: null, provider: getPref('printsim.provider', 'auto'),
    status: null, summary: null, dirty: false, stale: false, dragging: false, tab: 'print', session: 0, entering: false, latestJob: null,
    view: { ...getPref('printsim.view', {}) },
  };
  const els = {};

  // ---- profile ----------------------------------------------------------------------------------------

  const saveProfile = () => {
    const diff = {};
    for (const [k, v] of Object.entries(st.profile)) if (k !== 'label' && v !== st.defaults[k]) diff[k] = v;
    setPref('printsim.profile', diff);
  };

  // ---- building the DOM (once) -------------------------------------------------------------------------

  function field(f) {
    const get = () => st.profile[f.key];
    const set = (v) => { st.profile[f.key] = v; st.dirty = true; saveProfile(); renderStatus(); if (f.key.startsWith('build_')) bedChanged(); refreshFields(); };
    let input;
    if (f.type === 'check') {
      input = h('input', { type: 'checkbox', id: `ps-${f.key}`, onchange: (e) => set(e.target.checked) });
    } else if (f.type === 'select') {
      input = h('select', { id: `ps-${f.key}`, onchange: (e) => set(typeof f.options?.[0] === 'number' || f.key === 'layer_height' ? Number(e.target.value) : e.target.value) });
    } else {
      input = h('input', { type: f.type, id: `ps-${f.key}`, min: f.min, max: f.max, step: f.step,
        oninput: f.type === 'range' ? (e) => { st.profile[f.key] = Number(e.target.value); els[`out-${f.key}`].textContent = `${e.target.value}${f.unit ?? ''}`; } : undefined,
        onchange: (e) => { const v = Number(e.target.value); if (Number.isFinite(v)) set(v); } });
    }
    const out = f.type === 'range' ? h('span', { class: 'ps-out', id: `ps-out-${f.key}` }) : null;
    if (out) els[`out-${f.key}`] = out;
    els[f.key] = { input, f, get };
    return h('label', { class: 'ps-field', for: `ps-${f.key}` }, h('span', { class: 'ps-label' }, f.label), h('span', { class: 'ps-ctl' }, input, out, f.unit && f.type !== 'range' ? h('span', { class: 'muted ps-unit' }, f.unit) : null));
  }

  /** Writes the profile into the controls (and keeps the layer-height choices valid for the nozzle). */
  function refreshFields() {
    const maxLayer = st.profile.nozzle_diameter * 0.8 + 1e-9;
    for (const sec of SECTIONS) for (const f of sec.fields) {
      const { input } = els[f.key];
      if (f.key === 'layer_height') {
        const opts = LAYER_HEIGHTS.filter((v) => v <= maxLayer);
        if (!opts.includes(st.profile.layer_height) && opts.length) st.profile.layer_height = opts.filter((v) => v <= st.profile.layer_height).pop() ?? opts[0];
        input.replaceChildren(...opts.map((v) => h('option', { value: v }, String(v))));
      } else if (f.type === 'select' && !input.options.length) {
        input.replaceChildren(...f.options.map((v) => h('option', { value: v }, String(v))));
      }
      if (f.type === 'check') input.checked = !!st.profile[f.key];
      else input.value = st.profile[f.key];
      if (f.type === 'range') els[`out-${f.key}`].textContent = `${st.profile[f.key]}${f.unit ?? ''}`;
    }
    // the plate adhesion and supports only matter to the real slicer; the preview ignores supports
    els.providerNote.textContent = providerNote();
  }

  function providerNote() {
    const p = st.info?.providers ?? [];
    const prusa = p.find((x) => x.name === 'prusaslicer');
    if (st.provider === 'builtin') return 'Preview slicer: perimeters and simple infill only, no supports.';
    if (st.provider === 'prusaslicer' && prusa && !prusa.available) return prusa.problem;
    if (st.provider === 'auto' && prusa && !prusa.available) return 'PrusaSlicer was not found, so the built-in preview slicer will be used. ' + prusa.problem;
    return '';
  }

  function segmented(items, current, onPick) {
    const box = h('div', { class: 'ps-seg', role: 'group' });
    const draw = () => box.replaceChildren(...items.map(([val, label, title]) => h('button', {
      type: 'button', class: val === current() ? 'on' : '', title, 'aria-pressed': String(val === current()), onclick: () => { onPick(val); draw(); } }, label)));
    draw();
    box.redraw = draw;
    return box;
  }

  function buildPanel() {
    const root = $('print-panel');
    root.replaceChildren();
    els.status = h('div', { class: 'ps-status', 'aria-live': 'polite' });
    els.stale = h('div', { class: 'ps-note warn', hidden: true }, 'The model changed since this slice. Slice again to update the simulation.');
    els.sliceBtn = h('button', { type: 'button', class: 'primary wide', onclick: () => slice() });
    els.cancelBtn = h('button', { type: 'button', class: 'wide', hidden: true, onclick: () => api('printsim/cancel', {}).catch(() => {}) }, 'Cancel');
    els.providerNote = h('div', { class: 'muted small ps-hint' });
    els.provider = h('select', { id: 'ps-provider', onchange: (e) => { st.provider = e.target.value; setPref('printsim.provider', st.provider); st.dirty = true; refreshFields(); renderStatus(); } },
      h('option', { value: 'auto' }, 'Automatic (best available)'),
      h('option', { value: 'prusaslicer' }, 'PrusaSlicer'),
      h('option', { value: 'builtin' }, 'Preview slicer (built in)'));
    els.provider.value = st.provider;
    els.summary = h('div', { class: 'ps-summary' });
    els.diags = h('div', { class: 'ps-diags' });

    const settings = SECTIONS.map((sec) => {
      const body = h('div', { class: 'ps-fields' }, sec.fields.map(field));
      const d = h('details', { class: 'ps-sec', open: sec.collapsed ? undefined : true }, h('summary', {}, sec.title), body);
      return d;
    });
    els.reset = h('button', { type: 'button', class: 'ps-link', onclick: () => { st.profile = { ...st.defaults }; setPref('printsim.profile', {}); st.dirty = true; refreshFields(); bedChanged(); renderStatus(); } }, 'Reset to defaults');

    els.modeSeg = segmented([['realistic', 'Realistic', 'Filament as it would look'], ['technical', 'Technical', 'Colour-coded toolpaths'], ['diagnostics', 'Diagnostics', 'Problems and risk regions']],
      () => st.view.mode ?? 'realistic', (m) => { st.view.mode = m; applyView(); });
    els.legend = h('div', { class: 'ps-legend' });
    els.shows = Object.keys(GROUP_LABEL).map((g) => h('label', { class: 'ps-check' }, h('input', { type: 'checkbox', 'data-show': g, onchange: (e) => { st.view.show = { ...(st.view.show ?? {}), [g]: e.target.checked }; applyView(); } }), GROUP_LABEL[g]));
    els.opts = [
      ['overlay', 'Original model overlay'], ['heat', 'Nozzle heat glow'], ['completedOnly', 'Completed geometry only'],
    ].map(([k, label]) => h('label', { class: 'ps-check' }, h('input', { type: 'checkbox', 'data-opt': k, onchange: (e) => { st.view[k] = e.target.checked; applyView(); } }), label));
    els.frame = segmented([['part', 'Fit part', 'Frame the part'], ['bed', 'Fit plate', 'Frame the whole build plate']], () => st.view.frame ?? 'part', (f) => { st.view.frame = f; applyView(); sim.frameCamera(); });

    root.append(
      h('h2', {}, 'Print simulation'),
      h('div', { class: 'ps-badge', title: 'The default profile is a generic machine used for simulation, not a verified printer profile' }, icon('printer'), h('span', {}, 'Simulation profile — not verified hardware')),
      h('section', { class: 'ps-block' }, h('div', { class: 'ps-field' }, h('label', { class: 'ps-label', for: 'ps-provider' }, 'Slicer'), els.provider), els.providerNote),
      h('section', { class: 'ps-block' }, settings, els.reset),
      h('section', { class: 'ps-block ps-run' }, els.stale, els.status, els.sliceBtn, els.cancelBtn),
      h('section', { class: 'ps-block' }, h('h3', {}, 'Result'), els.summary, els.diags),
      h('section', { class: 'ps-block' }, h('h3', {}, 'View'), els.modeSeg, els.legend, h('div', { class: 'ps-checks' }, els.shows, els.opts), els.frame),
      h('p', { class: 'muted small ps-disclaimer' }, 'This simulates the slicer\'s toolpaths. It does not predict adhesion, warping, thermal behaviour or hardware faults, and it is not a guarantee that the part will print.'));
    refreshFields();
    applyViewControls();
  }

  function buildBar() {
    const bar = $('printbar');
    bar.replaceChildren();
    els.play = h('button', { type: 'button', class: 'ibtn', title: 'Play / pause (Space)', 'aria-label': 'Play', onclick: () => { if (!sim.playback) return; sim.playback.toggle(); sim.refresh(); renderBar(); } });
    els.restart = h('button', { type: 'button', class: 'ibtn', title: 'Restart', 'aria-label': 'Restart', onclick: () => { if (!sim.playback) return; sim.playback.restart(); sim.refresh(); renderBar(); } }, icon('restart'));
    els.speeds = h('div', { class: 'ps-seg ps-speeds', role: 'group', 'aria-label': 'Playback speed' });
    els.time = h('input', { type: 'range', class: 'ps-time', min: 0, max: SLIDER_STEPS, step: 1, value: 0, 'aria-label': 'Time',
      oninput: (e) => { st.dragging = true; sim.playback?.seekFraction(Number(e.target.value) / SLIDER_STEPS); sim.refresh(); renderBar(true); },
      onchange: () => { st.dragging = false; } });
    els.clock = h('span', { class: 'ps-clock' });
    els.layerMode = h('select', { 'aria-label': 'Layer view', title: 'All layers, everything up to a layer, or one layer', onchange: (e) => { { const pb = sim.playback; if (pb) pb.setLayerMode(e.target.value, pb.layerMode === 'all' ? pb.currentLayer() : pb.layer); } sim.refresh(); renderBar(); } },
      h('option', { value: 'all' }, 'All layers'), h('option', { value: 'upto' }, 'Up to layer'), h('option', { value: 'single' }, 'Single layer'));
    els.layerPrev = h('button', { type: 'button', class: 'ibtn', 'aria-label': 'Previous layer', title: 'Previous layer ([)', onclick: () => stepLayer(-1) }, icon('down'));
    els.layerNext = h('button', { type: 'button', class: 'ibtn', 'aria-label': 'Next layer', title: 'Next layer (])', onclick: () => stepLayer(1) }, icon('up'));
    els.layer = h('input', { type: 'range', class: 'ps-layer', min: 1, max: 1, step: 1, value: 1, 'aria-label': 'Layer',
      oninput: (e) => { st.dragging = true; sim.playback?.seekLayer(Number(e.target.value) - 1); sim.refresh(); renderBar(true); },
      onchange: () => { st.dragging = false; } });
    els.layerText = h('span', { class: 'ps-layertext' });
    els.material = h('span', { class: 'ps-material muted' });
    bar.append(
      h('div', { class: 'ps-row' }, els.play, els.restart, els.speeds, els.time, els.clock),
      h('div', { class: 'ps-row' }, els.layerMode, els.layerPrev, els.layer, els.layerNext, els.layerText, h('span', { class: 'spacer' }), els.material));
    renderSpeeds();
  }

  // ---- rendering ----------------------------------------------------------------------------------------

  function renderSpeeds() {
    const cur = sim.playback?.speed ?? sim._speed ?? 10;
    els.speeds.replaceChildren(...SPEEDS.map((s) => h('button', { type: 'button', class: s === cur ? 'on' : '', 'aria-pressed': String(s === cur),
      onclick: () => { sim.setSpeed(s); renderSpeeds(); } }, `${s}x`)));
  }

  function renderStatus() {
    const s = st.status;
    const busy = s && BUSY.has(s.state);
    els.sliceBtn.hidden = busy;
    els.cancelBtn.hidden = !busy;
    els.sliceBtn.textContent = st.summary ? 'Slice again' : 'Slice and simulate';
    els.sliceBtn.classList.toggle('primary', !st.summary || st.dirty || st.stale);
    els.stale.hidden = !st.stale;
    els.status.replaceChildren();
    els.status.className = 'ps-status';
    if (!s) return;
    if (busy) els.status.append(h('span', { class: 'ps-spinner', 'aria-hidden': 'true' }), h('span', {}, s.message || 'Working…'));
    else if (s.state === 'error') { els.status.classList.add('bad'); els.status.append(h('span', {}, s.error)); }
    else if (s.state === 'cancelled') els.status.append(h('span', { class: 'muted' }, 'Cancelled.'));
    else if (s.state === 'done') els.status.append(h('span', { class: 'ok' }, s.cached ? 'Loaded from cache.' : 'Done.'));
  }

  function renderSummary() {
    const m = st.summary;
    els.summary.replaceChildren();
    els.diags.replaceChildren();
    if (!m) { els.summary.append(h('p', { class: 'muted small' }, 'No result yet.')); return; }
    const row = (k, v) => h('div', { class: 'ps-kv' }, h('span', { class: 'muted' }, k), h('span', {}, v));
    els.summary.append(...[
      m.preview ? h('div', { class: 'ps-note warn' }, 'Preview slicer — a quick approximation, not production toolpaths.') : null, // append() would print a literal "null"

      row('Estimated time', formatDuration(m.duration_s) + (m.slicer_estimate_s ? ` (slicer: ${formatDuration(m.slicer_estimate_s)})` : '')),
      row('Filament', formatFilament(m.filament_mm, m.filament_g)),
      row('Layers', `${m.layers} × ${m.layer_height} mm`),
      row('Size', m.size.map((v) => v.toFixed(1)).join(' × ') + ' mm'),
      row('Slicer', [m.provider, m.version].filter(Boolean).join(' · '))].filter(Boolean));
    const ds = m.diagnostics ?? [];
    if (ds.length) {
      const order = { error: 0, warning: 1, info: 2 };
      const list = [...ds].sort((a, b) => order[a.level] - order[b.level]);
      els.diags.append(h('details', { class: 'ps-sec', open: list.some((d) => d.level !== 'info') ? true : undefined },
        h('summary', {}, `Slicer notes (${ds.length})`),
        h('ul', { class: 'ps-difflist' }, list.map((d) => h('li', { class: d.level }, h('b', {}, d.level), ' ', d.message, d.count > 1 ? h('span', { class: 'muted' }, ` ×${d.count}`) : null)))));
    }
  }

  function renderLegend() {
    const mode = st.view.mode ?? 'realistic';
    els.legend.replaceChildren();
    if (mode === 'realistic') return;
    const pal = PALETTES[mode];
    els.legend.append(...KINDS.map((k, i) => h('span', { class: 'ps-sw' }, h('i', { style: `background:${hex(pal[i])}` }), KIND_LABEL[k])));
  }

  function applyViewControls() {
    const v = st.view;
    for (const el of els.shows) { const g = el.querySelector('input').dataset.show; el.querySelector('input').checked = (v.show ?? {})[g] ?? (g !== 'travel'); }
    for (const el of els.opts) { const k = el.querySelector('input').dataset.opt; el.querySelector('input').checked = v[k] ?? (k === 'heat'); }
    els.modeSeg.redraw(); els.frame.redraw();
    renderLegend();
  }

  function applyView() {
    setPref('printsim.view', st.view);
    sim.setOptions({ mode: st.view.mode ?? 'realistic', show: st.view.show ?? {}, overlay: !!st.view.overlay, heat: st.view.heat ?? true,
      completedOnly: !!st.view.completedOnly, frame: st.view.frame ?? 'part' });
    applyViewControls();
    sim.refresh();
  }

  let barQueued = false;
  function renderBar(force = false) {
    if (barQueued && !force) return;
    barQueued = true;
    requestAnimationFrame(() => {
      barQueued = false;
      const pb = sim.playback;
      const has = !!pb;
      for (const e of [els.play, els.restart, els.time, els.layer, els.layerMode, els.layerPrev, els.layerNext]) e.disabled = !has;
      // Redraw the icon only when the state changes: replacing it every frame makes the browser drop a real
      // click (mouse down and up must land on the same element), so Pause would not respond while playing.
      const playing = !!pb?.playing;
      if (els.playState !== playing) {
        els.playState = playing;
        els.play.replaceChildren(icon(playing ? 'pause' : 'play'));
        els.play.setAttribute('aria-label', playing ? 'Pause' : 'Play');
      }
      if (!has) { els.clock.textContent = 'No simulation yet'; els.layerText.textContent = ''; els.material.textContent = ''; return; }
      const [w0, w1] = pb.window;
      if (!st.dragging || force) els.time.value = String(Math.round(((pb.t - w0) / Math.max(w1 - w0, 1e-9)) * SLIDER_STEPS));
      const L = pb.layerMode === 'all' ? pb.currentLayer() : pb.layer;
      els.layer.max = String(pb.tp.layerCount);
      if (!st.dragging || force) els.layer.value = String(L + 1);
      els.layerMode.value = pb.layerMode;
      els.clock.textContent = `${formatClock(pb.t)} / ${formatClock(pb.duration)} · ${Math.round(pb.progress() * 100)}%`;
      els.layerText.textContent = `Layer ${L + 1} / ${pb.tp.layerCount}`;
      els.material.textContent = st.summary ? `${formatFilament(st.summary.filament_mm, st.summary.filament_g)} · est. ${formatDuration(st.summary.duration_s)}` : '';
    });
  }

  function stepLayer(d) {
    const pb = sim.playback;
    if (!pb) return;
    const L = pb.layerMode === 'all' ? pb.currentLayer() : pb.layer;
    pb.seekLayer(L + d);
    sim.refresh(); renderBar(true);
  }

  // ---- slicing --------------------------------------------------------------------------------------------

  async function slice() {
    try {
      st.status = { state: 'queued', message: 'Starting…' };
      st.dirty = false;
      renderStatus();
      await api('printsim/slice', { profile: st.profile, provider: st.provider });
    } catch (e) {
      st.status = { state: 'error', error: e.message };
      renderStatus();
    }
  }

  async function onResult(ev) {
    const session = st.session;
    const current = () => st.active && st.session === session && st.latestJob === ev.job; // not left, not replaced
    try {
      const buf = await apiBytes(`printsim/result/${encodeURIComponent(ev.job)}`);
      if (!current()) return;
      sim.setLayerHeight(ev.summary.layer_height);
      sim.load(buf, ev.summary);
      st.summary = ev.summary;
      st.stale = false;
      sim.playback.play(); // start printing right away: a paused empty plate looks like nothing happened
      sim.refresh();
      renderSummary(); renderStatus(); renderSpeeds(); renderBar(true);
      onModelChanged(); // the model may have changed while the toolpath was in flight
    } catch (e) {
      if (!current()) return;
      st.status = { state: 'error', error: `The result could not be loaded: ${e.message}` };
      renderStatus();
    }
  }

  /** Server notices: slicing progress for the running job. */
  function onNotice(n) {
    if (!st.active || n.kind !== 'printsim' || !n.printsim) return;
    const ev = n.printsim;
    st.latestJob = ev.job;
    st.status = { state: ev.state, message: ev.message, error: ev.error, cached: ev.cached };
    renderStatus();
    if (ev.state === 'done' && ev.summary) onResult(ev);
  }

  /** The CAD model changed while the simulation is open: say so and refresh the overlay. */
  async function onModelChanged() {
    if (!st.active) return;
    sim.rebuildOverlay(); // right away: the CAD geometry it shares may just have been replaced
    try {
      st.info = await api('printsim/info');
      st.stale = isStale(st.summary, st.info.geom_hash);
      sim.rebuildOverlay();
      renderStatus();
    } catch { /* the next change retries */ }
  }

  function bedChanged() {
    // a new build volume needs a new plate; the toolpath is stale anyway
    if (!st.active) return;
    api('printsim/cancel', {}).catch(() => {});
    exitScene(); enterScene();
    st.summary = null; st.status = null;
    renderSummary(); renderStatus(); renderBar(true);
  }

  // ---- entering and leaving --------------------------------------------------------------------------------

  function enterScene() {
    sim.enter({ x: st.profile.build_x, y: st.profile.build_y, z: st.profile.build_z });
    sim.onUpdate = () => renderBar();
    applyView();
  }
  function exitScene() { sim.onUpdate = null; sim.exit(); }

  function setTab(tab) {
    st.tab = tab;
    $('print-panel').hidden = !(st.active && tab === 'print');
    $('right').querySelector('section.grow').hidden = st.active && tab === 'print';
    $('composer').hidden = st.active && tab === 'print';
    $('right-tabs').hidden = !st.active;
    for (const b of $('right-tabs').querySelectorAll('button')) { const on = b.dataset.tab === tab; b.classList.toggle('on', on); b.setAttribute('aria-selected', String(on)); }
  }

  async function enter() {
    if (st.active || st.entering) return;
    st.entering = true;
    try { await enterNow(); } finally { st.entering = false; }
  }

  async function enterNow() {
    if (S.playing) { toast('Wait for the design playback to finish first.', 'bad'); return; }
    let info;
    try { info = await api('printsim/info'); } catch (e) { toast(e.message, 'bad'); return; }
    if (!info.has_model) { toast('There is nothing to print yet. Create a model first.', 'bad'); return; }
    st.info = info;
    st.defaults = info.profile;
    st.profile = { ...info.profile, ...getPref('printsim.profile', {}) };
    st.active = true; S.printsim = true; st.session++; st.latestJob = null;
    document.body.dataset.printsim = 'on';
    $('btn-printsim').classList.add('on'); $('btn-printsim').setAttribute('aria-pressed', 'true');
    $('printbar').hidden = false;
    buildPanel(); buildBar();
    await clearSelection(); // dimension labels and selection highlights belong to the CAD scene
    if (!st.active) return; // left again while entering
    setTab('print');
    renderSummary(); renderStatus(); renderBar(true);
    enterScene();
    vp.resize();
    onChange?.(true);
    slice(); // one click to a running simulation; a cached result comes back instantly
  }

  function exit() {
    if (!st.active) return;
    st.session++;
    api('printsim/cancel', {}).catch(() => {});
    exitScene();
    st.active = false; S.printsim = false; st.summary = null; st.status = null;
    delete document.body.dataset.printsim;
    $('btn-printsim').classList.remove('on'); $('btn-printsim').setAttribute('aria-pressed', 'false');
    $('printbar').hidden = true;
    setTab('chat');
    vp.resize();
    onChange?.(false);
  }

  const toggle = () => (st.active ? exit() : enter());

  // ---- wiring ---------------------------------------------------------------------------------------------

  $('btn-printsim').addEventListener('click', toggle);
  for (const b of $('right-tabs').querySelectorAll('button')) b.addEventListener('click', () => setTab(b.dataset.tab));
  document.addEventListener('keydown', (e) => {
    if (!st.active || e.defaultPrevented || e.ctrlKey || e.metaKey || e.altKey) return;
    if (e.target.closest?.('input[type=text],input[type=number],input[type=search],textarea,select,[contenteditable]')) return;
    if (document.querySelector('.overlay:not([hidden])')) return;
    if (e.key === ' ' && !e.target.closest?.('button')) { e.preventDefault(); sim.playback?.toggle(); sim.refresh(); renderBar(true); }
    else if (e.key === '[') { e.preventDefault(); stepLayer(-1); }
    else if (e.key === ']') { e.preventDefault(); stepLayer(1); }
  });

  return { sim, state: st, enter, exit, toggle, onNotice, onModelChanged, get active() { return st.active; } };
}
