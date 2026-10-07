// Contextual actions: what the viewport action bar offers for the current selection. This is
// data, not UI: each action declares the selection contexts it is valid in, and the bar is a
// pure function of the selection. Adding a CAD tool to the bar = adding one entry here.
//
//   none | body | face | edge | vertex | multiBody | mixed
//
// Actions are only offered when a backing tool exists.

export const GROUPS = ['mode', 'create', 'view', 'transform', 'geometry', 'face', 'boolean', 'organize', 'appearance', 'visibility', 'measure', 'destructive'];

const kindOf = (sel) => /^(\w+)\(/.exec(sel)?.[1] ?? '';
const featureOf = (sel) => { const m = /feature=([^,)]+)/.exec(sel); return m ? m[1].trim() : ''; };

/** Summarise a selector list: { ctx, kinds:Set, features:[ids], count, grouped }. `groupOf`
 *  maps a feature id to its design-tree group ('' when none). */
export function selectionContext(selectors = [], groupOf = () => '') {
  const kinds = new Set(selectors.map(kindOf).filter(Boolean));
  const features = [...new Set(selectors.map(featureOf).filter(Boolean))];
  let ctx = 'none';
  if (selectors.length) {
    if (kinds.size > 1) ctx = 'mixed';
    else {
      const k = [...kinds][0];
      if (k === 'body') ctx = features.length > 1 ? 'multiBody' : 'body';
      else if (k === 'face' || k === 'edge' || k === 'vertex') ctx = k;
      else ctx = 'mixed';
    }
  }
  const grouped = features.some((f) => !!groupOf(f));
  return { ctx, kinds, features, count: selectors.length, selectors, grouped };
}

const ALL = ['none', 'body', 'face', 'edge', 'vertex', 'multiBody', 'mixed'];
const SOME = ['body', 'face', 'edge', 'vertex', 'multiBody', 'mixed'];

