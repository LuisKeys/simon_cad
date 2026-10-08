// Settings screen: AI models (primary and fallback), UI theme, viewport display preferences and the open project's
// workspace (units, grid, snapping). Values come from, and are saved to, the backend configuration layer
// (internal/config) and, for the project's workspace, the workspace tools; this module holds no other state.
import { api, h } from './api.js';
import { workspaceSection } from './workspace-settings.js';

const PROVIDERS = [['openai', 'OpenAI'], ['claude', 'Anthropic'], ['ollama', 'Ollama']];
// The fallback model is always a cloud model: it serves what the local or primary model cannot.
const CLOUD = PROVIDERS.filter(([p]) => p !== 'ollama');
const CONSENTS = [['ask', 'Ask before the first remote fallback'], ['allow', 'Always allow'], ['deny', 'Never — stay local']];
// Reasoning levels, weakest first. Not every model accepts every level: one that refuses the
// level asked for is sent the closest it does accept, so no choice here can break a turn.
const EFFORTS = [['', 'Model default'], ['none', 'None'], ['minimal', 'Minimal'], ['low', 'Low'],
  ['medium', 'Medium'], ['high', 'High'], ['xhigh', 'Extra high'], ['max', 'Max']];
const THEME_KEY = 'simon.theme';
const GIZMO_SIZES = [['small', 'Small'], ['medium', 'Medium'], ['large', 'Large']];
const GIZMO_POSITIONS = [['bottom-left', 'Bottom Left'], ['bottom-right', 'Bottom Right'], ['top-left', 'Top Left']];

/** Applies a theme to the interface; `onTheme` lets the viewport recolor its 3D scene. */
export function applyTheme(theme, onTheme) {
  document.documentElement.dataset.theme = theme === 'light' ? 'light' : 'dark';
  onTheme?.(document.documentElement.dataset.theme);
}

/**
 * Reads the saved theme and viewport preferences from the backend (the early-paint copy of
 * the theme in localStorage is only a cache).
 */
export async function loadTheme(onTheme, onViewport) {
  try {
    const s = await api('settings');
    applyTheme(s.theme, onTheme);
    if (s.viewport) onViewport?.(s.viewport);
    try { localStorage.setItem(THEME_KEY, s.theme); } catch { /* ignore */ }
  } catch { /* keep the early-paint theme */ }
}

const sel = (id, opts, value, onchange) => {
  const s = h('select', { id, onchange }, opts.map(([v, l]) => h('option', { value: v }, l)));
  s.value = value; return s;
};
const text = (id, attrs = {}) => h('input', { id, type: 'text', autocomplete: 'off', spellcheck: 'false', ...attrs });
const srow = (id, label, ctl) => h('div', { class: 'srow' }, h('label', { for: id }, label), ctl);

/**
 * One model's fields (provider, model, key, URL, effort and its own token prices), shared by the Primary and Fallback
 * sections. `models` holds one configuration per provider: switching provider swaps the fields to that provider's
 * values, so nothing typed for another provider is lost. Edits are written back to `models` by stash().
 */
