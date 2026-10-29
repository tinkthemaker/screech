// Strip renderer: draws the whole 480x128 face from one session.Snapshot.
// Listening behaviour lives in the Go engine — this file only reads the
// snapshot and calls bridge intents.
import bridge from './bridge.js';
import { ui } from './console.js';

const $ = (id) => document.getElementById(id);
const els = {
  strip: $('strip'), station: $('station'), stationRow: $('stationRow'), track: $('track'),
  status: $('status'), statusWord: $('statusWord'), statusRest: $('statusRest'),
  chip: $('chip'), meter: $('meter'),
  volTrack: $('volTrack'), volFill: $('volFill'), volThumb: $('volThumb'),
  volValue: $('volValue'),
  btnNext: $('btnNext'), btnLove: $('btnLove'), btnPause: $('btnPause'),
  btnMore: $('btnMore'), btnClose: $('btnClose'),
};

const BARS = 12;
for (let i = 0; i < BARS; i++) els.meter.appendChild(document.createElement('i'));
const bars = [...els.meter.children];

let snap = null;
let level = [0, 0, 0];
let lastUuid = null;
// The approved mockup's 12-bar shape. Each bar breathes on its own slow
// sine (ω in 1.5–3.5 rad/s, φ seeded from the shared clock) so the
// texture stays smooth at 60 fps and deterministic in fixtures; heights
// ease toward their target — ~60 ms attack, ~250 ms release. Bars 1–6
// follow L, 7–12 R. Anything without a live signal targets zero.
const METER_PROFILE = [40, 70, 55, 90, 65, 80, 45, 60, 85, 50, 30, 20];
let rng = (bridge.now() >>> 0) || 1;
const rand = () => (rng = (rng * 1103515245 + 12345) >>> 0) / 4294967296;
const METER_OMEGA = bars.map(() => 1.5 + rand() * 2);
const METER_PHASE = bars.map(() => rand() * Math.PI * 2);
const drawn = bars.map(() => 0);
let meterAt = 0;

const meterIdle = () => {
  const b = snap ? bucket(snap) : 'virgin';
  return b === 'paused' || b === 'tuning' || b === 'offline' || b === 'stopped' || b === 'virgin';
};
const meterTarget = (i, t) => {
  if (meterIdle()) return 0;
  const lvl = Math.max(Math.min(1, level[i < 6 ? 0 : 1] || 0), 0.001);
  const tex = 0.875 + 0.125 * Math.sin(t / 1000 * METER_OMEGA[i] + METER_PHASE[i]);
  return (METER_PROFILE[i] / 100) * tex * lvl;
};

const drawMeter = () => {
  const t = bridge.now();
  if (bridge.fixtureMode) {
    // One deterministic frame at the fixture's frozen clock.
    bars.forEach((b, i) => {
      b.style.height = Math.max(2, Math.round(24 * meterTarget(i, t))) + 'px';
    });
    return;
  }
  const dt = Math.min(200, t - meterAt || 16);
  meterAt = t;
  bars.forEach((b, i) => {
    const target = meterTarget(i, t);
    const tau = target > drawn[i] ? 60 : 250;
    drawn[i] += (target - drawn[i]) * (1 - Math.exp(-dt / tau));
    b.style.height = Math.max(2, Math.round(24 * drawn[i])) + 'px';
  });
};

const unfolded = () => window.innerHeight >= 200;

const mmss = (ms) => {
  const s = Math.max(0, Math.floor((bridge.now() - ms) / 1000));
  return String(Math.floor(s / 60)).padStart(2, '0') + ':' + String(s % 60).padStart(2, '0');
};

// Palette buckets the spec's state table defines. Reconnecting and
// resuming keep the playing treatment — full-opacity station and the
// last track, since nothing has been tuned away yet; only the status
// word changes. With no station the strip is in its virgin state: the
// SCREECH wordmark stands in for a name, the tuning word, "Finding a
// station…" in the track row.
const bucket = (s) => {
  switch (s.phase) {
    case 'paused': return 'paused';
    case 'offline': return 'offline';
    case 'stopped': return 'stopped';
    case 'tuning':
      if (s.reconnecting || s.resuming) return 'reconnecting';
      return s.haveStation ? 'tuning' : 'virgin';
    case 'idle': return 'virgin';
    default: return s.haveStation ? 'playing' : 'virgin';
  }
};

