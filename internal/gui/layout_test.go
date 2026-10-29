//go:build windows && layout

package gui

import (
	"encoding/json"
	"fmt"
	"html"
	"image"
	_ "image/png"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"testing"
)

// The layout guard renders every fixture in headless Edge at two scales
// and fails on any violation guard.js reports. It needs a real Edge; on a
// machine without one it skips rather than fails.

type guardRect struct {
	L, T, W, H float64
}

type guardResult struct {
	Violations []string             `json:"violations"`
	Fixed      map[string]guardRect `json:"fixed"`
	CFixed     map[string]guardRect `json:"cfixed"`
	Vw         float64              `json:"vw"`
	Vh         float64              `json:"vh"`
}

// fixedMaps collects the rendered #strip [data-fixed] rects and cfixedMaps
// the console's: mode is "folded" or "unfolded", then fixture, then scale.
var (
	fixedMaps  = map[string]map[string]map[string]map[string]guardRect{}
	cfixedMaps = map[string]map[string]map[string]map[string]guardRect{}
)

func findEdge(t *testing.T) string {
	t.Helper()
	if p, err := exec.LookPath("msedge"); err == nil {
		return p
	}
	for _, root := range []string{
		os.Getenv("ProgramFiles(x86)"), os.Getenv("ProgramFiles"),
	} {
		if root == "" {
			continue
		}
		p := filepath.Join(root, "Microsoft", "Edge", "Application", "msedge.exe")
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	t.Skip("msedge not found; the layout guard only runs where Edge exists")
	return ""
}

// headless=new treats --window-size differently per verb: --dump-dom
// measures the outer window, so ~92 px of chrome comes off the height,
// but --screenshot uses it as the viewport directly. The DOM run asks
// for h+92; the PNG run asks for exactly w,h so a folded fixture can
// never render unfolded. Widths below ~516 clamp to a 492-px minimum
// viewport (harmless — the page is 480 wide either way).
func edgeArgs(url string, w, outerH int, scale string) []string {
	return []string{
		"--headless=new", "--disable-gpu", "--hide-scrollbars",
		fmt.Sprintf("--window-size=%d,%d", w, outerH),
		"--force-device-scale-factor=" + scale,
		"--virtual-time-budget=3000",
		"--run-all-compositor-stages-before-draw",
		"--user-data-dir=" + filepath.Join(os.TempDir(), "screech-guard-edge"),
		url,
	}
}

var (
	preRE   = regexp.MustCompile(`(?s)<pre id="guard"[^>]*>(.*?)</pre>`)
	shotDir string
)

func runGuard(t *testing.T, edge, url string, w, h int, scale, shotPath string) guardResult {
	t.Helper()
	if shotPath != "" {
		abs, err := filepath.Abs(shotPath)
		if err != nil {
			t.Fatalf("screenshot path: %v", err)
		}
		// Screenshot viewport is exactly the page size — the window's
		// folded/unfolded mode must match what gets guarded below.
		args := append(edgeArgs(url, w, h, scale), "--screenshot="+abs)
		if out, err := exec.Command(edge, args...).CombinedOutput(); err != nil {
			t.Fatalf("edge screenshot failed: %v\n%s", err, out)
		}
		fh, err := os.Open(shotPath)
		if err != nil {
			t.Fatalf("edge wrote no screenshot to %s", shotPath)
		}
		cfg, _, err := image.DecodeConfig(fh)
		fh.Close()
		if err != nil {
			t.Fatalf("screenshot %s does not decode: %v", shotPath, err)
		}
		if sh := int(math.Round(float64(cfg.Height) / scaleFactor(scale))); sh != h {
			t.Fatalf("screenshot %s is %d px tall, want %d — guarded mode mismatch", shotPath, sh, h)
		}
	}
	args := append(edgeArgs(url, w, h+92, scale), "--dump-dom")
	out, err := exec.Command(edge, args...).Output()
	if err != nil {
		t.Fatalf("edge dump-dom failed: %v", err)
	}
	m := preRE.FindSubmatch(out)
	if m == nil {
		t.Fatalf("guard.js did not write <pre id=guard>; dom tail: %s", tail(string(out)))
	}
	var res guardResult
	if err := json.Unmarshal([]byte(html.UnescapeString(string(m[1]))), &res); err != nil {
		t.Fatalf("guard JSON did not parse: %v\n%s", err, m[1])
	}
	// The DOM run's viewport must be the page height we asked for —
	// otherwise the guard measured the wrong fold state.
	if int(res.Vh+0.5) != h {
		t.Fatalf("guard viewport height %v != %d", res.Vh, h)
	}
	return res
}

func scaleFactor(s string) float64 {
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 1
	}
	return f
}