function modelFields(slot, providers, models, current, defaultCurrency, onProvider) {
  const id = (f) => `set-${slot}-${f}`;
  const provider = sel(id('provider'), providers, current, () => { fill(); onProvider?.(); });
  const key = h('input', { id: id('key'), type: 'password', autocomplete: 'off', spellcheck: 'false' });
  const reveal = h('button', { type: 'button', class: 'linkbtn', 'aria-label': 'Show API key', onclick: () => {
    key.type = key.type === 'password' ? 'text' : 'password';
    reveal.textContent = key.type === 'password' ? 'Show' : 'Hide';
  } }, 'Show');
  const model = text(id('model'));
  const base = text(id('base'));
  const effort = sel(id('effort'), EFFORTS, '');
  // Ollama drops whatever does not fit in the window silently; a CAD turn needs ~16k tokens before any conversation.
  const ctxLen = text(id('ctx'), { inputmode: 'numeric', placeholder: '32768 tokens — 0 = the Ollama server\u2019s default' });
  // Token prices are optional: left empty, consumption is reported in tokens only.
  const price = (f, label) => text(id(f), { inputmode: 'decimal', placeholder: `Per 1M ${label} tokens — empty = tokens only` });
  const inPrice = price('price-in', 'input');
  const outPrice = price('price-out', 'output');
  const cachedPrice = text(id('price-cached'), { inputmode: 'decimal', placeholder: 'Per 1M cached input tokens — empty = full input price' });
  const currency = text(id('currency'), { maxlength: '8', placeholder: defaultCurrency || 'USD' });
  const rows = {
    key: h('div', { class: 'srow' }, h('label', { for: id('key') }, 'API Key'), h('div', { class: 'row' }, key, reveal)),
    effort: srow(id('effort'), 'Reasoning Effort', effort),
    ctx: srow(id('ctx'), 'Context window', ctxLen),
    // Local models cost nothing per token: no price is asked for, none is invented.
    prices: h('div', { class: 'sgroup' },
      h('h3', null, 'Token Prices (optional)'),
      h('div', { class: 'shint muted' },
        'What this model costs per million tokens. Leave empty to report consumption in tokens only; with prices loaded, '
        + 'each turn and the session also show an estimated cost. Cached input is the reduced rate for prompt the provider '
        + 'served from its cache \u2014 left empty it is charged at the full input price.'),
      srow(id('price-in'), 'Input price', inPrice),
      srow(id('price-cached'), 'Cached input price', cachedPrice),
      srow(id('price-out'), 'Output price', outPrice),
      srow(id('currency'), 'Currency', currency)),
  };
  let shown = null;
  const stash = () => {
    if (!shown) return;
    const p = models[shown];
    p.model = model.value.trim(); p.baseUrl = base.value.trim();
    if (shown !== 'ollama') {
      p.apiKey = key.value;
      p.inputPrice = inPrice.value.trim(); p.outputPrice = outPrice.value.trim();
      p.cachedInputPrice = cachedPrice.value.trim(); p.currency = currency.value.trim();
    }
    if (shown === 'openai') p.reasoningEffort = effort.value;
    if (shown === 'ollama') p.contextLength = ctxLen.value.trim();
  };
  function fill() {
    stash();
    shown = provider.value;
    const p = models[shown] ?? (models[shown] = {});
    model.value = p.model ?? ''; base.value = p.baseUrl ?? '';
    key.value = p.apiKey ?? ''; key.type = 'password'; reveal.textContent = 'Show';
    effort.value = p.reasoningEffort ?? '';
    ctxLen.value = p.contextLength ?? '';
    inPrice.value = p.inputPrice ?? ''; outPrice.value = p.outputPrice ?? '';
    cachedPrice.value = p.cachedInputPrice ?? ''; currency.value = p.currency ?? '';
    rows.key.hidden = shown === 'ollama';
    rows.effort.hidden = shown !== 'openai';
    rows.ctx.hidden = shown !== 'ollama';
    rows.prices.hidden = shown === 'ollama';
  }
  const nodes = [srow(id('provider'), 'Provider', provider), rows.key, srow(id('model'), 'Model', model),
    srow(id('base'), 'Base URL', base), rows.effort, rows.ctx, rows.prices];
  return { nodes, provider, stash, fill };
}

/**
 * Opens the Settings dialog. `workspace` is the open project's state.workspace, `onWorkspace` previews workspace
 * settings in the viewport and `onSaved` runs after a successful save.
 */
