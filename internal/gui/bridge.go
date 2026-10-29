package gui

import (
	"errors"
	"strings"
	"sync/atomic"
	"time"

	"github.com/tinkthemaker/screech/internal/core"
	"github.com/tinkthemaker/screech/internal/session"
)

// Emitter is how the bridge reaches the page: Wails' runtime.EventsEmit
// behind a tiny interface. "snapshot" carries a session.Snapshot, "level"
// a [3]float64{L, R, Peak}.
type Emitter interface {
	Emit(event string, data any)
}

// Window is the bit of the shell the bridge drives: fold state, quit,
// and the outline after a monitor-scale change.
type Window interface {
	SetUnfolded(unfolded bool) // resize, keep the strip where it is, persist
	Quit()
	Rescaled() // the window crossed to another scale; reshape it in place
}

// Prefs are the shell's display choices the page can't read on its own —
// Wails v2 has no start-URL option, so --reduced-motion has no query
// string to ride on.
type Prefs struct {
	ReducedMotion bool `json:"reducedMotion"`
}

// Bridge is the page's bound object: every method Wails exposes is one
// queued intent or one core read — nothing on this surface blocks.
type Bridge struct {
	c      *core.Core
	runner *session.Runner
	win    Window
	prefs  Prefs
	em     atomic.Value // Emitter; nil until SetEmitter
}

// New builds the bridge; Attach and SetEmitter finish the wiring once the
// shell has a runner and a runtime.
func New(c *core.Core, win Window, prefs Prefs) *Bridge {
	return &Bridge{c: c, win: win, prefs: prefs}
}

// Prefs reports the shell's display choices to the page.
func (b *Bridge) Prefs() Prefs { return b.prefs }

func (b *Bridge) Attach(r *session.Runner) { b.runner = r }

// SetEmitter is called from the shell's OnStartup; events raised before
// it are dropped, since the page isn't there to hear them.
func (b *Bridge) SetEmitter(e Emitter) { b.em.Store(e) }

func (b *Bridge) emitter() Emitter {
	if v := b.em.Load(); v != nil {
		return v.(Emitter)
	}
	return nil
}

// Sink returns the session.Sink side of the bridge. Its methods stay
// unexported names on an unexported type so Wails doesn't bind them.
func (b *Bridge) Sink() session.Sink { return sink{b} }

// intent is the one path intents take: Touch first (the window's
// "any keypress clears the notice" is the same rule the terminal
// follows), then the message. Send never blocks after Stop.
func (b *Bridge) intent(m session.Msg) {
	if b.runner == nil {
		return
	}
	b.runner.Send(session.Touch{})
	b.runner.Send(m)
}

func (b *Bridge) Current() session.Snapshot {
	if b.runner == nil {
		return session.Snapshot{}
	}
	return b.runner.Current()
}

func (b *Bridge) Next()         { b.intent(session.Next{}) }
func (b *Bridge) Love()         { b.intent(session.Love{}) }
func (b *Bridge) TogglePause()  { b.intent(session.TogglePause{}) }
func (b *Bridge) CycleSleep()   { b.intent(session.CycleSleep{}) }
func (b *Bridge) TogglePreset() { b.intent(session.TogglePreset{}) }

// RecallPreset tunes slot 1–9; anything else is ignored.
func (b *Bridge) RecallPreset(slot int) {
	if slot < 1 || slot > 9 {
		return
	}
	b.intent(session.RecallPreset{Slot: slot})
}

func (b *Bridge) SetVolume(percent int) { b.intent(session.SetVolume{Percent: percent}) }

// StepVolume nudges the volume; the page sends ±5.
func (b *Bridge) StepVolume(delta int) { b.intent(session.StepVolume{Delta: delta}) }

// Seed starts a discovery pick; a blank query is ignored.
func (b *Bridge) Seed(query string) {
	query = strings.TrimSpace(query)
	if query == "" {
		return
	}
	b.intent(session.Seed{Query: query})
}

// Wildcard is the virgin first-run shortcut: pick anything.
func (b *Bridge) Wildcard() { b.intent(session.Wildcard{}) }

