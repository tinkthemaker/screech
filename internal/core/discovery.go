package core

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/tinkthemaker/screech/internal/streamurl"
)

// Discovery stations: endless queues of songs found from a seed, a phrase
// ("swedish death metal") or a YouTube link, instead of one radio stream.
// They are ordinary stations rows under a disc: uuid, so listens, loves and
// the bandit attach to them like any other station. Where they differ is
// where they live. A custom: station is config.toml's record and startup
// deletes the ones the file no longer names; a discovery started from the
// FIND bar is history, not config, and must outlive every launch. So the
// namespaces are separate, the directory sync and the custom sync both
// leave disc: rows alone, and only config-defined ones are ever rewritten.

// DiscoverScheme starts the sentinel URL of a discovery station: the seed
// follows it, in the listener's own spelling. It is not a link mpv can
// open. Only the player resolves it, into songs.
const DiscoverScheme = "discover:"

const discoveryPrefix = "disc:"

// DiscoveryKey is what makes two seeds the same station. A YouTube link
// is its video, whichever way it was written (a watch page with a
// playlist along for the ride, a youtu.be share, the music site, a short);
// anything else is the phrase, lowercased with its spaces collapsed.
func DiscoveryKey(seed string) string {
	seed = CleanText(seed)
	if id, ok := streamurl.YouTubeID(seed); ok {
		return "https://www.youtube.com/watch?v=" + id
	}
	return strings.ToLower(seed)
}

// DiscoveryUUID is a discovery station's id: a hash of its key, so
// starting the same phrase or song again finds the same station and its
// history. Case, spacing and the shape of a link never re-identify it.
func DiscoveryUUID(seed string) string {
	sum := sha256.Sum256([]byte(DiscoverScheme + DiscoveryKey(seed)))
	return discoveryPrefix + hex.EncodeToString(sum[:8])
}

// IsDiscovery reports whether uuid names a discovery station.
func IsDiscovery(uuid string) bool { return strings.HasPrefix(uuid, discoveryPrefix) }

// discoveringReason is the pick reason for a discovery. The middle dot is
// the faces' to render: the terminal swaps it for its own glyph in ASCII
// mode.
func discoveringReason(name string) string { return "discovering · " + name }

// discoveryStation fills in what a caller can't be trusted with: the seed
// is read back from the sentinel URL, and the uuid, the resolved URL and
// the text all follow from it. A station that isn't a sentinel, or whose
// seed cleans to nothing, is not a discovery.
func discoveryStation(in Station) (Station, bool) {
	seed, ok := strings.CutPrefix(strings.TrimSpace(in.URL), DiscoverScheme)
	if !ok {
		return Station{}, false
	}
	seed = CleanText(seed)
	if seed == "" {
		return Station{}, false
	}
	name := CleanText(in.Name)
	if name == "" {
		name = seed
	}
	st := Station{
		UUID: DiscoveryUUID(seed), Name: name,
		URL: DiscoverScheme + seed, URLResolved: DiscoverScheme + seed,
		Homepage: CleanText(in.Homepage), Tags: CleanText(in.Tags),
		Country: CleanText(in.Country), Codec: CleanText(in.Codec),
		Bitrate: in.Bitrate, LastCheckOK: true,
	}
	st.AdRisk = ComputeAdRisk(&st)
	return st, true
}

// StartDiscovery tunes a discovery station from a phrase or a link,
// creating it the first time and resuming it (same row, same history,
// same name) after that. Like TuneTo it ends the current listen with skip
// semantics; the new listen starts when the player has been pointed at the
// station. The row lands in the database straight away: FIND-started
// stations are history, not config.
func (c *Core) StartDiscovery(seed string, now time.Time) (Pick, error) {
	st, ok := discoveryStation(Station{URL: DiscoverScheme + seed})
	if !ok {
		return Pick{}, errors.New("nothing to discover from")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	have := c.stationByUUIDLocked(st.UUID)
	if have == nil {
		// Only a new row is written. An upsert over a known one would put
		// a song's name back to its link and quietly forgive a strike.
		if err := c.store.UpsertStations([]Station{st}, now); err != nil {
			return Pick{}, err
		}
		c.stations = append(c.stations, st)
		have = c.stationByUUIDLocked(st.UUID)
	}
	c.endListenLocked(now, true)
	return Pick{Station: *have, Reason: discoveringReason(have.Name)}, nil
}

// SetDiscoveryStations upserts the discovery stations config.toml names,
// as SetCustomStations does for links, with one difference: it never
// deletes. A discovery taken out of the file stays as history, and the
// ones the FIND bar started were never in it. Entries that aren't a
// sentinel URL, or have an empty seed, are skipped.
func (c *Core) SetDiscoveryStations(sts []Station, now time.Time) error {
	rows := make([]Station, 0, len(sts))
	seen := map[string]bool{}
	for _, in := range sts {
		st, ok := discoveryStation(in)
		if !ok || seen[st.UUID] {
			continue
		}
		seen[st.UUID] = true
		rows = append(rows, st)
	}
	if len(rows) == 0 {
		return nil
	}
	if err := c.store.UpsertStations(rows, now); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.reload()
}

// RenameDiscovery gives a station that was started from a YouTube link the
// name of the song it started from, once the player knows it. It only ever
// replaces the link itself: a phrase is already a name, and a name the
// listener chose in config.toml, or one set before, stays. Anything that
// doesn't apply is not an error; the caller offers every first title.
func (c *Core) RenameDiscovery(uuid, name string) error {
	if !IsDiscovery(uuid) {
		return errors.New("not a discovery station")
	}
	name = CleanText(name)
	c.mu.Lock()
	defer c.mu.Unlock()
	st := c.stationByUUIDLocked(uuid)
	if st == nil {
		return errors.New("station no longer in cache")
	}
	seed := strings.TrimPrefix(st.URL, DiscoverScheme)
	if _, isLink := streamurl.YouTubeID(seed); !isLink || name == "" || st.Name != seed {
		return nil
	}
	if err := c.store.RenameStation(uuid, name); err != nil {
		return err
	}
	st.Name = name
	return nil
}

// DiscoveryRow is one line of FIND's "pick up where you left off".
type DiscoveryRow struct {
	UUID, Name string
	LastAt     time.Time // when its latest listen began
}

// RecentDiscoveries lists up to n discovery stations that have been
// listened to, the latest listen first.
func (c *Core) RecentDiscoveries(n int) ([]DiscoveryRow, error) {
	if n <= 0 {
		return nil, nil
	}
	return c.store.RecentDiscoveries(n)
}

// KnownArtist reports whether an artist (by NormalizeArtist key) has been
// heard on Screech or loved: the test behind NEW TO YOU. A song's artist
// is recorded by NoteTitle, so ask before noting its title, not after.
func (c *Core) KnownArtist(artistKey string) bool {
	if artistKey == "" {
		return false
	}
	known, err := c.store.HeardArtistKey(artistKey)
	return err == nil && known
}
