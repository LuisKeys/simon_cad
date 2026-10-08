// Visual Comparison panel: Reference | Current render | Difference for any iteration of the
// visual refinement, its scores against the stage thresholds, what changed, the critic's
// findings, region locks, and the controls (accept, revert, compare previous, run).
//
// Nothing here changes the model directly: runs, reverts and locks go through the server
// (refinement controller, checkpoint restore, lock_region / unlock_region tools).
import { api, h, imageUrl, renderUrl, diffUrl } from './api.js';
import { scoreRows, currentIteration, comparisonViews, parameterChanges, critiqueErrors, pct } from './visualcmp-model.js';

/** ctx: { $, toast, getState(), attachmentIds(), clearAttachments(), withFallbackConsent(run) } */
export function initVisualPanel(ctx) {
  const { $, toast } = ctx;
  const P = { open: false, data: null, sel: null, view: null, previous: false, goal: '', iterations: 8, busy: false, live: null };

  async function load() {
    try { P.data = await api('visual'); } catch (e) { toast(e.message, 'bad'); return; }
    const cur = currentIteration(P.data.history);
    if (!P.sel || !P.data.history.some((it) => it.iteration === P.sel) || P.followLatest) P.sel = P.data.history.length ? P.data.history[P.data.history.length - 1].iteration : null;
    if (!P.goal) P.goal = P.data.goal ?? '';
    P.cur = cur?.iteration ?? null;
    render();
  }

  function open() { P.open = true; P.followLatest = true; $('visual').hidden = false; load(); }
  function close() { P.open = false; $('visual').hidden = true; }

  const iteration = () => P.data?.history.find((it) => it.iteration === P.sel);

  async function act(path, body, okMsg) {
    P.busy = true; render();
    try { await api(path, body); if (okMsg) toast(okMsg, 'ok'); } catch (e) { toast(e.message, 'bad'); }
    P.busy = false; await load();
  }

  async function run() {
    const images = ctx.attachmentIds();
    if (!P.goal.trim() && !P.data?.goal) { toast('Describe what to model.', 'bad'); return; }
    if (!images.length && !(P.data?.references ?? []).length) { toast('Attach at least one reference image first (Attach, drop or paste).', 'bad'); return; }
    P.busy = true; render();
    try {
      await ctx.withFallbackConsent(() => api('visual/run', { goal: P.goal.trim(), images, max_iterations: Number(P.iterations) || 8, thinking: $('thinking').checked }));
      ctx.clearAttachments();
      P.followLatest = true;
    } catch (e) { toast(e.message, 'bad'); }
    P.busy = false; await load();
  }

  function img(src, alt) {
    if (!src) return h('div', { class: 'vc-missing' }, alt);
    const el = h('img', { src, alt, loading: 'lazy' });
    el.addEventListener('error', () => el.replaceWith(h('div', { class: 'vc-missing' }, `${alt} not available (renders are kept only for this session)`)));
    return el;
  }

  function panes(it) {
    const views = comparisonViews(it, P.data.references);
    if (!views.length) return h('div', { class: 'muted' }, 'This iteration has no renders.');
    if (!views.some((v) => v.key === P.view)) P.view = views[0].key;
    const v = views.find((x) => x.key === P.view);
    const avail = (id) => id && P.data.renders[id];
    const curSrc = avail(it.renders[v.key]) ? renderUrl(it.renders[v.key]) : null;
    let left, leftLabel, diffSrc = null, diffLabel = 'Difference';
    const prev = P.data.history.find((x) => x.iteration === it.iteration - 1);
    if (P.previous && prev) {
      leftLabel = `Previous (iteration ${prev.iteration})`;
      left = img(avail(prev.renders[v.key]) ? renderUrl(prev.renders[v.key]) : null, leftLabel);
      if (curSrc) diffSrc = diffUrl(it.iteration, v.ref ?? v.key, true);
      diffLabel = 'Change (red = before only, blue = now only)';
    } else if (v.ref) {
      leftLabel = 'Reference';
      left = img(imageUrl(v.ref), 'Reference');
      if (curSrc && P.data.has_mask[v.ref]) diffSrc = diffUrl(it.iteration, v.ref, false);
      diffLabel = 'Silhouette difference (red = missing, blue = extra)';
    } else {
      leftLabel = 'Reference';
      left = h('div', { class: 'vc-missing' }, 'No reference for this view');
    }
    const sil = it.silhouettes?.[v.key];
    return h('div', { class: 'vc-compare' },
      h('div', { class: 'vc-tabs', role: 'tablist' }, views.map((x) => h('button', { type: 'button', class: x.key === P.view ? 'on' : '', role: 'tab',
        'aria-selected': String(x.key === P.view), onclick: () => { P.view = x.key; render(); } }, x.label))),
      h('div', { class: 'vc-panes' },
        h('figure', {}, h('figcaption', {}, leftLabel), left),
        h('figure', {}, h('figcaption', {}, `Render · iteration ${it.iteration}`), img(curSrc, 'Render')),
        h('figure', {}, h('figcaption', {}, diffLabel), diffSrc ? img(diffSrc, 'Difference') : h('div', { class: 'vc-missing' }, 'No silhouette to compare'))),
      sil ? h('div', { class: 'muted small' }, `IoU ${pct(sil.iou)} · contour ${pct(sil.contour)} · bounding box ${pct(sil.bbox)}${sil.mirrored ? ' · reference mirrored' : ''}`) : null);
  }

  function scores(it) {
    const rows = scoreRows(it, P.data.stages);
    return h('div', { class: 'vc-scores' }, rows.map((r) => h('div', { class: `vc-score${r.key === 'overall' ? ' overall' : ''}` },
      h('span', { class: 'lbl' }, r.label),
      h('span', { class: 'bar' }, h('span', { class: `fill ${r.pass === false ? 'bad' : r.pass ? 'ok' : ''}`, style: `width:${Math.round(r.value * 100)}%` }),
        r.threshold !== undefined ? h('span', { class: 'thr', style: `left:${Math.round(r.threshold * 100)}%`, title: `threshold ${pct(r.threshold)}` }) : null),
      h('span', { class: 'val' }, pct(r.value)))),
    it.gate?.failing?.length ? h('div', { class: 'warn-box small' }, `Stage ${it.stage} not passed: ${it.gate.failing.join(', ')}`) : h('div', { class: 'ok-box small' }, `Stage ${it.stage} thresholds passed`));
  }

  function details(it) {
    const params = parameterChanges(it);
    const errs = critiqueErrors(it);
    return h('div', { class: 'vc-details' },
      it.changes?.length ? h('div', {}, h('h3', {}, 'Corrections applied'), h('ul', {}, it.changes.map((c) => h('li', {}, `[${c.region}] ${c.problem}`)))) : null,
      params.length ? h('div', {}, h('h3', {}, 'Parameters'), h('ul', {}, params.map((p) => h('li', {}, `${p.name}: ${p.before ?? '—'} → ${p.after ?? '—'}`)))) : null,
      it.note ? h('div', { class: 'muted small' }, it.note) : null,
      errs.length ? h('div', {}, h('h3', {}, 'Critic: largest errors'), h('ul', {}, errs.slice(0, 5).map((e) => h('li', {}, `${pct(e.severity)} · [${e.region}] ${e.problem}`)))) : null);
  }

  function regions() {
    const list = P.data.regions ?? [];
    return h('div', { class: 'vc-regions' }, h('h3', {}, 'Regions'),
      list.length ? list.map((r) => h('label', { class: 'inline', title: `objects: ${(r.objects ?? []).join(', ') || '—'} · parameters: ${(r.parameters ?? []).join(', ') || '—'}` },
        h('input', { type: 'checkbox', checked: !!r.locked, disabled: P.busy || P.data.running, onchange: (e) => act('visual/lock', { region: r.name, locked: e.target.checked }) }),
        `${r.locked ? '🔒' : '🔓'} ${r.name}`)) : h('div', { class: 'muted small' }, 'No regions yet: the agent defines them while building.'));
  }

  function runBox() {
    const running = P.data.running;
    return h('div', { class: 'vc-run' },
      h('h3', {}, 'Run visual refinement'),
      h('textarea', { rows: 2, placeholder: 'What to model, e.g. "Lamborghini Countach LP400, 1:10 scale"', disabled: running, oninput: (e) => { P.goal = e.target.value; } }, P.goal),
      h('div', { class: 'row' },
        h('label', { class: 'inline small' }, 'Iterations ', h('input', { type: 'number', min: 1, max: 30, value: P.iterations, style: 'width:56px', oninput: (e) => { P.iterations = e.target.value; } })),
        h('span', { class: 'muted small' }, `${ctx.attachmentIds().length} new image(s) attached · ${(P.data.references ?? []).length} reference(s)`),
        h('span', { class: 'spacer' }),
        running ? h('button', { type: 'button', onclick: () => act('visual/stop', {}, 'Stopping after the current step…') }, 'Stop')
          : h('button', { type: 'button', class: 'primary', disabled: P.busy || ctx.getState()?.busy || !ctx.getState()?.features?.refinement, onclick: run }, 'Run visual refinement')),
      !ctx.getState()?.features?.refinement ? h('div', { class: 'warn-box small' }, 'Visual refinement needs a vision model (OpenAI or Claude).') : null);
  }

  function render() {
    if (!P.open) return;
    const d = P.data;
    const root = $('visual');
    if (!d) { root.replaceChildren(h('div', { class: 'dialog vc', role: 'dialog', 'aria-label': 'Visual comparison' }, 'Loading…')); return; }
    const it = iteration();
    const live = d.running && d.state ? d.state : null;
    root.replaceChildren(h('div', { class: 'dialog vc', role: 'dialog', 'aria-label': 'Visual comparison' },
      h('div', { class: 'interp-head' },
        h('b', {}, 'Visual comparison'),
        d.stage ? h('span', { class: 'badge' }, `Stage ${d.stage}`) : null,
        live ? h('span', { class: 'vc-live' }, `${live.state} · ${live.message}`) : (d.state ? h('span', { class: 'muted small' }, `${d.state.state}: ${d.state.message}`) : null),
        h('span', { class: 'spacer' }),
        h('button', { type: 'button', class: 'linkbtn', 'aria-label': 'Close', onclick: close }, '✕')),
      h('div', { class: 'vc-body' },
        h('div', { class: 'vc-list' }, h('h3', {}, 'Iterations'),
          d.history.length ? [...d.history].reverse().map((x) => h('button', { type: 'button', class: `vc-it${x.iteration === P.sel ? ' on' : ''}${x.reverted ? ' reverted' : ''}`,
            onclick: () => { P.sel = x.iteration; P.followLatest = false; render(); } },
            h('span', {}, `#${x.iteration}`), h('span', { class: 'muted' }, `${x.stage.toLowerCase()} · ${x.kind}`),
            h('span', { class: 'val' }, pct(x.scores?.overall)),
            x.reverted ? h('span', { class: 'badge bad' }, 'reverted') : x.accepted ? h('span', { class: 'badge ok' }, 'accepted') : x.iteration === P.cur ? h('span', { class: 'badge' }, 'current') : null))
            : h('div', { class: 'muted small' }, 'No iterations yet.'),
          regions()),
        h('div', { class: 'vc-main' },
          it ? [
            panes(it),
            h('div', { class: 'row vc-actions' },
              h('button', { type: 'button', class: 'primary', disabled: P.busy || d.running || it.reverted, onclick: () => act('visual/accept', { iteration: it.iteration }, `Iteration ${it.iteration} accepted`) }, 'Accept iteration'),
              h('button', { type: 'button', disabled: P.busy || d.running || it.iteration < 2 || it.reverted, onclick: () => act('visual/revert', { iteration: it.iteration }, `Iteration ${it.iteration} reverted`) }, 'Revert'),
              h('button', { type: 'button', class: P.previous ? 'on' : '', 'aria-pressed': String(P.previous), disabled: it.iteration < 2, onclick: () => { P.previous = !P.previous; render(); } }, 'Compare previous')),
            h('div', { class: 'vc-info' }, scores(it), details(it)),
          ] : h('div', { class: 'muted' }, 'Attach reference images (side, front, top views work best), describe the object and run a visual refinement.'),
          runBox())),
    ));
  }

  $('btn-visual').addEventListener('click', () => (P.open ? close() : open()));
  $('visual').addEventListener('click', (e) => { if (e.target === $('visual')) close(); });
  document.addEventListener('keydown', (e) => { if (e.key === 'Escape' && P.open) close(); });

  return {
    /** Server notices: live progress and state changes. */
    onNotice(n) {
      if (!P.open) return;
      if (n.kind === 'refine' && P.data) { P.data.state = n.refine; P.data.running = true; render(); }
      if (n.kind === 'visual' || n.kind === 'agent_done') load();
    },
    open,
  };
}
