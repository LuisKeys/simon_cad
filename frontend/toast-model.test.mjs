import test from 'node:test';
import assert from 'node:assert/strict';
import { ToastQueue, MIN_MS, BACKLOG_MS, MAX_WAITING } from './toast-model.js';

test('the first message shows at once for its own duration', () => {
  const q = new ToastQueue();
  assert.deepEqual(q.push('a', 'ok', 4500, 0), { show: { msg: 'a', kind: 'ok', ms: 4500 }, ms: 4500 });
});

test('a message that arrives while another is fresh waits until the minimum has passed', () => {
  const q = new ToastQueue();
  q.push('a', '', 4500, 0);
  assert.deepEqual(q.push('b', 'ok', 4500, 400), { wait: MIN_MS - 400 });
  const n = q.next(MIN_MS);
  assert.equal(n.show.msg, 'b');
  assert.equal(n.ms, 4500, 'nothing else waits, so it keeps its full duration');
});

test('once the current message has had its minimum, a new one replaces it immediately', () => {
  const q = new ToastQueue();
  q.push('a', '', 4500, 0);
  const r = q.push('b', 'ok', 4500, MIN_MS);
  assert.equal(r.show.msg, 'b');
});

test('an error or warning replaces a fresh informational message at once', () => {
  for (const kind of ['bad', 'warn']) {
    const q = new ToastQueue();
    q.push('Redid cs_0003', 'ok', 4500, 0);
    const r = q.push('something failed', kind, 4500, 200);
    assert.equal(r.show.msg, 'something failed', kind);
    assert.equal(q.waiting.length, 0, 'the informational message is dropped, not queued');
  }
});

test('two errors in a row both get shown: the second waits for the first', () => {
  const q = new ToastQueue();
  q.push('first error', 'bad', 4500, 0);
  assert.deepEqual(q.push('second error', 'bad', 4500, 100), { wait: MIN_MS - 100 });
  assert.equal(q.next(MIN_MS).show.msg, 'second error');
  // an informational message never pre-empts an error
  const r = new ToastQueue();
  r.push('error', 'bad', 4500, 0);
  assert.ok(r.push('info', 'ok', 4500, 100).wait);
});

test('a repeat of the message on screen extends it instead of stacking', () => {
  const q = new ToastQueue();
  q.push('same', 'bad', 4500, 0);
  assert.deepEqual(q.push('same', 'bad', 3000, 100), { extend: 3000 });
  assert.equal(q.waiting.length, 0);
  // same text with another kind is a different message
  assert.ok(q.push('same', 'ok', 4500, 200).wait);
});

test('a repeat of a waiting message is ignored', () => {
  const q = new ToastQueue();
  q.push('a', '', 4500, 0);
  q.push('b', '', 4500, 100);
  assert.deepEqual(q.push('b', '', 4500, 200), {});
  assert.equal(q.waiting.length, 1);
});

test('at most MAX_WAITING wait; the oldest is dropped; a backlog drains faster', () => {
  const q = new ToastQueue();
  q.push('first', '', 4500, 0);
  for (const m of ['w1', 'w2', 'w3', 'w4']) q.push(m, '', 4500, 10);
  assert.equal(q.waiting.length, MAX_WAITING);
  assert.deepEqual(q.waiting.map((n) => n.msg), ['w2', 'w3', 'w4'], 'w1 was dropped');
  const n = q.next(MIN_MS);
  assert.equal(n.show.msg, 'w2');
  assert.equal(n.ms, BACKLOG_MS, 'others are still waiting, so it is shown for less');
  q.next(5000); q.next(8000);
  assert.deepEqual(q.next(9000), { hide: true });
  assert.equal(q.cur, null);
});

test('after it hides, the next message shows immediately', () => {
  const q = new ToastQueue();
  q.push('a', '', 100, 0);
  q.next(100);
  assert.equal(q.push('b', '', 4500, 101).show.msg, 'b');
});