// The status row is a colored state word plus a quieter detail, like the
// mockup's "● Live 00:42 · tag: ambient". Feedback takes the whole slot:
// split on the first double space, the lead word lands in the state-word
// position and the rest becomes the detail. Otherwise the detail follows
// Note > syncing > sleep > new-to-you > reason — and reason only where the
// phase shows it at all (tuning-family states put it in the track row). A
// discovery's reason is the session's own text, rendered as given; "new to
// you" takes its seat while the song's artist is one never heard here.
const detail = (rest) => rest ? '  ·  ' + rest : '';
const statusParts = (s) => {
  if (s.feedback) {
    const i = s.feedback.indexOf('  ');
    if (i < 0) return { word: s.feedback, cls: 'peach', rest: '' };
    return { word: s.feedback.slice(0, i), cls: 'peach', rest: detail(s.feedback.slice(i + 2)) };
  }
  if (s.phase === 'offline') {
    const rest = s.retryAtMs
      ? 'RETRYING IN ' + Math.max(0, Math.ceil((s.retryAtMs - bridge.now()) / 1000)) + ' S'
      : 'RETRYING NOW';
    return { word: '■ OFFLINE', cls: 'a3', rest: detail(rest) };
  }
  const play = s.playStartMs ? ' ' + mmss(s.playStartMs) : '';
  const hold = s.pausedAtMs ? ' ' + mmss(s.pausedAtMs) : '';
  const note = s.note
    || (s.syncing ? 'SYNCING DIRECTORY…' : '')
    || (s.sleepAtMs ? 'SLEEP ' + Math.max(0, Math.ceil((s.sleepAtMs - bridge.now()) / 60000)) + ' MIN' : '')
    || (s.newToYou ? 'NEW TO YOU' : '')
    || '';
  let word, cls = 'peach', reason = '';
  if (s.reconnecting) word = '◌ RECONNECTING';
  else if (s.resuming) word = '◌ RESUMING';
  else if (s.seeking) word = '◌ SEEKING';
  else switch (s.phase) {
    case 'buffering': word = 'BUFFERING'; cls = 'peach pulse'; break;
    case 'paused': word = '❚❚ PAUSED' + hold; cls = 'pale'; break;
    case 'stopped': word = '■ STOPPED'; cls = 'a2'; break;
    case 'idle': case 'tuning': word = '◌ TUNING'; break;
    default: word = '● LIVE' + play; cls = ''; reason = s.reason || ''; break;
  }
  if (bucket(s) === 'virgin') word = '◌ TUNING';
  return { word, cls, rest: detail(note || reason) };
};

const trackText = (s) => {
  if (s.notice) return { text: s.notice, cls: 'notice' };
  if (s.phase === 'stopped') {
    return { text: (s.fatal || 'mpv stopped') + ' · run screech doctor', cls: 'al2' };
  }
  if (s.phase === 'offline') {
    return { text: s.fatal || "No connection. Stations aren't being blamed for it.", cls: '' };
  }
  const b = bucket(s);
  if (b === 'virgin') return { text: 'Finding a station…', cls: '' };
  if (b === 'tuning') return { text: s.reason || '', cls: 'dim' };
  if (s.haveTrack) return { text: [s.artist, s.title].filter(Boolean).join(' — '), cls: '' };
  if (s.reconnecting || s.resuming) return { text: s.reason || '', cls: 'dim' };
  // Ten locked seconds with a station and no title: the blank stops
  // reading as patience and starts reading as a hole — the terminal
  // face's rule.
  if (s.phase === 'playing' && s.haveStation && s.playStartMs &&
      bridge.now() - s.playStartMs >= 10000) {
    return { text: 'No track info sent', cls: 'dim' };
  }
  return { text: '', cls: '' };
};

