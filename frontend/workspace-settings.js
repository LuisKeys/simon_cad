// Settings → Workspace / Units & Grid: the open project's display units, grid and snapping.
// The form edits a canonical copy (mm, degrees) and shows lengths in the unit chosen in the
// form, with the factors of the server's UnitService table (state.workspace.linear_units).
// Saving calls the same tools the agent uses (set_units, configure_grid, configure_snapping),
// so the validation, state and persistence are the tools'; "Use as default for new projects"
// also stores the values as the global defaults (Settings › workspace).
import { h } from './api.js';

const LINEAR = [['mm', 'Millimeters (mm)'], ['cm', 'Centimeters (cm)'], ['m', 'Meters (m)'], ['in', 'Inches (in)'], ['ft', 'Feet (ft)']];
const ANGULAR = [['deg', 'Degrees (°)'], ['rad', 'Radians (rad)']];
const PLANES = [['XY', 'XY'], ['XZ', 'XZ'], ['YZ', 'YZ']];

const clone = (v) => JSON.parse(JSON.stringify(v));
const same = (a, b) => JSON.stringify(a) === JSON.stringify(b);
/** A value for a field: enough decimals to round-trip what is stored, no trailing zeros. */
const show = (v) => (Number.isFinite(v) ? String(Number(v.toFixed(6))) : '');

/**
 * Builds the section. `view` is state.workspace (the project's settings), `defaults` the
 * global defaults (Settings › workspace) and `preview` shows unsaved settings in the viewport.
 * Returns { nodes, invalid(), save(callTool) → global defaults to store or null, revert() }.
 */