func tail(s string) string {
	if len(s) > 400 {
		return s[len(s)-400:]
	}
	return s
}

// A keytest step: what keys-test.js recorded after one scripted event.
type keyStep struct {
	Name          string   `json:"name"`
	Calls         []string `json:"calls"`
	Sel           int      `json:"sel"`
	Top           int      `json:"top"`
	Tab           string   `json:"tab"`
	Unf           bool     `json:"unf"`
	RowTop        *string  `json:"rowTop"`
	Remove        *string  `json:"remove"`
	FilterInput   bool     `json:"filterInput"`
	SearchFocused bool     `json:"searchFocused"`
	Prevented     bool     `json:"prevented"`
}

type keyResult struct {
	Steps  []keyStep `json:"steps"`
	Errors []string  `json:"errors"`
}

var keysPreRE = regexp.MustCompile(`(?s)<pre id="keys"[^>]*>(.*?)</pre>`)

// keyWant is one row of the keymap expectation: which calls the step
// must have made (a panel reload's data read counts), and the face state
// it must have left. nil fields go unchecked.
type keyWant struct {
	calls         []string
	sel           *int
	top           *int
	tab           string
	remove        string
	filterInput   *bool
	searchFocused *bool
	prevented     *bool
}

func ip(i int) *int   { return &i }
func bp(b bool) *bool { return &b }

