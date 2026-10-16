// The Markdown the agent answers with, parsed into a small tree apart from the DOM (markdown.js renders it).
// It covers what chat models actually write: paragraphs, headings, bullet and numbered lists (nested by
// indentation), block quotes, fenced code, horizontal rules, simple tables, and inline **bold**, *italic*,
// ~~strike~~, `code` and [links](https://…). Anything it does not recognise stays literal text, and nothing is
// ever turned into HTML, so a model's output cannot inject markup. A single newline inside a paragraph is kept
// as a line break: models use it on purpose.
//
// Blocks: { t: 'p', c } | { t: 'h', level, c } | { t: 'list', ordered, start, items: [blocks[]] }
//       | { t: 'quote', c: blocks } | { t: 'code', lang, v } | { t: 'hr' } | { t: 'table', align, head, rows }
// Inline: { t: 'text', v } | { t: 'strong' | 'em' | 'del', c } | { t: 'code', v } | { t: 'link', href, c } | { t: 'br' }

const ITEM = /^( *)([*+-]|\d{1,9}[.)])( +|$)(.*)$/;
const FENCE = /^ {0,3}(`{3,}|~{3,})\s*([\w+#.-]*)/;
const HEADING = /^ {0,3}(#{1,6})\s+(.*?)\s*#*\s*$/;
const HR = /^ {0,3}([-*_])( *\1){2,} *$/;
const QUOTE = /^ {0,3}> ?(.*)$/;
const TABLE_SEP = /^ *\|? *:?-+:? *(\| *:?-+:? *)*\|? *$/;

const indentOf = (s) => s.match(/^ */)[0].length;
const blank = (s) => s.trim() === '';

export function parseMarkdown(text) {
  return parseBlocks(String(text ?? '').replace(/\r\n?/g, '\n').replace(/\t/g, '    ').split('\n'));
}

// A line that starts a block of its own, so it ends a paragraph (or a lazily continued list item).
function startsBlock(lines, i) {
  const s = lines[i];
  return FENCE.test(s) || HEADING.test(s) || HR.test(s) || QUOTE.test(s) || ITEM.test(s) || isTable(lines, i);
}

function isTable(lines, i) {
  return lines[i].includes('|') && i + 1 < lines.length && lines[i + 1].includes('-') && TABLE_SEP.test(lines[i + 1]);
}

function parseBlocks(lines) {
  const out = [];
  let i = 0;
  while (i < lines.length) {
    const s = lines[i];
    if (blank(s)) { i++; continue; }
    let m;
    if ((m = FENCE.exec(s))) {
      const close = new RegExp(`^ {0,3}${m[1][0]}{${m[1].length},}\\s*$`);
      const body = [];
      for (i++; i < lines.length && !close.test(lines[i]); i++) body.push(lines[i]);
      i++; // the closing fence (or the end of an unterminated block)
      out.push({ t: 'code', lang: m[2], v: body.join('\n') });
    } else if ((m = HEADING.exec(s))) {
      out.push({ t: 'h', level: m[1].length, c: parseInline(m[2]) });
      i++;
    } else if (HR.test(s)) {
      out.push({ t: 'hr' });
      i++;
    } else if (QUOTE.test(s)) {
      const body = [];
      for (; i < lines.length && !blank(lines[i]); i++) {
        const q = QUOTE.exec(lines[i]);
        if (!q && startsBlock(lines, i)) break;
        body.push(q ? q[1] : lines[i]);
      }
      out.push({ t: 'quote', c: parseBlocks(body) });
    } else if (ITEM.test(s)) {
      i = parseList(lines, i, out);
    } else if (isTable(lines, i)) {
      i = parseTable(lines, i, out);
    } else {
      const para = [s.trim()];
      for (i++; i < lines.length && !blank(lines[i]) && !startsBlock(lines, i); i++) para.push(lines[i].trim());
      out.push({ t: 'p', c: parseInline(para.join('\n')) });
    }
  }
  return out;
}

// One list: its items, each parsed as blocks of its own (which is how nested lists come out). An item owns the
// lines indented past its marker; a blank line keeps the list going when more of it follows.
function parseList(lines, i, out) {
  const first = ITEM.exec(lines[i]);
  const ordered = /\d/.test(first[2]);
  const list = { t: 'list', ordered, start: ordered ? parseInt(first[2], 10) : 1, items: [] };
  const base = first[1].length;
  const sibling = (j) => {
    const m = j < lines.length && ITEM.exec(lines[j]);
    return m && /\d/.test(m[2]) === ordered && m[1].length <= base + 1 ? m : null;
  };
  let m = first;
  while (m) {
    const marker = m[1].length;
    const content = marker + m[2].length + (m[3].length > 4 || m[3].length === 0 ? 1 : m[3].length);
    const body = [m[4]];
    i++;
    while (i < lines.length) {
      const s = lines[i];
      if (blank(s)) {
        let j = i + 1;
        while (j < lines.length && blank(lines[j])) j++;
        if (j < lines.length && indentOf(lines[j]) >= content) { body.push(''); i = j; continue; }
        if (sibling(j)) i = j;
        break;
      }
      const ind = indentOf(s);
      if (ind >= content || (ind > marker && ITEM.test(s))) { body.push(s.slice(Math.min(ind, content))); i++; continue; }
      if (ITEM.test(s) || startsBlock(lines, i) || body.at(-1) === '') break;
      body.push(s.trim()); // a lazy continuation of the item's text
      i++;
    }
    list.items.push(parseBlocks(body));
    m = sibling(i);
  }
  out.push(list);
  return i;
}

function parseTable(lines, i, out) {
  const cells = (s) => s.trim().replace(/^\|/, '').replace(/\|$/, '').split('|').map((c) => c.trim());
  const align = cells(lines[i + 1]).map((c) => (c.endsWith(':') ? (c.startsWith(':') ? 'center' : 'right') : c.startsWith(':') ? 'left' : null));
  const head = cells(lines[i]).map(parseInline);
  const rows = [];
  for (i += 2; i < lines.length && !blank(lines[i]) && lines[i].includes('|'); i++) rows.push(cells(lines[i]).map(parseInline));
  out.push({ t: 'table', align, head, rows });
  return i;
}

const alnum = (ch) => !!ch && /[\p{L}\p{N}]/u.test(ch);
const space = (ch) => !ch || /\s/.test(ch);

// Where the emphasis opened with delim at i closes, or -1. `_` never opens or closes inside a word, so
// snake_case names and ids like image_o1 stay as they are.
function closing(s, i, delim) {
  const n = delim.length;
  if (space(s[i + n]) || (delim[0] === '_' && alnum(s[i - 1]))) return -1;
  for (let j = i + n + 1; j <= s.length - n; j++) {
    if (s[j] === '`') { const e = s.indexOf('`', j + 1); if (e > 0) { j = e; continue; } }
    if (s[j] === '\\') { j++; continue; }
    if (s.startsWith(delim, j) && !space(s[j - 1])) {
      if (n === 1 && s[j + 1] === delim) { j++; continue; } // part of a ** inside
      if (delim[0] === '_' && alnum(s[j + n])) continue;
      return j;
    }
  }
  return -1;
}

export function parseInline(s) {
  const out = [];
  let text = '';
  const flush = () => { if (text) out.push({ t: 'text', v: text }); text = ''; };
  const push = (node) => { flush(); out.push(node); };
  for (let i = 0; i < s.length;) {
    const ch = s[i];
    if (ch === '\\' && /[!-/:-@[-`{-~]/.test(s[i + 1] ?? '')) { text += s[i + 1]; i += 2; continue; }
    if (ch === '\n') { push({ t: 'br' }); i++; continue; }
    if (ch === '`') {
      const run = s.slice(i).match(/^`+/)[0];
      const e = s.indexOf(run, i + run.length);
      if (e > 0 && s[e + run.length] !== '`') {
        let v = s.slice(i + run.length, e).replace(/\n/g, ' ');
        if (/^ .*[^ ].* $/.test(v)) v = v.slice(1, -1);
        push({ t: 'code', v });
        i = e + run.length;
        continue;
      }
      text += run; i += run.length; continue;
    }
    if (ch === '*' || ch === '_' || ch === '~') {
      const two = s[i + 1] === ch;
      const delims = ch === '~' ? (two ? ['~~'] : []) : two ? [ch + ch, ch] : [ch];
      const delim = delims.find((d) => closing(s, i, d) >= 0);
      if (!delim) { text += two ? ch + ch : ch; i += two ? 2 : 1; continue; }
      const e = closing(s, i, delim);
      push({ t: delim === '~~' ? 'del' : delim.length === 2 ? 'strong' : 'em', c: parseInline(s.slice(i + delim.length, e)) });
      i = e + delim.length;
      continue;
    }
    if (ch === '[') {
      const m = /^\[([^\]\n]+)\]\(\s*(https?:\/\/[^\s)]+|mailto:[^\s)]+)\s*\)/.exec(s.slice(i));
      if (m) { push({ t: 'link', href: m[2], c: parseInline(m[1]) }); i += m[0].length; continue; }
    }
    text += ch; i++;
  }
  flush();
  return out;
}
