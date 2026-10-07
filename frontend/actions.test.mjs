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

test('delete is always last', () => {
  for (const s of [['body(feature=a)'], ['face(feature=a,side=+Z)'], ['edge(feature=a,index=1)'], ['vertex(feature=a,index=1)'], ['body(feature=a)', 'body(feature=b)']]) {
    const l = ids(s);
    assert.equal(l[l.length - 1], 'delete', s.join());
  }
});
