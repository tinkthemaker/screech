// The terminal's key map, one to one. Listening behaviour stays in the
// session engine — keys only send bridge intents and drive the console's
// face state (tabs, selection, filter). Only the two inputs are
// focusable; while one has focus, keys go to it except ESC and ENTER.
import bridge from './bridge.js';
import { ui } from './console.js';

const $ = (id) => document.getElementById(id);
// Reduced motion comes from the OS media query, a fixture's ?reduced=1,
// or — in the real window, which has no query string — the shell's
// --reduced-motion flag through the bridge.
let reduced = matchMedia('(prefers-reduced-motion: reduce)').matches
  || new URLSearchParams(location.search).get('reduced') === '1';
if (reduced) document.body.classList.add('reduced');
if (bridge.Prefs) {
  bridge.Prefs().then((p) => {
    if (p && p.reducedMotion) { reduced = true; document.body.classList.add('reduced'); }
  });
}

// A pressed control flashes brighter for a blink — a face effect, never
// under reduced motion.
function flash(el) {
  if (!el || reduced) return;
  el.classList.add('flash');
  setTimeout(() => el.classList.remove('flash'), 120);
}
document.addEventListener('click', (e) => {
  const b = e.target.closest && e.target.closest('.btn,.pill,.tab,.pslot');
  if (b) flash(b);
});

const phase = () => (ui.state.snap && ui.state.snap.phase) || '';
const stopped = () => phase() === 'stopped';

// Repeat is only safe where a held key means the same thing the TUI
// means: volume nudges and row movement. Holding SPACE must not skip
// twenty stations.
const repeatable = (key) =>
  ['-', '[', '+', '=', ']', 'j', 'k', 'ArrowDown', 'ArrowUp',
    'PageDown', 'PageUp', 'Home', 'End'].includes(key);

// WebView browser keys we never want to reach Chromium.
const browserKey = (e) =>
  e.key === 'F5' || e.key === 'BrowserBack' ||
  (e.ctrlKey && ['r', 'R', 'p', 'P', 'f', 'F', 'g', 'G', 'u', 'U', 's', 'S', '-', '=', '+', '0'].includes(e.key)) ||
  (e.altKey && (e.key === 'ArrowLeft' || e.key === 'ArrowRight'));

window.addEventListener('keydown', (e) => {
  if (browserKey(e)) { e.preventDefault(); return; }
  if (e.ctrlKey || e.altKey || e.metaKey) return;

  const active = document.activeElement;
  const inInput = active && active.classList && active.classList.contains('qin');
  if (e.key === 'Escape') { ui.escape(); e.preventDefault(); return; }
  if (inInput) return; // inputs own every other key, Enter included

  if (e.repeat && !repeatable(e.key)) return;

  switch (e.key) {
    case ' ': case 'n': if (!stopped()) { bridge.Next(); flash($('btnNext')); } break;
    case 'l': if (phase() !== 'offline' && !stopped()) { bridge.Love(); flash($('btnLove')); } break;
    case 'f': bridge.TogglePreset(); flash($('presetSave')); break;
    case 'p': case 'P': if (!stopped()) { bridge.TogglePause(); flash($('btnPause')); } break;
    case 'z': case 'Z': bridge.CycleSleep(); flash($('tabSleep')); break;
    case '/':
      if (stopped()) break;
      if (ui.unfolded() && (ui.state.tab === 'loved' || ui.state.tab === 'stations')) {
        ui.openFilter();
      } else {
        if (!ui.unfolded()) bridge.SetUnfolded(true);
        ui.setTab('search');
      }
      break;
    case 'h': ui.setTab('recent'); if (!ui.unfolded()) bridge.SetUnfolded(true); break;
    case 'H': ui.setTab('loved'); if (!ui.unfolded()) bridge.SetUnfolded(true); break;
    case 'i': case 'I': ui.setTab('info'); if (!ui.unfolded()) bridge.SetUnfolded(true); break;
    case '-': case '[': bridge.StepVolume(-5); break;
    case '+': case '=': case ']': bridge.StepVolume(5); break;
    case 'Tab':
      if (ui.unfolded()) { ui.cycleTab(e.shiftKey ? -1 : 1); e.preventDefault(); }
      break;
    // Row keys act on a visible list only — folded, there's nothing to
    // aim at and state.rows is empty besides.
    case 'j': case 'ArrowDown': if (ui.unfolded()) ui.moveSel(1); break;
    case 'k': case 'ArrowUp': if (ui.unfolded()) ui.moveSel(-1); break;
    case 'PageDown': if (ui.unfolded()) ui.moveSel(5); break;
    case 'PageUp': if (ui.unfolded()) ui.moveSel(-5); break;
    case 'Home': if (ui.unfolded()) ui.selEnd(false); break;
    case 'End': if (ui.unfolded()) ui.selEnd(true); break;
    case 'Enter': if (ui.unfolded()) ui.primary(); break;
    case 'r': case 'R': if (ui.unfolded()) ui.returnTo(); break;
    case 'x': case 'X': if (ui.unfolded()) ui.remove(); break;
    case 'q': case 'Q': bridge.Quit(); break;
    default:
      if (e.key >= '1' && e.key <= '9') bridge.RecallPreset(Number(e.key));
  }
});

// No menus, no drags.
window.addEventListener('contextmenu', (e) => e.preventDefault());
window.addEventListener('dragstart', (e) => e.preventDefault());
