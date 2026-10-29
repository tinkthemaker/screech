package core

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// Every way of naming one YouTube song is one station; a phrase is the
// same station however it was cased or spaced.
func TestDiscoveryKeyCanonicalisesLinks(t *testing.T) {
	const want = "https://www.youtube.com/watch?v=abcdefghijk"
	for _, link := range []string{
		"https://www.youtube.com/watch?v=abcdefghijk&list=PL1",
		"https://youtu.be/abcdefghijk?t=3",
		"https://music.youtube.com/watch?v=abcdefghijk",
		"https://www.youtube.com/shorts/abcdefghijk",
		" youtu.be/abcdefghijk ",
	} {
		if got := DiscoveryKey(link); got != want {
			t.Errorf("DiscoveryKey(%q) = %q, want %q", link, got, want)
		}
		if DiscoveryUUID(link) != DiscoveryUUID("https://youtu.be/abcdefghijk") {
			t.Errorf("DiscoveryUUID(%q) differs from the same video's other links", link)
		}
	}
	if got := DiscoveryKey(" Swedish  Death Metal "); got != "swedish death metal" {
		t.Errorf("phrase key = %q, want %q", got, "swedish death metal")
	}
	if DiscoveryUUID("Swedish Death Metal") != DiscoveryUUID("  swedish   death metal") {
		t.Error("case and spacing must not make a second station")
	}
	if DiscoveryUUID("swedish death metal") == DiscoveryUUID("dungeon synth") {
		t.Error("different phrases must be different stations")
	}
	// A link that isn't a video stays a phrase: nothing to canonicalise.
	if got := DiscoveryKey("https://www.youtube.com/playlist?list=PL1"); got != "https://www.youtube.com/playlist?list=pl1" {
		t.Errorf("non-video link key = %q, want it lowercased as text", got)
	}
}

func TestDiscoveryUUIDShapeAndNamespace(t *testing.T) {
	id := DiscoveryUUID("swedish death metal")
	if !IsDiscovery(id) || IsCustom(id) || len(id) != len("disc:")+16 {
		t.Fatalf("DiscoveryUUID = %q, want disc: and 16 hex digits", id)
	}
	if IsDiscovery(CustomUUID("https://x.example/")) || IsDiscovery("seed:abc") || IsDiscovery("") {
		t.Error("IsDiscovery must be true for the disc: namespace only")
	}
	// Control bytes in typed text must not make a second station.
	if DiscoveryUUID("swedish \x07death metal") != DiscoveryUUID("swedish death metal") {
		t.Error("the id of a phrase is the id of its cleaned text")
	}
}

