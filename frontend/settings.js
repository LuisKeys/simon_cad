// Settings screen: AI provider configuration, UI theme and viewport display preferences. Values come from, and are saved
// to, the backend configuration layer (internal/config); this module holds no other state.
import { api, h } from './api.js';

const PROVIDERS = [['openai', 'OpenAI'], ['claude', 'Anthropic'], ['ollama', 'Ollama']];
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

/** Opens the Settings dialog. */
export async function openSettings({ toast, onTheme, onViewport }) {
  let cfg;
  try { cfg = await api('settings'); } catch (e) { toast(e.message, 'bad'); return; }
  const saved = cfg.theme, savedView = { ...cfg.viewport };
  const el = document.getElementById('settings');
  const sel = (id, opts, value, onchange) => {
    const s = h('select', { id, onchange }, opts.map(([v, l]) => h('option', { value: v }, l)));
    s.value = value; return s;
  };
  const provider = sel('set-provider', PROVIDERS, cfg.provider, () => fill());
  const key = h('input', { id: 'set-key', type: 'password', autocomplete: 'off', spellcheck: 'false' });
  const reveal = h('button', { type: 'button', class: 'linkbtn', 'aria-label': 'Show API key', onclick: () => {
    key.type = key.type === 'password' ? 'text' : 'password';
    reveal.textContent = key.type === 'password' ? 'Show' : 'Hide';
  } }, 'Show');
  const model = h('input', { id: 'set-model', type: 'text', autocomplete: 'off', spellcheck: 'false' });
  const base = h('input', { id: 'set-base', type: 'text', autocomplete: 'off', spellcheck: 'false' });
  const effort = sel('set-effort', EFFORTS, '');
  // Token prices are optional: left empty, consumption is reported in tokens only.
  const price = (id, label) => h('input', { id, type: 'text', inputmode: 'decimal', autocomplete: 'off',
    spellcheck: 'false', placeholder: `Per 1M ${label} tokens — empty = tokens only` });
  const inPrice = price('set-price-in', 'input');
  const outPrice = price('set-price-out', 'output');
  const cachedPrice = h('input', { id: 'set-price-cached', type: 'text', inputmode: 'decimal', autocomplete: 'off',
    spellcheck: 'false', placeholder: 'Per 1M cached input tokens — empty = full input price' });
  const currency = h('input', { id: 'set-currency', type: 'text', autocomplete: 'off', spellcheck: 'false',
    maxlength: '8', placeholder: 'USD', value: cfg.currency ?? '' });
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
  const srow = (id, text, ctl) => h('div', { class: 'srow' }, h('label', { for: id }, text), ctl);
  const status = h('span', { class: 'muted', role: 'status' });
  const rows = {
    key: h('div', { class: 'srow' }, h('label', { for: 'set-key' }, 'API Key'), h('div', { class: 'row' }, key, reveal)),
    effort: h('div', { class: 'srow' }, h('label', { for: 'set-effort' }, 'Reasoning Effort'), effort),
  };

  // Each provider keeps its own key/model/URL: edits are held in cfg until Save, and switching
  // provider swaps the fields to that provider's values.
  let shown = null;
  const stash = () => {
    if (!shown) return;
    const p = cfg.providers[shown];
    p.model = model.value.trim(); p.baseUrl = base.value.trim();
    if (shown !== 'ollama') p.apiKey = key.value;
    if (shown === 'openai') p.reasoningEffort = effort.value;
    p.inputPrice = inPrice.value.trim(); p.outputPrice = outPrice.value.trim();
    p.cachedInputPrice = cachedPrice.value.trim();
  };
  function fill() {
    stash();
    shown = provider.value;
    const p = cfg.providers[shown];
    model.value = p.model ?? ''; base.value = p.baseUrl ?? '';
    key.value = p.apiKey ?? ''; key.type = 'password'; reveal.textContent = 'Show';
    effort.value = p.reasoningEffort ?? '';
    inPrice.value = p.inputPrice ?? ''; outPrice.value = p.outputPrice ?? '';
    cachedPrice.value = p.cachedInputPrice ?? '';
    rows.key.hidden = shown === 'ollama';
    rows.effort.hidden = shown !== 'openai';
    status.textContent = '';
  }

  const close = (revert) => {
    if (revert) { applyTheme(saved, onTheme); onViewport?.(savedView); }
    el.hidden = true; el.replaceChildren();
  };
  const form = h('form', { class: 'dialog settings', 'aria-label': 'Settings', onsubmit: async (e) => {
    e.preventDefault();
    stash();
    try {
      const out = await api('settings', { provider: provider.value, providers: cfg.providers, theme: theme.value,
        componentLibrary: complib.value.trim(), currency: currency.value.trim(), viewport: view });
      try { localStorage.setItem(THEME_KEY, out.theme); } catch { /* ignore */ }
      el.hidden = true; el.replaceChildren();
      toast('Settings saved', 'ok');
    } catch (err) { status.textContent = err.message; status.className = 'bad'; }
  } },
    h('div', { class: 'dtitle' }, 'Settings'),
    h('h2', null, 'AI Configuration'),
    h('div', { class: 'srow' }, h('label', { for: 'set-provider' }, 'Provider'), provider),
    rows.key,
    h('div', { class: 'srow' }, h('label', { for: 'set-model' }, 'Model'), model),
    h('div', { class: 'srow' }, h('label', { for: 'set-base' }, 'Base URL'), base),
    rows.effort,
    h('h2', null, 'Token Prices (optional)'),
    h('div', { class: 'shint muted' },
      'What this provider\u2019s model costs per million tokens. Leave empty to report consumption in tokens only; '
      + 'with prices loaded, each turn and the session also show an estimated cost. Cached input is the reduced rate '
      + 'for prompt the provider served from its cache \u2014 left empty it is charged at the full input price.'),
    h('div', { class: 'srow' }, h('label', { for: 'set-price-in' }, 'Input price'), inPrice),
    h('div', { class: 'srow' }, h('label', { for: 'set-price-cached' }, 'Cached input price'), cachedPrice),
    h('div', { class: 'srow' }, h('label', { for: 'set-price-out' }, 'Output price'), outPrice),
    h('div', { class: 'srow' }, h('label', { for: 'set-currency' }, 'Currency'), currency),
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
    h('div', { class: 'row' }, h('button', { type: 'submit', class: 'primary' }, 'Save Settings'),
      h('button', { type: 'button', onclick: () => close(true) }, 'Cancel'), status));
  form.addEventListener('keydown', (e) => { if (e.key === 'Escape') close(true); });
  el.replaceChildren(form);
  el.addEventListener('mousedown', (e) => { if (e.target === el) close(true); }, { once: true });
  el.hidden = false;
  fill();
  provider.focus();
}
