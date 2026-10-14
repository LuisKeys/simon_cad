// The toast line: one message at a time, queued by the rules in toast-model.js.
import { ToastQueue } from './toast-model.js';
import { $ } from './dom.js';

let toastTimer = null;
// The queueing rules live in toast-model.js; this applies them to the DOM and the timer.
const toasts = new ToastQueue();
function applyToast(act) {
  const t = $('toast');
  if (act.show) {
    t.textContent = act.show.msg; t.className = `toast ${act.show.kind}`; t.hidden = false; t.style.whiteSpace = 'pre-line';
    t.setAttribute('role', act.show.kind === 'bad' ? 'alert' : 'status'); // announced by screen readers
    clearTimeout(toastTimer); toastTimer = setTimeout(() => applyToast(toasts.next(performance.now())), act.ms);
  } else if (act.extend !== undefined) {
    clearTimeout(toastTimer); toastTimer = setTimeout(() => applyToast(toasts.next(performance.now())), act.extend);
  } else if (act.wait !== undefined) {
    clearTimeout(toastTimer); toastTimer = setTimeout(() => applyToast(toasts.next(performance.now())), act.wait);
  } else if (act.hide) t.hidden = true;
}
export function toast(msg, kind = '', ms = 4500) { applyToast(toasts.push(msg, kind, ms, performance.now())); }