export function workspaceSection({ view, defaults, preview }) {
  const original = clone({ units: view.units, grid: view.grid, snap: view.snap });
  const table = view.linear_units ?? { mm: 1 };
  let w = clone(original);
  const errors = new Map();
  const status = h('div', { class: 'shint', role: 'status' });

  const id = (k) => `set-ws-${k}`;
  const srow = (k, label, ctl) => h('div', { class: 'srow' }, h('label', { for: id(k) }, label), ctl);
  const select = (k, opts, value, onchange) => {
    const s = h('select', { id: id(k), onchange: () => onchange(s.value) }, opts.map(([v, l]) => h('option', { value: v }, l)));
    s.value = value; return s;
  };
  const check = (k, get, set) => {
    const c = h('input', { id: id(k), type: 'checkbox', onchange: () => { set(c.checked); changed(); } });
    c.checked = !!get(); return c;
  };
  const k = () => table[w.units.linear] ?? 1;          // mm per form unit
  const rad = () => w.units.angular === 'rad';
  const unitTags = [];
  const unitTag = (angle) => { const s = h('span', { class: 'muted', dataset: { angle: angle ? '1' : '' } }); unitTags.push(s); return s; };

  /**
   * A numeric field holding a canonical value. `toShow`/`fromShow` convert for display;
   * `check` returns an error message or ''. An invalid entry is refused in place (the stored
   * value and the preview keep the last valid one).
   */
  const fields = [];
  const num = (key, label, { get, set, toShow = (v) => v, fromShow = (v) => v, check: chk = () => '', unit = null, int = false, optional = false, placeholder }) => {
    const input = h('input', { id: id(key), type: 'text', inputmode: 'decimal', autocomplete: 'off', spellcheck: 'false', placeholder });
    const refill = () => { const v = get(); input.value = v == null ? '' : show(toShow(v)); input.removeAttribute('aria-invalid'); errors.delete(key); };
    input.addEventListener('input', () => {
      const t = input.value.trim().replace(',', '.');
      let msg = '';
      if (t === '' && optional) { set(null); } else {
        const n = Number(t);
        if (t === '' || !Number.isFinite(n)) msg = `${label}: type a number`;
        else if (int && !Number.isInteger(n)) msg = `${label}: type a whole number`;
        else { msg = chk(n); if (!msg) set(fromShow(n)); }
      }
      if (msg) { errors.set(key, msg); input.setAttribute('aria-invalid', 'true'); } else { errors.delete(key); input.removeAttribute('aria-invalid'); }
      changed();
    });
    fields.push(refill);
    refill();
    return srow(key, label, unit ? h('div', { class: 'row' }, input, unit) : input);
  };
  const positive = (label) => (n) => (n > 0 ? '' : `${label} must be greater than 0`);

  const linear = select('linear', LINEAR, w.units.linear, (v) => { w.units.linear = v; refillAll(); changed(); });
  const angular = select('angular', ANGULAR, w.units.angular, (v) => { w.units.angular = v; refillAll(); changed(); });
  const plane = select('plane', PLANES, w.grid.plane, (v) => { w.grid.plane = v; changed(); });
  const showGrid = check('grid-visible', () => w.grid.visible, (v) => { w.grid.visible = v; if (v) w.grid.enabled = true; });
  const snapChecks = [
    ['snap-enabled', 'Enable snapping', 'enabled'], ['snap-grid', 'Snap to grid', 'grid'],
    ['snap-translation', 'Snap translation', 'translation'], ['snap-rotation', 'Snap rotation', 'rotation'],
  ].map(([key, label, f]) => [f, srow(key, label, check(key, () => w.snap[f], (v) => { w.snap[f] = v; }))]);

  const prec = (key, label, f) => num(key, label, { int: true, get: () => w.units[f], set: (v) => { w.units[f] = v; },
    check: (n) => (n >= 0 && n <= 8 ? '' : `${label} must be from 0 to 8`) });
  const len = (key, label, get, set, opts = {}) => num(key, label, { get, set, unit: unitTag(false),
    toShow: (v) => v / k(), fromShow: (v) => v * k(), check: opts.signed ? () => '' : positive(label), ...opts });
  const nodes = [
    h('h2', null, 'Workspace / Units & Grid'),
    h('div', { class: 'shint muted' }, 'Settings of the open project. Geometry is always stored in millimetres: '
      + 'changing units only changes how values are shown and typed.'),
    h('div', { class: 'sgroup' },
      h('h3', null, 'Units'),
      srow('linear', 'Linear unit', linear),
      srow('angular', 'Angular unit', angular),
      prec('linear-precision', 'Linear precision', 'linear_precision'),
      prec('angular-precision', 'Angular precision', 'angular_precision')),
    h('div', { class: 'sgroup' },
      h('h3', null, 'Grid'),
      srow('grid-visible', 'Show grid', showGrid),
      srow('plane', 'Grid plane', plane),
      len('spacing', 'Grid spacing', () => w.grid.spacing, (v) => { w.grid.spacing = v; }),
      num('major', 'Major line interval', { int: true, get: () => w.grid.major_interval, set: (v) => { w.grid.major_interval = v; },
        unit: h('span', { class: 'muted' }, 'cells'), check: (n) => (n >= 1 && n <= 1000 ? '' : 'Major line interval must be from 1 to 1000 cells') }),
      len('extent', 'Grid extent', () => w.grid.extent, (v) => { w.grid.extent = v; }),
      ...[0, 1, 2].map((i) => len(`origin-${'xyz'[i]}`, `Grid origin ${'XYZ'[i]}`, () => w.grid.origin[i], (v) => { w.grid.origin[i] = v; }, { signed: true }))),
    h('div', { class: 'sgroup' },
      h('h3', null, 'Snapping'),
      ...snapChecks.map(([, row]) => row),
      num('rotation-increment', 'Rotation increment', { get: () => w.snap.rotation_increment, set: (v) => { w.snap.rotation_increment = v; },
        unit: unitTag(true), toShow: (v) => (rad() ? (v * Math.PI) / 180 : v), fromShow: (v) => (rad() ? (v * 180) / Math.PI : v),
        check: (n) => { const deg = rad() ? (n * 180) / Math.PI : n; return deg > 0 && deg <= 360 ? '' : 'Rotation increment must be greater than 0 and at most a full turn'; } }),
      len('translation-increment', 'Translation increment', () => w.snap.translation_increment, (v) => { w.snap.translation_increment = v; },
        { optional: true, placeholder: 'Empty = grid spacing' })),
  ];
  const asDefault = h('input', { id: id('as-default'), type: 'checkbox' });
  nodes.push(h('div', { class: 'row' },
    h('button', { type: 'button', onclick: () => load(defaults), title: 'Load your defaults for new projects into the form' }, 'Restore Defaults')),
  srow('as-default', 'Use as default for new projects', asDefault), status);

  function refillAll() {
    for (const f of fields) f();
    for (const t of unitTags) t.textContent = t.dataset.angle ? (rad() ? 'rad' : '°') : w.units.linear;
  }
  function load(s) {
    if (!s) return;
    w = clone({ units: s.units, grid: s.grid, snap: s.snap });
    linear.value = w.units.linear; angular.value = w.units.angular; plane.value = w.grid.plane;
    showGrid.checked = !!w.grid.visible;
    for (const [f, row] of snapChecks) row.querySelector('input').checked = !!w.snap[f];
    refillAll(); changed();
  }
  function changed() {
    status.textContent = [...errors.values()].join(' · ');
    status.className = errors.size ? 'shint bad' : 'shint';
    if (!errors.size) preview?.({ ...view, ...clone(w) });
  }
  refillAll();

  return {
    nodes,
    invalid: () => (errors.size ? [...errors.values()][0] : ''),
    revert: () => preview?.(view),
    /** Applies the changed sections through the tools; returns the global defaults to store, or null. */
    async save(callTool) {
      if (!same(w.units, original.units)) await callTool('set_units', { ...w.units });
      if (!same(w.grid, original.grid)) {
        const g = w.grid;
        await callTool('configure_grid', { visible: g.visible, enabled: g.enabled, plane: g.plane, spacing: g.spacing,
          major_interval: g.major_interval, extent: g.extent, origin: g.origin, unit: 'mm' });
      }
      if (!same(w.snap, original.snap)) {
        const s = w.snap;
        const inc = s.translation_increment == null ? { translation_increment_from_grid: true } : { translation_increment: s.translation_increment, unit: 'mm' };
        await callTool('configure_snapping', { enabled: s.enabled, grid: s.grid, translation: s.translation, rotation: s.rotation,
          rotation_increment: s.rotation_increment, angle_unit: 'deg', ...inc });
      }
      return asDefault.checked ? { schema_version: 1, ...clone(w) } : null;
    },
  };
}