// `run` receives the app's `env` (see app.js `actionEnv`); `menu` actions open a submenu.
export const ACTIONS = [
  // -- no selection ------------------------------------------------------------------------
  { id: 'select-mode', label: 'Select', icon: 'select', group: 'mode', contexts: ALL, primary: true, kind: 'pick-mode' },
  { id: 'create', label: 'Create', icon: 'create', group: 'create', contexts: ['none'], primary: true, kind: 'menu',
    items: [['create_box', 'Box', 'box'], ['create_cylinder', 'Cylinder', 'cylinder'], ['create_cone', 'Cone', 'cone'],
      ['create_sphere', 'Sphere', 'sphere'], ['create_torus', 'Torus', 'torus'], ['create_sketch', 'Sketch', 'sketch']] },
  { id: 'measure', label: 'Measure', icon: 'measure', group: 'measure', contexts: ['none', 'face', 'edge', 'vertex', 'mixed'], primary: true, kind: 'toggle', run: (e) => e.toggleMeasure() },
  { id: 'section', label: 'Section', icon: 'section', group: 'view', contexts: ['none'], kind: 'section' },
  { id: 'view', label: 'View', icon: 'view', group: 'view', contexts: ['none'], kind: 'view' },
  { id: 'fit', label: 'Fit', icon: 'fit', group: 'view', contexts: ['none'], primary: true, run: (e) => e.fit() },

  // -- bodies ------------------------------------------------------------------------------
  { id: 'move', label: 'Move', icon: 'move', group: 'transform', contexts: ['body', 'multiBody', 'vertex'], primary: true, run: (e) => e.transform('move') },
  { id: 'rotate', label: 'Rotate', icon: 'rotate', group: 'transform', contexts: ['body', 'multiBody'], primary: true, run: (e) => e.transform('rotate') },
  { id: 'scale', label: 'Scale', icon: 'scale', group: 'transform', contexts: ['body', 'multiBody'], run: (e) => e.scale() },
  { id: 'duplicate', label: 'Duplicate', icon: 'duplicate', group: 'transform', contexts: ['body', 'multiBody'], key: 'Ctrl+D', run: (e) => e.duplicate() },
  { id: 'align', label: 'Align', icon: 'align', group: 'transform', contexts: ['body', 'multiBody'], run: (e) => e.align() },
  { id: 'mirror', label: 'Mirror', icon: 'mirror', group: 'geometry', contexts: ['body'], run: (e) => e.toolDialog('mirror') },
  { id: 'pattern', label: 'Pattern', icon: 'pattern', group: 'geometry', contexts: ['body'], kind: 'menu',
    items: [['linear_pattern', 'Linear pattern', 'pattern'], ['circular_pattern', 'Circular pattern', 'rotate']] },
  { id: 'round-all', label: 'Fillet', icon: 'fillet', group: 'geometry', contexts: ['body'], run: (e) => e.edgeOp('fillet') },
  { id: 'chamfer-all', label: 'Chamfer', icon: 'chamfer', group: 'geometry', contexts: ['body'], run: (e) => e.edgeOp('chamfer') },
  { id: 'union', label: 'Union', icon: 'union', group: 'boolean', contexts: ['multiBody'], visible: (s) => s.features.length === 2, primary: true, run: (e) => e.boolean('union') },
  { id: 'subtract', label: 'Subtract', icon: 'subtract', group: 'boolean', contexts: ['multiBody'], visible: (s) => s.features.length === 2, primary: true, run: (e) => e.boolean('subtract') },
  { id: 'intersect', label: 'Intersect', icon: 'intersect', group: 'boolean', contexts: ['multiBody'], visible: (s) => s.features.length === 2, run: (e) => e.boolean('intersect') },
  { id: 'group', label: 'Group', icon: 'group', group: 'organize', contexts: ['body', 'multiBody'], key: 'Ctrl+G', run: (e) => e.group() },
  { id: 'ungroup', label: 'Ungroup', icon: 'ungroup', group: 'organize', contexts: ['body', 'multiBody'], visible: (s) => !!s.grouped, key: 'Ctrl+Shift+G', run: (e) => e.ungroup() },

  // -- faces / edges -----------------------------------------------------------------------
  { id: 'hole', label: 'Hole', icon: 'hole', group: 'geometry', contexts: ['face'], visible: (s) => s.count === 1, primary: true, run: (e) => e.toolDialog('add_hole') },
  { id: 'face-extrude', label: 'Extrude', icon: 'extrude', group: 'face', contexts: ['face'], primary: true, title: 'Push/pull the face along its normal', run: (e) => e.faceOp('face_extrude') },
  { id: 'face-inset', label: 'Inset', icon: 'inset', group: 'face', contexts: ['face'], title: 'Shrink the face outline (optionally as a pocket or pad)', run: (e) => e.faceOp('face_inset') },
  { id: 'face-offset', label: 'Offset', icon: 'offset', group: 'face', contexts: ['face'], title: 'Move the face; adjacent faces follow (holes change size)', run: (e) => e.faceOp('face_offset') },
  { id: 'fillet', label: 'Fillet', icon: 'fillet', group: 'geometry', contexts: ['edge'], primary: true, run: (e) => e.edgeOp('fillet') },
  { id: 'chamfer', label: 'Chamfer', icon: 'chamfer', group: 'geometry', contexts: ['edge'], primary: true, run: (e) => e.edgeOp('chamfer') },

  // -- appearance / visibility (apply to the owning bodies) --------------------------------
  { id: 'material', label: 'Material', icon: 'material', group: 'appearance', contexts: SOME, primary: true, run: (e, a, ev) => e.materialMenu(ev) },
  { id: 'color', label: 'Color', icon: 'color', group: 'appearance', contexts: SOME, run: (e) => e.pickColor() },
  { id: 'hide', label: 'Hide', icon: 'eye-off', group: 'visibility', contexts: ['body', 'multiBody', 'mixed'], run: (e) => e.hide() },

  // -- destructive: always last, separated from everything else -----------------------------
  { id: 'delete', label: 'Delete', icon: 'trash', group: 'destructive', contexts: SOME, destructive: true, primary: true,
    labelFor: (s) => (s.ctx === 'face' || s.ctx === 'edge' || s.ctx === 'vertex' ? 'Delete feature' : 'Delete'), run: (e) => e.remove() },
];

/** Actions valid for a selection summary, ordered by GROUPS (stable within a group). */
export function actionsFor(sel) {
  const rank = (a) => GROUPS.indexOf(a.group);
  return ACTIONS.filter((a) => a.contexts.includes(sel.ctx) && (!a.visible || a.visible(sel)))
    .map((a, i) => [a, i]).sort((x, y) => rank(x[0]) - rank(y[0]) || x[1] - y[1]).map(([a]) => a);
}

/** Split a list of actions into runs of the same group, for spacing. */
export function groupRuns(actions) {
  const runs = [];
  for (const a of actions) {
    const last = runs[runs.length - 1];
    if (last && last.group === a.group) last.items.push(a); else runs.push({ group: a.group, items: [a] });
  }
  return runs;
}
