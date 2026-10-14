// The rules of the toast line, apart from the DOM and the timers (app.js applies the actions).
// Messages queue instead of overwriting each other: a new one shows at once unless the current
// one has been up for less than MIN_MS, in which case it waits its turn (at most MAX_WAITING
// wait; the oldest is dropped). Errors and warnings are urgent: they replace an informational
// message that is still fresh instead of waiting behind it (two urgent ones in a row do queue, so
// neither is lost). A repeat of what is showing or waiting is not added again, and a backlog
// drains at a shorter duration.

export const MIN_MS = 1500, BACKLOG_MS = 2500, MAX_WAITING = 3;
const urgent = (kind) => kind === 'bad' || kind === 'warn';

export class ToastQueue {
  constructor() { this.cur = null; this.shownAt = 0; this.waiting = []; }

  _show(n, now) {
    this.cur = n; this.shownAt = now;
    return { show: n, ms: this.waiting.length ? Math.min(n.ms, BACKLOG_MS) : n.ms };
  }

  /**
   * A message arrives. Returns what the view must do:
   *   { show, ms }   display `show` now and call next() after `ms`
   *   { extend: ms } the same message is already up: keep it up for ms more
   *   { wait: ms }   it is queued; call next() after `ms` (when the current one has had its minimum)
   *   {}             nothing (a repeat of a waiting message)
   */
  push(msg, kind, ms, now) {
    const same = (q) => q.msg === msg && q.kind === kind;
    if (this.cur && same(this.cur)) return { extend: ms };
    if (this.waiting.some(same)) return {};
    const n = { msg, kind, ms };
    const up = now - this.shownAt;
    if (!this.cur || up >= MIN_MS || (urgent(kind) && !urgent(this.cur.kind))) return this._show(n, now);
    this.waiting.push(n);
    if (this.waiting.length > MAX_WAITING) this.waiting.shift();
    return { wait: MIN_MS - up };
  }

  /** The timer fired: show the next waiting message, or hide. */
  next(now) {
    const n = this.waiting.shift();
    if (n) return this._show(n, now);
    this.cur = null;
    return { hide: true };
  }
}
