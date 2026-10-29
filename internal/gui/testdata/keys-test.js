// Keyboard-map driver, served only by the layout-guard harness under
// ?keytest=1. It replays a scripted sequence of real KeyboardEvents and
// WheelEvents on the live page — not a mock keymap — and after each step
// records the bridge calls it caused plus the face's state (selection,
// scroll offset, tab, fold). The Go test holds the expectation table and
// asserts every row from <pre id="keys">.
import { ui } from '/js/console.js';

const out = { steps: [], errors: [] };
const settle = () => new Promise((r) => setTimeout(r, 60));

// Dispatch on the focused element so an input's own listeners run first,
// then bubble to the window map — the same path a real keypress takes.
const target = () => {
  const a = document.activeElement;
  return a && a !== document.body ? a : window;
};
const key = (k, o = {}) =>
  target().dispatchEvent(new KeyboardEvent('keydown', { key: k, bubbles: true, cancelable: true, ...o }));
const wheel = (el, dy, o = {}) => {
  const ev = new WheelEvent('wheel', { deltaY: dy, bubbles: true, cancelable: true, ...o });
  el.dispatchEvent(ev);
  return ev;
};
const type = (el, v) => { el.value = v; el.dispatchEvent(new Event('input', { bubbles: true })); };

const q = (sel) => document.querySelector(sel);
const fmtCall = (c) => c[0] + '(' + c.slice(1).map((a) => JSON.stringify(a)).join(',') + ')';
const focusedBar = () =>
  !!(document.activeElement && document.activeElement.classList &&
    document.activeElement.classList.contains('findin'));
const rec = () => ({
  sel: ui.state.sel,
  top: ui.state.top,
  tab: ui.state.tab,
  unf: ui.unfolded(),
  rowTop: (q('[data-id="row-top"]') || {}).textContent || null,
  remove: (q('[data-id="act-remove"]') || {}).textContent || null,
  filterInput: !!q('[data-id="filter-input"]'),
  barFocused: focusedBar(),
  barValue: (q('.findin') || {}).value ?? null,
  rowLit: !!q('.crow.sel'),
});
const run = async (name, fn, extra) => {
  const before = window.__calls.length;
  const r = await fn();
  await settle();
  out.steps.push({
    name,
    calls: window.__calls.slice(before).map(fmtCall),
    ...rec(),
    ...(r instanceof Event ? { prevented: r.defaultPrevented } : {}),
    ...(extra || {}),
  });
};

const firstRow = () => q('.crow');
const clickRow = () => {
  firstRow().dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true }));
};
const wait = (ms) => new Promise((r) => setTimeout(r, ms));

