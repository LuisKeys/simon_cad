// The "Trace image" dialog: pick a PNG, JPEG or SVG, tune how it is traced while a live preview is
// drawn over it, give its real size and create sketches / paths from it. All geometry comes from
// the deterministic tools (vectorize_image for the preview, image_to_paths to create); the dialog
// only collects settings. See vectorize-model.js for the rules.
import { api, h, uploadImage, imageUrl } from './api.js';
import { defaults, problems, createCall, traceArgs, summary, warningTexts, debounce, latestOnly, MODES, PLANES, TARGETS, plateView } from './vectorize-model.js';

const ACCEPT = '.png,.jpg,.jpeg,.svg,image/png,image/jpeg,image/svg+xml';

export function initTracePanel({ toast, afterCreate }) {
  let state = defaults();
  let result = null;       // last vectorize_image result
  let error = '';
  let objectUrl = null;
  const latest = latestOnly();

  const root = h('div', { class: 'overlay', id: 'trace', hidden: true });
  document.body.appendChild(root);

  const view = h('div', { class: 'trace-view', 'aria-label': 'Source image and traced preview' });
  const srcImg = h('img', { class: 'trace-src', alt: '', hidden: true });
  const overlay = h('img', { class: 'trace-overlay', alt: 'Traced preview', hidden: true });
  const hint = h('div', { class: 'trace-hint' }, 'Drop a PNG, JPEG or SVG here, or choose a file');
  // the stamp preview: the plate with the design on it, as printed (physical) or as pressed (impression)
  const plate = h('div', { class: 'trace-plate', hidden: true }, h('img', { alt: 'Design on the stamp', class: 'trace-plate-design' }));
  const plateTabs = h('div', { class: 'trace-plate-tabs', role: 'tablist', hidden: true });
  view.append(srcImg, overlay, hint, plate);
  let plateMode = 'physical';

  const fileInput = h('input', { type: 'file', accept: ACCEPT, hidden: true, 'aria-label': 'Image file',
    onchange: (e) => { const f = e.target.files?.[0]; if (f) load(f); e.target.value = ''; } });
  const pickBtn = h('button', { type: 'button', onclick: () => fileInput.click() }, 'Choose file…');
  const sourceName = h('span', { class: 'trace-name' }, 'No file');
  const status = h('div', { class: 'trace-status', role: 'status', 'aria-live': 'polite' });
  const warn = h('ul', { class: 'trace-warn' });
  const errBox = h('div', { class: 'trace-error', role: 'alert', hidden: true });
  const createBtn = h('button', { type: 'submit', class: 'primary' }, 'Create');
  const heightNote = h('span', { class: 'trace-note' });

  const controls = h('div', { class: 'trace-controls' });
  const setStamp = (patch) => { state = { ...state, stamp: { ...state.stamp, ...patch } }; renderControls(); renderPlate(); };
  const preview = debounce(() => runPreview(), 220);

  function set(patch) {
    state = { ...state, ...patch };
    renderControls();
    preview();
  }

  const row = (label, ...kids) => h('label', { class: 'trace-row' }, h('span', null, label), ...kids);
  const num = (key, { step = 'any', min, max } = {}) => h('input', { type: 'number', step, min, max, value: String(state[key] ?? ''),
    onchange: (e) => { const v = parseFloat(e.target.value); set({ [key]: Number.isFinite(v) ? v : null }); },
    onkeydown: (e) => e.stopPropagation() });
  const range = (key, min, max, step, fmt = (v) => v) => {
    const out = h('output', null, String(fmt(state[key])));
    const input = h('input', { type: 'range', min, max, step, value: String(state[key]),
      oninput: (e) => { out.textContent = String(fmt(parseFloat(e.target.value))); },
      onchange: (e) => set({ [key]: parseFloat(e.target.value) }) });
    return h('span', { class: 'trace-range' }, input, out);
  };
  const check = (key, label, disabled = false) => h('label', { class: 'trace-check' },
    h('input', { type: 'checkbox', checked: !!state[key], disabled, onchange: (e) => set({ [key]: e.target.checked }) }), label);

  // Re-rendering replaces the focused input, whose blur fires its change handler, which renders
  // again: guard against the nested call (the browser refuses to remove a node twice).
  let rendering = false, renderAgain = false;
  function renderControls() {
    if (rendering) { renderAgain = true; return; }
    rendering = true;
    try { paintControls(); } finally { rendering = false; }
    if (renderAgain) { renderAgain = false; renderControls(); }
  }

  function paintControls() {
    const modeBtns = MODES.map(([id, label, tip]) => h('label', { class: `trace-mode${state.mode === id ? ' on' : ''}`, title: tip },
      h('input', { type: 'radio', name: 'trace-mode', value: id, checked: state.mode === id, onchange: () => set({ mode: id }) }),
      h('span', { class: 'lbl' }, label), h('span', { class: 'tip' }, tip)));
    controls.replaceChildren(
      h('div', { class: 'trace-src-row' }, pickBtn, sourceName),
      h('div', { class: 'trace-targets', role: 'radiogroup', 'aria-label': 'What to create' },
        ...TARGETS.map(([id, label, tip]) => h('label', { class: `trace-target${state.target === id ? ' on' : ''}`, title: tip },
          h('input', { type: 'radio', name: 'trace-target', value: id, checked: state.target === id, onchange: () => set({ target: id }) }), label))),
      h('fieldset', { class: 'trace-modes' }, h('legend', null, 'What to extract'), ...modeBtns),
      h('fieldset', null, h('legend', null, 'Detection'),
        check('autoThreshold', 'Automatic threshold', state.adaptive),
        state.autoThreshold || state.adaptive ? null : row('Threshold', range('threshold', 1, 255, 1)),
        check('adaptive', 'Adaptive (uneven lighting)'),
        check('invert', 'Light subject on dark background'),
        state.mode === 'centerline' ? check('closeGaps', 'Close one-pixel gaps in strokes') : null,
        row('Ignore specks below (px)', num('minArea', { step: 1, min: 1 })),
        row('Smoothing', range('smooth', 0.2, 1.5, 0.05, (v) => v.toFixed(2))),
        row('Curve tolerance (px)', range('tolerance', 0.2, 3, 0.1, (v) => v.toFixed(1)))),
      state.target === 'stamp' ? stampFieldset() : h('fieldset', null, h('legend', null, 'Real size and placement'),
        row('Real width of the image (mm)', num('widthMm', { step: 'any', min: 0.01 })), heightNote,
        row('Plane', h('select', { onchange: (e) => set({ plane: e.target.value }) }, ...PLANES.map((p) => h('option', { value: p, selected: p === state.plane }, p)))),
        check('center', 'Centre on the origin')));
    const probs = problems(state);
    createBtn.disabled = probs.length > 0 || !result;
    createBtn.title = probs[0] ?? '';
    renderStatus();
    renderPlate();
  }

  const snum = (key, { step = 'any', min, placeholder } = {}) => h('input', { type: 'number', step, min, placeholder, value: state.stamp[key] === null || state.stamp[key] === undefined ? '' : String(state.stamp[key]),
    onchange: (e) => { const v = parseFloat(e.target.value); setStamp({ [key]: Number.isFinite(v) ? v : null }); preview(); },
    onkeydown: (e) => e.stopPropagation() });
  const scheck = (key, label) => h('label', { class: 'trace-check' },
    h('input', { type: 'checkbox', checked: !!state.stamp[key], onchange: (e) => setStamp({ [key]: e.target.checked }) }), label);

  function stampFieldset() {
    const t = state.stamp;
    return h('fieldset', null, h('legend', null, 'Stamp'),
      row('Plate', h('select', { onchange: (e) => setStamp({ shape: e.target.value }) },
        ...[['rectangle', 'Rectangle'], ['circle', 'Circle']].map(([v, l]) => h('option', { value: v, selected: v === t.shape }, l)))),
      ...(t.shape === 'circle' ? [row('Diameter (mm)', snum('diameter', { min: 1 }))]
        : [row('Width (mm)', snum('width', { min: 1 })), row('Height (mm)', snum('height', { min: 1 }))]),
      row('Plate thickness (mm)', snum('thickness', { min: 0.1 })),
      row('Relief height (mm)', snum('relief', { min: 0.1 })),
      state.mode === 'centerline' ? row('Ridge width (mm)', snum('strokeWidth', { min: 0.1 })) : null,
      row('Free margin (mm)', snum('margin', { min: 0 })),
      scheck('mirror', 'Mirror the relief (the impression reads correctly)'),
      scheck('rounded', 'Rounded edges'),
      scheck('handle', 'Handle on the back'),
      t.handle ? row('Handle height (mm)', snum('handleHeight', { min: 1 })) : null,
      row('Material shrinkage (%)', snum('shrinkage', { min: 0, placeholder: 'none' })),
      h('div', { class: 'trace-note' }, 'The design is scaled to fit the plate. Shrinkage is never assumed: leave it blank unless you know your clay.'));
  }

  function renderPlate() {
    const on = state.target === 'stamp' && !!state.source;
    plateTabs.hidden = !on;
    plate.hidden = true;
    srcImg.hidden = !state.source || on && !!result;
    overlay.hidden = on || !result;
    if (!on) return;
    plateTabs.replaceChildren(...[['physical', 'Physical stamp'], ['impression', 'Impression']].map(([id, label]) =>
      h('button', { type: 'button', role: 'tab', 'aria-selected': String(plateMode === id), class: plateMode === id ? 'on' : '', onclick: () => { plateMode = id; renderPlate(); } }, label)));
    const aspect = result ? result.image_px[1] / result.image_px[0] : 1;
    const v = plateView(state.stamp, plateMode, aspect);
    if (!v || !result) return;
    plate.hidden = false;
    plate.style.aspectRatio = String(v.aspect);
    plate.classList.toggle('round', v.round);
    const img = plate.firstChild;
    img.src = `data:image/svg+xml;charset=utf-8,${encodeURIComponent(result.preview_svg)}`;
    img.style.width = `${v.designWidthPct}%`;
    img.style.height = `${v.designHeightPct}%`;
    img.style.transform = v.flip ? 'scaleX(-1)' : 'none';
  }

  function renderStatus() {
    status.replaceChildren(...summary(result).map((l) => h('span', null, l)));
    warn.replaceChildren(...warningTexts(result).map((w) => h('li', null, w)));
    errBox.hidden = !error;
    errBox.textContent = error;
    const probs = problems(state);
    const aspect = result ? result.image_px[1] / result.image_px[0] : null;
    heightNote.textContent = state.widthMm > 0 && aspect ? `→ height ${+(state.widthMm * aspect).toFixed(2)} mm` : (probs.find((p) => /width/.test(p)) ?? '');
  }

  async function runPreview() {
    if (!state.source) return;
    status.classList.add('busy');
    const r = await latest(() => api('query', { name: 'vectorize_image', args: traceArgs(state, { preview: true }) }).catch((e) => ({ ok: false, error: e.message })));
    if (!r.current) return;
    status.classList.remove('busy');
    const out = r.value;
    if (!out.ok) {
      result = null; error = out.error ?? 'Could not trace this image'; overlay.hidden = true;
    } else {
      result = out.data; error = '';
      overlay.src = `data:image/svg+xml;charset=utf-8,${encodeURIComponent(result.preview_svg)}`;
      overlay.hidden = false;
    }
    renderControls();
  }

  async function load(file) {
    error = '';
    try {
      const isSvg = /\.svg$/i.test(file.name) || file.type === 'image/svg+xml';
      let source;
      if (isSvg) {
        const text = await file.text();
        source = { kind: 'svg', svg: text, name: file.name };
        setPreviewImage(URL.createObjectURL(new Blob([text], { type: 'image/svg+xml' })));
      } else {
        const info = await uploadImage(file);
        source = { kind: 'image', imageId: info.id, name: file.name, width: info.width, height: info.height };
        setPreviewImage(imageUrl(info.id));
      }
      sourceName.textContent = `${file.name}`;
      hint.hidden = true;
      result = null;
      state = { ...state, source };
      renderControls();
      preview();
    } catch (e) {
      error = e.message;
      renderStatus();
    }
  }

  function setPreviewImage(url) {
    if (objectUrl) URL.revokeObjectURL(objectUrl);
    objectUrl = url.startsWith('blob:') ? url : null;
    srcImg.src = url;
    srcImg.hidden = false;
  }

  const form = h('form', { class: 'dialog trace-dialog', 'aria-labelledby': 'trace-title', onsubmit: async (e) => {
    e.preventDefault();
    const probs = problems(state);
    if (probs.length) { error = probs[0]; renderStatus(); return; }
    createBtn.disabled = true;
    try {
      const call = createCall(state);
      const o = await api('tool', call);
      if (!o.ok) { error = o.error; renderControls(); return; }
      close();
      toast(o.committed ? (state.target === 'stamp' ? `Stamp created from ${state.source.name}` : `Traced ${state.source.name}`) : 'Proposed: review it in the conversation', 'ok');
      await afterCreate?.();
    } catch (err) { error = err.message; renderControls(); }
  } },
  h('div', { class: 'trace-head' }, h('div', { id: 'trace-title' }, 'Trace image'),
    h('button', { type: 'button', class: 'trace-x', 'aria-label': 'Close', onclick: () => close() }, '×')),
  h('div', { class: 'trace-body' }, h('div', { class: 'trace-left' }, plateTabs, view), controls),
  status, warn, errBox,
  h('div', { class: 'row' }, createBtn, h('button', { type: 'button', onclick: () => close() }, 'Cancel')),
  fileInput);
  root.appendChild(form);

  // drag and drop onto the dialog
  for (const t of [view, form]) {
    t.addEventListener('dragover', (e) => { e.preventDefault(); view.classList.add('drop'); });
    t.addEventListener('dragleave', () => view.classList.remove('drop'));
    t.addEventListener('drop', (e) => {
      e.preventDefault(); view.classList.remove('drop');
      const f = e.dataTransfer?.files?.[0];
      if (f) load(f);
    });
  }
  const onKey = (e) => { if (e.key === 'Escape') { e.preventDefault(); e.stopPropagation(); close(); } };

  function open(file = null) {
    state = { ...defaults(), widthMm: state.widthMm };
    result = null; error = ''; overlay.hidden = true; srcImg.hidden = true; hint.hidden = false; sourceName.textContent = 'No file';
    root.hidden = false;
    window.addEventListener('keydown', onKey, true);
    renderControls();
    if (file) load(file); else pickBtn.focus();
  }

  function close() {
    preview.cancel();
    root.hidden = true;
    window.removeEventListener('keydown', onKey, true);
    if (objectUrl) { URL.revokeObjectURL(objectUrl); objectUrl = null; }
  }

  return { open, close, get isOpen() { return !root.hidden; } };
}
