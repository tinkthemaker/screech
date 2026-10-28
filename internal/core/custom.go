package core

import (
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"strings"
	"time"
)

// Listener-added stations: streams named in config.toml's [[stations]]
// tables, by hand or by `screech add`. The config file is their record; the
// database only caches them, under a custom: uuid, so listens, loves and the
// bandit attach to them like any other station and the database stays
// disposable.

const customPrefix = "custom:"

// CustomUUID is a listener-added station's id: a hash of its link, so
// reordering or renaming entries in the config never re-identifies a
// station, and removing a link and adding it back finds its history again.
// Scheme and host are case-folded first because they are case-insensitive.
func CustomUUID(link string) string {
	key := link
	if u, err := url.Parse(link); err == nil {
		u.Scheme = strings.ToLower(u.Scheme)
		u.Host = strings.ToLower(u.Host)
		key = u.String()
	}
	sum := sha256.Sum256([]byte(key))
	return customPrefix + hex.EncodeToString(sum[:8])
}

// IsCustom reports whether uuid names a listener-added station.
func IsCustom(uuid string) bool { return strings.HasPrefix(uuid, customPrefix) }

// customStation fills in what the config doesn't carry: the uuid, the link
// as the resolved URL (there is no directory to resolve it), and the same
// ad-risk curation every directory station gets. Text is cleaned again
// here so the cache never trusts its caller.
func customStation(st Station) Station {
	link := strings.TrimSpace(st.URL)
	return Station{
		UUID: CustomUUID(link), Name: CleanText(st.Name),
		URL: link, URLResolved: link,
		Homepage: CleanText(st.Homepage), Tags: CleanText(st.Tags),
		Country: CleanText(st.Country), Codec: CleanText(st.Codec),
		Bitrate: st.Bitrate, LastCheckOK: true,
	}
}

// SetCustomStations makes the listener's own stations part of the cache:
// each is upserted under its custom: uuid, and custom rows whose link is no
// longer configured are dropped. Startup calls it once with config.toml's
// list, so an edit to the file (or `screech add`/`remove`) lands on the
// next launch. The directory sync never touches these rows: its uuids can't
// collide with custom: ones, and PruneStale spares the namespace.
func (c *Core) SetCustomStations(sts []Station, now time.Time) error {
	rows := make([]Station, 0, len(sts))
	keep := map[string]bool{}
	for _, in := range sts {
		st := customStation(in)
		if keep[st.UUID] {
			continue
		}
		keep[st.UUID] = true
		st.AdRisk = ComputeAdRisk(&st)
		rows = append(rows, st)
	}
	if err := c.store.UpsertStations(rows, now); err != nil {
		return err
	}
	if _, err := c.store.DeleteCustomExcept(keep); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.reload()
}
