// Thin client for the Simon CAD UI service. Every call maps one-to-one onto a ui.Service
// method, which is also what the Wails shell binds.
const token = (typeof document !== 'undefined' && document.querySelector('meta[name="simon-token"]')?.content) || '';

// A request that gets no answer must end, not hang the panel that awaits it for ever. The agent runs
// on the server and its progress arrives as notices, so no call here legitimately takes this long.
export const DEFAULT_TIMEOUT_MS = 120000;

// fetchTimeout is fetch that gives up after ms, with an error that says so.
async function fetchTimeout(path, init, ms, run) {
  const ctl = new AbortController();
  const timer = setTimeout(() => ctl.abort(), ms);
  try {
    return await run(await fetch('/api/' + path, { ...init, signal: ctl.signal }));
  } catch (e) {
    if (e.name === 'AbortError') throw new Error(`${path}: no answer from the server after ${Math.round(ms / 1000)} s`);
    throw e;
  } finally { clearTimeout(timer); }
}

export async function api(path, body, { timeout = DEFAULT_TIMEOUT_MS } = {}) {
  return fetchTimeout(path, {
    method: body === undefined ? 'GET' : 'POST',
    headers: { 'X-Simon-Token': token, 'Content-Type': 'application/json' },
    body: body === undefined ? undefined : JSON.stringify(body),
  }, timeout, async (res) => {
    let data = null;
    try { data = await res.json(); } catch { /* empty body */ }
    if (!res.ok) throw failure(data, res.status, `${path}: HTTP ${res.status}`);
    return data;
  });
}

// Binary reply (the print simulation's toolpath buffer).
export async function apiBytes(path, { timeout = DEFAULT_TIMEOUT_MS } = {}) {
  return fetchTimeout(path, { headers: { 'X-Simon-Token': token } }, timeout, async (res) => {
    if (!res.ok) {
      let data = null;
      try { data = await res.json(); } catch { /* empty body */ }
      throw failure(data, res.status, `${path}: HTTP ${res.status}`);
    }
    return res.arrayBuffer();
  });
}

// failure turns an error reply into an Error that keeps its status, its code (e.g. "fallback_consent") and its details.
function failure(data, status, fallback) {
  const err = new Error(data?.error ?? fallback);
  err.status = status; err.code = data?.code; err.data = data;
  return err;
}

// Uploads raw image bytes (PNG/JPEG/WebP); resolves to {id, width, height, media_type, retained}.
export async function uploadImage(blob) {
  const res = await fetch('/api/image', { method: 'POST', headers: { 'X-Simon-Token': token }, body: blob });
  let data = null;
  try { data = await res.json(); } catch { /* empty body */ }
  if (!res.ok) throw new Error(data?.error ?? `image upload failed (HTTP ${res.status})`);
  return data;
}

// Uploads a text document (specification) as short-term memory; resolves to {name, chars}.
export async function uploadSpec(file) {
  const res = await fetch(`/api/spec?name=${encodeURIComponent(file.name)}`, { method: 'POST', headers: { 'X-Simon-Token': token }, body: file });
  let data = null;
  try { data = await res.json(); } catch { /* empty body */ }
  if (!res.ok) throw failure(data, res.status, `document upload failed (HTTP ${res.status})`);
  return data;
}

// Uploads a STEP file. The server reads it with the kernel first (it must be valid and hold a
// solid) and only then adds it as a body with its exact geometry; a refused file rejects with the
// reason. STEP declares its own units, so there are none to send. Resolves to the tool outcome.
export async function uploadStep(file, { placement = '' } = {}) {
  const q = new URLSearchParams({ name: file.name });
  if (placement) q.set('placement', placement);
  const res = await fetch(`/api/step?${q}`, { method: 'POST', headers: { 'X-Simon-Token': token }, body: file });
  let data = null;
  try { data = await res.json(); } catch { /* empty body */ }
  if (!res.ok) throw failure(data, res.status, `STEP import failed (HTTP ${res.status})`);
  return data;
}

