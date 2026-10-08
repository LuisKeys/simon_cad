import test from 'node:test';
import assert from 'node:assert/strict';
import { SECTIONS, keyLabel, isToggleKey } from './shortcuts-model.js';

test('the most used shortcuts come first and the panel lists its own key', () => {
  assert.equal(SECTIONS[0].title, 'Essentials');
  assert.deepEqual(SECTIONS[0].rows[0].keys, [['Ctrl', 'Z']]);
  assert.ok(SECTIONS[0].rows.some((r) => r.keys.some((k) => k.join() === '?')));
});

test('every row has a label and at least one non-empty key combination', () => {
  for (const s of SECTIONS) {
    assert.ok(s.rows.length, s.title);
    for (const r of s.rows) {
      assert.ok(r.label, s.title);
      assert.ok(r.keys.length && r.keys.every((k) => k.length && k.every(Boolean)), r.label);
    }
  }
});

test('no combination is listed twice within a section', () => {
  for (const s of SECTIONS) {
    const seen = s.rows.flatMap((r) => r.keys.map((k) => k.join('+')));
    assert.equal(new Set(seen).size, seen.length, s.title);
  }
});

test('modifiers use the macOS symbols only on macOS', () => {
  assert.equal(keyLabel('Ctrl'), 'Ctrl');
  assert.equal(keyLabel('Ctrl', true), '⌘');
  assert.equal(keyLabel('Shift', true), '⇧');
  assert.equal(keyLabel('G', true), 'G');
});

test('"?" toggles the panel, but not while typing or with Ctrl/Cmd/Alt', () => {
  const ev = (o) => ({ key: '?', ctrlKey: false, metaKey: false, altKey: false, ...o });
  assert.equal(isToggleKey(ev({}), false), true);
  assert.equal(isToggleKey(ev({ shiftKey: true }), false), true); // "?" is Shift+/ on most layouts
  assert.equal(isToggleKey(ev({}), true), false);
  assert.equal(isToggleKey(ev({ ctrlKey: true }), false), false);
  assert.equal(isToggleKey(ev({ key: '/' }), false), false);
});
