// Viewport screenshot: one PNG of the whole viewport as shown, Single or Quad. Every view is
// already drawn into the one shared WebGL canvas (preserveDrawingBuffer), so the capture is
// that canvas over the viewport's background, plus the overlays that live in the DOM: the
// axis gizmos, the dimension labels and, in Quad, the view names and the split lines.

// The #viewport CSS background (radial-gradient(ellipse at 50% 35%, inner, outer)) per theme.
const BACKGROUND = { dark: ['#1b1b1b', '#0f0f0f'], light: ['#fbfbfc', '#dfe2e7'] };

/** CSS radial-gradient(ellipse at 50% 35%) with its default farthest-corner size. */
function fillBackground(g, W, H) {
  const [inner, outer] = BACKGROUND[document.documentElement.dataset.theme === 'light' ? 'light' : 'dark'];
  const cx = W * 0.5, cy = H * 0.35;
  const rx = Math.max(cx, W - cx) * Math.SQRT2, ry = Math.max(cy, H - cy) * Math.SQRT2;
  g.save();
  g.translate(cx, cy);
  g.scale(rx, ry);
  const grad = g.createRadialGradient(0, 0, 0, 0, 0, 1);
  grad.addColorStop(0, inner);
  grad.addColorStop(1, outer);
  g.fillStyle = grad;
  g.fillRect(-cx / rx, -cy / ry, W / rx, H / ry);
  g.restore();
}

const shown = (el) => el && !el.hidden && el.offsetParent !== null && getComputedStyle(el).visibility !== 'hidden';

/** A text chip (view name, dimension value) drawn from its computed style. */
function drawChip(g, el, text, at, k) {
  const r = el.getBoundingClientRect(), cs = getComputedStyle(el);
  const x = (r.left - at.left) * k, y = (r.top - at.top) * k, w = r.width * k, h = r.height * k;
  const px = (v) => (parseFloat(v) || 0) * k;
  g.save();
  g.globalAlpha = parseFloat(cs.opacity) || 1;
  g.beginPath();
  g.roundRect(x, y, w, h, Math.min(px(cs.borderTopLeftRadius), h / 2));
  g.fillStyle = cs.backgroundColor;
  g.fill();
  if (px(cs.borderTopWidth) > 0) { g.lineWidth = px(cs.borderTopWidth); g.strokeStyle = cs.borderTopColor; g.stroke(); }
  g.font = `${cs.fontWeight} ${px(cs.fontSize)}px ${cs.fontFamily}`;
  if ('letterSpacing' in g) g.letterSpacing = `${px(cs.letterSpacing)}px`;
  g.fillStyle = cs.color;
  g.textBaseline = 'middle';
  if (cs.textAlign === 'center' || el.classList.contains('dim-label')) {
    g.textAlign = 'center';
    g.fillText(text, x + w / 2, y + h / 2);
  } else {
    g.fillText(text, x + px(cs.paddingLeft), y + h / 2);
  }
  g.restore();
}

/** Composes the viewport into a PNG blob. `vp` is the Viewport. */
export async function captureViewport(vp) {
  vp.render(); // the current state, not the last frame the loop happened to draw
  const gl = vp.canvas, host = gl.parentElement;
  const W = gl.width, H = gl.height, k = W / (gl.clientWidth || 1);
  const at = gl.getBoundingClientRect();
  const c = document.createElement('canvas');
  c.width = W; c.height = H;
  const g = c.getContext('2d');
  fillBackground(g, W, H);
  g.drawImage(gl, 0, 0);

  const quad = host.dataset.layout === 'quad'; // not a maximized view
  for (const pane of vp.layout.visible()) {
    g.save();
    g.beginPath(); // overlays stay inside their view, as on screen
    g.rect(pane.rect.x * k, pane.rect.y * k, pane.rect.w * k, pane.rect.h * k);
    g.clip();
    const gz = pane.gizmo.canvas;
    if (shown(gz)) {
      const r = gz.getBoundingClientRect();
      g.globalAlpha = parseFloat(getComputedStyle(gz).opacity) || 1;
      g.drawImage(gz, (r.left - at.left) * k, (r.top - at.top) * k, r.width * k, r.height * k);
      g.globalAlpha = 1;
    }
    for (const el of pane.el.querySelectorAll('.dim-label')) {
      if (shown(el) && !el.classList.contains('editing')) drawChip(g, el, el.textContent, at, k);
    }
    if (quad && shown(pane.label)) drawChip(g, pane.label, pane.labelText.textContent.toUpperCase(), at, k);
    g.restore();
  }
  if (quad) { // the split lines between the views
    g.fillStyle = getComputedStyle(host).getPropertyValue('--line').trim() || '#333';
    const { v, h } = vp.layout.dividers;
    const lw = Math.max(1, Math.round(k));
    if (shown(v)) g.fillRect(Math.round((v.getBoundingClientRect().left - at.left + 4) * k), 0, lw, H);
    if (shown(h)) g.fillRect(0, Math.round((h.getBoundingClientRect().top - at.top + 4) * k), W, lw);
  }
  const blob = await new Promise((res) => c.toBlob(res, 'image/png'));
  if (!blob) throw new Error('Could not encode the screenshot.');
  return blob;
}
