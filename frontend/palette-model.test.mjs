import test from 'node:test';
import assert from 'node:assert/strict';
import { filterEntries, prefillSelection, MAX_ENTRIES } from './palette-model.js';

const entry = (id, title = id, description = '') => ({ kind: 'tool', id, title, description });
const ALL = [
  entry('create_box', 'Create box', 'A rectangular block'),
  entry('add_hole', 'Add hole', 'Drill holes into a face'),
  entry('fillet', 'Fillet', 'Round edges'),
];

test('an empty query lists everything and offers no "ask the agent" entry', () => {
  const r = filterEntries(ALL, '   ');
  assert.equal(r.length, 3);
  assert.ok(!r.some((e) => e.kind === 'ask'));
});

test('a query matches title, id or description, ignoring case', () => {
  assert.deepEqual(filterEntries(ALL, 'BOX').filter((e) => e.kind !== 'ask').map((e) => e.id), ['create_box']);
  assert.deepEqual(filterEntries(ALL, 'drill').filter((e) => e.kind !== 'ask').map((e) => e.id), ['add_hole']);
  assert.deepEqual(filterEntries(ALL, 'add_hole').filter((e) => e.kind !== 'ask').map((e) => e.id), ['add_hole']);
});

test('whatever is typed can always be handed to the agent, with the text trimmed', () => {
  const r = filterEntries(ALL, '  make it taller ');
  assert.equal(r.length, 1, 'nothing matches, only the ask entry');
  assert.equal(r[0].kind, 'ask');
  assert.equal(r[0].template, 'make it taller');
  assert.match(r[0].title, /make it taller/);
});

test('the list is capped but the ask entry is still added after the cap', () => {
  const many = Array.from({ length: 100 }, (_, i) => entry(`t${i}`, `tool ${i}`));
  assert.equal(filterEntries(many, '').length, MAX_ENTRIES);
  const q = filterEntries(many, 'tool');
  assert.equal(q.length, MAX_ENTRIES + 1);
  assert.equal(q.at(-1).kind, 'ask');
});

test('a selected face fills the face argument only when the tool takes one and the template has it', () => {
  const e = { schema: { properties: { face: {} } } };
  assert.equal(prefillSelection({ face: '' }, e, ['body(feature=a)', 'face(feature=a,side=+Z)']).face, 'face(feature=a,side=+Z)');
  assert.deepEqual(prefillSelection({}, e, ['face(feature=a,side=+Z)']), {}, 'no face key in the template: left alone');
  assert.equal(prefillSelection({ face: '' }, { schema: { properties: {} } }, ['face(feature=a,side=+Z)']).face, '', 'the tool takes no face');
});

test('selected edges fill the edges and the feature they belong to', () => {
  const e = { schema: { properties: { edges: {}, target: {} } } };
  const r = prefillSelection({ target: '' }, e, ['edge(feature=plate,index=3)', 'edge(feature=plate,index=5)']);
  assert.deepEqual(r.edges, ['edge(feature=plate,index=3)', 'edge(feature=plate,index=5)']);
  assert.equal(r.target, 'plate');
});

test('one selected object fills target and source; several do not guess', () => {
  const e = { schema: { properties: {} } };
  assert.deepEqual(prefillSelection({ target: '', source: '', keep: 'x' }, e, ['body(feature=b)', 'face(feature=b,side=-X)']), { target: 'b', source: 'b', keep: 'x' });
  assert.deepEqual(prefillSelection({ target: '' }, e, ['body(feature=a)', 'body(feature=b)']), { target: '' });
  assert.deepEqual(prefillSelection({ target: 'given' }, e, ['body(feature=b)']), { target: 'given' }, 'an explicit value is never overwritten');
});

test('no selection leaves the arguments alone', () => {
  assert.deepEqual(prefillSelection({ target: '', face: '' }, { schema: { properties: { face: {} } } }, undefined), { target: '', face: '' });
});