// Uploads an STL file. The server validates it first (it must be one closed, watertight solid) and
// only then adds it as a body; a refused file rejects with the reasons (code "mesh_has_holes" when
// holes are the only fault: retry with fillHoles). Resolves to the tool outcome.
export async function uploadMesh(file, { units = '', placement = '', fillHoles = false } = {}) {
  const q = new URLSearchParams({ name: file.name });
  if (units) q.set('units', units);
  if (placement) q.set('placement', placement);
  if (fillHoles) q.set('fill_holes', '1');
  const res = await fetch(`/api/mesh?${q}`, { method: 'POST', headers: { 'X-Simon-Token': token }, body: file });
  let data = null;
  try { data = await res.json(); } catch { /* empty body */ }
  if (!res.ok) throw failure(data, res.status, `Mesh import failed (HTTP ${res.status})`);
  return data;
}

// <img src> cannot send headers, so the token travels in the query string for images.
export const imageUrl = (id) => `/api/image/${encodeURIComponent(id)}?token=${encodeURIComponent(token)}`;

// An attached document as stored inside the project (opened from the chat).
export const documentUrl = (id) => `/api/document/${encodeURIComponent(id)}?token=${encodeURIComponent(token)}`;

// Offscreen renders and silhouette differences of the visual refinement.
export const renderUrl = (id) => `/api/render/${encodeURIComponent(id)}?token=${encodeURIComponent(token)}`;
export const diffUrl = (iteration, ref, previous) =>
  `/api/visual/diff?iteration=${iteration}&ref=${encodeURIComponent(ref)}${previous ? '&previous=1' : ''}&token=${encodeURIComponent(token)}&t=${Date.now()}`;

// Texture files of a material (loaded by Three's TextureLoader, which cannot set headers).
export const materialUrl = (id, file) => `/api/materials/${encodeURIComponent(id)}/${encodeURIComponent(file)}?token=${encodeURIComponent(token)}`;

// Server-sent notices; reconnects on its own (EventSource). onStatus('lost') / onStatus('open')
// report the connection, because notices sent while it was down are gone: the caller should
// refetch the state when it comes back.
export function subscribe(onNotice, onStatus) {
  const es = new EventSource('/api/events?token=' + encodeURIComponent(token));
  es.onmessage = (e) => { try { onNotice(JSON.parse(e.data)); } catch { /* ignore malformed */ } };
  es.onopen = () => onStatus?.('open');
  es.onerror = () => onStatus?.('lost');
  return es;
}

// h builds DOM without innerHTML: model-provided strings (intents, explanations) are
// untrusted text and must never be parsed as markup.
export function h(tag, attrs, ...kids) {
  const el = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs ?? {})) {
    if (v === undefined || v === null || v === false) continue;
    if (k === 'class') el.className = v;
    else if (k.startsWith('on') && typeof v === 'function') el.addEventListener(k.slice(2), v);
    else if (k === 'dataset') Object.assign(el.dataset, v);
    else el.setAttribute(k, v === true ? '' : v);
  }
  for (const kid of kids.flat()) {
    if (kid === undefined || kid === null || kid === false) continue;
    el.append(kid.nodeType ? kid : document.createTextNode(String(kid)));
  }
  return el;
}

const SVG_NS = 'http://www.w3.org/2000/svg';
/** svg builds SVG elements like h builds HTML ones (no innerHTML; text via createTextNode). */
export function svg(tag, attrs, ...kids) {
  const el = document.createElementNS(SVG_NS, tag);
  for (const [k, v] of Object.entries(attrs ?? {})) {
    if (v === undefined || v === null || v === false) continue;
    if (k === 'class') el.setAttribute('class', v);
    else el.setAttribute(k, v === true ? '' : v);
  }
  for (const kid of kids.flat()) {
    if (kid === undefined || kid === null || kid === false) continue;
    el.append(kid.nodeType ? kid : document.createTextNode(String(kid)));
  }
  return el;
}
