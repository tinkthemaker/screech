// The one object the page talks to: same method names as the Go Bridge,
// plus onSnapshot/onLevel/now. In fixture mode (a ?fixture=<name> query)
// the harness's /fixture.js supplies the data; against Wails it wraps
// window.go.gui.Bridge and the runtime events.
const params = new URLSearchParams(location.search);
const fixtureName = params.get('fixture');

function realBridge() {
  const B = window.go && window.go.gui && window.go.gui.Bridge;
  const missing = !B;
  if (missing) {
    // The Wails binding never attached — a broken build, not a dead
    // station. main.js paints the stopped look; this makes noise too.
    console.error('screech: window.go.gui.Bridge is missing — the Wails binding did not attach');
  }
  const rt = window.runtime || {};
  const snapSubs = [];
  const lvlSubs = [];
  if (rt.EventsOn) {
    rt.EventsOn('snapshot', (s) => { for (const fn of snapSubs) fn(s); });
    rt.EventsOn('level', (l) => { for (const fn of lvlSubs) fn(l); });
  }
  const call = (name) => (B && B[name] ? B[name].bind(B) : () => Promise.resolve());
  return {
    missing,
    Prefs: call('Prefs'),
    Current: call('Current'),
    Next: call('Next'),
    Love: call('Love'),
    TogglePause: call('TogglePause'),
    CycleSleep: call('CycleSleep'),
    TogglePreset: call('TogglePreset'),
    RecallPreset: call('RecallPreset'),
    SetVolume: call('SetVolume'),
    StepVolume: call('StepVolume'),
    Seed: call('Seed'),
    Wildcard: call('Wildcard'),
    Discover: call('Discover'),
    ResumeDiscovery: call('ResumeDiscovery'),
    RecentDiscoveries: call('RecentDiscoveries'),
    RadioTop: call('RadioTop'),
    ReturnToStation: call('ReturnToStation'),
    MoreFromArtist: call('MoreFromArtist'),
    TuneSaved: call('TuneSaved'),
    Recent: call('Recent'),
    Loved: call('Loved'),
    Stations: call('Stations'),
    Taste: call('Taste'),
    Info: call('Info'),
    RemoveLoved: call('RemoveLoved'),
    RemoveStation: call('RemoveStation'),
    SetUnfolded: call('SetUnfolded'),
    Quit: call('Quit'),
    Rescaled: call('Rescaled'),
    onSnapshot(fn) { snapSubs.push(fn); },
    onLevel(fn) { lvlSubs.push(fn); },
    now: () => Date.now(),
    fixture: () => null,
    fixtureMode: false,
  };
}

const bridge = fixtureName
  ? await import('/fixture.js').then((m) => m.default(fixtureName))
  : realBridge();

export default bridge;
