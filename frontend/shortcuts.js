// Shortcuts panel: a popup over the main screen listing every keyboard and mouse shortcut,
// most used first. Opened by the header button, the More menu or "?"; closed by the same
// key, Esc, the ✕ or a click outside.
import { h } from './api.js';
import { SECTIONS, keyLabel } from './shortcuts-model.js';

const MAC = /Mac|iPhone|iPad/.test(navigator.platform);

function combo(keys) {
  const out = [];
  keys.forEach((k, i) => { if (i) out.push(h('span', { class: 'plus' }, '+')); out.push(h('kbd', {}, keyLabel(k, MAC))); });
  return out;
}

function row(r) {
  const alts = [];
  r.keys.forEach((k, i) => { if (i) alts.push(h('span', { class: 'or' }, '/')); alts.push(...combo(k)); });
  return h('div', { class: 'sc-row', role: 'row' },
    h('span', { class: 'sc-keys', role: 'cell' }, alts),
    h('span', { class: 'sc-label', role: 'cell' }, r.label));
}

/** Builds the panel inside `root` (an .overlay). Returns { open, close, toggle, isOpen }. */
export function initShortcuts(root) {
  let back = null; // element focused before opening, refocused on close
  const close = () => {
    if (root.hidden) return;
    root.hidden = true;
    back?.focus?.();
    back = null;
  };
  const closeBtn = h('button', { type: 'button', class: 'linkbtn', 'aria-label': 'Close', title: 'Close (Esc)', onclick: close }, '✕');
  root.append(h('div', { class: 'dialog shortcuts', role: 'dialog', 'aria-modal': 'true', 'aria-labelledby': 'sc-title' },
    h('div', { class: 'sc-head' },
      h('b', { id: 'sc-title' }, 'Keyboard shortcuts'),
      h('span', { class: 'muted small' }, 'Press ', h('kbd', {}, '?'), ' anytime'),
      h('span', { class: 'spacer' }),
      h('a', { href: 'help.html#shortcuts', target: '_blank', rel: 'noopener', class: 'small' }, 'Full help'),
      closeBtn),
    h('div', { class: 'sc-body' },
      SECTIONS.map((s) => h('section', { class: 'sc-sec' },
        h('h3', {}, s.title),
        h('div', { role: 'table', 'aria-label': s.title }, s.rows.map(row)))))));
  root.addEventListener('mousedown', (e) => { if (e.target === root) close(); });

  const open = () => {
    if (!root.hidden) return;
    back = document.activeElement;
    root.hidden = false;
    closeBtn.focus();
  };
  return { open, close, toggle: () => (root.hidden ? open() : close()), isOpen: () => !root.hidden };
}
