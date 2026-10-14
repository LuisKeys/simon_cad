// The modal dialogs: yes/no confirmation, a form of typed fields, and a one-line question.
import { h } from './api.js';
import { units } from './units.js';
import { $ } from './dom.js';

// confirmBox resolves true/false; with `remember` it shows a "Remember my choice" box and resolves { ok, remember }.
export function confirmBox({ title, body, ok = 'Delete', okClass = 'danger', remember = false }) {
  return new Promise((resolve) => {
    const d = $('confirm');
    $('confirm-title').textContent = title;
    $('confirm-body').textContent = body;
    $('confirm-ok').textContent = ok;
    $('confirm-ok').className = okClass;
    $('confirm-remember').hidden = !remember;
    $('confirm-remember-box').checked = true;
    d.hidden = false;
    $('confirm-cancel').focus();
    const close = (v) => {
      d.hidden = true; $('confirm-form').onsubmit = null; $('confirm-cancel').onclick = null;
      resolve(remember ? { ok: v, remember: $('confirm-remember-box').checked } : v);
    };
    $('confirm-form').onsubmit = (e) => { e.preventDefault(); close(true); };
    $('confirm-cancel').onclick = () => close(false);
  });
}

export function formDialog({ title, fields, ok = 'Apply' }) {
  return new Promise((resolve) => {
    const d = $('formdlg');
    $('fd-title').textContent = title;
    $('fd-ok').textContent = ok;
    const inputs = fields.map((f) => {
      const id = `fd-${f.key}`;
      // Length fields hold mm and angle fields degrees; they are shown and typed in the display units.
      const shown = (v) => (f.length ? Number(units.toDisplay(v).toFixed(6)) : f.angle ? Number(units.angleToDisplay(v).toFixed(6)) : v);
      const input = f.options
        ? h('select', { id }, ...f.options.map((o) => h('option', { value: o.value ?? o, selected: (o.value ?? o) === f.value }, o.label ?? o)))
        : f.text ? h('input', { id, type: 'text', value: String(f.value ?? ''), spellcheck: 'false' })
          : h('input', { id, type: 'number', step: 'any', value: String(shown(f.value ?? 0)), min: f.min === undefined ? undefined : shown(f.min), title: f.hint });
      return [f, input];
    });
    $('fd-fields').replaceChildren(...inputs.flatMap(([f, i]) => [h('label', { for: i.id }, f.label), i]));
    d.hidden = false;
    inputs[0][1].focus(); inputs[0][1].select?.();
    const close = (v) => { d.hidden = true; $('fd-form').onsubmit = null; $('fd-cancel').onclick = null; resolve(v); };
    $('fd-form').onsubmit = (e) => {
      e.preventDefault();
      const out = {};
      for (const [f, i] of inputs) {
        out[f.key] = f.options || f.text ? i.value : Number(i.value);
        if (f.length) out[f.key] = units.toMM(out[f.key]);
        else if (f.angle) out[f.key] = units.angleToDeg(out[f.key]);
      }
      close(out);
    };
    $('fd-cancel').onclick = () => close(null);
  });
}

export function ask({ title, value = '', format = false, ok = 'OK', choices = null }) {
  return new Promise((resolve) => {
    const d = $('dialog'), input = $('dialog-input'), fmt = $('dialog-format');
    $('dialog-title').textContent = title;
    input.value = value; fmt.hidden = !format; $('dialog-ok').textContent = ok;
    const list = $('dialog-list');
    list.replaceChildren();
    list.hidden = !choices;
    if (choices) {
      if (!choices.length) {
        const li = document.createElement('li'); li.className = 'd'; li.textContent = 'No saved projects yet'; list.append(li);
      }
      for (const c of choices) {
        const li = document.createElement('li');
        const k = document.createElement('span'); k.textContent = c.label;
        const dsc = document.createElement('span'); dsc.className = 'd'; dsc.textContent = c.detail ?? '';
        li.append(k, dsc);
        li.onclick = () => close({ value: c.value, format: fmt.value });
        list.append(li);
      }
    }
    d.hidden = false; input.focus(); input.select();
    const close = (v) => { d.hidden = true; list.hidden = true; $('dialog-form').onsubmit = null; $('dialog-cancel').onclick = null; resolve(v); };
    $('dialog-form').onsubmit = (e) => { e.preventDefault(); close({ value: input.value.trim(), format: fmt.value }); };
    $('dialog-cancel').onclick = () => close(null);
  });
}
