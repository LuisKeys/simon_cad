import test from 'node:test';
import assert from 'node:assert/strict';
import { parseMarkdown, parseInline } from './markdown-model.js';

const txt = (v) => ({ t: 'text', v });

test('a bullet list with bold names, as models write it', () => {
  const md = 'I found a few materials. Please choose one:\n\n*   **Cast Iron** (darker)\n*   **Polished Steel** (shiny)';
  assert.deepEqual(parseMarkdown(md), [
    { t: 'p', c: [txt('I found a few materials. Please choose one:')] },
    { t: 'list', ordered: false, start: 1, items: [
      [{ t: 'p', c: [{ t: 'strong', c: [txt('Cast Iron')] }, txt(' (darker)')] }],
      [{ t: 'p', c: [{ t: 'strong', c: [txt('Polished Steel')] }, txt(' (shiny)')] }],
    ] },
  ]);
});

test('nested and numbered lists, with blank lines between items', () => {
  const [list] = parseMarkdown('3. one\n   - a\n   - b\n\n4. two');
  assert.equal(list.ordered, true);
  assert.equal(list.start, 3);
  assert.equal(list.items.length, 2);
  assert.equal(list.items[0][1].t, 'list');
  assert.equal(list.items[0][1].items.length, 2);
  // two-space nesting under a numbered item also nests
  const [l2] = parseMarkdown('1. one\n  - a\n2. two');
  assert.equal(l2.items.length, 2);
  assert.equal(l2.items[0][1].t, 'list');
});

test('a paragraph right after a list ends it only after a blank line', () => {
  const b = parseMarkdown('- a\ncontinued\n\nAfter.');
  assert.equal(b.length, 2);
  assert.deepEqual(b[0].items[0], [{ t: 'p', c: [txt('a'), { t: 'br' }, txt('continued')] }]);
  assert.deepEqual(b[1], { t: 'p', c: [txt('After.')] });
});

test('headings, rules, quotes and fenced code', () => {
  const b = parseMarkdown('## Plan\n---\n> note\n```scad\ncube(**2**);\n```');
  assert.deepEqual(b.map((x) => x.t), ['h', 'hr', 'quote', 'code']);
  assert.equal(b[0].level, 2);
  assert.equal(b[3].lang, 'scad');
  assert.equal(b[3].v, 'cube(**2**);', 'code is never formatted');
});

test('a table', () => {
  const [t] = parseMarkdown('| Part | Size |\n|---|--:|\n| plate | **4** mm |');
  assert.equal(t.t, 'table');
  assert.deepEqual(t.align, [null, 'right']);
  assert.deepEqual(t.rows[0][1], [{ t: 'strong', c: [txt('4')] }, txt(' mm')]);
});

test('inline emphasis, code and links', () => {
  assert.deepEqual(parseInline('*it* and __b__ ~~x~~ `a*b*` [doc](https://x.dev/a)'), [
    { t: 'em', c: [txt('it')] }, txt(' and '), { t: 'strong', c: [txt('b')] }, txt(' '),
    { t: 'del', c: [txt('x')] }, txt(' '), { t: 'code', v: 'a*b*' }, txt(' '),
    { t: 'link', href: 'https://x.dev/a', c: [txt('doc')] },
  ]);
  assert.deepEqual(parseInline('**bold *both* bold**'), [{ t: 'strong', c: [txt('bold '), { t: 'em', c: [txt('both')] }, txt(' bold')] }]);
});

test('what is not emphasis stays literal', () => {
  assert.deepEqual(parseInline('Sets material for [image_o1] and my_part_2'), [txt('Sets material for [image_o1] and my_part_2')]);
  assert.deepEqual(parseInline('5 * 3 * 2 and **unclosed'), [txt('5 * 3 * 2 and **unclosed')]);
  assert.deepEqual(parseInline('\\*not\\*'), [txt('*not*')]);
  assert.deepEqual(parseInline('[x](javascript:alert(1))'), [txt('[x](javascript:alert(1))')]);
});
