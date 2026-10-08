import { test } from 'node:test';
import assert from 'node:assert/strict';
import { selectionContext, actionsFor } from './actions.js';

const ids = (sels) => actionsFor(selectionContext(sels)).map((a) => a.id);

test('selectionContext classifies selections', () => {
  assert.equal(selectionContext([]).ctx, 'none');
  assert.equal(selectionContext(['body(feature=a)']).ctx, 'body');
  assert.equal(selectionContext(['body(feature=a)', 'body(feature=b)']).ctx, 'multiBody');
  assert.equal(selectionContext(['face(feature=a,side=+Z)', 'face(feature=a,side=-Z)']).ctx, 'face');
  assert.equal(selectionContext(['edge(feature=a,index=1)']).ctx, 'edge');
  assert.equal(selectionContext(['vertex(feature=a,index=1)']).ctx, 'vertex');
  assert.equal(selectionContext(['face(feature=a,side=+Z)', 'edge(feature=a,index=1)']).ctx, 'mixed');
});

test('only valid actions are offered', () => {
  assert.ok(ids([]).includes('create') && !ids([]).includes('delete'));
  assert.ok(ids(['body(feature=a)']).includes('move') && !ids(['body(feature=a)']).includes('union'));
  assert.ok(ids(['edge(feature=a,index=1)']).includes('fillet') && !ids(['edge(feature=a,index=1)']).includes('move'));
  assert.deepEqual(ids(['body(feature=a)', 'body(feature=b)']).filter((x) => ['union', 'subtract', 'intersect'].includes(x)), ['union', 'subtract', 'intersect']);
  // booleans need exactly two bodies
  assert.ok(!ids(['body(feature=a)', 'body(feature=b)', 'body(feature=c)']).includes('union'));
});

test('body, face and group actions follow the selection', () => {
  const body = ids(['body(feature=a)']);
  for (const a of ['scale', 'duplicate', 'align', 'group']) assert.ok(body.includes(a), a);
  assert.ok(!body.includes('ungroup'), 'ungroup needs a grouped object');
  const grouped = actionsFor(selectionContext(['body(feature=a)'], (f) => (f === 'a' ? 'Base' : ''))).map((a) => a.id);
  assert.ok(grouped.includes('ungroup'));
  const face = ids(['face(feature=a,side=+Z)']);
  for (const a of ['face-extrude', 'face-inset', 'face-offset']) assert.ok(face.includes(a), a);
  assert.ok(!face.includes('scale') && !ids(['edge(feature=a,index=1)']).includes('face-extrude'));
  assert.ok(ids(['body(feature=a)', 'body(feature=b)']).includes('align'));
});

test('delete is always last', () => {
  for (const s of [['body(feature=a)'], ['face(feature=a,side=+Z)'], ['edge(feature=a,index=1)'], ['vertex(feature=a,index=1)'], ['body(feature=a)', 'body(feature=b)']]) {
    const l = ids(s);
    assert.equal(l[l.length - 1], 'delete', s.join());
  }
});
