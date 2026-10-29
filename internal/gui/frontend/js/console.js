// Console renderer: the unfolded 480x428 face — sidebar tabs, the panel
// for the selected tab, and the presets footer. Like the strip, it only
// reads snapshots and DTOs and sends intents; listening behaviour stays
// in the session engine.
import bridge from './bridge.js';

const $ = (id) => document.getElementById(id);
const unfolded = () => window.innerHeight >= 200;
const content = $('content');
const mk = (tag, cls) => { const e = document.createElement(tag); if (cls) e.className = cls; return e; };
const txt = (cls, id, text) => {
  const e = mk('span', cls);
  e.dataset.text = '';
  if (id) e.dataset.id = id;
  e.textContent = text;
  return e;
};

// NUL is the field separator wherever a key joins user text: it can never
// appear in a station name or track title.
const NUL = String.fromCharCode(0);

const state = {
  snap: null, tab: 'recent', sel: 0, top: 0, wheelAcc: 0,
  filter: '', filterOpen: false, pendingFocus: '', removeKey: '',
  search: '', rows: null, cfg: null, panelKey: '',
  lastClickIdx: -1, lastClickAt: 0,
};
const fx = bridge.fixture && bridge.fixture();
if (fx) {
  state.tab = fx.tab || 'recent';
  state.sel = fx.selected || 0;
  state.top = fx.top || 0;
  state.filter = fx.filter || '';
  state.filterOpen = !!fx.filter;
  const r = (fx.loved || [])[state.sel];
  if (fx.removeArmed && r) state.removeKey = 'loved:' + r.artistKey + NUL + r.title;
}

// SLEEP stays in the sidebar but is an action, not a view: a press calls
// CycleSleep and the current tab keeps showing.
const VIEWS = ['recent', 'loved', 'stations', 'taste', 'search', 'info'];
const TABS = [...VIEWS, 'sleep'];
const tabEl = (t) => $('tab' + t[0].toUpperCase() + t.slice(1));

// The TUI's coarse figures, kept identical so faces don't drift.
const fmtDur = (sec) => {
  if (sec <= 0) return '';
  if (sec < 60) return '<1m';
  if (sec < 3600) return Math.floor(sec / 60) + 'm';
  if (sec < 86400) return Math.floor(sec / 3600) + 'h ' + String(Math.floor(sec / 60) % 60).padStart(2, '0') + 'm';
  return Math.floor(sec / 86400) + 'd ' + String(Math.floor(sec / 3600) % 24).padStart(2, '0') + 'h';
};
const rel = (ms) => {
  const now = bridge.now();
  if (!ms || now < ms) return 'just now';
  const d = now - ms;
  if (d < 60e3) return 'just now';
  if (d < 3600e3) return Math.floor(d / 60e3) + 'm ago';
  if (d < 3600e3 * 24) return Math.floor(d / 3600e3) + 'h ago';
  if (d < 30 * 86400e3) return Math.floor(d / 86400e3) + 'd ago';
  return new Date(ms).toLocaleDateString('en-US', { month: 'short', day: 'numeric' });
};
const hours = (sec) => Math.round(sec / 3600) + ' h';
const DAYPART_WORD = { morning: 'this morning', day: 'this afternoon', evening: 'this evening', night: 'tonight' };
const daypartFor = (ms) => {
  const h = new Date(ms).getHours();
  return h >= 5 && h < 11 ? 'morning' : h < 17 ? 'day' : h < 22 ? 'evening' : 'night';
};

// ---- fixed chrome ------------------------------------------------------

// The footer mounts once — a click that straddles a rebuild would be
// lost, so the nine slots keep their listeners and only their classes
// change. setText touches textContent only when it differs.
const presetSlots = [];
(function initPresets() {
  const host = $('presetSlots');
  for (let i = 1; i <= 9; i++) {
    const p = mk('span', 'pslot empty');
    p.dataset.leaf = ''; p.dataset.fixed = ''; p.dataset.id = 'preset-' + i;
    const t = txt('t', 'preset-digit-' + i, String(i));
    p.append(t);
    p.addEventListener('click', () => bridge.RecallPreset(i));
    host.append(p);
    presetSlots.push(p);
  }
})();
const setText = (el, s) => { if (el.textContent !== s) el.textContent = s; };

