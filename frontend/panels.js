// Resizable, collapsible side panels. Sizes and collapsed state persist in localStorage
// (every access is guarded: storage can be blocked or empty). The grid columns come from the
// --left-w / --right-w custom properties on #layout; collapsed = 0.
const KEY = 'simon.layout.v1';
const DEF = { left: { w: 280, collapsed: false }, right: { w: 360, collapsed: false } };
const LIM = { left: [220, 480], right: [300, 560] };
const MIN_CENTER = 360;     // the viewport never gets squeezed below this
const NARROW = 1100;        // below this the design tree starts collapsed

export function getPref(k, fallback) {
  try { const v = localStorage.getItem('simon.' + k); return v === null ? fallback : JSON.parse(v); } catch { return fallback; }
}
export function setPref(k, v) { try { localStorage.setItem('simon.' + k, JSON.stringify(v)); } catch { /* ignore */ } }

function load() {
  try {
    const s = JSON.parse(localStorage.getItem(KEY) ?? 'null');
    if (s && typeof s === 'object') {
      return { left: { ...DEF.left, ...s.left }, right: { ...DEF.right, ...s.right } };
    }
  } catch { /* ignore */ }
  return structuredClone(DEF);
}

const clamp = (v, [lo, hi]) => Math.max(lo, Math.min(hi, v));

export function initPanels({ layout, toggles = {} }) {
  const st = load();
  let narrow = innerWidth < NARROW;
  let stored = null;
  try { stored = localStorage.getItem(KEY); } catch { /* ignore */ }
  if (narrow && !stored) st.left.collapsed = true;
  if (innerWidth < 760 && !stored) st.right.collapsed = true;   // phones: panels are overlays, start with the viewport

  const save = () => { try { localStorage.setItem(KEY, JSON.stringify(st)); } catch { /* ignore */ } };

  function apply() {
    // Keep the viewport usable: shrink the panels (never below their minimum) before it gets small.
    const avail = layout.clientWidth || innerWidth;
    const lw = st.left.collapsed ? 0 : clamp(st.left.w, LIM.left);
    let rw = st.right.collapsed ? 0 : clamp(st.right.w, LIM.right);
    if (avail - lw - rw < MIN_CENTER && !st.right.collapsed) rw = Math.max(LIM.right[0], avail - lw - MIN_CENTER);
    layout.style.setProperty('--left-w', lw + 'px');
    layout.style.setProperty('--right-w', rw + 'px');
    layout.dataset.left = st.left.collapsed ? 'off' : 'on';
    layout.dataset.right = st.right.collapsed ? 'off' : 'on';
    for (const side of ['left', 'right']) {
      const b = toggles[side];
      if (b) { b.setAttribute('aria-pressed', String(!st[side].collapsed)); }
    }
  }

  function toggle(side) { st[side].collapsed = !st[side].collapsed; apply(); save(); }
  function show(side) { if (st[side].collapsed) { st[side].collapsed = false; apply(); save(); } }

  function splitter(el, side) {
    const set = (w) => { st[side].w = Math.round(clamp(w, LIM[side])); st[side].collapsed = false; apply(); };
    el.addEventListener('pointerdown', (e) => {
      e.preventDefault();
      el.setPointerCapture(e.pointerId);
      el.classList.add('drag'); layout.classList.add('drag');
      const box = layout.getBoundingClientRect();
      const move = (ev) => set(side === 'left' ? ev.clientX - box.left : box.right - ev.clientX);
      const up = () => { el.classList.remove('drag'); layout.classList.remove('drag'); el.removeEventListener('pointermove', move); el.removeEventListener('pointerup', up); el.removeEventListener('pointercancel', up); save(); };
      el.addEventListener('pointermove', move); el.addEventListener('pointerup', up); el.addEventListener('pointercancel', up);
    });
    el.addEventListener('dblclick', () => { st[side] = { ...DEF[side] }; apply(); save(); });
    el.addEventListener('keydown', (e) => {
      const dir = e.key === 'ArrowLeft' ? -1 : e.key === 'ArrowRight' ? 1 : 0;
      if (!dir) return;
      e.preventDefault();
      set(st[side].w + (side === 'left' ? dir : -dir) * 16);
      save();
    });
  }

  splitter(layout.querySelector('#split-left'), 'left');
  splitter(layout.querySelector('#split-right'), 'right');
  for (const side of ['left', 'right']) toggles[side]?.addEventListener('click', () => toggle(side));
  addEventListener('resize', () => {
    const n = innerWidth < NARROW;
    if (n !== narrow) { narrow = n; st.left.collapsed = n; save(); }   // tree collapses first on narrow screens
    apply();
  });
  apply();
  return { toggle, show };
}
