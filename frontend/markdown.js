// Renders the agent's Markdown (parsed by markdown-model.js) as DOM nodes. Text only ever becomes text nodes,
// never HTML, so a model's answer cannot inject markup.
import { h } from './api.js';
import { parseMarkdown } from './markdown-model.js';

function inline(nodes) {
  return nodes.map((n) => {
    switch (n.t) {
      case 'text': return n.v;
      case 'br': return h('br');
      case 'code': return h('code', {}, n.v);
      case 'link': return h('a', { href: n.href, target: '_blank', rel: 'noopener noreferrer' }, inline(n.c));
      default: return h(n.t, {}, inline(n.c));
    }
  });
}

function block(b) {
  switch (b.t) {
    case 'p': return h('p', {}, inline(b.c));
    case 'h': return h(`h${Math.min(b.level + 2, 6)}`, { class: 'md-h' }, inline(b.c));
    case 'hr': return h('hr');
    case 'code': return h('pre', {}, h('code', b.lang ? { 'data-lang': b.lang } : {}, b.v));
    case 'quote': return h('blockquote', {}, b.c.map(block));
    // An item whose text is a single paragraph shows it inline, so tight lists stay tight.
    case 'list': return h(b.ordered ? 'ol' : 'ul', b.ordered && b.start !== 1 ? { start: b.start } : {},
      b.items.map((it) => h('li', {}, it.flatMap((x, k) => (k === 0 && x.t === 'p' ? inline(x.c) : block(x))))));
    case 'table': {
      const cell = (tag) => (c, k) => h(tag, b.align[k] ? { style: `text-align:${b.align[k]}` } : {}, inline(c));
      return h('div', { class: 'md-table' }, h('table', {},
        h('thead', {}, h('tr', {}, b.head.map(cell('th')))),
        h('tbody', {}, b.rows.map((r) => h('tr', {}, r.map(cell('td')))))));
    }
    default: return null;
  }
}

export function markdown(text) {
  return parseMarkdown(text).map(block);
}
