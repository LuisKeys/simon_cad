// The context menu shared by the viewport, the design tree, the top bar and the action bar.
import { h } from './api.js';
import { icon } from './icons.js';
import { $ } from './dom.js';

// A small menu shared by the viewport, the design tree, the top bar and the action bar.
// `at` is a point {x, y} or an anchor {el}. Items: {label, icon, key, run, danger, disabled,
// checked (radio), data} plus {sep: true} and {head: 'Title'}.
export function showMenu(at, items) {
  const m = $('ctxmenu');
  const opener = document.activeElement;
  const close = (restore = false) => {
    m.hidden = true; document.removeEventListener('pointerdown', away, true);
    if (restore && opener?.focus) opener.focus();
  };
  const away = (e) => { if (!m.contains(e.target)) close(); };
  const focusable = () => [...m.querySelectorAll('li[tabindex]')];
  m.replaceChildren(...items.filter(Boolean).map((it) => {
    if (it.sep) return h('li', { class: 'sep', role: 'separator' });
    if (it.head) return h('li', { class: 'head', role: 'presentation' }, it.head);
    const radio = it.checked !== undefined;
    return h('li', { role: radio ? 'menuitemradio' : 'menuitem', tabindex: '-1', class: it.danger ? 'danger' : '', 'aria-disabled': String(!!it.disabled),
      'aria-checked': radio ? String(!!it.checked) : null, dataset: it.data,
      onclick: () => { if (it.disabled) return; close(); it.run?.(); },
      onkeydown: (e) => {
        if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); e.currentTarget.click(); }
        else if (e.key === 'Escape') { e.preventDefault(); close(true); }
        else if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
          e.preventDefault();
          const f = focusable(); const i = f.indexOf(e.currentTarget);
          f[(i + (e.key === 'ArrowDown' ? 1 : -1) + f.length) % f.length]?.focus();
        }
      } },
    it.icon ? icon(it.icon) : radio ? (it.checked ? icon('check') : h('span', { class: 'ic' })) : null,
    h('span', { class: 'lbl' }, it.label), it.key ? h('kbd', {}, it.key) : null);
  }));
  m.hidden = false;
  const w = m.offsetWidth, ht = m.offsetHeight;
  let x, y;
  if (at.el) {
    const r = at.el.getBoundingClientRect();
    x = r.left; y = r.bottom + 4;
    if (y + ht > innerHeight - 8) y = Math.max(8, r.top - ht - 4);
  } else { x = at.x; y = at.y; }
  m.style.left = Math.max(8, Math.min(x, innerWidth - w - 8)) + 'px';
  m.style.top = Math.max(8, Math.min(y, innerHeight - ht - 8)) + 'px';
  document.addEventListener('pointerdown', away, true);
  focusable()[0]?.focus();
}