function renderChrome() {
  const s = state.snap || {};
  for (const t of TABS) {
    const el = tabEl(t);
    if (el) el.classList.toggle('sel', t === state.tab && t !== 'sleep');
  }
  const sleep = $('sleepTabLabel');
  if (sleep) {
    const left = s.sleepAtMs ? Math.max(0, Math.ceil((s.sleepAtMs - bridge.now()) / 60000)) : 0;
    setText(sleep, left ? 'SLEEP ' + left : 'SLEEP');
  }
  const d = new Date(bridge.now());
  setText($('clock'),
    String(d.getHours()).padStart(2, '0') + '-' + String(d.getMinutes()).padStart(2, '0'));

  // Presets footer: slots mounted once at init get only class updates;
  // SAVE/SAVED text changes only when it must.
  presetSlots.forEach((p, i) => {
    const saved = (s.slots || [])[i];
    p.className = 'pslot' + (i + 1 === s.currentSlot ? ' cur' : saved ? ' saved' : ' empty');
  });
  const save = $('presetSave');
  const cur = s.currentSlot || 0;
  setText(save.firstElementChild, cur ? 'SAVED' : 'SAVE');
  save.classList.toggle('saved', !!cur);
  save.onclick = () => bridge.TogglePreset();
}

// ---- shared list chrome -------------------------------------------------

// The header band: name + quiet count at left, an optional /FILTER pill
// (or live filter input) right-aligned to x=464. No point filtering an
// empty list, so the pill only shows with rows or an active filter.
function header(name, count, filterable, hasRows, host) {
  const h = mk('div', 'hdr');
  h.dataset.box = '';
  h.append(txt('', 'hdr-name', name));
  if (count) h.append(txt('', 'hdr-count', count));
  host.append(h);
  state.filterableNow = !!(filterable && (hasRows || state.filter || state.filterOpen));
  if (!state.filterableNow) return;
  if (state.filterOpen) {
    const f = mk('input', 'qin hdrfilter');
    f.dataset.box = ''; f.dataset.leaf = ''; f.dataset.id = 'filter-input';
    f.maxLength = 40;
    f.value = state.filter;
    let deb = 0;
    const apply = () => { state.filter = f.value; state.sel = 0; state.top = 0; renderPanel(); };
    f.addEventListener('input', () => {
      state.filter = f.value;
      clearTimeout(deb);
      deb = setTimeout(() => { state.sel = 0; state.top = 0; renderPanel(); }, 120);
    });
    f.addEventListener('keydown', (e) => { if (e.key === 'Enter') { clearTimeout(deb); apply(); } });
    host.append(f);
    // focus() lands after the host is committed — a detached input can't
    // take focus. renderPanel handles it on the 'focus' sentinel.
  } else {
    const p = mk('div', 'pill filterpill');
    p.dataset.leaf = ''; p.dataset.id = 'filter-pill';
    p.append(txt('t', null, '/ FILTER'));
    p.addEventListener('click', () => { state.filterOpen = true; state.pendingFocus = 'hdrfilter'; renderPanel(); });
    host.append(p);
  }
}

function emptyLine(message, host) {
  const e = txt('t meta emptyline', 'empty', message);
  host.append(e);
}

// Rows: 28-tall pills at x=124, six visible from y=168. Selection is ice.
// Only the visible slice is mounted — clipped DOM would still report
// bounding rects past the window edge to the guard. state.top is the
// scroll offset; it never follows the mouse (wheel) — only the keyboard
// scrolls the selection into view.
const VISIBLE = 6;
const selRow = () => state.rows && state.rows[state.sel];