(async () => {
  const foldedOnly = new URLSearchParams(location.search).get('keytest') === 'folded';

  // Wait for the first full render before driving — a folded fixture has
  // no rows to wait on, just the strip's paint.
  const deadline = Date.now() + 2000;
  while (!(window.__rendered && (foldedOnly || q('.crow')))) {
    if (Date.now() > deadline) { out.errors.push('never rendered'); break; }
    await settle();
  }

  if (foldedOnly) {
    // Row keys on a hidden list: every one must be a no-op.
    for (const k of ['x', 'x', 'Enter', 'j', 'r']) {
      await run('folded-' + k, () => key(k));
    }
    // Folded, "/" and the MORE button both open the console on FIND —
    // from LOVED, which the fixture leaves selected.
    await run('folded-slash', () => key('/'));
    await run('folded-back-to-loved', () => ui.setTab('loved'));
    await run('folded-more-click', () => q('#btnMore').dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true })));
    out.errors = (window.__errors || []).slice();
    const pre = document.createElement('pre');
    pre.id = 'keys';
    pre.style.display = 'none';
    pre.textContent = JSON.stringify(out);
    document.body.appendChild(pre);
    return;
  }

  // ---- row movement (every move re-reads the panel's data) ----------
  await run('j', () => key('j'));
  await run('j-repeat', () => key('j', { repeat: true }));
  await run('k', () => key('k'));
  await run('arrow-down', () => key('ArrowDown'));
  await run('pgdn', () => key('PageDown'));
  await run('pgup', () => key('PageUp'));
  await run('end', () => key('End'));
  await run('home', () => key('Home'));

  // ---- strip intents -------------------------------------------------
  await run('space-repeat', () => key(' ', { repeat: true }));
  await run('space', () => key(' '));
  await run('n', () => key('n'));
  await run('l', () => key('l'));
  await run('p', () => key('p'));
  await run('z', () => key('z'));
  await run('f', () => key('f'));
  await run('digit', () => key('3'));
  await run('vol-down', () => key('-'));
  await run('vol-up', () => key('='));
  await run('q', () => key('q'));

  // ---- tabs ----------------------------------------------------------
  await run('tab', () => key('Tab'));
  await run('shift-tab', () => key('Tab', { shiftKey: true }));
  await run('i', () => key('i'));
  await run('h', () => key('h'));
  await run('enter-recent', () => key('Enter')); // RECENT has no primary
  await run('H', () => key('H'));

  // ---- row actions ---------------------------------------------------
  await run('x-arm', () => key('x'));
  await run('x-remove', () => key('x'));
  await run('r', () => key('r'));
  await run('enter-loved', () => key('Enter')); // LOVED: more from artist

  // ---- manual double-click -------------------------------------------
  // A click selects (repaint, no refetch); a same-row click inside
  // 400 ms is the double-click the platform won't deliver after the
  // first click's repaint. Virtual time makes the waits real.
  await run('click', () => clickRow());
  await wait(100);
  await run('click-100ms', () => clickRow());
  await wait(500);
  await run('click-far-1', () => clickRow());
  await wait(500);
  await run('click-far-2', () => clickRow());

  // ---- wheel ---------------------------------------------------------
  await run('wheel-row', () => wheel(firstRow(), 28));
  await run('wheel-row-back', () => wheel(firstRow(), -28));
  await run('wheel-vol', () => wheel(document.getElementById('volTrack'), -120));
  await run('ctrl-wheel', () => wheel(window, -120, { ctrlKey: true }));

  // ---- filter --------------------------------------------------------
  await run('slash-filter', () => key('/'));
  await run('filter-letter', () => key('l')); // swallowed by the input
  await run('filter-type', () => { type(q('[data-id="filter-input"]'), 'zz'); key('Enter'); });
  await run('esc-filter-text', () => key('Escape'));
  await run('esc-filter-close', () => key('Escape'));
  // A filter left behind after the input closed: ESC clears it before
  // anything folds.
  await run('filter-leftover',
    () => { ui.state.filter = 'ab'; ui.state.filterOpen = false; return ui.renderPanel(); });
  await run('esc-filter-leftover', () => key('Escape'));

  // ---- FIND, RADIO and ESC order -------------------------------------
  // "/" from MORE lands on FIND with the bar focused; letters are the
  // bar's; Enter on nothing does nothing.
  await run('i-again', () => key('i'));
  await run('slash-find', () => key('/'));
  await run('find-letter', () => key('l')); // swallowed
  await run('find-blank-enter', () => key('Enter'));
  await run('find-type', () => type(q('[data-id="bar-input"]'), 'swedish death metal'));
  // Tab leaves the bar for RADIO and the new bar takes focus; an empty
  // RADIO bar's space is the virgin wildcard, once per press.
  await run('find-tab', () => key('Tab'));
  await run('radio-space', () => key(' '));
  await run('radio-space-repeat', () => key(' ', { repeat: true }));
  await run('radio-type', () => type(q('[data-id="bar-input"]'), 'ambient'));
  await run('radio-enter', () => key('Enter'));
  // Back on FIND the phrase is still there; ↓ hands over to the rows and
  // ↑ from the first row takes the bar back.
  await run('radio-shift-tab', () => key('Tab', { shiftKey: true }));
  await run('find-down', () => key('ArrowDown'));
  await run('find-up', () => key('ArrowUp'));
  await run('find-down-again', () => key('ArrowDown'));
  await run('find-row-next', () => key('j'));
  await run('find-row-enter', () => key('Enter'));
  await run('find-row-click', () => clickRow());
  // "/" with the rows in hand returns to the bar; Enter starts a station.
  await run('slash-find-again', () => key('/'));
  await run('find-enter', () => key('Enter'));
  // ESC unwinds text first, then folds.
  await run('find-type-2', () => { const bar = q('.findin'); bar.focus(); type(bar, 'x'); });
  await run('esc-find-text', () => key('Escape'));
  await run('esc-fold', () => key('Escape'));

  out.errors = (window.__errors || []).slice();
  const pre = document.createElement('pre');
  pre.id = 'keys';
  pre.style.display = 'none';
  pre.textContent = JSON.stringify(out);
  document.body.appendChild(pre);
})();