// TestKeyboardMap drives the real page — the keys fixture unfolded on
// LOVED with eight rows — through keys-test.js's scripted events and
// asserts every step: the calls it made, the face state it left, repeat
// suppression, input swallowing, the ESC order, and that wheel never
// touches volume. Face-only changes (selection, scroll, arming) repaint
// from state and must not re-read rows; only the A4 triggers do.
func TestKeyboardMap(t *testing.T) {
	edge := findEdge(t)
	srv := startFixtureServer(t)
	url := srv.URL + "/?fixture=keys&keytest=1"
	args := []string{
		"--headless=new", "--disable-gpu", "--hide-scrollbars",
		"--window-size=480,520", // ~428 px viewport after chrome
		"--force-device-scale-factor=1",
		"--virtual-time-budget=15000",
		"--run-all-compositor-stages-before-draw",
		"--user-data-dir=" + filepath.Join(os.TempDir(), "screech-guard-edge"),
		url,
		"--dump-dom",
	}
	out, err := exec.Command(edge, args...).Output()
	if err != nil {
		t.Fatalf("edge dump-dom failed: %v", err)
	}
	m := keysPreRE.FindSubmatch(out)
	if m == nil {
		t.Fatalf("keys-test.js did not write <pre id=keys>; dom tail: %s", tail(string(out)))
	}
	var res keyResult
	if err := json.Unmarshal([]byte(html.UnescapeString(string(m[1]))), &res); err != nil {
		t.Fatalf("keys JSON did not parse: %v\n%s", err, m[1])
	}
	for _, e := range res.Errors {
		t.Errorf("page error during keytest: %s", e)
	}

	steps := map[string]keyStep{}
	var order []string
	for _, s := range res.Steps {
		steps[s.Name] = s
		order = append(order, s.Name)
	}
	// The whole scripted sequence, in order — a missing step means the
	// driver died partway.
	wantOrder := []string{
		"j", "j-repeat", "k", "arrow-down", "pgdn", "pgup", "end", "home",
		"space-repeat", "space", "n", "l", "p", "z", "f", "digit",
		"vol-down", "vol-up", "q",
		"tab", "shift-tab", "i", "h", "enter-recent", "H",
		"x-arm", "x-remove", "r", "enter-loved",
		"click", "click-100ms", "click-far-1", "click-far-2",
		"wheel-row", "wheel-row-back", "wheel-vol", "ctrl-wheel",
		"slash-filter", "filter-letter", "filter-type",
		"esc-filter-text", "esc-filter-close",
		"filter-leftover", "esc-filter-leftover",
		"i-again", "slash-search", "search-letter", "search-space",
		"search-space-repeat", "search-type", "search-enter",
		"esc-search", "esc-fold",
	}
	if len(order) != len(wantOrder) {
		t.Fatalf("driver ran %d steps, want %d: got %v", len(order), len(wantOrder), order)
	}
	for i, name := range wantOrder {
		if order[i] != name {
			t.Fatalf("step %d is %q, want %q (full order: %v)", i, order[i], name, order)
		}
	}

	want := map[string]keyWant{
		// Row movement: repeatable, and repaints off state — never a
		// refetch for a face-only change.
		"j":          {calls: []string{}, sel: ip(1)},
		"j-repeat":   {calls: []string{}, sel: ip(2)},
		"k":          {calls: []string{}, sel: ip(1)},
		"arrow-down": {calls: []string{}, sel: ip(2)},
		"pgdn":       {calls: []string{}, sel: ip(7)},
		"pgup":       {calls: []string{}, sel: ip(2)},
		"end":        {calls: []string{}, sel: ip(7)},
		"home":       {calls: []string{}, sel: ip(0)},

		// Strip intents: exactly one call each; a held spacebar is one Next.
		"space-repeat": {calls: []string{}},
		"space":        {calls: []string{"Next()"}},
		"n":            {calls: []string{"Next()"}},
		"l":            {calls: []string{"Love()"}},
		"p":            {calls: []string{"TogglePause()"}},
		"z":            {calls: []string{"CycleSleep()"}},
		"f":            {calls: []string{"TogglePreset()"}},
		"digit":        {calls: []string{"RecallPreset(3)"}},
		"vol-down":     {calls: []string{"StepVolume(-5)"}},
		"vol-up":       {calls: []string{"StepVolume(5)"}},
		"q":            {calls: []string{"Quit()"}},

		// Tabs: TAB cycles the six views; h/H/i jump straight to a view.
		"tab":          {calls: []string{`Stations("")`}, tab: "stations", sel: ip(0)},
		"shift-tab":    {calls: []string{`Loved("")`}, tab: "loved"},
		"i":            {calls: []string{"Info()"}, tab: "info"},
		"h":            {calls: []string{"Recent()"}, tab: "recent"},
		"enter-recent": {calls: []string{}, tab: "recent"},
		"H":            {calls: []string{`Loved("")`}, tab: "loved"},

		// Row actions: x arms (a repaint), x again removes and reloads,
		// r returns to the station, enter seeds more from the artist.
		"x-arm":       {calls: []string{}, remove: "CONFIRM"},
		"x-remove":    {calls: []string{`RemoveLoved("artist:Boards of Canada","Roygbiv")`, `Loved("")`}},
		"r":           {calls: []string{`ReturnToStation("seed:somafm-groovesalad")`}},
		"enter-loved": {calls: []string{`MoreFromArtist("Boards of Canada","Roygbiv")`}},

		// The manual double-click: a same-row click inside 400 ms is the
		// primary action; 500 ms apart is just two selections.
		"click":       {calls: []string{}, sel: ip(0)},
		"click-100ms": {calls: []string{`MoreFromArtist("Boards of Canada","Roygbiv")`}},
		"click-far-1": {calls: []string{}, sel: ip(0)},
		"click-far-2": {calls: []string{}, sel: ip(0)},

		// Wheel scrolls the view, never the selection, never the volume.
		"wheel-row":      {calls: []string{}, top: ip(1), sel: ip(0)},
		"wheel-row-back": {calls: []string{}, top: ip(0)},
		"wheel-vol":      {calls: []string{}},
		"ctrl-wheel":     {calls: []string{}, prevented: bp(true)},

		// Filter: / opens it on LOVED, letters go to the input, Enter
		// applies, ESC unwinds text → input → leftover filter.
		"slash-filter":        {calls: []string{`Loved("")`}, filterInput: bp(true)},
		"filter-letter":       {calls: []string{}},
		"filter-type":         {calls: []string{`Loved("zz")`}},
		"esc-filter-text":     {calls: []string{`Loved("")`}, filterInput: bp(true)},
		"esc-filter-close":    {calls: []string{`Loved("")`}, filterInput: bp(false)},
		"filter-leftover":     {calls: []string{`Loved("ab")`}, filterInput: bp(false)},
		"esc-filter-leftover": {calls: []string{`Loved("")`}, filterInput: bp(false)},

		// Search: / from another tab lands focused; letters are the input's;
		// an empty input's space is the virgin wildcard (once, not per
		// repeat); Enter seeds; ESC clears then the next ESC folds.
		"i-again":             {calls: []string{"Info()"}, tab: "info"},
		"slash-search":        {calls: []string{}, tab: "search", searchFocused: bp(true)},
		"search-letter":       {calls: []string{}},
		"search-space":        {calls: []string{"Wildcard()"}},
		"search-space-repeat": {calls: []string{}},
		"search-type":         {calls: []string{}},
		"search-enter":        {calls: []string{`Seed("ambient")`}},
		"esc-search":          {calls: []string{}, searchFocused: bp(false)},
		"esc-fold":            {calls: []string{"SetUnfolded(false)"}},
	}

	for _, name := range wantOrder {
		got := steps[name]
		w := want[name]
		if !reflect.DeepEqual(got.Calls, w.calls) {
			t.Errorf("%s: calls %v, want %v", name, got.Calls, w.calls)
		}
		if w.sel != nil && got.Sel != *w.sel {
			t.Errorf("%s: sel %d, want %d", name, got.Sel, *w.sel)
		}
		if w.top != nil && got.Top != *w.top {
			t.Errorf("%s: top %d, want %d", name, got.Top, *w.top)
		}
		if w.tab != "" && got.Tab != w.tab {
			t.Errorf("%s: tab %q, want %q", name, got.Tab, w.tab)
		}
		if w.remove != "" && (got.Remove == nil || *got.Remove != w.remove) {
			t.Errorf("%s: remove label %v, want %q", name, got.Remove, w.remove)
		}
		if w.filterInput != nil && got.FilterInput != *w.filterInput {
			t.Errorf("%s: filterInput %v, want %v", name, got.FilterInput, *w.filterInput)
		}
		if w.searchFocused != nil && got.SearchFocused != *w.searchFocused {
			t.Errorf("%s: searchFocused %v, want %v", name, got.SearchFocused, *w.searchFocused)
		}
		if w.prevented != nil && got.Prevented != *w.prevented {
			t.Errorf("%s: prevented %v, want %v", name, got.Prevented, *w.prevented)
		}
	}
}