// mountRows draws state.rows' visible slice and the action pills into
// host. It runs during a panel build AND standalone in repaintRows, so
// face-only changes (selection, scroll, remove-arm) redraw without a
// data refetch.
function mountRows(cfg, host) {
  const rows = state.rows;
  rows.slice(state.top, state.top + VISIBLE).forEach((r, i) => {
    const idx = state.top + i;
    const row = mk('div', 'crow' + (idx === state.sel ? ' sel' : ''));
    row.dataset.box = ''; row.dataset.leaf = ''; row.dataset.col = 'fill';
    if (i === 0) row.dataset.id = 'row-top';
    row.style.top = 40 + i * 28 + 'px';
    row.append(txt('tk ellipsis', idx === 0 ? 'row-0' : null, cfg.main(r)));
    if (cfg.heart && cfg.heart(r)) row.append(txt('ht', 'row-' + idx + '-heart', '♥'));
    const sp = mk('span', 'spacer');
    row.append(sp);
    row.append(txt('st ellipsis', idx === 0 ? 'row-0-right' : null, cfg.side(r)));
    row.addEventListener('click', () => rowClick(idx, r, cfg));
    row.addEventListener('wheel', (e) => wheelRows(e.deltaY), { passive: true });
    host.append(row);
  });
  if (cfg.actions || cfg.removeKind) actionsPanel(cfg, host);
}

// A click selects; a second click on the same row inside 400 ms is the
// double-click the platform won't give us (the first click repaints, so
// the native dblclick lands on a dead element). The last-click pair lives
// in state so it survives the repaint.
function rowClick(idx, r, cfg) {
  const now = Date.now();
  if (state.lastClickIdx === idx && now - state.lastClickAt < 400) {
    state.lastClickIdx = -1;
    if (cfg.primary) cfg.primary(r);
    return;
  }
  state.lastClickIdx = idx;
  state.lastClickAt = now;
  if (state.sel !== idx || state.removeKey) {
    state.sel = idx;
    state.removeKey = '';
    repaintRows();
  }
}

function listPanel(cfg, host) {
  const rows = cfg.rows;
  if (!rows.length) { state.rows = null; state.cfg = null; emptyLine(cfg.empty, host); return; }
  state.rows = rows;
  state.cfg = cfg;
  state.sel = Math.min(Math.max(state.sel, 0), rows.length - 1);
  state.top = Math.min(Math.max(state.top, 0), Math.max(rows.length - VISIBLE, 0));
  mountRows(cfg, host);
}

// repaintRows redraws the mounted rows and pills from state — the
// face-only repaint path that must never hit the bridge.
function repaintRows() {
  if (!state.cfg || !unfolded()) return;
  for (const el of [...content.querySelectorAll('.crow, .actions')]) el.remove();
  mountRows(state.cfg, content);
}

// Wheel over the list scrolls one row per 28-px notch; trackpad deltas
// accumulate. It moves the view, never the selection.
function wheelRows(dy) {
  const rows = state.rows;
  if (!rows) return;
  state.wheelAcc += dy;
  let moved = false;
  while (state.wheelAcc >= 28) { state.wheelAcc -= 28; if (state.top < Math.max(rows.length - VISIBLE, 0)) { state.top++; moved = true; } }
  while (state.wheelAcc <= -28) { state.wheelAcc += 28; if (state.top > 0) { state.top--; moved = true; } }
  if (moved) repaintRows();
}

// Keep the selected row inside the window after a keyboard move.
function scrollSelIntoView() {
  const rows = state.rows;
  if (!rows) return;
  if (state.sel < state.top) state.top = state.sel;
  if (state.sel > state.top + VISIBLE - 1) state.top = state.sel - VISIBLE + 1;
}

// Pills are static descriptors: their handlers read the selected row at
// click time, never a row baked into a stale render's closure.
function actionsPanel(cfg, host) {
  const bar = mk('div', 'actions');
  bar.dataset.box = ''; bar.dataset.col = 'fill';
  const pill = (a) => {
    const p = mk('div', 'pill act ' + (typeof a.cls === 'function' ? a.cls() : a.cls));
    p.dataset.leaf = ''; if (a.id) p.dataset.id = a.id;
    p.append(txt('t', null, typeof a.label === 'function' ? a.label() : a.label));
    p.style.width = a.w + 'px';
    p.addEventListener('click', a.fn);
    bar.append(p);
  };
  for (const a of cfg.actions || []) pill(a);
  if (cfg.removeKind) {
    const r = selRow();
    const armed = r && state.removeKey === cfg.removeKind + ':' + cfg.removeKeyOf(r);
    pill({
      id: 'act-remove',
      cls: () => armed ? 'rm armed' : 'rm',
      w: 72,
      label: () => armed ? 'CONFIRM' : 'REMOVE',
      fn: pressRemoveKey,
    });
  }
  host.append(bar);
}