func TestStartDiscoveryCreatesAndResumes(t *testing.T) {
	c := openTestCore(t)
	now := time.Now()
	p, err := c.StartDiscovery("Swedish death metal", now)
	if err != nil {
		t.Fatal(err)
	}
	if p.Station.UUID != DiscoveryUUID("swedish death metal") || !IsDiscovery(p.Station.UUID) {
		t.Errorf("uuid = %q", p.Station.UUID)
	}
	if got := p.Station.StreamURL(); got != "discover:Swedish death metal" {
		t.Errorf("StreamURL = %q, want the sentinel keeping the seed's text", got)
	}
	if p.Station.Name != "Swedish death metal" || !p.Station.LastCheckOK {
		t.Errorf("station = %+v", p.Station)
	}
	if !strings.HasPrefix(p.Reason, "discovering") || !strings.HasSuffix(p.Reason, "Swedish death metal") {
		t.Errorf("reason = %q, want discovering · <name>", p.Reason)
	}
	before := c.StationCount()
	if _, ok := c.StationByUUID(p.Station.UUID); !ok {
		t.Fatal("the station must be in the cache")
	}

	again, err := c.StartDiscovery(" swedish  DEATH metal ", now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if again.Station.UUID != p.Station.UUID || c.StationCount() != before {
		t.Errorf("the same phrase must resume one row: %q vs %q, %d vs %d rows",
			again.Station.UUID, p.Station.UUID, c.StationCount(), before)
	}
	if again.Station.StreamURL() != "discover:Swedish death metal" {
		t.Errorf("resuming kept %q, want the first spelling", again.Station.StreamURL())
	}

	if _, err := c.StartDiscovery("  \x1b[31m  ", now); err == nil {
		t.Error("a seed with nothing in it can't start a station")
	}
}

// A discovery is a deliberate choice: starting one ends the listen in
// progress as a preset recall does, and writes nothing about the new one
// until the player is pointed at it (StartListen).
func TestStartDiscoveryEndsTheCurrentListen(t *testing.T) {
	c := openTestCore(t)
	now := time.Now()
	c.StartListen("seed:one", now.Add(-time.Hour))
	if !c.ListenOpen() {
		t.Fatal("no listen to end")
	}
	if _, err := c.StartDiscovery("dungeon synth", now); err != nil {
		t.Fatal(err)
	}
	if c.ListenOpen() {
		t.Error("starting a discovery must end the current listen")
	}
	if got := c.ResumeUUID(); got != "seed:one" {
		t.Errorf("last_station = %q; a station not yet playing must not become the resume point", got)
	}
}

func TestDiscoveryRowsSurvivePruneAndCustomSync(t *testing.T) {
	c := openTestCore(t)
	old := time.Now().Add(-30 * 24 * time.Hour)
	p, err := c.StartDiscovery("dungeon synth", old)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.store.UpsertStations([]Station{{UUID: "gone-plain", Name: "G", URL: "http://x/g", URLResolved: "http://x/g"}}, old); err != nil {
		t.Fatal(err)
	}
	pruned, err := c.store.PruneStale(time.Now(), "")
	if err != nil {
		t.Fatal(err)
	}
	if pruned != 1 {
		t.Fatalf("pruned %d, want only the plain directory station", pruned)
	}
	if err := c.SetCustomStations(nil, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.StationByUUID(p.Station.UUID); !ok {
		t.Fatal("PruneStale and SetCustomStations(nil) must both leave disc: rows")
	}
	left, err := c.store.LoadStations()
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, st := range left {
		if IsDiscovery(st.UUID) {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%d disc: rows in the database, want 1", n)
	}
}

func TestNeedsSyncIgnoresDiscovery(t *testing.T) {
	c := openTestCore(t)
	var mine []Station
	for i := 0; i < 2*len(seedStations); i++ {
		seed := fmt.Sprint("phrase ", i)
		mine = append(mine, Station{Name: seed, URL: DiscoverScheme + seed})
	}
	if err := c.SetDiscoveryStations(mine, time.Now()); err != nil {
		t.Fatal(err)
	}
	if c.StationCount() < 2*len(seedStations) {
		t.Fatalf("StationCount = %d; the discovery stations should be cached", c.StationCount())
	}
	if !c.NeedsSync() {
		t.Fatal("seeds plus discovery stations is still a cache that has never synced")
	}
	if c.SyncStale(20000) {
		t.Fatal("the first-run sync owns this case, not the background refresh")
	}
}

// Config-defined discovery stations are upserted at every launch and never
// deleted: removing one from the file leaves it as history.
func TestSetDiscoveryStationsUpsertsAndNeverDeletes(t *testing.T) {
	c := openTestCore(t)
	now := time.Now()
	a := Station{Name: "Dungeon \x1b]0;pwned\x07Synth", URL: DiscoverScheme + "dungeon synth", Tags: "dungeon synth,\x1b[31mambient"}
	b := Station{Name: "Deaths", URL: DiscoverScheme + "swedish death metal"}
	if err := c.SetDiscoveryStations([]Station{a, b, b, {Name: "No seed", URL: DiscoverScheme + "  "}, {Name: "Link", URL: "https://x.example/"}}, now); err != nil {
		t.Fatal(err)
	}
	ua := DiscoveryUUID("dungeon synth")
	st, ok := c.StationByUUID(ua)
	if !ok {
		t.Fatal("dungeon synth should be cached under its disc: id")
	}
	if st.Name != "Dungeon Synth" || st.StreamURL() != "discover:dungeon synth" || !st.LastCheckOK || st.Tags != "dungeon synth,ambient" {
		t.Errorf("cached station = %+v, want cleaned text and the sentinel as its stream", st)
	}
	if c.StationCount() != len(seedStations)+2 {
		t.Errorf("StationCount = %d, want seeds plus 2 (a repeat collapses; empty and non-sentinel entries are dropped)", c.StationCount())
	}

	if err := c.SetDiscoveryStations([]Station{{Name: "Renamed", URL: DiscoverScheme + "dungeon synth"}}, now); err != nil {
		t.Fatal(err)
	}
	if st, _ := c.StationByUUID(ua); st.Name != "Renamed" {
		t.Errorf("a second launch must refresh the name, have %q", st.Name)
	}
	if _, ok := c.StationByUUID(DiscoveryUUID("swedish death metal")); !ok {
		t.Error("an entry missing from the config must stay: SetDiscoveryStations never deletes")
	}
}

func TestRecentDiscoveriesNewestFirst(t *testing.T) {
	c := openTestCore(t)
	t0 := time.Now().Add(-10 * time.Hour).Truncate(time.Second)
	t1, t2, t3 := t0.Add(time.Hour), t0.Add(2*time.Hour), t0.Add(3*time.Hour)
	var ids []string
	for _, seed := range []string{"one", "two", "three"} {
		p, err := c.StartDiscovery(seed, t0)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, p.Station.UUID)
	}
	// A discovery never listened to is not "where you left off", and a
	// radio station's listens are not discoveries.
	if _, err := c.StartDiscovery("never played", t0); err != nil {
		t.Fatal(err)
	}
	// Listens in an order unlike creation order: two is oldest, one newest.
	c.StartListen(ids[1], t1)
	c.EndListen(t1.Add(time.Minute))
	c.StartListen(ids[2], t2)
	c.EndListen(t2.Add(time.Minute))
	c.StartListen("seed:radio", t2.Add(time.Hour))
	c.EndListen(t2.Add(time.Hour + time.Minute))
	c.StartListen(ids[0], t3)
	c.EndListen(t3.Add(time.Minute))
	// An older listen of the newest must not duplicate or reorder it.
	c.StartListen(ids[1], t3.Add(-time.Minute*30))
	c.EndListen(t3.Add(-time.Minute * 29))

	rows, err := c.RecentDiscoveries(2)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].UUID != ids[0] || rows[1].UUID != ids[1] {
		t.Fatalf("RecentDiscoveries(2) = %+v, want [one, two] (latest listen start first)", rows)
	}
	if rows[0].Name != "one" || !rows[0].LastAt.Equal(t3) {
		t.Errorf("row 0 = %+v, want name one at %v", rows[0], t3)
	}
	all, err := c.RecentDiscoveries(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("RecentDiscoveries(10) = %+v, want the 3 that were listened to", all)
	}
	if none, err := c.RecentDiscoveries(0); err != nil || len(none) != 0 {
		t.Errorf("RecentDiscoveries(0) = %+v, %v; want nothing", none, err)
	}
}

func TestKnownArtist(t *testing.T) {
	c := openTestCore(t)
	now := time.Now()
	if c.KnownArtist("entombed") {
		t.Fatal("nobody has been heard yet")
	}
	if c.KnownArtist("") {
		t.Fatal("an empty key is nobody")
	}
	c.StartListen("seed:one", now)
	tr, ok, _, _ := c.NoteTitle("Entombed - Left Hand Path", now)
	if !ok || tr.ArtistKey != "entombed" {
		t.Fatalf("parsed %+v ok=%v", tr, ok)
	}
	if !c.KnownArtist("entombed") {
		t.Error("a heard artist is known")
	}
	if c.KnownArtist("dismember") {
		t.Error("an artist never heard is new to you")
	}

	// A loved artist is known even if the history has been cleared.
	if err := c.store.InsertLoved("seed:one", "carcass", "Carcass", "Heartwork", now); err != nil {
		t.Fatal(err)
	}
	if !c.KnownArtist("carcass") {
		t.Error("a loved artist is known")
	}
}

func TestRenameDiscoveryOnlyReplacesLinkName(t *testing.T) {
	c := openTestCore(t)
	now := time.Now()
	link := "https://youtu.be/abcdefghijk?t=3"
	p, err := c.StartDiscovery(link, now)
	if err != nil {
		t.Fatal(err)
	}
	id := p.Station.UUID
	if p.Station.Name != link {
		t.Fatalf("a link station starts named after its link, have %q", p.Station.Name)
	}
	if err := c.RenameDiscovery(id, "Entombed - Left Hand Path"); err != nil {
		t.Fatal(err)
	}
	if st, _ := c.StationByUUID(id); st.Name != "Entombed - Left Hand Path" {
		t.Errorf("name = %q, want the song's", st.Name)
	}
	if st, _ := c.StationByUUID(id); st.StreamURL() != "discover:"+link {
		t.Errorf("the sentinel must keep the link, have %q", st.StreamURL())
	}
	// Once named, it stays named, even after the link is started again.
	if err := c.RenameDiscovery(id, "Something Else"); err != nil {
		t.Fatal(err)
	}
	if st, _ := c.StationByUUID(id); st.Name != "Entombed - Left Hand Path" {
		t.Errorf("a renamed station was renamed again: %q", st.Name)
	}
	again, err := c.StartDiscovery(link, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if again.Station.Name != "Entombed - Left Hand Path" {
		t.Errorf("resuming a link must keep its song name, have %q", again.Station.Name)
	}

	// A phrase is already a name.
	ph, err := c.StartDiscovery("dungeon synth", now)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.RenameDiscovery(ph.Station.UUID, "Some Song"); err != nil {
		t.Fatal(err)
	}
	if st, _ := c.StationByUUID(ph.Station.UUID); st.Name != "dungeon synth" {
		t.Errorf("a phrase station was renamed to %q", st.Name)
	}

	// Radio stations and unknown ids are not this method's business.
	if err := c.RenameDiscovery("seed:one", "Nope"); err == nil {
		t.Error("renaming a non-discovery station should be refused")
	}
	if err := c.RenameDiscovery(DiscoveryUUID("never started"), "Nope"); err == nil {
		t.Error("renaming a station that isn't cached should be refused")
	}
	if st, ok := c.StationByUUID("seed:one"); ok && st.Name == "Nope" {
		t.Error("a radio station was renamed")
	}
	// A name that cleans to nothing changes nothing.
	lk, _ := c.StartDiscovery("https://youtu.be/zyxwvutsrqp", now)
	if err := c.RenameDiscovery(lk.Station.UUID, " \x07 "); err != nil {
		t.Fatal(err)
	}
	if st, _ := c.StationByUUID(lk.Station.UUID); st.Name != "https://youtu.be/zyxwvutsrqp" {
		t.Errorf("name = %q after an empty rename", st.Name)
	}
}

// The listener's discoveries are seated beside their own links: a few
// seats per pick, labelled for what they are.
func TestPoolSeatsDiscoveryStations(t *testing.T) {
	c := openTestCore(t)
	now := time.Now()
	var dir []Station
	for i := 0; i < 3000; i++ {
		id := fmt.Sprintf("dir-%04d", i)
		dir = append(dir, Station{UUID: id, Name: id, URL: "http://x/" + id, URLResolved: "http://x/" + id, Tags: "rock", LastCheckOK: true})
	}
	if err := c.store.UpsertStations(dir, now); err != nil {
		t.Fatal(err)
	}
	if err := c.store.PutTagAffinity("rock", 6, now); err != nil {
		t.Fatal(err)
	}
	if err := c.SetDiscoveryStations([]Station{
		{Name: "Dungeon", URL: DiscoverScheme + "dungeon synth"},
		{Name: "Deaths", URL: DiscoverScheme + "swedish death metal"},
	}, now); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		seated := 0
		for _, cd := range c.buildPool(now, c.rng) {
			if IsDiscovery(cd.st.UUID) {
				seated++
				if r := c.reason(cd, DaypartFor(now), now); r != discoveringReason(cd.st.Name) {
					t.Errorf("reason for a discovery = %q, want %q", r, discoveringReason(cd.st.Name))
				}
			}
		}
		if seated != 2 {
			t.Fatalf("pool %d seated %d discoveries, want both", i, seated)
		}
	}
	if r := discoveringReason("Dungeon"); !strings.HasPrefix(r, "discovering ") || !strings.HasSuffix(r, " Dungeon") {
		t.Errorf("discoveringReason = %q", r)
	}
}

// reportTransport tells the test when a request went out.
type reportTransport chan string

func (r reportTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	r <- req.URL.Path
	return nil, errors.New("offline")
}

// radio-browser knows nothing of the listener's discoveries, and their
// ids are nobody else's business.
func TestClickSkipsDiscovery(t *testing.T) {
	sent := make(reportTransport, 4)
	rb := NewRadioBrowser()
	rb.client = &http.Client{Transport: sent}
	rb.base = "https://radio.invalid" // no server lookup to report

	rb.Click("some-directory-uuid")
	select {
	case path := <-sent:
		if path != "/json/url/some-directory-uuid" {
			t.Fatalf("click went to %q", path)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a directory station's tune-in should be reported; the harness sees nothing")
	}

	rb.Click(DiscoveryUUID("dungeon synth"))
	select {
	case path := <-sent:
		t.Fatalf("a discovery station was reported to radio-browser: %q", path)
	case <-time.After(200 * time.Millisecond):
	}
}

// A seed that is a link but not a YouTube video is not something discovery
// can start from: it would only strike a station that was never real.
func TestStartDiscoveryRefusesLinksThatAreNotAVideo(t *testing.T) {
	c := openTestCore(t)
	rows := c.StationCount()
	for _, seed := range []string{
		"https://www.youtube.com/playlist?list=PL1234567890",
		"https://soundcloud.com/some/song",
		"ftp://example.com/x",
	} {
		if _, err := c.StartDiscovery(seed, time.Now()); err == nil {
			t.Errorf("StartDiscovery(%q) succeeded", seed)
		}
	}
	if n := c.StationCount(); n != rows {
		t.Errorf("refused seeds left %d station rows behind", n-rows)
	}
	// A video link and a phrase still start.
	for _, seed := range []string{"https://youtu.be/abcdefghijk", "swedish death metal"} {
		if _, err := c.StartDiscovery(seed, time.Now()); err != nil {
			t.Errorf("StartDiscovery(%q): %v", seed, err)
		}
	}
}