// TestKeyboardMapFolded runs the same driver on keys-folded — a fixture
// that carries loved rows but stays folded — and asserts every row key
// is a no-op: nothing may act on a list that isn't drawn.
func TestKeyboardMapFolded(t *testing.T) {
	edge := findEdge(t)
	srv := startFixtureServer(t)
	url := srv.URL + "/?fixture=keys-folded&keytest=folded"
	args := []string{
		"--headless=new", "--disable-gpu", "--hide-scrollbars",
		"--window-size=480,220", // ~128 px viewport after chrome
		"--force-device-scale-factor=1",
		"--virtual-time-budget=10000",
		"--run-all-compositor-stages-before-draw",
		"--user-data-dir=" + filepath.Join(os.TempDir(), "screech-guard-edge"),
		url,
		"--dump-dom",
	}
	out, err := exec.Command(edge, args...).Output()
	if err != nil {
		t.Fatalf("edge dump-dom failed: %v", err)
	}
	m := keysPreRE.FindSubmatch(out)
	if m == nil {
		t.Fatalf("keys-test.js did not write <pre id=keys>; dom tail: %s", tail(string(out)))
	}
	var res keyResult
	if err := json.Unmarshal([]byte(html.UnescapeString(string(m[1]))), &res); err != nil {
		t.Fatalf("keys JSON did not parse: %v\n%s", err, m[1])
	}
	for _, e := range res.Errors {
		t.Errorf("page error during folded keytest: %s", e)
	}
	wantNames := []string{"folded-x", "folded-x", "folded-Enter", "folded-j", "folded-r"}
	if len(res.Steps) != len(wantNames) {
		t.Fatalf("folded driver ran %d steps, want %d: %+v", len(res.Steps), len(wantNames), res.Steps)
	}
	for i, s := range res.Steps {
		if s.Name != wantNames[i] {
			t.Fatalf("folded step %d is %q, want %q", i, s.Name, wantNames[i])
		}
		if len(s.Calls) != 0 {
			t.Errorf("%s: calls %v, want none — row keys must not act folded", s.Name, s.Calls)
		}
		if s.Unf {
			t.Errorf("%s: reported unfolded", s.Name)
		}
	}
}