// REMOVE is a two-press action: first press arms for 3 s (a repaint, not
// a refetch), second removes — and a failed removal still repaints.
function pressRemove(kind, key, call) {
  const k = kind + ':' + key;
  if (state.removeKey !== k) {
    state.removeKey = k;
    repaintRows();
    setTimeout(() => { if (state.removeKey === k) { state.removeKey = ''; repaintRows(); } }, 3000);
    return;
  }
  state.removeKey = '';
  call().catch(() => {}).finally(render);
}

// ---- panels ------------------------------------------------------------

const trackLine = (r) => (r.artist ? r.artist + ' — ' + r.title : r.title);

const PANELS = {
  async recent(host) {
    const rows = await bridge.Recent();
    header('Recent', rows.length ? rows.length + ' tracks' : '', false, rows.length, host);
    listPanel({
      rows,
      empty: 'nothing heard yet',
      main: trackLine,
      side: (r) => r.stationName,
      heart: (r) => r.loved,
    }, host);
  },

  async loved(host) {
    const rows = await bridge.Loved(state.filter);
    header('Loved', rows.length ? rows.length + ' tracks' : '', true, rows.length, host);
    listPanel({
      rows,
      empty: state.filter ? 'no matches · esc clears the filter' : 'love a track and it will appear here',
      main: trackLine,
      side: (x) => x.stationName,
      actions: [
        { id: 'act-return', cls: 'tan', w: 112, label: 'RETURN TO STATION',
          fn: () => { const r = selRow(); if (r) bridge.ReturnToStation(r.stationUuid); } },
        { id: 'act-more', cls: 'tan', w: 108, label: 'MORE FROM ARTIST',
          fn: () => { const r = selRow(); if (r) bridge.MoreFromArtist(r.artist, r.title); } },
      ],
      primary: (x) => bridge.MoreFromArtist(x.artist, x.title),
      removeKind: 'loved',
      removeKeyOf: (x) => x.artistKey + NUL + x.title,
      removeCall: (x) => bridge.RemoveLoved(x.artistKey, x.title),
    }, host);
  },

  async stations(host) {
    const rows = await bridge.Stations(state.filter);
    header('Stations', rows.length ? rows.length + ' stations' : '', true, rows.length, host);
    listPanel({
      rows,
      empty: state.filter ? 'no matches · esc clears the filter' : 'save a station with f, or love a track',
      main: (x) => x.name,
      side: (x) => [
        fmtDur(x.totalSec),
        x.presetSlot ? 'P' + x.presetSlot : '',
        x.loveCount ? '♥ ' + x.loveCount : '',
      ].filter(Boolean).join(' · '),
      actions: [
        { id: 'act-tune', cls: 'tan', w: 52, label: 'TUNE',
          fn: () => { const r = selRow(); if (r) bridge.TuneSaved(r.uuid); } },
      ],
      primary: (x) => bridge.TuneSaved(x.uuid),
      removeKind: 'station',
      removeKeyOf: (x) => x.uuid,
      removeCall: (x) => bridge.RemoveStation(x.uuid),
    }, host);
  },

  async taste(host) {
    const t = await bridge.Taste() || {};
    header('Taste', t.totalSec
      ? hours(t.totalSec) + ' all time · ' + hours(t.weekSec) + ' this week · ' + (t.lovedTracks || 0) + ' loved'
      : '', false, false, host);
    if (!t.totalSec) { emptyLine('Nothing yet. Listen for a while and this fills in.', host); return; }

    // "When you listen" — four daypart rows on a 16-px pitch. All console
    // coordinates are local to #console (page y − 128).
    const when = mk('div', 'sec');
    when.dataset.box = ''; when.dataset.leaf = ''; when.style.cssText = 'left:136px;top:40px;width:328px;height:16px';
    when.append(txt('t lbl', null, 'When you listen'));
    host.append(when);
    const cur = daypartFor(bridge.now());
    (t.dayparts || []).forEach((dp, i) => {
      const y = 60 + i * 16;
      const on = dp.name === cur;
      const pct = Math.round(dp.sec / t.totalSec * 100);
      const row = mk('div', 'dprow');
      row.dataset.leaf = ''; row.style.top = y + 'px';
      row.append(txt('t dpname' + (on ? ' on' : ''), i === 0 ? 'dp-0' : null, dp.name));
      const bar = mk('span', 'bar2');
      const fill = mk('i'); fill.style.width = pct + '%';
      fill.style.background = on ? 'var(--o)' : 'var(--tan)';
      bar.append(fill); row.append(bar);
      row.append(txt('t dppct' + (on ? ' on' : ''), null, pct + '%'));
      host.append(row);
    });

    // Artists left, tags right — up to five rows each from y=284.
    const arts = (t.topArtists || []).slice(0, 5);
    const tags = (t.topTags || []).slice(0, 5);
    const al = mk('div', 'sec');
    al.dataset.box = ''; al.dataset.leaf = ''; al.style.cssText = 'left:136px;top:136px;width:156px;height:16px';
    al.append(txt('t lbl', null, 'Artists · 90 days'));
    host.append(al);
    arts.forEach((a, i) => {
      const row = mk('div', 'arow');
      row.dataset.leaf = ''; row.style.cssText = 'left:136px;top:' + (156 + i * 16) + 'px;width:156px';
      row.append(txt('t aname ellipsis', i === 0 ? 'artist-0' : null, a.artist));
      row.append(txt('t acount', null, String(a.plays)));
      host.append(row);
    });
    const tl = mk('div', 'sec');
    tl.dataset.box = ''; tl.dataset.leaf = ''; tl.style.cssText = 'left:312px;top:136px;width:152px;height:16px';
    tl.append(txt('t lbl', null, 'Tags you lean on'));
    host.append(tl);
    const maxW = Math.max(...tags.map((x) => x.weight), 0.001);
    tags.forEach((tag, i) => {
      const row = mk('div', 'trow');
      row.dataset.leaf = ''; row.style.cssText = 'left:312px;top:' + (156 + i * 16) + 'px;width:152px';
      row.append(txt('t tname ellipsis', i === 0 ? 'tag-0' : null, tag.tag));
      const bar = mk('span', 'bar2');
      const fill = mk('i'); fill.style.width = Math.round(tag.weight / maxW * 100) + '%';
      fill.style.background = 'var(--blue)';
      bar.append(fill); row.append(bar);
      host.append(row);
    });
  },

  async search(host) {
    header('Search', '', false, false, host);
    const q = mk('input', 'qin searchin');
    q.dataset.box = ''; q.dataset.leaf = ''; q.dataset.id = 'search-input';
    q.maxLength = 40;
    q.value = state.search || '';
    q.placeholder = (state.snap && state.snap.virgin)
      ? 'type a genre or artist · space for a wildcard'
      : 'genre or artist · enter seeds';
    q.addEventListener('input', () => { state.search = q.value; });
    q.addEventListener('keydown', (e) => {
      if (e.key === 'Enter' && q.value.trim()) bridge.Seed(q.value.trim());
      // Virgin shortcut: an empty search and a space picks anything. A
      // held spacebar is still one pick.
      if (e.key === ' ' && !e.repeat && !q.value && state.snap && state.snap.virgin) {
        e.preventDefault();
        bridge.Wildcard();
      }
    });
    host.append(q);
    host.append(searchNoteEl());
  },

  async info(host) {
    let i = {};
    try { i = await bridge.Info() || {}; } catch (e) { i = {}; }
    header('Info', '', false, false, host);
    if (!i.name) { emptyLine('nothing tuned yet', host); return; }
    const from = [i.country, [i.codec, i.bitrate ? i.bitrate + 'K' : ''].filter(Boolean).join(' ')].filter(Boolean).join(' · ');
    const rows = [
      ['FROM', from || 'unknown'],
      ['TAGS', (i.tags || []).join(', ') || 'none listed'],
      ['HOME', (i.homepage || '').replace(/^https?:\/\//, '').replace(/\/$/, '') || 'none listed'],
      ['STREAM', i.streamHost || 'none listed'],
      ['HEARD', !i.firstHeardMs || (!i.visits && i.listenedSec < 60) ? 'first time here'
        : [fmtDur(i.listenedSec), i.visits + ' visits', 'first ' + rel(i.firstHeardMs), 'last ' + rel(i.lastHeardMs)].filter(Boolean).join(' · ')],
      ['LOVES', i.lovedTracks ? i.lovedTracks + ' tracks loved here' : 'none yet'],
      ['MATCH', i.heard ? Math.round(i.match * 100) + '% ' + (DAYPART_WORD[i.daypart] || 'now') : 'still learning', i.heard ? i.match : -1],
      ['PRESET', i.presetSlot ? 'slot ' + i.presetSlot : 'not saved'],
    ];
    if (i.failCount > 0) rows.push(['STRIKES', i.failCount + ' of ' + i.maxFail]);
    const clip = mk('div', 'infoclip');
    clip.dataset.box = '';
    rows.slice(0, 10).forEach(([label, value, match], idx) => {
      const row = mk('div', 'irow');
      row.dataset.leaf = ''; row.style.top = idx * 20 + 'px';
      row.append(txt('t ilabel', idx === 0 ? 'info-0' : null, label));
      const val = txt('t ivalue ellipsis', idx === 0 ? 'info-0-value' : null, value);
      if (match !== undefined && match >= 0) {
        // The picker's posterior as a ten-cell meter at the head of the
        // value column, like the TUI's MATCH row.
        const m = mk('span', 'mmeter');
        const knob = Math.round(match * 9);
        for (let c = 0; c < 10; c++) {
          const cell = mk('i');
          cell.className = c === knob ? 'knob' : c < knob ? 'fill' : '';
          m.append(cell);
        }
        row.append(m);
        val.style.left = '168px';
        val.style.width = '160px';
      }
      row.append(val);
      clip.append(row);
    });
    host.append(clip);
  },
};

// SEARCH never re-renders on a snapshot — the note updates in place.
function searchNoteEl() {
  const note = (state.snap && state.snap.note) || '';
  const n = txt('t meta searchnote', 'search-note', note);
  if (!note) n.style.visibility = 'hidden';
  return n;
}

// ---- rendering ---------------------------------------------------------

// The panel reloads only when its own key changes: the strip's live
// clock must not wipe a half-typed query. Keys follow the plan's table.
function panelKey() {
  const s = state.snap || {};
  switch (state.tab) {
    case 'recent': return [s.station && s.station.uuid, s.artist, s.title, s.loved].join(NUL);
    case 'loved': return ['L', s.loved, s.artist, s.title].join(NUL);
    case 'stations': return [(s.slots || []).join(''), s.currentSlot, s.station && s.station.uuid].join(NUL);
    case 'info': return [s.station && s.station.uuid, s.currentSlot].join(NUL);
    default: return 'open';
  }
}

// Panels build into a detached host and commit atomically: two renders
// racing an await can never double-mount rows. A focused input survives a
// rebuild — focus and caret move to its replacement.
let seq = 0;
async function renderPanel() {
  if (!unfolded()) {
    content.textContent = '';
    state.panelKey = '';
    state.rows = null;
    state.cfg = null;
    return;
  }
  const active = document.activeElement;
  const focused = active && content.contains(active) && active.classList.contains('qin')
    ? { cls: active.classList.contains('hdrfilter') ? 'hdrfilter' : 'searchin', at: active.selectionStart }
    : null;
  const my = ++seq;
  const host = mk('div');
  try {
    await PANELS[state.tab](host);
  } catch (e) {
    // A failed read renders the empty panel, never a stuck one.
    if (window.__errors) window.__errors.push('panel ' + state.tab + ': ' + (e && e.message));
  }
  if (my !== seq) return;
  state.panelKey = panelKey();
  content.textContent = '';
  while (host.firstChild) content.append(host.firstChild);
  if (focused) {
    const f = content.querySelector('.qin.' + focused.cls);
    if (f) { f.focus(); f.setSelectionRange(focused.at, focused.at); }
  } else if (state.pendingFocus) {
    // "/"-from-folded and friends land here: the input only exists after
    // this commit, so the request had to wait in state.
    const f = content.querySelector('.qin.' + state.pendingFocus);
    if (f) {
      f.focus();
      f.setSelectionRange(f.value.length, f.value.length);
      state.pendingFocus = '';
    }
  }
}

function render() {
  if (!unfolded()) return;
  renderChrome();
  renderPanel();
}

// ---- face actions the keys drive ---------------------------------------

function setTab(t) {
  if (t === 'search') state.pendingFocus = 'searchin';
  if (t === state.tab) {
    // Already on the panel: the input may just be blurred, focus it now
    // if it exists (folded, pendingFocus waits for the unfold mount).
    if (t === 'search') { const q = content.querySelector('.searchin'); if (q) { q.focus(); state.pendingFocus = ''; } }
    return;
  }
  state.tab = t;
  state.sel = 0; state.top = 0; state.removeKey = '';
  renderPanel();
}
function cycleTab(dir) {
  const i = VIEWS.indexOf(state.tab);
  setTab(VIEWS[(i + dir + VIEWS.length) % VIEWS.length]);
}
function moveSel(d) {
  const rows = state.rows;
  if (!rows || !rows.length) return;
  state.sel = Math.min(Math.max(state.sel + d, 0), rows.length - 1);
  state.removeKey = '';
  scrollSelIntoView();
  repaintRows();
}
function selEnd(last) {
  const rows = state.rows;
  if (!rows || !rows.length) return;
  state.sel = last ? rows.length - 1 : 0;
  state.removeKey = '';
  scrollSelIntoView();
  repaintRows();
}
function primary() {
  const r = state.rows && state.rows[state.sel];
  if (r && state.cfg && state.cfg.primary) state.cfg.primary(r);
}
function pressRemoveKey() {
  const r = state.rows && state.rows[state.sel];
  const cfg = state.cfg;
  if (!r || !cfg || !cfg.removeCall) return;
  pressRemove(cfg.removeKind, cfg.removeKeyOf(r), () => cfg.removeCall(r));
}
function openFilter() {
  if (!state.filterableNow) return;
  state.filterOpen = true;
  state.pendingFocus = 'hdrfilter';
  renderPanel();
}
function returnTo() {
  const r = state.rows && state.rows[state.sel];
  if (r && r.stationUuid) bridge.ReturnToStation(r.stationUuid);
}
function escapeStep() {
  // The TUI's unwind order: filter text → filter pill → search input →
  // a filter left behind → fold.
  const active = document.activeElement;
  if (active && content.contains(active) && active.classList.contains('hdrfilter')) {
    if (active.value) { active.value = ''; state.filter = ''; state.sel = 0; state.top = 0; renderPanel(); return true; }
    state.filterOpen = false; renderPanel(); return true;
  }
  if (active && content.contains(active) && active.classList.contains('searchin')) {
    active.value = ''; state.search = ''; active.blur(); return true;
  }
  if (state.filter && !state.filterOpen) { state.filter = ''; state.sel = 0; state.top = 0; renderPanel(); return true; }
  if (unfolded()) { bridge.SetUnfolded(false); return true; }
  return false;
}

export const ui = {
  state, unfolded, setTab, cycleTab, moveSel, selEnd, primary,
  remove: pressRemoveKey, openFilter, escape: escapeStep, returnTo,
  render, renderPanel, renderChrome,
};

// ---- wiring ------------------------------------------------------------

for (const t of TABS) {
  const el = tabEl(t);
  if (!el) continue;
  if (t === 'sleep') el.addEventListener('click', () => bridge.CycleSleep());
  else el.addEventListener('click', () => setTab(t));
}

// Snapshots drive chrome every time; the panel only reloads when its own
// key changes — see panelKey's table.
bridge.onSnapshot((s) => {
  state.snap = s;
  if (!unfolded()) { state.rows = null; state.cfg = null; return; }
  renderChrome();
  if (state.tab === 'search') {
    const n = content.querySelector('[data-id="search-note"]');
    // Only the note updates in place — but the panel must exist first.
    if (n) {
      const note = s.note || '';
      n.textContent = note;
      n.style.visibility = note ? 'visible' : 'hidden';
      return;
    }
  }
  if (panelKey() !== state.panelKey) renderPanel();
});

// Unfolding resizes the window; MORE may not change the snapshot, so the
// console still needs to paint on that edge.
window.addEventListener('resize', () => { if (unfolded()) render(); });

// Real mode can miss the runner's first publish if the page loads after
// it — seed from Current() the way the strip does. The chrome's clock and
// sleep label need a 1-s tick, which the strip already drives.
if (!bridge.fixtureMode) {
  bridge.Current().then((s) => { state.snap = s; if (unfolded()) render(); });
  setInterval(() => { if (unfolded()) renderChrome(); }, 1000);
}
