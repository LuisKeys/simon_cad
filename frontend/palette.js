// The command palette (Ctrl+K): filter every tool, intent verb and app command, fill a tool's
// arguments as JSON, or hand a sentence to the agent. The rules are in palette-model.js; what runs
// an app command (undo, save, ...) belongs to app.js and arrives as env.command.
import { h } from './api.js';
import { $ } from './dom.js';
import { filterEntries, prefillSelection } from './palette-model.js';

/**
 * env: { S, api, toast, callTool, autosize, command(id) }. `S.paletteAll` caches the server's
 * entries; `S.selection` prefills arguments. Returns { open, close, toggle, run, openTool }.
 */
export function initPalette(env) {
  const { S, api, toast } = env;
  const pal = { items: [], idx: 0 };

  async function open() {
    $('palette').hidden = false;
    $('pal-form').hidden = true; $('pal-list').hidden = false;
    $('pal-input').value = '';
    $('pal-input').focus();
    if (!S.paletteAll.length) S.paletteAll = await api('palette');
    filter($('pal-input').value); // the user may already be typing
  }
  function close() { $('palette').hidden = true; }
  const toggle = () => ($('palette').hidden ? open() : close());

  function filter(q) {
    pal.items = filterEntries(S.paletteAll, q);
    pal.idx = 0;
    draw();
  }
  function draw() {
    $('pal-list').replaceChildren(...pal.items.map((e, i) => h('li', { class: i === pal.idx ? 'on' : '', onclick: () => run(e), onmousemove: () => { pal.idx = i; draw(); } },
      h('span', { class: 'k' }, e.kind), h('span', {}, e.title), h('span', { class: 'd' }, e.description))));
    $('pal-list').children[pal.idx]?.scrollIntoView({ block: 'nearest' });
  }

  async function run(e) {
    if (e.kind === 'ask') { close(); $('prompt').value = e.template; $('composer').requestSubmit(); return; }
    if (e.kind === 'intent') { close(); $('prompt').value = e.template; env.autosize(); $('prompt').focus(); return; }
    if (e.kind === 'tool') {
      $('pal-list').hidden = true; $('pal-form').hidden = false;
      $('pal-form-title').textContent = `${e.title} \u2014 ${e.description}`;
      $('pal-args').value = JSON.stringify(prefillSelection(JSON.parse(e.template || '{}'), e, S.selection?.selectors), null, 2);
      $('pal-run').onclick = async () => {
        let args;
        try { args = JSON.parse($('pal-args').value || '{}'); } catch (err) { toast('Arguments are not valid JSON: ' + err.message, 'bad'); return; }
        close();
        await env.callTool(e.id, args);
      };
      $('pal-back').onclick = () => { $('pal-form').hidden = true; $('pal-list').hidden = false; $('pal-input').focus(); };
      $('pal-args').focus();
      return;
    }
    close();
    return env.command(e.id);
  }

  // The palette opened on one tool's argument form (the action bar's "tool dialog").
  async function openTool(name) {
    if (!S.paletteAll.length) { try { S.paletteAll = await api('palette'); } catch (e) { toast(e.message, 'bad'); return; } }
    const e = S.paletteAll.find((x) => x.kind === 'tool' && x.id === name);
    if (!e) { toast(`${name} is not available`, 'bad'); return; }
    const tpl = JSON.parse(e.template || '{}');
    const sels = S.selection?.selectors ?? [];
    if (sels.some((x) => x.startsWith('face(')) && e.schema?.properties?.face !== undefined) tpl.face = '';
    $('palette').hidden = false;
    run({ ...e, template: JSON.stringify(tpl) });
  }

  $('pal-input').addEventListener('input', (e) => filter(e.target.value));
  $('pal-input').addEventListener('keydown', (e) => {
    if (e.key === 'ArrowDown') { e.preventDefault(); pal.idx = Math.min(pal.idx + 1, pal.items.length - 1); draw(); }
    else if (e.key === 'ArrowUp') { e.preventDefault(); pal.idx = Math.max(pal.idx - 1, 0); draw(); }
    else if (e.key === 'Enter') { e.preventDefault(); if (pal.items[pal.idx]) run(pal.items[pal.idx]); }
  });
  $('palette').addEventListener('mousedown', (e) => { if (e.target === $('palette')) close(); });

  return { open, close, toggle, run, openTool };
}