func TestLayout(t *testing.T) {
	edge := findEdge(t)
	srv := startFixtureServer(t)
	// Test binaries run in the package dir; .review sits at the repo root.
	shotDir = filepath.Join("..", "..", ".review", "gui")
	if err := os.MkdirAll(shotDir, 0o755); err != nil {
		t.Fatalf("cannot create %s: %v", shotDir, err)
	}
	for _, f := range fixtures() {
		f := f
		t.Run(f.Name, func(t *testing.T) {
			modes := []bool{f.Unfolded}
			if f.BothModes {
				modes = []bool{false, true}
			}
			for _, unfolded := range modes {
				mode := "folded"
				h := 128
				if unfolded {
					mode, h = "unfolded", 428
				}
				for _, scale := range []string{"1", "1.5"} {
					scale := scale
					t.Run(mode+"@"+scale, func(t *testing.T) {
						url := srv.URL + "/?fixture=" + f.Name
						shot := ""
						if scale == "1" {
							name := fmt.Sprintf("%s@%s.png", f.Name, scale)
							if unfolded && f.BothModes {
								name = fmt.Sprintf("%s-unfolded@%s.png", f.Name, scale)
							}
							shot = filepath.Join(shotDir, name)
						}
						res := runGuard(t, edge, url, 480, h, scale, shot)
						for _, v := range res.Violations {
							t.Errorf("%s %s %s: %s", f.Name, mode, scale, v)
						}
						if fixedMaps[mode] == nil {
							fixedMaps[mode] = map[string]map[string]map[string]guardRect{}
						}
						if fixedMaps[mode][f.Name] == nil {
							fixedMaps[mode][f.Name] = map[string]map[string]guardRect{}
						}
						fixedMaps[mode][f.Name][scale] = res.Fixed
						if cfixedMaps[mode] == nil {
							cfixedMaps[mode] = map[string]map[string]map[string]guardRect{}
						}
						if cfixedMaps[mode][f.Name] == nil {
							cfixedMaps[mode][f.Name] = map[string]map[string]guardRect{}
						}
						cfixedMaps[mode][f.Name][scale] = res.CFixed
					})
				}
			}
		})
	}

	// Nothing in the strip may move when the state changes or the window
	// unfolds: every fixture's #strip [data-fixed] rects must equal
	// playing's at the same mode and scale.
	t.Run("strip-compare", func(t *testing.T) {
		for _, mode := range []string{"folded", "unfolded"} {
			base := fixedMaps[mode]["playing"]
			if base == nil {
				t.Fatalf("no playing baseline captured for %s", mode)
			}
			for name, byScale := range fixedMaps[mode] {
				if name == "playing" {
					continue
				}
				for scale, rects := range byScale {
					for id, br := range base[scale] {
						got, ok := rects[id]
						if !ok {
							t.Errorf("%s %s@%s: missing data-fixed id %q", name, mode, scale, id)
							continue
						}
						if got != br {
							t.Errorf("%s %s@%s: %q moved from %+v to %+v", name, mode, scale, id, br, got)
						}
					}
				}
			}
		}
	})

	// The sidebar and presets footer are the console's own fixed shell:
	// every unfolded fixture's #console [data-fixed] rects must equal
	// console-loved's.
	t.Run("console-compare", func(t *testing.T) {
		base := cfixedMaps["unfolded"]["console-loved"]
		if base == nil {
			t.Fatal("no console-loved baseline captured")
		}
		for name, byScale := range cfixedMaps["unfolded"] {
			if name == "console-loved" {
				continue
			}
			for scale, rects := range byScale {
				if len(rects) == 0 {
					continue // folded-mode runs of strip fixtures
				}
				for id, br := range base[scale] {
					got, ok := rects[id]
					if !ok {
						t.Errorf("%s unfolded@%s: missing console data-fixed id %q", name, scale, id)
						continue
					}
					if got != br {
						t.Errorf("%s unfolded@%s: console %q moved from %+v to %+v", name, scale, id, br, got)
					}
				}
			}
		}
	})
}
