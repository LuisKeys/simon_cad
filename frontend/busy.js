// A modal "please wait" dialog for work the server does in one go (importing a mesh): it covers
// the interface, shows the current stage with a spinner and a progress bar, and cannot be dismissed.
// The server reports stages as `progress` notices; the bar follows the stage's estimated duration.
import { $ } from './dom.js';
import { stageFraction, elapsedText, etaText } from './busy-model.js';

export function createBusy(now = () => performance.now()) {
  let open = false, started = 0, stageStarted = 0, eta = 0, timer = null;

  // The dialog is modal: while it is up no key reaches the rest of the page.
  const swallow = (e) => { e.preventDefault(); e.stopPropagation(); };

  function paint() {
    const t = now();
    const f = stageFraction(t - stageStarted, eta);
    const fill = $('busy-fill'), bar = $('busy-bar');
    bar.classList.toggle('indeterminate', f === null);
    fill.style.width = f === null ? '' : `${Math.round(f * 100)}%`;
    if (f === null) bar.removeAttribute('aria-valuenow'); else bar.setAttribute('aria-valuenow', String(Math.round(f * 100)));
    $('busy-time').textContent = `${elapsedText(t - started)} elapsed${eta ? ` · ${etaText(eta)} for this step` : ''}`;
  }

  const api = {
    get isOpen() { return open; },
    /** Shows the dialog (or brings it back after hide()). */
    show(title, stage = '') {
      if (title !== undefined) $('busy-title').textContent = title;
      if (!open) {
        open = true; started = now(); stageStarted = started; eta = 0;
        $('busy').hidden = false;
        window.addEventListener('keydown', swallow, true);
        window.addEventListener('keyup', swallow, true);
        timer = setInterval(paint, 200);
      }
      $('busy').hidden = false;
      if (stage) api.stage({ stage });
      paint();
    },
    /** A new stage: its text, and its estimated duration in ms (0 or absent: unknown). */
    stage({ stage, eta_ms: etaMs = 0 }) {
      if (!open) return;
      $('busy-stage').textContent = stage;
      stageStarted = now(); eta = etaMs;
      paint();
    },
    /** Hides the dialog without ending the operation (a question is being asked meanwhile). */
    hide() { $('busy').hidden = true; },
    close() {
      if (!open) return;
      open = false;
      clearInterval(timer); timer = null;
      window.removeEventListener('keydown', swallow, true);
      window.removeEventListener('keyup', swallow, true);
      $('busy').hidden = true;
    },
  };
  return api;
}