export async function openSettings({ toast, onTheme, onViewport, workspace, onWorkspace, onSaved }) {
  let cfg;
  try { cfg = await api('settings'); } catch (e) { toast(e.message, 'bad'); return; }
  const saved = cfg.theme, savedView = { ...cfg.viewport };
  const el = document.getElementById('settings');
  const fb = cfg.fallback ?? { enabled: false, provider: 'openai', providers: {}, consent: 'ask' };
  const primary = modelFields('primary', PROVIDERS, cfg.providers, cfg.provider, cfg.currency, () => hints());
  const fallback = modelFields('fallback', CLOUD, fb.providers, fb.provider, cfg.currency, () => hints());
  const fbEnabled = h('input', { id: 'set-fallback-enabled', type: 'checkbox', onchange: () => hints() });
  fbEnabled.checked = !!fb.enabled;
  const consent = sel('set-fallback-consent', CONSENTS, fb.consent || 'ask');
  const consentRow = srow('set-fallback-consent', 'Remote data', consent);
  // What the fallback means for this configuration, and what is missing for it to work.
  const fbHint = h('div', { class: 'shint muted' });
  const hints = () => {
    const local = primary.provider.value === 'ollama';
    consentRow.hidden = !local;
    const lines = [];
    if (fb.migrated && fb.enabled) lines.push('Set up from your existing ' + (fb.provider === 'claude' ? 'Anthropic' : 'OpenAI')
      + ' configuration, which SimonCAD already used for attachments the primary model cannot read. Review it and save.');
    if (!fbEnabled.checked) lines.push(local
      ? 'Fallback is off: images and PDFs the local model cannot read are refused, never sent elsewhere.'
      : 'Fallback is off: only the primary model is used.');
    else if (local) lines.push('Used only for what the local model cannot do (unsupported attachments, or the local model being '
      + 'unavailable). Those requests, with their attachments and the context they need, are sent to the fallback provider.');
    else lines.push('Used when the primary model cannot serve a request or is unavailable.');
    if (fbEnabled.checked && fb.problem && fallback.provider.value === fb.provider) lines.push('Not usable yet: ' + fb.problem + '.');
    fbHint.textContent = lines.join(' ');
  };
  const theme = sel('set-theme', [['light', 'Light'], ['dark', 'Dark']], cfg.theme, () => applyTheme(theme.value, onTheme));
  // Where reusable projects are looked up, on top of the ones next to the open project. It is
  // never resolved relative to the working directory, so it has to be an absolute path.
  const complib = h('input', { id: 'set-complib', type: 'text', autocomplete: 'off', spellcheck: 'false',
    placeholder: 'Absolute path — empty searches next to the open project',
    value: cfg.componentLibrary ?? '' });
  // Viewport preferences preview live, like the theme, and are reverted on Cancel.
  const view = { ...cfg.viewport };
  const preview = () => onViewport?.({ ...view });
  const check = (id, key) => {
    const c = h('input', { id, type: 'checkbox', onchange: () => { view[key] = c.checked; preview(); } });
    c.checked = !!view[key]; return c;
  };
  const opacityOut = h('output', { for: 'set-axis-opacity', class: 'muted' }, `${view.axisOpacity}%`);
  const opacity = h('input', { id: 'set-axis-opacity', type: 'range', min: '0', max: '100', step: '5', oninput: () => {
    view.axisOpacity = Number(opacity.value); opacityOut.textContent = `${opacity.value}%`; preview();
  } });
  opacity.value = String(view.axisOpacity);
  const gizmoSize = sel('set-gizmo-size', GIZMO_SIZES, view.gizmoSize, () => { view.gizmoSize = gizmoSize.value; preview(); });
  const gizmoPos = sel('set-gizmo-pos', GIZMO_POSITIONS, view.gizmoPosition, () => { view.gizmoPosition = gizmoPos.value; preview(); });
  const status = h('span', { class: 'muted', role: 'status' });
  const ws = workspace ? workspaceSection({ view: workspace, defaults: cfg.workspace, preview: onWorkspace }) : null;

  const close = (revert) => {
    if (revert) { applyTheme(saved, onTheme); onViewport?.(savedView); ws?.revert(); }
    el.hidden = true; el.replaceChildren();
  };
  const form = h('form', { class: 'dialog settings', 'aria-label': 'Settings', onsubmit: async (e) => {
    e.preventDefault();
    primary.stash(); fallback.stash();
    // An invalid workspace value is already reported next to the section; nothing is saved.
    if (ws?.invalid()) { form.querySelector('[aria-invalid=true]')?.focus(); return; }
    status.textContent = ''; status.className = 'muted';
    try {
      // The project's workspace goes through the same tools as the agent's requests.
      const wsDefaults = ws ? await ws.save(async (name, args) => {
        const o = await api('tool', { name, args });
        if (!o.ok) throw new Error(o.error);
      }) : null;
      // The global currency is only the default label now (each model names its own); it is sent back unchanged.
      const out = await api('settings', { provider: primary.provider.value, providers: cfg.providers, theme: theme.value,
        componentLibrary: complib.value.trim(), currency: cfg.currency ?? '', viewport: view, workspace: wsDefaults ?? undefined,
        fallback: { enabled: fbEnabled.checked, provider: fallback.provider.value, providers: fb.providers, consent: consent.value } });
      try { localStorage.setItem(THEME_KEY, out.theme); } catch { /* ignore */ }
      el.hidden = true; el.replaceChildren();
      toast('Settings saved', 'ok');
      onSaved?.();
    } catch (err) { status.textContent = err.message; status.className = 'bad'; }
  } },
    h('div', { class: 'dtitle' }, 'Settings'),
    h('h2', null, 'Primary Model'),
    h('div', { class: 'shint muted' }, 'Serves every request it can, locally or in the cloud.'),
    ...primary.nodes,
    h('h2', null, 'Fallback Model'),
    srow('set-fallback-enabled', 'Enable Fallback', fbEnabled),
    fbHint,
    ...fallback.nodes,
    consentRow,
    h('h2', null, 'UI Configuration'),
    h('div', { class: 'srow' }, h('label', { for: 'set-theme' }, 'Theme'), theme),
    h('div', { class: 'srow' }, h('label', { for: 'set-complib' }, 'Component library'), complib),
    h('h2', null, 'Viewport'),
    srow('set-axes', 'Show Coordinate Axes', check('set-axes', 'showAxes')),
    srow('set-axis-labels', 'Show Axis Labels', check('set-axis-labels', 'showAxisLabels')),
    srow('set-axis-opacity', 'Axis Opacity', h('div', { class: 'row' }, opacity, opacityOut)),
    srow('set-gizmo', 'Show Axis Gizmo', check('set-gizmo', 'showGizmo')),
    srow('set-gizmo-size', 'Axis Gizmo Size', gizmoSize),
    srow('set-gizmo-pos', 'Axis Gizmo Position', gizmoPos),
    ...(ws?.nodes ?? []),
    h('div', { class: 'row' }, h('button', { type: 'submit', class: 'primary' }, 'Save Settings'),
      h('button', { type: 'button', onclick: () => close(true) }, 'Cancel'), status));
  form.addEventListener('keydown', (e) => { if (e.key === 'Escape') close(true); });
  el.replaceChildren(form);
  el.addEventListener('mousedown', (e) => { if (e.target === el) close(true); }, { once: true });
  el.hidden = false;
  primary.fill(); fallback.fill(); hints();
  primary.provider.focus();
}
