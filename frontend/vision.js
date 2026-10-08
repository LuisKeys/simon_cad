// Image-to-CAD UI: attaching images (file, drag-and-drop, paste), the interpretation panel
// (source image, detected-geometry overlay, confidence, missing/uncertain dimensions,
// depth and placement) and the hand-off to the normal ChangeSet preview/commit flow.
//
// Nothing here changes the model: every action is a tool call through /api/tool
// (confirm_detected_dimension, set_sketch_scale, update_sketch_proposal, create_from_image),
// and the result is an ordinary proposed ChangeSet.
import { api, h, svg, uploadImage, uploadSpec, imageUrl } from './api.js';
import { confidenceClass, pct, entityShapes, entityRoles, drawingWidthPx, uncertain, ambiguityEdits, CHOICES } from './vision-geom.js';

const OK_TYPES = ['image/png', 'image/jpeg', 'image/webp'];
const MAX_SIDE = 2048;          // longest side sent to the model
const MAX_PLAIN_BYTES = 4.5e6;  // below this and ≤ MAX_SIDE the original file is sent as is
const MAX_ATTACH = 10;         // images per request (the server enforces the same bound)

/** Downscales large images (and re-encodes as JPEG) so uploads stay well under the API limits. */
async function prepare(file) {
  if (!OK_TYPES.includes(file.type)) throw new Error('Use a PNG, JPEG or WebP image.');
  const bmp = await createImageBitmap(file);
  const long = Math.max(bmp.width, bmp.height);
  if (file.size <= MAX_PLAIN_BYTES && long <= MAX_SIDE) { bmp.close?.(); return file; }
  const k = Math.min(1, MAX_SIDE / long);
  const c = document.createElement('canvas');
  c.width = Math.max(1, Math.round(bmp.width * k));
  c.height = Math.max(1, Math.round(bmp.height * k));
  const g = c.getContext('2d');
  g.fillStyle = '#fff'; g.fillRect(0, 0, c.width, c.height); // JPEG has no alpha
  g.drawImage(bmp, 0, 0, c.width, c.height);
  bmp.close?.();
  const blob = await new Promise((res) => c.toBlob(res, 'image/jpeg', 0.9));
  if (!blob) throw new Error('Could not process the image.');
  return blob;
}

/**
 * ctx: { $, toast, getState(), getSelection(), showPreview(id), refresh(), withFallbackConsent(run) }
 * Returns hooks the main app calls: attachmentIds(), clearAttachments(), onState(st),
 * proposalCards(st), thumbs(ids, docs), uploading().
 */
