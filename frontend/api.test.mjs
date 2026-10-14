import test from 'node:test';
import assert from 'node:assert/strict';

// api.js talks to the server through fetch; a stub stands in for it.
function stubFetch(impl) {
  const calls = [];
  globalThis.fetch = (url, init) => { calls.push({ url, init }); return impl(url, init); };
  return calls;
}
const respond = (body, status = 200) => Promise.resolve({ ok: status < 300, status, json: async () => body, arrayBuffer: async () => new ArrayBuffer(4) });
const hangUntilAborted = (_url, init) => new Promise((_, reject) => {
  init.signal.addEventListener('abort', () => reject(Object.assign(new Error('aborted'), { name: 'AbortError' })));
});

const { api, apiBytes, subscribe, DEFAULT_TIMEOUT_MS } = await import('./api.js');

test('a request that gets no answer ends with a message that says so', async () => {
  stubFetch(hangUntilAborted);
  const t0 = Date.now();
  await assert.rejects(() => api('state', undefined, { timeout: 30 }), /state: no answer from the server after 0 s/);
  assert.ok(Date.now() - t0 < 1000, 'it gave up at the timeout, not later');
});

test('the same applies to binary replies', async () => {
  stubFetch(hangUntilAborted);
  await assert.rejects(() => apiBytes('printsim/toolpath', { timeout: 30 }), /no answer from the server/);
});

test('a prompt answer is passed through, with the method and body of the call', async () => {
  const calls = stubFetch(() => respond({ ok: true }));
  assert.deepEqual(await api('tool', { name: 'x' }), { ok: true });
  assert.equal(calls[0].url, '/api/tool');
  assert.equal(calls[0].init.method, 'POST');
  assert.equal(calls[0].init.body, '{"name":"x"}');
  await api('state');
  assert.equal(calls[1].init.method, 'GET');
  assert.equal(calls[1].init.body, undefined);
});

test('an error reply keeps its status, code and details', async () => {
  stubFetch(() => respond({ error: 'consent needed', code: 'fallback_consent', fallback: { model: 'm' } }, 409));
  await assert.rejects(() => api('prompt', {}), (e) => e.message === 'consent needed' && e.status === 409 && e.code === 'fallback_consent' && e.data.fallback.model === 'm');
});

test('a network failure is passed on unchanged, not mistaken for a timeout', async () => {
  stubFetch(() => Promise.reject(new TypeError('Failed to fetch')));
  await assert.rejects(() => api('state'), /Failed to fetch/);
});

test('the default timeout is generous (the agent reports through notices, not through a long call)', () => {
  assert.ok(DEFAULT_TIMEOUT_MS >= 60000);
});

test('subscribe reports the connection state and still delivers notices', () => {
  const created = [];
  globalThis.EventSource = class { constructor(url) { this.url = url; created.push(this); } };
  const notices = [], status = [];
  const es = subscribe((n) => notices.push(n), (s) => status.push(s));
  es.onopen(); es.onerror(); es.onopen();
  es.onmessage({ data: '{"kind":"state"}' });
  es.onmessage({ data: 'not json' }); // ignored
  assert.deepEqual(status, ['open', 'lost', 'open']);
  assert.deepEqual(notices, [{ kind: 'state' }]);
  assert.match(created[0].url, /^\/api\/events\?token=/);
  subscribe(() => {}).onerror(); // status callback is optional
});
