// Layout guard, served only by the test harness. It polls until the page
// reports its first render (window.__rendered) and the webfont is loaded
// — timers advance under Edge's --virtual-time-budget where awaits on
// fonts.ready can outlive the budget — then measures the DOM in CSS px
// and writes JSON into <pre id="guard"> for --dump-dom.
(() => {
  const out = { violations: [], fixed: {}, cfixed: {} };
  const v = (msg) => out.violations.push(msg);
  const grid = (n) => Math.abs(n - Math.round(n)) < 0.51 && Math.round(n) % 4 === 0;
  const fmtR = (r) => `${r.left},${r.top} ${r.width}x${r.height}`;
  const rect = (el) => {
    const r = el.getBoundingClientRect();
    return { left: r.left, top: r.top, width: r.width, height: r.height };
  };
  // Hidden pieces (the console while folded, a hidden chip) report all-zero
  // rects; measuring them would flag phantoms.
  const gone = (el) => {
    const r = el.getBoundingClientRect();
    return r.width === 0 && r.height === 0;
  };
  const words = (s) => (s || '').replace(/\s+/g, ' ').trim();
  const tag = (el) => {
    const id = el.getAttribute('data-id') || el.id || el.className;
    return `<${el.tagName.toLowerCase()} ${id}>`;
  };

  const measure = () => {
    const winW = document.documentElement.clientWidth;
    const winH = window.innerHeight;
    // Report the viewport the page actually guarded against; Go fails any
    // run where vh isn't the requested page height, so a folded DOM can
    // never be measured in an unfolded window again.
    out.vw = winW;
    out.vh = winH;

    // Rule 1: the text column is x=136; filled rows/pills start at x=124.
    const cols = document.querySelectorAll('[data-col]');
    if (cols.length === 0) v('missing: no [data-col] elements');
    for (const el of cols) {
      if (gone(el)) continue;
      const r = rect(el);
      if (el.dataset.col === 'text' && r.left !== 136) {
        v(`col text ${tag(el)}: left ${r.left} != 136`);
      }
      if (el.dataset.col === 'fill' && r.left !== 124) {
        v(`col fill ${tag(el)}: left ${r.left} != 124`);
      }
    }

    // Rule 2: every [data-box] is on the 4-px grid.
    const boxes = document.querySelectorAll('[data-box]');
    if (boxes.length === 0) v('missing: no [data-box] elements');
    for (const el of boxes) {
      if (gone(el)) continue;
      const r = rect(el);
      for (const [k, val] of Object.entries(r)) {
        if (!grid(val)) {
          v(`box ${tag(el)}: ${k}=${val} is not a multiple of 4 (${fmtR(r)})`);
        }
      }
    }

    // Rule 3: every [data-text] fits inside its nearest [data-box]
    // ancestor and the window; it must not overflow its own box unless it
    // carries the ellipsis class (whose own rect must still fit).
    const texts = document.querySelectorAll('[data-text]');
    if (texts.length === 0) v('missing: no [data-text] elements');
    for (const el of texts) {
      if (gone(el)) continue;
      const r = el.getBoundingClientRect();
      const box = el.parentElement && el.parentElement.closest('[data-box]');
      if (!box) {
        v(`text ${tag(el)}: no [data-box] ancestor`);
      } else {
        const b = box.getBoundingClientRect();
        if (r.left < b.left - 0.5 || r.top < b.top - 0.5 ||
            r.right > b.right + 0.5 || r.bottom > b.bottom + 0.5) {
          v(`text ${tag(el)}: escapes its box ${tag(box)} (${fmtR(rect(el))} vs ${fmtR(rect(box))})`);
        }
      }
      if (r.left < -0.5 || r.top < -0.5 || r.right > winW + 0.5 || r.bottom > winH + 0.5) {
        v(`text ${tag(el)}: escapes the window (${fmtR(rect(el))} in ${winW}x${winH})`);
      }
      if (!el.classList.contains('ellipsis') && el.scrollWidth > el.clientWidth + 1) {
        v(`text ${tag(el)}: content overflows (${el.scrollWidth} > ${el.clientWidth}) without ellipsis`);
      }
    }

    // Rule 3b: a placeholder is text the rules above cannot see — it lives
    // in the input's shadow, so scrollWidth ignores it. Measure it the way
    // the input would set it (its own ::placeholder font and tracking) and
    // hold it to the room inside the input's padding.
    const ctx = document.createElement('canvas').getContext('2d');
    for (const el of document.querySelectorAll('input[placeholder]')) {
      if (gone(el) || el.value) continue;
      const ph = getComputedStyle(el, '::placeholder');
      const cs = getComputedStyle(el);
      ctx.font = `${ph.fontStyle} ${ph.fontWeight} ${ph.fontSize} ${ph.fontFamily}`;
      ctx.letterSpacing = ph.letterSpacing === 'normal' ? '0px' : ph.letterSpacing;
      const need = ctx.measureText(el.placeholder).width;
      const room = el.clientWidth - parseFloat(cs.paddingLeft) - parseFloat(cs.paddingRight);
      if (need > room + 0.5) {
        v(`placeholder ${tag(el)}: "${el.placeholder}" needs ${need.toFixed(1)} px, has ${room} px`);
      }
    }

    // Rule 3c: a button's label is never cut. The labels carry the
    // ellipsis class so a stray long string cannot spill, which also
    // silences Rule 3 — and PAUSE, NEXT and LOVE shrank to 80 px when EXIT
    // gave its room to MORE, so RESUME and RETRY NOW must still fit whole.
    for (const el of document.querySelectorAll('.btn [data-text], .barcap [data-text]')) {
      if (gone(el)) continue;
      if (el.scrollWidth > el.clientWidth + 1) {
        v(`label ${tag(el)}: "${words(el.textContent)}" is clipped (${el.scrollWidth} > ${el.clientWidth})`);
      }
    }

    // Rule 4: [data-leaf] elements never intersect, ignoring
    // ancestor/descendant pairs.
    const leaves = [...document.querySelectorAll('[data-leaf]')];
    if (leaves.length === 0) v('missing: no [data-leaf] elements');
    for (let i = 0; i < leaves.length; i++) {
      for (let j = i + 1; j < leaves.length; j++) {
        const a = leaves[i], b = leaves[j];
        if (a.contains(b) || b.contains(a)) continue;
        if (gone(a) || gone(b)) continue;
        const ra = a.getBoundingClientRect(), rb = b.getBoundingClientRect();
        if (ra.left < rb.right - 0.5 && rb.left < ra.right - 0.5 &&
            ra.top < rb.bottom - 0.5 && rb.top < ra.bottom - 0.5) {
          v(`leaves ${tag(a)} and ${tag(b)} intersect (${fmtR(rect(a))} vs ${fmtR(rect(b))})`);
        }
      }
    }

    // Rule 5: report the strip's fixed geometry — and the console's when
    // unfolded — for the Go side to compare across states.
    for (const [rootId, key] of [['strip', 'fixed'], ['console', 'cfixed']]) {
      const root = document.getElementById(rootId);
      if (!root) {
        v(`missing: #${rootId} element`);
        continue;
      }
      const fixed = [...root.querySelectorAll('[data-fixed]')].filter((el) => !gone(el));
      if (fixed.length === 0 && rootId === 'strip') v('missing: no #strip [data-fixed] elements');
      for (const el of fixed) {
        const r = rect(el);
        out[key][el.getAttribute('data-id') || '(no-id)'] = {
          l: r.left, t: r.top, w: r.width, h: r.height,
        };
      }
    }

    // Rule 6: content and style expectations — geometry passing can hide
    // wrong words. Expect maps data-id to exact collapsed textContent;
    // ExpectStyle maps data-id to computed-style property/value pairs.
    // An input's content is its value, not its textContent; "id@attr" pins
    // an attribute (a placeholder) instead of the text.
    const expect = window.__expect || {};
    for (const [key, want] of Object.entries(expect)) {
      const [id, attr] = key.split('@');
      const el = document.querySelector(`[data-id="${id}"]`);
      if (!el || gone(el)) {
        v(`expect ${key}: element missing`);
        continue;
      }
      const got = words(attr ? el.getAttribute(attr)
        : el.tagName === 'INPUT' ? el.value : el.textContent);
      if (got !== words(want)) {
        v(`expect ${key}: "${got}" != "${words(want)}"`);
      }
    }
    const styles = window.__expectStyle || {};
    for (const [id, props] of Object.entries(styles)) {
      const el = document.querySelector(`[data-id="${id}"]`);
      if (!el || gone(el)) {
        v(`expect-style ${id}: element missing`);
        continue;
      }
      const cs = getComputedStyle(el);
      for (const [prop, want] of Object.entries(props)) {
        // Custom properties aren't named members of the style object —
        // they only resolve through getPropertyValue (and keep their
        // authored whitespace, hence the trim).
        const got = prop.startsWith('--') ? cs.getPropertyValue(prop).trim() : cs[prop];
        if (got !== want) {
          v(`expect-style ${id}: ${prop} "${got}" != "${want}"`);
        }
      }
    }

    // Rule 6b: exact boxes. Geometry on the 4-px grid can still be the
    // wrong geometry; these are the approved mockup's coordinates, page px.
    const near = (a, b) => Math.abs(a - b) <= 0.5;
    for (const [id, want] of Object.entries(window.__expectRect || {})) {
      const el = document.querySelector(`[data-id="${id}"]`);
      if (!el || gone(el)) {
        v(`expect-rect ${id}: element missing`);
        continue;
      }
      const r = rect(el);
      const got = [r.left, r.top, r.width, r.height];
      if (!want.every((w, i) => near(w, got[i]))) {
        v(`expect-rect ${id}: ${got.join(',')} != ${want.join(',')} (left,top,width,height)`);
      }
    }

    // Rule 7: what was taken out stays out — EXIT gave way to the ✕ cap,
    // and SEARCH, INFO and SLEEP stopped being tabs — plus whatever a
    // fixture says must be absent (FIND's label with no rows under it).
    for (const id of ['btn-exit', 'tab-search', 'tab-info', 'tab-sleep', ...(window.__absent || [])]) {
      if (document.querySelector(`[data-id="${id}"]`)) v(`absent ${id}: element exists`);
    }
  };

  const write = () => {
    for (const e of window.__errors || []) out.violations.push('page error: ' + e);
    out.calls = (window.__calls || []).map((c) => c[0]);
    const pre = document.createElement('pre');
    pre.id = 'guard';
    pre.style.display = 'none';
    pre.textContent = JSON.stringify(out);
    document.body.appendChild(pre);
  };

  const deadline = Date.now() + 2400;
  const tick = () => {
    const ready = window.__rendered && document.fonts.status === 'loaded';
    if (ready) {
      measure();
      write();
    } else if (Date.now() > deadline) {
      v(`guard timed out waiting for render/font (rendered=${!!window.__rendered} fonts=${document.fonts.status})`);
      measure();
      write();
    } else {
      setTimeout(tick, 25);
    }
  };
  setTimeout(tick, 0);
})();