// Move a long name once, after a short reading pause, then return to its
// beginning. Recompute only when the text or viewport changes: snapshots
// arrive frequently and must not restart the scroll.
const refreshScroll = (el) => {
  const inner = el.firstElementChild;
  inner.classList.remove('scrolling');
  if (bridge.fixtureMode) return;
  const distance = Math.ceil(inner.getBoundingClientRect().width - el.clientWidth);
  if (distance <= 0) return;
  inner.style.setProperty('--scroll-distance', distance + 'px');
  inner.style.setProperty('--scroll-duration', Math.max(6, distance / 24 + 3) + 's');
  void inner.offsetWidth;
  inner.classList.add('scrolling');
};
const setScrollingText = (el, value) => {
  if (el.firstElementChild.textContent === value) return;
  el.firstElementChild.textContent = value;
  el.title = value;
  refreshScroll(el);
};

function render() {
  const s = snap;
  if (!s) return;
  const b = bucket(s);
  document.body.dataset.state = b;
  document.body.classList.toggle('unfolded', unfolded());

  // The mockup drops the meter in red-alert states; there is no signal
  // to meter anyway.
  els.meter.style.visibility = (b === 'offline' || b === 'stopped') ? 'hidden' : 'visible';

  if (s.haveStation) {
    // The 400 ms name fade fires when the station changes, not on every
    // snapshot — restart the animation only on a new uuid.
    if (lastUuid !== null && s.station.uuid !== lastUuid) {
      els.stationRow.classList.remove('fadein');
      void els.stationRow.offsetWidth;
      els.stationRow.classList.add('fadein');
    }
    lastUuid = s.station.uuid;
    setScrollingText(els.station, s.station.name);
  } else {
    lastUuid = null;
    setScrollingText(els.station, 'SCREECH');
  }
  const tr = trackText(s);
  setScrollingText(els.track, tr.text);
  els.track.className = 't trk ellipsis scroll-viewport' + (tr.cls ? ' ' + tr.cls : '');

  const st = statusParts(s);
  els.statusWord.textContent = st.word;
  els.statusWord.className = 'w' + (st.cls ? ' ' + st.cls : '');
  els.statusRest.textContent = st.rest;
  els.status.classList.toggle('pulse', st.cls === 'peach pulse');
  els.chip.hidden = !s.suspect;

  els.btnNext.firstElementChild.textContent = b === 'offline' ? 'RETRY NOW' : 'NEXT';
  els.btnLove.firstElementChild.textContent = s.loved ? 'LOVED' : 'LOVE';
  els.btnLove.classList.toggle('loved', !!s.loved);
  els.btnPause.firstElementChild.textContent = b === 'paused' ? 'RESUME' : 'PAUSE';
  els.btnMore.firstElementChild.textContent = unfolded() ? 'LESS' : 'MORE';

  // The drag's own paint survives release until a snapshot carries the
  // dragged value, or a second has passed — whichever comes first. A
  // stale snapshot must never flicker the old level back on.
  const vol = s.volume || 0;
  if (dragVol !== null && vol !== dragVol && (dragging || bridge.now() < dragUntil)) {
    drawVol(dragVol);
  } else {
    dragVol = null;
    drawVol(vol);
  }

  drawMeter();
  window.__rendered = true;
}

// Dead buttons ignore clicks in the page — the engine guards them too,
// but a dead button shouldn't even flash.
els.btnNext.addEventListener('click', () => { if (bucket(snap || {}) !== 'stopped') bridge.Next(); });
els.btnLove.addEventListener('click', () => { const b = bucket(snap || {}); if (b !== 'offline' && b !== 'stopped') bridge.Love(); });
els.btnPause.addEventListener('click', () => { if (bucket(snap || {}) !== 'stopped') bridge.TogglePause(); });
// MORE opens the console on FIND, whichever tab it was last left on; LESS
// folds it. ✕ quits, like Q.
els.btnMore.addEventListener('click', () => {
  if (unfolded()) { bridge.SetUnfolded(false); return; }
  ui.openFind();
  bridge.SetUnfolded(true);
});
els.btnClose.addEventListener('click', () => bridge.Quit());

