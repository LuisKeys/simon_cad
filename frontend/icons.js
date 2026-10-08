// One consistent outline icon set (24px grid, 1.75 stroke, Lucide-style geometry). Icons are
// built with svg() — no innerHTML — and inherit the text color.
import { svg } from './api.js';

const P = (d) => ['path', { d }];
const C = (cx, cy, r, extra) => ['circle', { cx, cy, r, ...extra }];
const E = (cx, cy, rx, ry) => ['ellipse', { cx, cy, rx, ry }];
const R = (x, y, width, height, rx = 2) => ['rect', { x, y, width, height, rx }];
const L = (x1, y1, x2, y2) => ['line', { x1, y1, x2, y2 }];

const ICONS = {
  undo: [P('M9 14 4 9l5-5'), P('M4 9h10.5a5.5 5.5 0 0 1 5.5 5.5a5.5 5.5 0 0 1-5.5 5.5H11')],
  redo: [P('m15 14 5-5-5-5'), P('M20 9H9.5A5.5 5.5 0 0 0 4 14.5A5.5 5.5 0 0 0 9.5 20H13')],
  save: [P('M19 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h11l5 5v11a2 2 0 0 1-2 2z'), P('M17 21v-8H7v8'), P('M7 3v5h8')],
  export: [P('M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4'), P('m7 10 5 5 5-5'), P('M12 15V3')],
  settings: [C(12, 12, 3), P('M19.4 15a1.65 1.65 0 0 0 .33 1.82l.06.06a2 2 0 1 1-2.83 2.83l-.06-.06a1.65 1.65 0 0 0-1.82-.33 1.65 1.65 0 0 0-1 1.51V21a2 2 0 1 1-4 0v-.09A1.65 1.65 0 0 0 9 19.4a1.65 1.65 0 0 0-1.82.33l-.06.06a2 2 0 1 1-2.83-2.83l.06-.06a1.65 1.65 0 0 0 .33-1.82 1.65 1.65 0 0 0-1.51-1H3a2 2 0 1 1 0-4h.09A1.65 1.65 0 0 0 4.6 9a1.65 1.65 0 0 0-.33-1.82l-.06-.06a2 2 0 1 1 2.83-2.83l.06.06a1.65 1.65 0 0 0 1.82.33H9a1.65 1.65 0 0 0 1-1.51V3a2 2 0 1 1 4 0v.09a1.65 1.65 0 0 0 1 1.51 1.65 1.65 0 0 0 1.82-.33l.06-.06a2 2 0 1 1 2.83 2.83l-.06.06a1.65 1.65 0 0 0-.33 1.82V9a1.65 1.65 0 0 0 1.51 1H21a2 2 0 1 1 0 4h-.09a1.65 1.65 0 0 0-1.51 1z')],
  help: [C(12, 12, 10), P('M9.09 9a3 3 0 0 1 5.83 1c0 2-3 3-3 3'), L(12, 17, 12.01, 17)],
  more: [C(5, 12, 1), C(12, 12, 1), C(19, 12, 1)],
  down: [P('m6 9 6 6 6-6')],
  right: [P('m9 18 6-6-6-6')],
  up: [P('m18 15-6-6-6 6')],
  eye: [P('M2 12s3.5-7 10-7 10 7 10 7-3.5 7-10 7S2 12 2 12z'), C(12, 12, 3)],
  'eye-off': [P('M9.88 9.88a3 3 0 1 0 4.24 4.24'), P('M10.73 5.08A10.43 10.43 0 0 1 12 5c7 0 10 7 10 7a13.16 13.16 0 0 1-1.67 2.68'),
    P('M6.61 6.61A13.53 13.53 0 0 0 2 12s3 7 10 7a9.74 9.74 0 0 0 5.39-1.61'), L(2, 2, 22, 22)],
  box: [P('M21 8a2 2 0 0 0-1-1.73l-7-4a2 2 0 0 0-2 0l-7 4A2 2 0 0 0 3 8v8a2 2 0 0 0 1 1.73l7 4a2 2 0 0 0 2 0l7-4A2 2 0 0 0 21 16z'), P('m3.3 7 8.7 5 8.7-5'), P('M12 22V12')],
  cylinder: [E(12, 5, 9, 3), P('M3 5v14a9 3 0 0 0 18 0V5')],
  cone: [P('M4 18 12 3l8 15'), P('M4 18a8 3 0 0 0 16 0')],
  sphere: [C(12, 12, 9), P('M3 12a9 3.5 0 0 0 18 0')],
  torus: [E(12, 12, 10, 6), E(12, 12, 4, 2.2)],
  sketch: [P('M12 20h9'), P('M16.5 3.5a2.121 2.121 0 0 1 3 3L7 19l-4 1 1-4z')],
  pencil: [P('M17 3a2.85 2.83 0 1 1 4 4L7.5 20.5 2 22l1.5-5.5Z')],
  move: [P('m5 9-3 3 3 3'), P('m9 5 3-3 3 3'), P('m15 19-3 3-3-3'), P('m19 9 3 3-3 3'), L(2, 12, 22, 12), L(12, 2, 12, 22)],
  rotate: [P('M21 12a9 9 0 1 1-9-9c2.52 0 4.93 1 6.74 2.74L21 8'), P('M21 3v5h-5')],
  mirror: [P('M8 3H5a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h3'), P('M16 3h3a2 2 0 0 1 2 2v14a2 2 0 0 1-2 2h-3'), L(12, 20, 12, 22), L(12, 14, 12, 16), L(12, 8, 12, 10), L(12, 2, 12, 4)],
  pattern: [R(3, 3, 7, 7), R(14, 3, 7, 7), R(14, 14, 7, 7), R(3, 14, 7, 7)],
  fillet: [P('M3 20V10a6 6 0 0 1 6-6h12')],
  chamfer: [P('M3 20v-8l8-8h10')],
  material: [P('M12 22a1 1 0 0 1 0-20 10 9 0 0 1 10 9 5 5 0 0 1-5 5h-2.25a1.75 1.75 0 0 0-1.4 2.8l.3.4a1.75 1.75 0 0 1-1.4 2.8z'),
    C(13.5, 6.5, .5), C(17.5, 10.5, .5), C(8.5, 7.5, .5), C(6.5, 12.5, .5)],
  color: [P('M12 22a7 7 0 0 0 7-7c0-2-1-3.9-3-5.5s-3.5-4-4-6.5c-.5 2.5-2 4.9-4 6.5C6 11.1 5 13 5 15a7 7 0 0 0 7 7z')],
  trash: [P('M3 6h18'), P('M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6'), P('M8 6V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2')],
  measure: [P('M21.3 15.3a2.4 2.4 0 0 1 0 3.4l-2.6 2.6a2.4 2.4 0 0 1-3.4 0L2.7 8.7a2.41 2.41 0 0 1 0-3.4l2.6-2.6a2.41 2.41 0 0 1 3.4 0z'),
    P('m14.5 12.5 2-2'), P('m11.5 9.5 2-2'), P('m8.5 6.5 2-2'), P('m17.5 15.5 2-2')],
  section: [P('M3 12h4m3 0h4m3 0h4'), P('m5 7 7-4 7 4'), P('m5 17 7 4 7-4')],
  view: [P('M3 7V5a2 2 0 0 1 2-2h2'), P('M17 3h2a2 2 0 0 1 2 2v2'), P('M21 17v2a2 2 0 0 1-2 2h-2'), P('M7 21H5a2 2 0 0 1-2-2v-2'), C(12, 12, 3)],
  maximize: [P('M15 3h6v6'), P('M9 21H3v-6'), P('M21 3l-7 7'), P('M3 21l7-7')],
  minimize: [P('M4 14h6v6'), P('M20 10h-6V4'), P('M14 10l7-7'), P('M3 21l7-7')],
  fit: [P('M8 3H5a2 2 0 0 0-2 2v3'), P('M21 8V5a2 2 0 0 0-2-2h-3'), P('M3 16v3a2 2 0 0 0 2 2h3'), P('M16 21h3a2 2 0 0 0 2-2v-3')],
  select: [P('M4.04 4.69a.5.5 0 0 1 .65-.65l16 6.5a.5.5 0 0 1-.06.95l-6.12 1.58a2 2 0 0 0-1.44 1.43l-1.58 6.13a.5.5 0 0 1-.95.06z')],
  create: [C(12, 12, 10), L(12, 8, 12, 16), L(8, 12, 16, 12)],
  union: [C(9, 12, 6), C(15, 12, 6)],
  subtract: [C(9, 12, 6), C(15, 12, 6, { 'stroke-dasharray': '3 3' })],
  intersect: [C(9, 12, 6, { opacity: .4 }), C(15, 12, 6, { opacity: .4 }), P('M12 7.2a6 6 0 0 1 0 9.6a6 6 0 0 1 0-9.6z')],
  hole: [C(12, 12, 10), C(12, 12, 3)],
  send: [P('m5 12 7-7 7 7'), P('M12 19V5')],
  attach: [P('m21.44 11.05-9.19 9.19a6 6 0 0 1-8.49-8.49l8.57-8.57A4 4 0 1 1 18 8.84l-8.59 8.57a2 2 0 0 1-2.83-2.83l8.49-8.48')],
  image: [R(3, 3, 18, 18), C(9, 9, 2), P('m21 15-3.09-3.09a2 2 0 0 0-2.82 0L6 21')],
  'panel-left': [R(3, 3, 18, 18), L(9, 3, 9, 21)],
  'panel-right': [R(3, 3, 18, 18), L(15, 3, 15, 21)],
  play: [P('M6 3l14 9-14 9z')],
  mesh: [R(3, 3, 18, 18), L(3, 9, 21, 9), L(3, 15, 21, 15), L(9, 3, 9, 21), L(15, 3, 15, 21)],
  x: [P('M18 6 6 18'), P('m6 6 12 12')],
  check: [P('M20 6 9 17l-5-5')],
  'file-plus': [P('M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z'), P('M14 2v6h6'), P('M12 18v-6'), P('M9 15h6')],
  folder: [P('m6 14 1.5-2.9A2 2 0 0 1 9.24 10H20a2 2 0 0 1 1.94 2.5l-1.54 6a2 2 0 0 1-1.95 1.5H4a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h3.9a2 2 0 0 1 1.69.9l.81 1.2a2 2 0 0 0 1.67.9H18a2 2 0 0 1 2 2v2')],
  command: [P('M15 6v12a3 3 0 1 0 3-3H6a3 3 0 1 0 3 3V6a3 3 0 1 0-3 3h12a3 3 0 1 0-3-3')],
  ban: [C(12, 12, 10), L(4.9, 4.9, 19.1, 19.1)],
  scale: [P('M21 3 9 15'), P('M15 3h6v6'), P('M3 15v6h6'), R(3, 3, 8, 8, 1)],
  duplicate: [R(8, 8, 14, 14), P('M4 16c-1.1 0-2-.9-2-2V4c0-1.1.9-2 2-2h10c1.1 0 2 .9 2 2')],
  align: [L(3, 3, 3, 21), R(7, 5, 13, 5, 1), R(7, 14, 8, 5, 1)],
  group: [R(2, 2, 20, 20, 3), R(6, 6, 6, 5, 1), R(12, 13, 6, 5, 1)],
  ungroup: [R(2, 2, 9, 8, 2), R(13, 14, 9, 8, 2), P('M15 4h5v5'), P('M9 20H4v-5')],
  extrude: [P('M4 15h16v5H4z'), P('M12 12V3'), P('m8 7 4-4 4 4')],
  inset: [R(3, 3, 18, 18), R(8, 8, 8, 8, 1), P('m3 3 5 5'), P('m21 3-5 5'), P('m3 21 5-5'), P('m21 21-5-5')],
  offset: [P('M4 20V9a5 5 0 0 1 5-5h11'), P('M8 20v-9a3 3 0 0 1 3-3h9'), P('m14 14 3 3'), P('m17 14-3 3')],
  restore: [P('M3 12a9 9 0 1 0 9-9 9.75 9.75 0 0 0-6.74 2.74L3 8'), P('M3 3v5h5'), P('M12 7v5l4 2')],
  component: [P('M12 2 4 6v12l8 4 8-4V6z'), P('M12 11 4 7'), P('m12 11 8-4'), P('M12 11v11'), C(12, 6.5, 1.6)],
  compare: [P('M16 3h5v5'), P('M8 3H3v5'), P('M12 22v-8.3a4 4 0 0 0-1.17-2.87L3 3'), P('m15 9 6-6'), P('M21 16v5h-5'), P('m15 15 6 6')],
};

/** An <svg> icon node (aria-hidden); size comes from CSS (.ic). */
export function icon(name) {
  const parts = ICONS[name] ?? ICONS.box;
  return svg('svg', { class: 'ic', viewBox: '0 0 24 24', fill: 'none', stroke: 'currentColor', 'stroke-width': '1.75',
    'stroke-linecap': 'round', 'stroke-linejoin': 'round', 'aria-hidden': 'true', focusable: 'false' },
  ...parts.map(([tag, attrs]) => svg(tag, attrs)));
}

/** Feature type (as shown in the design tree) -> icon name. */
export function featureIcon(type = '') {
  const t = type.toLowerCase();
  const exact = { copy: 'duplicate', scale: 'scale', face_extrude: 'extrude', face_inset: 'inset', face_offset: 'offset',
    component_instance: 'component' };
  if (exact[t]) return exact[t];
  for (const k of ['cylinder', 'cone', 'sphere', 'torus', 'hole', 'mirror', 'fillet', 'chamfer', 'sketch', 'union', 'subtract', 'intersect']) if (t.includes(k)) return k;
  if (t.includes('pattern')) return 'pattern';
  if (t.includes('transform')) return 'move';
  if (t.includes('boolean')) return 'union';
  return 'box';
}
