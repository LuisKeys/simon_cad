// Thin client for the Simon CAD UI service. Every call maps one-to-one onto a ui.Service
// method, which is also what the Wails shell binds.
const token = document.querySelector('meta[name="simon-token"]')?.content ?? '';

export async function api(path, body) {
  const res = await fetch('/api/' + path, {
    method: body === undefined ? 'GET' : 'POST',
    headers: { 'X-Simon-Token': token, 'Content-Type': 'application/json' },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  let data = null;
  try { data = await res.json(); } catch { /* empty body */ }
  if (!res.ok) throw failure(data, res.status, `${path}: HTTP ${res.status}`);
  return data;
}

// Binary reply (the print simulation's toolpath buffer).
export async function apiBytes(path) {
  const res = await fetch('/api/' + path, { headers: { 'X-Simon-Token': token } });
  if (!res.ok) {
    let data = null;
    try { data = await res.json(); } catch { /* empty body */ }
    throw failure(data, res.status, `${path}: HTTP ${res.status}`);
  }
  return res.arrayBuffer();
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

// <img src> cannot send headers, so the token travels in the query string for images.
export const imageUrl = (id) => `/api/image/${encodeURIComponent(id)}?token=${encodeURIComponent(token)}`;

// Offscreen renders and silhouette differences of the visual refinement.
export const renderUrl = (id) => `/api/render/${encodeURIComponent(id)}?token=${encodeURIComponent(token)}`;
export const diffUrl = (iteration, ref, previous) =>
  `/api/visual/diff?iteration=${iteration}&ref=${encodeURIComponent(ref)}${previous ? '&previous=1' : ''}&token=${encodeURIComponent(token)}&t=${Date.now()}`;

// Texture files of a material (loaded by Three's TextureLoader, which cannot set headers).
export const materialUrl = (id, file) => `/api/materials/${encodeURIComponent(id)}/${encodeURIComponent(file)}?token=${encodeURIComponent(token)}`;

// Server-sent notices; reconnects on its own (EventSource), and callers refetch state.
export function subscribe(onNotice) {
  const es = new EventSource('/api/events?token=' + encodeURIComponent(token));
  es.onmessage = (e) => { try { onNotice(JSON.parse(e.data)); } catch { /* ignore malformed */ } };
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