export function initVision(ctx) {
  const { $, toast } = ctx;
  const V = { attach: [], open: null, dismissed: new Set(), seen: new Set(), drafts: {}, error: '', busy: false };
  // Which attachment workflows the configured providers support (State.features).
  const features = () => ctx.getState()?.features ?? { images: true, documents: false };
  const imagesOff = () => {
    if (features().images) return false;
    toast(features().image_reason || 'Image features need a vision model (OpenAI or Claude).', 'bad');
    return true;
  };

  // ---- attachments ------------------------------------------------------------------

  const isImage = (f) => OK_TYPES.includes(f.type) || f.type.startsWith('image/');
  let seq = 0;
  // The server's error without the file name it repeats ("x.txt is empty: …" → "File is empty: …").
  const reason = (name, msg) => {
    let r = (msg || 'upload failed').trim();
    if (r.startsWith(name)) r = r.slice(name.length).replace(/^[:\s]+/, '');
    if (/^(is|has|was)\b/.test(r)) r = 'file ' + r;
    return r.charAt(0).toUpperCase() + r.slice(1);
  };
  const kb = (n) => (n >= 1 << 20 ? `${(n / (1 << 20)).toFixed(1)} MB` : `${Math.max(1, Math.round(n / 1024))} KB`);

  // Documents being uploaded (a PDF is extracted by a model first, which takes a while). They
  // show as cards right away; the server's spec list replaces them once stored.
  V.docs = [];
  async function addSpec(file) {
    const d = { key: ++seq, name: file.name || 'document', size: file.size, status: 'uploading' };
    V.docs.push(d); renderSpecs(ctx.getState());
    try {
      await ctx.withFallbackConsent(() => uploadSpec(file));
      V.docs = V.docs.filter((x) => x !== d);
      ctx.refresh();
    } catch (e) {
      d.status = 'error'; d.error = e.message;
      toast(`${d.name}: ${e.message}`, 'bad');
    }
    renderSpecs(ctx.getState());
  }

  function renderSpecs(st) {
    const stored = (st?.specs ?? []).map((s) => h('span', { class: 'chip spec', title: `${s.name} · ${s.chars} characters · remembered until Clear` },
      h('span', { class: 'ficon', 'aria-hidden': 'true' }, '📄'),
      h('span', { class: 'fname' }, s.name), h('span', { class: 'fmeta' }, `${s.chars.toLocaleString()} chars`),
      h('button', { type: 'button', 'aria-label': `Remove ${s.name}`, onclick: async () => { try { await api('spec/remove', { name: s.name }); ctx.refresh(); } catch (e) { toast(e.message, 'bad'); } } }, '✕')));
    const pending = V.docs.map((d) => h('span', { class: `chip spec ${d.status}`, title: d.error ? `${d.name}: ${d.error}` : `${d.name} · uploading…` },
      h('span', { class: 'ficon', 'aria-hidden': 'true' }, d.status === 'error' ? '⚠' : '📄'),
      h('span', { class: 'fname' }, d.name), h('span', { class: 'fmeta' }, d.status === 'error' ? reason(d.name, d.error) : `${kb(d.size)} · uploading…`),
      h('button', { type: 'button', 'aria-label': `Remove ${d.name}`, onclick: () => { V.docs = V.docs.filter((x) => x !== d); renderSpecs(ctx.getState()); } }, '✕')));
    $('specs').replaceChildren(...stored, ...pending);
    renderCount(st);
  }

  // The Attach button shows how many attachments go with the next message.
  function renderCount(st = ctx.getState()) {
    const n = V.attach.length + (st?.specs ?? []).length + V.docs.length;
    let b = $('btn-attach').querySelector('.count');
    if (!n) { b?.remove(); return; }
    if (!b) { b = h('span', { class: 'count' }); $('btn-attach').append(b); }
    b.textContent = String(n);
    b.title = `${n} attachment${n === 1 ? '' : 's'}`;
  }

  const addAny = (f) => (isImage(f) ? addFile(f) : addSpec(f));

  // An image card appears immediately (local preview, "uploading…"), then becomes ready or failed.
  async function addFile(file) {
    if (imagesOff()) return null;
    if (V.attach.length >= MAX_ATTACH) { toast(`Attach at most ${MAX_ATTACH} images to one message.`, 'bad'); return null; }
    const a = { key: ++seq, name: file.name || 'pasted image', status: 'uploading', preview: URL.createObjectURL(file) };
    V.attach.push(a); renderAttachments();
    try {
      const info = await uploadImage(await prepare(file));
      if (V.attach.some((x) => x.id === info.id)) V.attach = V.attach.filter((x) => x !== a); // same image twice
      else Object.assign(a, { id: info.id, w: info.width, h: info.height, status: 'ready' });
      return info;
    } catch (e) {
      Object.assign(a, { status: 'error', error: e.message });
      toast(`${a.name}: ${e.message}`, 'bad');
      return null;
    } finally {
      renderAttachments();
      if (a.status !== 'error') URL.revokeObjectURL(a.preview); // the card now shows the stored image
    }
  }

  function renderAttachments() {
    $('attachments').replaceChildren(...V.attach.map((a) => {
      const meta = a.status === 'ready' ? `${a.w}×${a.h}` : a.status === 'error' ? reason(a.name, a.error) : 'uploading…';
      return h('span', { class: `chip attach ${a.status}`, title: a.error ? `${a.name}: ${a.error}` : `${a.name} · ${meta}` },
        h('img', { src: a.id ? imageUrl(a.id) : a.preview, alt: '' }),
        h('span', { class: 'fname' }, `${a.name} · ${meta}`),
        h('button', { type: 'button', 'aria-label': `Remove ${a.name}`, onclick: () => { V.attach = V.attach.filter((x) => x !== a); renderAttachments(); } }, '✕'));
    }));
    renderCount();
  }

  const hasFiles = (e) => [...(e.dataTransfer?.types ?? [])].includes('Files');
  let dragDepth = 0;
  document.addEventListener('dragenter', (e) => { if (hasFiles(e)) { dragDepth++; $('drop-hint').hidden = false; } });
  document.addEventListener('dragleave', (e) => { if (hasFiles(e) && --dragDepth <= 0) { dragDepth = 0; $('drop-hint').hidden = true; } });
  document.addEventListener('dragover', (e) => { if (hasFiles(e)) e.preventDefault(); });
  document.addEventListener('drop', async (e) => {
    if (!hasFiles(e)) return;
    e.preventDefault(); dragDepth = 0; $('drop-hint').hidden = true;
    await Promise.all([...e.dataTransfer.files].map(addAny));
    $('prompt').focus();
  });
  document.addEventListener('paste', async (e) => {
    const files = [...(e.clipboardData?.files ?? [])].filter((f) => f.type.startsWith('image/'));
    if (!files.length) return;
    e.preventDefault();
    await Promise.all(files.map(addFile));
    $('prompt').focus();
  });
  $('btn-attach').addEventListener('click', () => { V.fromImage = false; $('file-input').click(); });
  $('btn-from-image').addEventListener('click', () => { if (imagesOff()) return; V.fromImage = true; $('file-input').click(); });
  $('file-input').addEventListener('change', async (e) => {
    const files = [...e.target.files];
    e.target.value = '';
    await Promise.all(files.map(addAny));
    if (V.fromImage && V.attach.some((a) => a.status === 'ready') && !$('prompt').value.trim()) {
      const onFace = (ctx.getSelection()?.selectors ?? []).some((s) => /^face\(/.test(s));
      $('prompt').value = onFace ? 'Create this shape on the selected face from the attached image.' : 'Create this part from the attached image.';
      $('composer').requestSubmit();
    }
    V.fromImage = false;
  });

  // ---- interpretation panel -----------------------------------------------------------

  const find = (st, id) => (st?.proposals ?? []).find((p) => p.id === id);
  const draftOf = (id) => (V.drafts[id] ??= { dims: {}, confirm: {}, depth: '', scaleMM: '', placement: '', amb: '' });
  const label = (r) => r.dimension === 'scale' ? 'overall width of the drawing' : r.ask || r.dimension;

  function status(p) {
    const r = p.report;
    if (r.errors?.length) return { cls: 'bad', text: 'has errors' };
    if (p.ready) return { cls: 'ok', text: 'ready to create' };
    const need = [...(r.missing ?? []).map(label), ...(r.unconfirmed ?? []).map((id) => `confirm ${id}`), ...(r.ambiguous?.length ? ['choose how shapes combine'] : [])];
    return { cls: 'warn', text: 'needs: ' + need.join('; ') };
  }

  function openPanel(id) { V.open = id; V.error = ''; renderPanel(); }
  function closePanel() { V.open = null; $('interp').hidden = true; }

  function overlay(p) {
    const W = p.image_w || p.proposal.image_size?.[0] || 1, H = p.image_h || p.proposal.image_size?.[1] || 1;
    const roles = entityRoles(p.proposal);
    const scale = Math.max(W, H) / 700; // keeps strokes and labels legible whatever the resolution
    const shapes = [];
    for (const e of p.proposal.entities ?? []) {
      const cls = confidenceClass(e.confidence);
      const g = svg('g', { class: `ov ${cls} ${roles[e.id] ?? ''}`, 'stroke-width': 2.5 * scale },
        svg('title', {}, `${e.id} · ${e.kind} · confidence ${pct(e.confidence)}${roles[e.id] === 'hole' ? ' · hole' : ''}`));
      for (const s of entityShapes(e)) g.append(svg(s.tag, s.attrs));
      const b = entityShapes(e)[0];
      if (b) {
        const at = labelPoint(e);
        g.append(svg('text', { x: at[0], y: at[1], 'font-size': 13 * scale, class: 'ov-label' }, e.id));
      }
      shapes.push(g);
    }
    for (const d of p.proposal.dimensions ?? []) {
      if (!d.bbox || d.bbox.length !== 4) continue;
      const unc = (p.report.unconfirmed ?? []).includes(d.id);
      shapes.push(svg('g', { class: `ovd ${unc ? 'warn' : 'ok'}`, 'stroke-width': 2 * scale },
        svg('title', {}, `${d.id}${d.value != null ? ' = ' + d.value + ' mm' : ''} · ${pct(d.confidence)}`),
        svg('rect', { x: d.bbox[0], y: d.bbox[1], width: d.bbox[2], height: d.bbox[3] }),
        d.value != null ? svg('text', { x: d.bbox[0], y: d.bbox[1] - 3 * scale, 'font-size': 12 * scale, class: 'ov-label' }, `${d.value} mm`) : null));
    }
    return svg('svg', { viewBox: `0 0 ${W} ${H}`, preserveAspectRatio: 'xMidYMid meet', class: 'interp-svg', 'aria-label': 'Detected geometry' }, shapes);
  }

  function labelPoint(e) {
    switch (e.kind) {
      case 'circle': return [e.cx, e.cy];
      case 'rectangle': return [e.x + 3, e.y + 14];
      case 'line': case 'arc': return [(e.p1[0] + e.p2[0]) / 2, (e.p1[1] + e.p2[1]) / 2];
      case 'slot': return [(e.x1 + e.x2) / 2, (e.y1 + e.y2) / 2];
      case 'polygon': return e.points[0];
    }
    return [0, 0];
  }

  /** Everything the user still has to answer is answered, and the proposal has no errors. */
  function isReady(p) {
    const d = draftOf(p.id), rep = p.report;
    const amb = rep.ambiguous ?? [], missing = rep.missing ?? [];
    const ambOk = amb.length === 1 ? !!d.amb : amb.length === 0;
    const answered = missing.every((m) => m.dimension === 'scale' ? +d.scaleMM > 0 : m.dimension === 'extrusion_depth' ? +d.depth > 0 : +d.dims[m.dimension] > 0);
    const confirmed = (rep.unconfirmed ?? []).every((id) => d.confirm[id] || +d.dims[id] > 0);
    return ambOk && answered && confirmed && !(rep.errors?.length);
  }

  // The form is not rebuilt on every keystroke, so the Create button follows the answers here.
  function syncCreate() {
    const p = find(ctx.getState(), V.open), btn = document.querySelector('#interp [data-act="create"]');
    if (p && btn) btn.disabled = !isReady(p) || V.busy || ctx.getState().busy || (p.used ?? []).length > 0;
  }

  function numInput(value, onchange, props = {}) {
    return h('input', { type: 'number', step: 'any', min: '0', value: value ?? '', class: 'num', ...props, oninput: (e) => { onchange(e.target.value); syncCreate(); } });
  }

  function renderPanel() {
    const st = ctx.getState();
    const p = find(st, V.open);
    const root = $('interp');
    if (!p) { root.hidden = true; return; }
    root.hidden = false;
    const d = draftOf(p.id);
    const rep = p.report;
    const stat = status(p);
    const missing = rep.missing ?? [];
    const dims = p.proposal.dimensions ?? [];
    const faceSel = (ctx.getSelection()?.selectors ?? []).find((s) => /^face\(/.test(s));
    if (!d.placement) d.placement = p.proposal.placement?.face ? 'selection' : (p.proposal.placement?.plane || 'XY');
    const img = st.images?.find((i) => i.id === p.image_id);
    const unc = uncertain(p.proposal, rep);
    const amb = rep.ambiguous ?? [];
    const needDepth = missing.some((m) => m.dimension === 'extrusion_depth');
    const used = (p.used ?? []).length > 0;

    const questions = missing.map((m) => {
      if (m.dimension === 'scale') {
        return h('label', { class: 'q' }, h('span', {}, `Real ${label(m)} (mm)`),
          numInput(d.scaleMM, (v) => { d.scaleMM = v; }, { 'data-q': 'scale', placeholder: 'e.g. 80' }));
      }
      if (m.dimension === 'extrusion_depth') {
        return h('label', { class: 'q' }, h('span', {}, 'Extrusion depth / thickness (mm)'),
          numInput(d.depth, (v) => { d.depth = v; }, { 'data-q': 'depth', placeholder: 'e.g. 5' }));
      }
      return h('label', { class: 'q' }, h('span', {}, `${m.ask || m.dimension} (mm)`),
        numInput(d.dims[m.dimension], (v) => { d.dims[m.dimension] = v; }, { 'data-q': m.dimension, placeholder: 'mm' }));
    });

    const dimRows = dims.filter((x) => !missing.some((m) => m.dimension === x.id)).map((x) => {
      const isUnc = (rep.unconfirmed ?? []).includes(x.id);
      return h('tr', {},
        h('td', {}, x.id), h('td', { class: 'muted' }, x.binds || x.kind),
        h('td', {}, numInput(d.dims[x.id] ?? (x.value ?? ''), (v) => { d.dims[x.id] = v; }, { 'data-dim': x.id })),
        h('td', { class: 'muted' }, x.source),
        h('td', {}, h('span', { class: `badge ${confidenceClass(x.confidence)}`, title: 'confidence' }, pct(x.confidence))),
        h('td', {}, isUnc ? h('label', { class: 'inline' }, h('input', { type: 'checkbox', checked: !!d.confirm[x.id], 'data-confirm': x.id,
          onchange: (e) => { d.confirm[x.id] = e.target.checked; syncCreate(); } }), 'confirm') : h('span', { class: 'ok-mark' }, '✓')));
    });

    const ambUI = amb.length === 1 ? h('fieldset', { class: 'amb' },
      h('legend', {}, amb[0].question),
      ...CHOICES.map((c) => h('label', { class: 'inline' },
        h('input', { type: 'radio', name: 'amb', value: c.id, checked: d.amb === c.id, onchange: () => { d.amb = c.id; syncCreate(); } }), c.label))) :
      (amb.length > 1 ? h('div', { class: 'warn-box' }, amb.map((a) => a.question).join(' '), ' Describe how they should combine in the chat.') : null);

    root.replaceChildren(h('div', { class: 'dialog wide', role: 'dialog', 'aria-label': 'Image interpretation' },
      h('div', { class: 'interp-head' },
        h('b', {}, `Image interpretation ${p.id}`),
        h('span', { class: `badge ${stat.cls}` }, stat.text),
        h('span', { class: 'muted' }, `revision ${p.revision} · overall confidence ${pct(p.proposal.confidence)}`),
        h('span', { class: 'spacer' }),
        h('button', { type: 'button', onclick: closePanel, 'aria-label': 'Close' }, '✕')),
      h('div', { class: 'interp-body' },
        h('div', { class: 'interp-left' },
          h('div', { class: 'interp-img' }, h('img', { src: imageUrl(p.image_id), alt: 'Source image' }), overlay(p)),
          h('div', { class: 'legend' },
            h('span', { class: 'badge ok' }, '≥85%'), h('span', { class: 'badge warn' }, '≥60%'), h('span', { class: 'badge bad' }, 'uncertain'),
            h('span', { class: 'muted' }, 'solid = outline · dashed = hole · boxes = read dimensions')),
          p.proposal.description ? h('div', { class: 'muted' }, p.proposal.description) : null),
        h('div', { class: 'interp-right' },
          (rep.errors?.length ? h('div', { class: 'warn-box bad' }, h('b', {}, 'Problems'), h('ul', {}, rep.errors.map((x) => h('li', {}, x))), ' Ask the agent to correct the interpretation.') : null),
          unc.length ? h('div', { class: 'warn-box' }, 'Uncertain: ' + unc.map((u) => `${u.kind} ${u.id} (${pct(u.confidence)})`).join(', ')) : null,
          questions.length ? h('div', { class: 'questions' }, h('h3', {}, 'Needed from you'), ...questions) : null,
          dimRows.length ? h('div', {}, h('h3', {}, 'Detected dimensions'),
            h('table', { class: 'dims' }, h('thead', {}, h('tr', {}, ['dimension', 'on', 'mm', 'source', 'conf.', ''].map((t) => h('th', {}, t)))), h('tbody', {}, dimRows))) : null,
          ambUI,
          (rep.warnings ?? []).length ? h('ul', { class: 'warns' }, rep.warnings.map((w) => h('li', { class: 'warn' }, '⚠ ' + w))) : null,
          h('div', { class: 'placement' },
            h('label', { class: 'inline' }, 'Place on ',
              h('select', { 'data-q': 'placement', onchange: (e) => { d.placement = e.target.value; } },
                ...['XY', 'XZ', 'YZ'].map((pl) => h('option', { value: pl, selected: d.placement === pl }, `plane ${pl}`)),
                h('option', { value: 'selection', disabled: !faceSel, selected: d.placement === 'selection' }, faceSel ? `selected face (${faceSel})` : 'selected face (select a planar face first)')))),
          h('label', { class: 'inline' }, h('input', { type: 'checkbox', checked: !!img?.retained,
            onchange: async (e) => { try { await api(`image/${encodeURIComponent(p.image_id)}/retain`, { keep: e.target.checked }); ctx.refresh(); } catch (err) { toast(err.message, 'bad'); } } }),
            'Keep the source image in the project (assets/images)'),
          V.error ? h('div', { class: 'warn-box bad', role: 'alert' }, V.error) : null,
          h('div', { class: 'row actions' },
            h('button', { type: 'button', class: 'primary', disabled: !isReady(p) || V.busy || st.busy || used, 'data-act': 'create', onclick: () => create(p) },
              V.busy ? 'Working…' : used ? 'Already created' : 'Create & preview'),
            h('button', { type: 'button', onclick: () => correctWithAgent(p) }, 'Correct with the agent…'),
            h('button', { type: 'button', onclick: () => { V.dismissed.add(p.id); closePanel(); ctx.refresh(); } }, 'Discard'))))));
  }

  function correctWithAgent(p) {
    closePanel();
    if (!V.attach.some((a) => a.id === p.image_id)) {
      const info = (ctx.getState().images ?? []).find((i) => i.id === p.image_id);
      V.attach.push({ key: ++seq, status: 'ready', id: p.image_id, w: info?.width ?? p.image_w, h: info?.height ?? p.image_h, name: 'source image' });
      renderAttachments();
    }
    $('prompt').value = `Correct interpretation ${p.id}: `;
    $('prompt').focus();
  }

  async function tool(name, args) {
    const o = await api('tool', { name, args });
    if (!o.ok) throw new Error(o.error || `${name} failed`);
    return o;
  }

  async function create(p) {
    const d = draftOf(p.id), id = p.id;
    V.busy = true; V.error = ''; renderPanel();
    try {
      const rep = p.report;
      for (const x of p.proposal.dimensions ?? []) {
        const typed = d.dims[x.id];
        const wants = +typed > 0 && +typed !== x.value;
        if (wants) await tool('confirm_detected_dimension', { interpretation_id: id, dimension: x.id, value: +typed });
        else if ((rep.unconfirmed ?? []).includes(x.id) && d.confirm[x.id]) await tool('confirm_detected_dimension', { interpretation_id: id, dimension: x.id });
      }
      for (const m of rep.missing ?? []) {
        if (m.dimension === 'scale') {
          const px = drawingWidthPx(p.proposal);
          if (!(px > 0)) throw new Error('Cannot measure the drawing width; give a dimension through the chat instead.');
          await tool('set_sketch_scale', { interpretation_id: id, pixels: px, mm: +d.scaleMM });
        }
      }
      if ((rep.ambiguous ?? []).length === 1 && d.amb) {
        await tool('update_sketch_proposal', { interpretation_id: id, edits: ambiguityEdits(rep.ambiguous[0].profiles, d.amb) });
      }
      const args = { interpretation_id: id, mode: 'solid' };
      if (+d.depth > 0) args.extrusion_depth = +d.depth;
      if (d.placement === 'selection') args.face = 'selection';
      else if (d.placement) args.plane = d.placement;
      const o = await tool('create_from_image', args);
      V.busy = false;
      closePanel();
      toast('Preview ready: review it, then commit or reject.', 'ok');
      await ctx.refresh();
      if (o.changeset?.id) ctx.showPreview(o.changeset.id);
    } catch (e) {
      V.busy = false; V.error = e.message; renderPanel();
      ctx.refresh();
    }
  }

  // ---- chat integration -----------------------------------------------------------------

  function proposalCards(st) {
    return (st.proposals ?? []).filter((p) => !(p.used ?? []).length && !V.dismissed.has(p.id)).map((p) => {
      const s = status(p);
      return h('div', { class: 'cs interp-card' },
        h('div', { class: 'title' }, h('span', {}, `Image interpretation ${p.id}`), h('span', { class: `badge ${s.cls}` }, s.text)),
        h('div', { class: 'actions' }, h('button', { class: 'primary', onclick: () => openPanel(p.id) }, 'Review interpretation')));
    });
  }

  /** What a sent user message carried: image thumbnails and the documents attached with it. */
  function thumbs(ids, docs) {
    const imgs = (ids ?? []).map((id) => h('img', { class: 'thumb', src: imageUrl(id), alt: 'attached image', loading: 'lazy' }));
    const files = (docs ?? []).map((d) => h('span', { class: 'msg-doc', title: `${d} · remembered until Clear` }, `📄 ${d}`));
    return imgs.length || files.length ? h('div', { class: 'thumbs' }, ...imgs, ...files) : null;
  }

  // Hide image workflows that cannot work with the configured providers.
  const TEXT_ACCEPT = '.txt,.md,.markdown,.json,.csv,.yaml,.yml,.xml,text/*';
  function renderFeatures(st) {
    const f = st.features ?? { images: true, documents: false };
    $('btn-from-image').hidden = !f.images;
    $('file-input').accept = [f.images ? OK_TYPES.join(',') : '', TEXT_ACCEPT, f.documents ? '.pdf,application/pdf' : ''].filter(Boolean).join(',');
    $('btn-attach').title = (f.images ? 'Attach an image or a text document' : 'Attach a text document') +
      (f.documents ? ' (or PDF)' : '') + ' with specifications (or drop / paste one anywhere). Documents are remembered until Clear.' +
      (f.images ? '' : ' Images are unavailable: ' + (f.image_reason || 'no vision model is configured.'));
    if (!f.images && V.attach.length) { V.attach = []; renderAttachments(); }
  }

  /** Called after every state refresh: opens the panel for a new interpretation, re-renders an open one. */
  function onState(st) {
    renderSpecs(st);
    renderFeatures(st);
    for (const p of st.proposals ?? []) {
      if ((p.used ?? []).length || V.dismissed.has(p.id) || V.seen.has(p.id)) continue;
      V.seen.add(p.id);
      if (!V.open) { V.open = p.id; V.error = ''; }
    }
    if (V.open) {
      if (!find(st, V.open)) closePanel();
      else if (!V.busy && !isTyping()) renderPanel();
      else syncCreate(); // the form stays as is, but Create follows the agent finishing
    }
  }

  // Do not rebuild the form under the user's cursor.
  const isTyping = () => $('interp').contains(document.activeElement) && /^(INPUT|SELECT)$/.test(document.activeElement.tagName);

  document.addEventListener('keydown', (e) => { if (e.key === 'Escape' && V.open) closePanel(); });
  $('interp').addEventListener('click', (e) => { if (e.target === $('interp')) closePanel(); });

  return {
    attachmentIds: () => V.attach.filter((a) => a.status === 'ready').map((a) => a.id),
    uploading: () => V.attach.some((a) => a.status === 'uploading') || V.docs.some((d) => d.status === 'uploading'),
    clearAttachments: () => {
      V.attach = []; V.docs = V.docs.filter((d) => d.status === 'uploading');
      renderAttachments(); renderSpecs(ctx.getState());
    },
    onState, proposalCards, thumbs,
  };
}