// The whole 14-px track row is the click/drag target; there is no range
// input and no wheel listener on the slider. SetVolume persists to SQLite,
// so a drag coalesces to one call per frame and only on integer change.
// The strip draws the drag's own value until the pointer is up and a
// snapshot has carried it.
let dragVol = null;
let dragging = false;
let dragUntil = 0;
let volSent = -1;
let volQueued = false;
const drawVol = (v) => {
  const x = Math.round(v * 82 / 100);
  els.volFill.style.width = (x + 3) + 'px';
  els.volThumb.style.left = x + 'px';
  els.volValue.textContent = String(v);
};
const sendVol = (v) => {
  if (v === volSent) return;
  volSent = v;
  if (volQueued) return;
  volQueued = true;
  requestAnimationFrame(() => {
    volQueued = false;
    bridge.SetVolume(volSent);
  });
};
els.volTrack.addEventListener('pointerdown', (e) => {
  els.volTrack.setPointerCapture(e.pointerId);
  dragging = true;
  const set = (ev) => {
    const r = els.volTrack.getBoundingClientRect();
    const v = Math.max(0, Math.min(100, Math.round((ev.clientX - r.left - 3) / 82 * 100)));
    dragVol = v;
    drawVol(v);
    sendVol(v);
  };
  set(e);
  const mv = (ev) => set(ev);
  const up = () => {
    dragging = false;
    dragUntil = bridge.now() + 1000;
    els.volTrack.removeEventListener('pointermove', mv);
    els.volTrack.removeEventListener('pointerup', up);
    els.volTrack.removeEventListener('pointercancel', up);
    els.volTrack.removeEventListener('lostpointercapture', up);
  };
  els.volTrack.addEventListener('pointermove', mv);
  els.volTrack.addEventListener('pointerup', up);
  // A cancelled pointer or a stolen capture ends the drag the same way —
  // otherwise the strip would paint the drag value forever.
  els.volTrack.addEventListener('pointercancel', up);
  els.volTrack.addEventListener('lostpointercapture', up);
});
window.addEventListener('wheel', (e) => { if (e.ctrlKey) e.preventDefault(); }, { passive: false });

bridge.onSnapshot((s) => { snap = s; render(); });
bridge.onLevel((l) => { level = l || level; if (bridge.fixtureMode) drawMeter(); });

// SetUnfolded resizes the window without necessarily changing the
// snapshot; the console needs a repaint on that edge too.
window.addEventListener('resize', () => {
  if (snap) render();
  refreshScroll(els.station);
  refreshScroll(els.track);
});
document.fonts.ready.then(() => {
  refreshScroll(els.station);
  refreshScroll(els.track);
});

// Dragged onto a monitor at another scale, the window is resized by
// Wails without Go hearing of it, so the shell's outline would keep the
// old monitor's pixels. The page sees the new devicePixelRatio; a
// resolution query matches only the ratio it was built for, so each
// change re-arms one for the next.
const watchScale = () => {
  matchMedia(`(resolution: ${devicePixelRatio}dppx)`).addEventListener('change', () => {
    bridge.Rescaled();
    watchScale();
  }, { once: true });
};
watchScale();

if (!bridge.fixtureMode && !bridge.missing) {
  // Live text (elapsed clock, retry countdown) and meter animation.
  setInterval(() => { if (snap) render(); }, 1000);
  const tick = () => { drawMeter(); requestAnimationFrame(tick); };
  requestAnimationFrame(tick);
  bridge.Current().then((s) => { snap = s; render(); });
}

// A missing binding can't be a silent no-op — paint the stopped look and
// name the fault on the strip.
if (bridge.missing) {
  snap = { phase: 'stopped', fatal: 'window bridge missing · reinstall screech' };
  render();
}
