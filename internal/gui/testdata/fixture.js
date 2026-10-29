// Fixture-mode bridge, served only by the layout-guard harness. It reads
// /fixtures.json for the fixture's data and /arity.json — the reflected
// arity of every *Bridge method — for the intent stubs: a call with the
// wrong argument count, or to a name Go doesn't have, lands in
// window.__errors and becomes a guard violation. Every call is recorded
// in window.__calls, and now() freezes at the shared fixture clock.
export default async function fixtureBridge(name) {
  const [data, arity] = await Promise.all([
    fetch('/fixtures.json').then((r) => r.json()),
    fetch('/arity.json').then((r) => r.json()),
  ]);
  const f = (data.fixtures || []).find((x) => x.name === name);
  if (!f) {
    document.title = 'MISSING FIXTURE ' + name;
  }
  const fx = f || { snapshot: {}, unfolded: false };
  window.__calls = [];
  window.__errors = [];
  window.addEventListener('error', (e) => window.__errors.push(e.message + ' @' + e.filename + ':' + e.lineno));
  window.addEventListener('unhandledrejection', (e) => window.__errors.push('rejection: ' + e.reason));
  const unfoldedNow = window.innerHeight >= 200;
  window.__expect = Object.assign({}, fx.expect, unfoldedNow ? fx.expectUnfolded : null);
  window.__expectStyle = fx.expectStyle || {};
  const call = (n, args) => window.__calls.push([n, ...args]);
  const chk = (n, a) => {
    if (!(n in arity)) window.__errors.push('call to unbound method ' + n);
    else if (a.length !== arity[n]) window.__errors.push('arity ' + n + ': ' + a.length + ' args, want ' + arity[n]);
  };
  const snapSubs = [];
  const lvlSubs = [];
  const deliver = () => {
    for (const fn of snapSubs) fn(fx.snapshot);
    if (fx.level) for (const fn of lvlSubs) fn(fx.level);
  };
  // The guard rides along in fixture mode only; production never serves it.
  const guard = document.createElement('script');
  guard.src = '/guard.js';
  document.body.appendChild(guard);

  const b = {};
  for (const n of Object.keys(arity)) {
    b[n] = (...a) => { chk(n, a); call(n, a); return Promise.resolve(); };
  }
  // The data-returning reads keep their fixture payloads but stay
  // arity-checked like everything else.
  b.Recent = (...a) => { chk('Recent', a); call('Recent', a); return Promise.resolve(fx.recent || []); };
  b.Loved = (...a) => { chk('Loved', a); call('Loved', a); return Promise.resolve(fx.loved || []); };
  b.Stations = (...a) => { chk('Stations', a); call('Stations', a); return Promise.resolve(fx.stations || []); };
  b.Taste = (...a) => { chk('Taste', a); call('Taste', a); return Promise.resolve(fx.taste); };
  b.Info = (...a) => { chk('Info', a); call('Info', a); return Promise.resolve(fx.info || {}); };
  b.Current = (...a) => { chk('Current', a); return Promise.resolve(fx.snapshot); };
  b.Prefs = (...a) => { chk('Prefs', a); return Promise.resolve({ reducedMotion: !!fx.reduced }); };
  b.onSnapshot = (fn) => { snapSubs.push(fn); queueMicrotask(deliver); };
  b.onLevel = (fn) => { lvlSubs.push(fn); queueMicrotask(deliver); };
  b.now = () => data.nowMs;
  b.fixture = () => fx;
  b.fixtureMode = true;

  // ?keytest=1 swaps the guard's measurement run for the keyboard driver:
  // it replays a scripted event sequence on the real page and records
  // what every key did into <pre id="keys"> for the Go test.
  if (new URLSearchParams(location.search).get('keytest')) {
    const kt = document.createElement('script');
    kt.type = 'module';
    kt.src = '/keys-test.js';
    document.body.appendChild(kt);
  }
  return b;
}
