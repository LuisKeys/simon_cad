// Keyboard and mouse shortcuts shown in the Shortcuts panel. Sections and the rows inside
// them go from most to least used. Keys are tokens; "Ctrl" is shown as ⌘ on macOS.
// Keep in sync with the handlers in app.js / transform.js and the table in help.html.

export const SECTIONS = [
  { title: 'Essentials', rows: [
    { keys: [['Ctrl', 'Z']], label: 'Undo' },
    { keys: [['Ctrl', 'Shift', 'Z'], ['Ctrl', 'Y']], label: 'Redo' },
    { keys: [['Ctrl', 'S']], label: 'Save the project' },
    { keys: [['Delete'], ['Backspace']], label: 'Delete the selection' },
    { keys: [['Esc']], label: 'Close a menu or dialog · clear the selection' },
    { keys: [['Ctrl', 'K']], label: 'Command palette' },
    { keys: [['?']], label: 'Show / hide these shortcuts' },
  ] },
  { title: 'Select & edit', rows: [
    { keys: [['Click']], label: 'Select an object' },
    { keys: [['Shift', 'Click']], label: 'Add to the selection' },
    { keys: [['Ctrl', 'Click']], label: 'Toggle in the selection (design tree)' },
    { keys: [['B']], label: 'Box select: drag a rectangle (left → right inside, right → left touching)' },
    { keys: [['G']], label: 'Move the selected object with the mouse' },
    { keys: [['R']], label: 'Rotate the selected object with the mouse' },
    { keys: [['Ctrl', 'D']], label: 'Duplicate' },
    { keys: [['Ctrl', 'G']], label: 'Group' },
    { keys: [['Ctrl', 'Shift', 'G']], label: 'Ungroup' },
    { keys: [['Right-click']], label: 'Context menu' },
  ] },
  { title: 'View & camera', rows: [
    { keys: [['F']], label: 'Fit all views' },
    { keys: [['Wheel']], label: 'Zoom' },
    { keys: [['Left-drag']], label: 'Orbit (perspective) · pan (Top, Front…)' },
    { keys: [['Middle-drag'], ['Right-drag']], label: 'Pan' },
    { keys: [['M']], label: 'Measure: click two points' },
    { keys: [['Ctrl', 'Alt', '4']], label: 'Single / Quad view' },
    { keys: [['Double-click']], label: 'Maximize a view (on its label, Quad view)' },
  ] },
  { title: 'Print simulation', rows: [
    { keys: [['Space']], label: 'Play / pause the simulated print' },
    { keys: [['[']], label: 'Previous layer' },
    { keys: [[']']], label: 'Next layer' },
  ] },
  { title: 'While moving or rotating', rows: [
    { keys: [['Click'], ['Enter']], label: 'Confirm' },
    { keys: [['Esc'], ['Right-click']], label: 'Cancel (the object goes back)' },
    { keys: [['X'], ['Y'], ['Z']], label: 'Constrain to an axis · rotation axis' },
    { keys: [['Shift', 'X'], ['Shift', 'Y'], ['Shift', 'Z']], label: 'Move in the plane normal to that axis' },
    { keys: [['Ctrl']], hold: true, label: 'Hold: no snapping' },
    { keys: [['P']], label: 'Rotate: pivot at origin / center' },
  ] },
  { title: 'Simon Agent', rows: [
    { keys: [['Enter']], label: 'Send the message' },
    { keys: [['Shift', 'Enter']], label: 'New line' },
  ] },
  { title: 'Tree, menus & dialogs', rows: [
    { keys: [['↑'], ['↓']], label: 'Move between rows and items' },
    { keys: [['Enter']], label: 'Select · run · apply' },
    { keys: [['F2']], label: 'Rename the focused object' },
    { keys: [['←'], ['→']], label: 'Fold / unfold a group' },
  ] },
];

/** The key token as shown on this platform. */
export function keyLabel(token, mac = false) {
  if (!mac) return token;
  return { Ctrl: '⌘', Alt: '⌥', Shift: '⇧', Delete: 'Del', Backspace: '⌫' }[token] ?? token;
}

/** True when the keydown event should open or close the panel ("?" outside text fields). */
export function isToggleKey(e, typing) {
  return e.key === '?' && !typing && !e.ctrlKey && !e.metaKey && !e.altKey;
}