// ReturnToStation tunes to the station a loved track came from.
func (b *Bridge) ReturnToStation(uuid string) {
	b.intent(session.TuneTo{UUID: uuid, Reason: "from a loved track"})
}

// MoreFromArtist seeds a pick from the track's artist — or its title when
// the title is all the stream gave us; both empty is ignored.
func (b *Bridge) MoreFromArtist(artist, title string) {
	q := strings.TrimSpace(artist)
	if q == "" {
		q = strings.TrimSpace(title)
	}
	if q == "" {
		return
	}
	b.intent(session.Seed{Query: q})
}

// TuneSaved tunes a station from the saved list.
func (b *Bridge) TuneSaved(uuid string) {
	b.intent(session.TuneTo{UUID: uuid, Reason: "from saved stations"})
}

// Recent is the RECENT tab: the last hundred tracks heard.
func (b *Bridge) Recent() ([]RecentRow, error) {
	tracks, err := b.c.RecentlyHeard(100)
	if err != nil {
		return nil, err
	}
	return recentRows(tracks), nil
}

// Loved is the LOVED tab: the deduplicated library run through the same
// ranking filter the terminal uses.
func (b *Bridge) Loved(filter string) ([]LovedRow, error) {
	entries, err := b.c.LovedTracks(500)
	if err != nil {
		return nil, err
	}
	return lovedRows(core.FilterLovedTracks(entries, filter)), nil
}

// Stations is the STATIONS tab: presets and loved stations, filtered the
// same way the terminal filters them.
func (b *Bridge) Stations(filter string) ([]StationRow, error) {
	entries, err := b.c.SavedStations()
	if err != nil {
		return nil, err
	}
	return stationRows(core.FilterSavedStations(entries, filter)), nil
}

// Taste is the TASTE tab.
func (b *Bridge) Taste() (TasteView, error) {
	t, err := b.c.Taste(time.Now())
	if err != nil {
		return TasteView{}, err
	}
	return tasteView(t), nil
}

// Info is the station sheet for whatever is tuned right now.
func (b *Bridge) Info() (InfoView, error) {
	sn := b.Current()
	if !sn.HaveStation {
		return InfoView{}, errors.New("nothing tuned yet")
	}
	st, ok := b.c.StationByUUID(sn.Station.UUID)
	if !ok {
		return InfoView{}, errors.New("nothing tuned yet")
	}
	info, err := b.c.StationInfo(sn.Station.UUID, time.Now())
	if err != nil {
		return InfoView{}, err
	}
	return infoView(st, info, sn.CurrentSlot), nil
}

// RemoveLoved forgets one track; the terminal's wording rides back both
// as the error and as the session's note.
func (b *Bridge) RemoveLoved(artistKey, title string) error {
	removed, err := b.c.ForgetLovedTrack(artistKey, title)
	if err != nil {
		b.intent(session.Notify{Note: "could not remove loved track"})
		return errors.New("could not remove loved track")
	}
	if removed {
		b.intent(session.Notify{Feedback: "LIBRARY  Track removed"})
	}
	return nil
}

// RemoveStation drops a station's preset slot and loved rows, then tells
// the session to reread its presets.
func (b *Bridge) RemoveStation(uuid string) error {
	if err := b.c.RemoveStation(uuid); err != nil {
		b.intent(session.Notify{Note: "could not remove station"})
		return errors.New("could not remove station")
	}
	b.intent(session.RefreshPresets{})
	b.intent(session.Notify{Feedback: "STATIONS  Removed"})
	return nil
}

// SetUnfolded folds the window back to the strip or opens the console;
// the shell owns the resize and the placement save.
func (b *Bridge) SetUnfolded(unfolded bool) {
	if b.runner != nil {
		b.runner.Send(session.Touch{})
	}
	b.win.SetUnfolded(unfolded)
}

// Quit asks the shell to close; the shell's OnBeforeClose stops the
// runner.
func (b *Bridge) Quit() { b.win.Quit() }

// Rescaled is the page reporting a devicePixelRatio change: the window
// moved to a monitor at another scale. The page is the only part that
// hears it — Wails resizes the window on WM_DPICHANGED without telling
// Go — so it relays it to the shell, which reshapes the outline.
func (b *Bridge) Rescaled() { b.win.Rescaled() }
