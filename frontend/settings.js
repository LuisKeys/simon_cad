// Settings screen: AI provider configuration and UI theme. Values come from, and are saved
// to, the backend configuration layer (internal/config); this module holds no other state.
import { api, h } from './api.js';

const PROVIDERS = [['openai', 'OpenAI'], ['claude', 'Anthropic'], ['ollama', 'Ollama']];
const EFFORTS = [['', 'Model default'], ['none', 'None'], ['low', 'Low'], ['medium', 'Medium'], ['high', 'High']];
const THEME_KEY = 'simon.theme';

/** Applies a theme to the interface; `onTheme` lets the viewport recolor its 3D scene. */
export function applyTheme(theme, onTheme) {
  document.documentElement.dataset.theme = theme === 'light' ? 'light' : 'dark';
  onTheme?.(document.documentElement.dataset.theme);
}

/** Reads the saved theme from the backend (the early-paint copy in localStorage is only a cache). */
export async function loadTheme(onTheme) {
  try {
    const s = await api('settings');
    applyTheme(s.theme, onTheme);
    try { localStorage.setItem(THEME_KEY, s.theme); } catch { /* ignore */ }
  } catch { /* keep the early-paint theme */ }
}

/** Opens the Settings dialog. */
export async function openSettings({ toast, onTheme }) {
  let cfg;
  try { cfg = await api('settings'); } catch (e) { toast(e.message, 'bad'); return; }
  const saved = cfg.theme;
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
  const theme = sel('set-theme', [['light', 'Light'], ['dark', 'Dark']], cfg.theme, () => applyTheme(theme.value, onTheme));
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
  };
  function fill() {
    stash();
    shown = provider.value;
    const p = cfg.providers[shown];
    model.value = p.model ?? ''; base.value = p.baseUrl ?? '';
    key.value = p.apiKey ?? ''; key.type = 'password'; reveal.textContent = 'Show';
    effort.value = p.reasoningEffort ?? '';
    rows.key.hidden = shown === 'ollama';
    rows.effort.hidden = shown !== 'openai';
    status.textContent = '';
  }

  const close = (revert) => {
    if (revert) applyTheme(saved, onTheme);
    el.hidden = true; el.replaceChildren();
  };
  const form = h('form', { class: 'dialog settings', 'aria-label': 'Settings', onsubmit: async (e) => {
    e.preventDefault();
    stash();
    try {
      const out = await api('settings', { provider: provider.value, providers: cfg.providers, theme: theme.value });
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
    h('h2', null, 'UI Configuration'),
    h('div', { class: 'srow' }, h('label', { for: 'set-theme' }, 'Theme'), theme),
    h('div', { class: 'row' }, h('button', { type: 'submit', class: 'primary' }, 'Save Settings'),
      h('button', { type: 'button', onclick: () => close(true) }, 'Cancel'), status));
  form.addEventListener('keydown', (e) => { if (e.key === 'Escape') close(true); });
  el.replaceChildren(form);
  el.addEventListener('mousedown', (e) => { if (e.target === el) close(true); }, { once: true });
  el.hidden = false;
  fill();
  provider.focus();
}
