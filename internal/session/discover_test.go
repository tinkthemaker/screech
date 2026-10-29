package session

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tinkthemaker/screech/internal/core"
	"github.com/tinkthemaker/screech/internal/player"
)

const nextSongFeedback = "NEXT SONG  Again for a new station"

// playingDiscovery runs Discover through the session and locks the stream,
// the way a listener's FIND does.
func playingDiscovery(t *testing.T, s State, seed string) State {
	t.Helper()
	s = drive(t, s, Discover{Query: seed})
	if !s.HaveStation || !core.IsDiscovery(s.Station.UUID) {
		t.Fatalf("Discover(%q) should land a discovery station, got %q", seed, s.Station.UUID)
	}
	s = drive(t, s, PlayerEvent(player.Event{Type: player.EventPlaying}))
	if s.Phase != Playing {
		t.Fatalf("want Playing, got %v", s.Phase)
	}
	return s
}

func evTitle(title string, newToYou bool) Msg {
	return PlayerEvent(player.Event{Type: player.EventTitle, Title: title, NewToYou: newToYou})
}

func TestDiscoverIntentTunesDiscoveryStation(t *testing.T) {
	s, pl := testState(t)
	s = drive(t, s, Discover{Query: "swedish death metal"})
	if s.Phase != Tuning {
		t.Fatalf("Discover should tune, got %v", s.Phase)
	}
	snap := s.Snapshot()
	if !snap.Discovery {
		t.Fatal("a discovery station must say so on the snapshot")
	}
	if !strings.HasPrefix(snap.Reason, "discovering") {
		t.Fatalf("reason should start with discovering, got %q", snap.Reason)
	}
	if got := pl.lastPlay(); got != core.DiscoverScheme+"swedish death metal" {
		t.Fatalf("the player should be pointed at the sentinel, got %q", got)
	}
	if !s.core.ListenOpen() {
		t.Fatal("the listen should open once the player has been pointed at the station")
	}
}

// The session owns the dot: core hands back a literal middle dot, and the
// terminal's ASCII mode must stay 7-bit.
func TestDiscoverReasonUsesTheSessionDot(t *testing.T) {
	c, err := core.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	s := New(c, newStubPlayer(), Options{Dot: "-"})
	s = drive(t, s, Discover{Query: "Swedish   Death Metal"})
	want := "discovering - " + s.Station.Name
	if s.Reason != want {
		t.Fatalf("reason = %q, want %q", s.Reason, want)
	}
	for _, r := range s.Reason {
		if r > 0x7f {
			t.Fatalf("reason %q leaks a non-ASCII rune into ascii mode", s.Reason)
		}
	}
}

func TestDiscoverBlankQueryDoesNothing(t *testing.T) {
	s, pl := testState(t)
	s, cmds := s.Update(Discover{Query: "   "})
	if len(cmds) != 0 || s.Phase != Idle || pl.playCount() != 0 {
		t.Fatalf("a blank query starts nothing: cmds=%d phase=%v plays=%d", len(cmds), s.Phase, pl.playCount())
	}
}

// A seed that cleans to nothing is the listener's typo, not a dead
// session: on a virgin session a failed Tuned would set Fatal.
func TestDiscoverFailureIsANoteNotFatal(t *testing.T) {
	s, _ := testState(t)
	s = drive(t, s, Discover{Query: "\x1b[31m"})
	if s.Fatal != "" || s.Phase == Stopped {
		t.Fatalf("a bad query must not stop the session: fatal=%q phase=%v", s.Fatal, s.Phase)
	}
	if s.Note == "" {
		t.Fatal("the listener should be told why nothing started")
	}
}

func TestNextOnDiscoveryPlaysNextSong(t *testing.T) {
	s, pl := testState(t)
	s = playingDiscovery(t, s, "swedish death metal")
	uuid, seq, started, plays := s.Station.UUID, s.PickSeq, s.PlayStart, pl.playCount()

	s = drive(t, s, Next{})

	if n := pl.nextSongCount(); n != 1 {
		t.Fatalf("NextSong should be called once, got %d", n)
	}
	if s.Feedback != nextSongFeedback {
		t.Fatalf("feedback = %q, want %q", s.Feedback, nextSongFeedback)
	}
	if got := s.Snapshot().Feedback; got != nextSongFeedback {
		t.Fatalf("snapshot feedback = %q", got)
	}
	// The listen is unchanged: no skip, no retune, no new Play.
	if s.Phase != Playing || s.PickSeq != seq || s.Station.UUID != uuid || !s.PlayStart.Equal(started) {
		t.Fatalf("NEXT SONG must leave the station alone: phase=%v seq=%d->%d", s.Phase, seq, s.PickSeq)
	}
	if pl.playCount() != plays {
		t.Fatal("NEXT SONG must not Play anything: the player walks its own queue")
	}
	if !s.core.ListenOpen() {
		t.Fatal("the listen must stay open, so the station is not charged a skip")
	}
}

func TestNextOnBufferingDiscoveryPlaysNextSong(t *testing.T) {
	s, pl := testState(t)
	s = playingDiscovery(t, s, "swedish death metal")
	s = drive(t, s, PlayerEvent(player.Event{Type: player.EventBuffering}))
	if s.Phase != Buffering {
		t.Fatalf("want Buffering, got %v", s.Phase)
	}
	s = drive(t, s, Next{})
	if pl.nextSongCount() != 1 || s.Phase != Buffering {
		t.Fatalf("NEXT while buffering is still next-song: calls=%d phase=%v", pl.nextSongCount(), s.Phase)
	}
}

// Until the stream locks there is no song to move on from; NEXT keeps its
// meaning as a station skip.
func TestNextWhileTuningDiscoverySkipsTheStation(t *testing.T) {
	s, pl := testState(t)
	s = drive(t, s, Discover{Query: "swedish death metal"})
	if s.Phase != Tuning {
		t.Fatalf("want Tuning, got %v", s.Phase)
	}
	seq := s.PickSeq
	s = drive(t, s, Next{})
	if pl.nextSongCount() != 0 {
		t.Fatal("no song is playing yet, so there is none to skip")
	}
	if s.PickSeq == seq {
		t.Fatal("NEXT while tuning should still change station")
	}
}

func TestDoubleNextChangesStation(t *testing.T) {
	s, pl := testState(t)
	s = playingDiscovery(t, s, "swedish death metal")
	seq := s.PickSeq

	s = drive(t, s, Next{})
	if pl.nextSongCount() != 1 || s.PickSeq != seq {
		t.Fatalf("first NEXT is next-song: calls=%d seq %d->%d", pl.nextSongCount(), seq, s.PickSeq)
	}
	s = drive(t, s, Tick{Now: s.Now.Add(2 * time.Second)})
	s = drive(t, s, Next{})
	if pl.nextSongCount() != 1 {
		t.Fatalf("a second NEXT inside 3 s must not ask for another song, got %d", pl.nextSongCount())
	}
	if s.PickSeq == seq {
		t.Fatal("a second NEXT inside 3 s should change station (a new PickSeq)")
	}
}

func TestNextAfterTheWindowIsNextSongAgain(t *testing.T) {
	s, pl := testState(t)
	s = playingDiscovery(t, s, "swedish death metal")
	seq := s.PickSeq

	s = drive(t, s, Next{})
	s = drive(t, s, Tick{Now: s.Now.Add(3 * time.Second)})
	s = drive(t, s, Next{})
	if pl.nextSongCount() != 2 || s.PickSeq != seq {
		t.Fatalf("after 3 s NEXT is next-song again: calls=%d seq %d->%d", pl.nextSongCount(), seq, s.PickSeq)
	}
}

// The window is a station's own: a fresh pick starts with no NEXT behind it.
func TestFirstNextOnANewStationIsNextSong(t *testing.T) {
	s, pl := testState(t)
	s = playingDiscovery(t, s, "swedish death metal")
	s = drive(t, s, Next{})
	s = drive(t, s, Discover{Query: "ambient drone"})
	s = drive(t, s, PlayerEvent(player.Event{Type: player.EventPlaying}))
	seq := s.PickSeq
	s = drive(t, s, Next{})
	if pl.nextSongCount() != 2 || s.PickSeq != seq {
		t.Fatalf("the first NEXT on a new station is next-song: calls=%d", pl.nextSongCount())
	}
}

func TestNextSongFailureIsANote(t *testing.T) {
	s, pl := testState(t)
	pl.failNextSong(errors.New("no queue"))
	s = playingDiscovery(t, s, "swedish death metal")
	s = drive(t, s, Next{})
	if !strings.Contains(s.Note, "no queue") {
		t.Fatalf("a refused NEXT SONG should say so, note=%q", s.Note)
	}
	if s.Phase != Playing {
		t.Fatalf("a refused NEXT SONG leaves the station playing, got %v", s.Phase)
	}
}

func TestNextOnRadioUnchanged(t *testing.T) {
	s, pl := testState(t)
	s = playing(t, s)
	seq := s.PickSeq
	s = drive(t, s, Next{})
	if pl.nextSongCount() != 0 {
		t.Fatal("a radio station has no queue to advance")
	}
	if s.PickSeq == seq || s.Phase != Tuning {
		t.Fatalf("NEXT on radio still changes station: seq %d->%d phase=%v", seq, s.PickSeq, s.Phase)
	}
	if !strings.HasPrefix(s.Feedback, "LEARNED") {
		t.Fatalf("radio feedback is the skip's, got %q", s.Feedback)
	}
	// Twice in a row is still just two station skips.
	seq = s.PickSeq
	s = drive(t, s, PlayerEvent(player.Event{Type: player.EventPlaying}))
	s = drive(t, s, Next{})
	if pl.nextSongCount() != 0 || s.PickSeq == seq {
		t.Fatal("repeat NEXT on radio must never become next-song")
	}
}

func TestLoveOnDiscoveryTellsQueue(t *testing.T) {
	s, pl := testState(t)
	s = playingDiscovery(t, s, "swedish death metal")
	s = drive(t, s, evTitle("Artist - Title", true))

	s = drive(t, s, Love{})
	if !s.LovedTrack {
		t.Fatal("the track should be loved")
	}
	if n := pl.songLovedCount(); n != 1 {
		t.Fatalf("SongLoved should be called once, got %d", n)
	}
	// Taking the love back is not a love: the source is not told.
	s = drive(t, s, Love{})
	if s.LovedTrack || pl.songLovedCount() != 1 {
		t.Fatalf("unloving must not tell the queue: loved=%v calls=%d", s.LovedTrack, pl.songLovedCount())
	}
}

func TestLoveOnRadioLeavesQueueAlone(t *testing.T) {
	s, pl := testState(t)
	s = playing(t, s)
	s = drive(t, s, evTitle("Artist - Title", false))
	s = drive(t, s, Love{})
	if !s.LovedTrack || pl.songLovedCount() != 0 {
		t.Fatalf("radio has no queue: loved=%v calls=%d", s.LovedTrack, pl.songLovedCount())
	}
}

func TestTitleEventSetsNewToYou(t *testing.T) {
	s, _ := testState(t)
	s = playingDiscovery(t, s, "swedish death metal")
	if s.NewToYou || s.Snapshot().NewToYou {
		t.Fatal("nothing is new before a song plays")
	}
	s = drive(t, s, evTitle("Fresh Band - First Song", true))
	if !s.NewToYou || !s.Snapshot().NewToYou {
		t.Fatal("a title marked new-to-you should say so")
	}
	s = drive(t, s, evTitle("Old Band - Second Song", false))
	if s.NewToYou || s.Snapshot().NewToYou {
		t.Fatal("the next song is not new, and must not inherit the flag")
	}
	s = drive(t, s, evTitle("Fresh Band - Third Song", true))
	s = drive(t, s, Discover{Query: "ambient drone"})
	if s.NewToYou {
		t.Fatal("a new station starts with no song, so nothing is new")
	}
}

func TestFirstTitleRenamesLinkStation(t *testing.T) {
	const link = "https://youtu.be/dQw4w9WgXcQ"
	s, _ := testState(t)
	s = playingDiscovery(t, s, link)
	uuid := s.Station.UUID
	if s.Station.Name != link {
		t.Fatalf("a link station starts out named by its link, got %q", s.Station.Name)
	}

	s = drive(t, s, evTitle("Rick Astley - Never Gonna Give You Up", true))
	const want = "Rick Astley - Never Gonna Give You Up"
	if s.Station.Name != want || s.Snapshot().Station.Name != want {
		t.Fatalf("the station should take the song's name, got %q", s.Station.Name)
	}
	if st, _ := s.core.StationByUUID(uuid); st.Name != want {
		t.Fatalf("the rename should reach the store's cache, got %q", st.Name)
	}
	if s.Reason != "discovering · "+want {
		t.Fatalf("the reason should follow the name, got %q", s.Reason)
	}

	// Only the first title names it.
	s = drive(t, s, evTitle("Someone Else - Another Song", false))
	if s.Station.Name != want {
		t.Fatalf("a later song must not rename the station, got %q", s.Station.Name)
	}
}

func TestFirstTitleLeavesPhraseStationNamed(t *testing.T) {
	s, _ := testState(t)
	s = playingDiscovery(t, s, "swedish death metal")
	s = drive(t, s, evTitle("Entombed - Left Hand Path", true))
	if s.Station.Name != "swedish death metal" {
		t.Fatalf("a phrase is already a name, got %q", s.Station.Name)
	}
	if s.Reason != "discovering · swedish death metal" {
		t.Fatalf("reason = %q", s.Reason)
	}
}

func evNetworkStreamError() Msg {
	return PlayerEvent(player.Event{
		Type: player.EventStreamError,
		Err:  fmt.Errorf("next song: %w", player.ErrSourceNetwork),
	})
}

// A queue that lost its network reports it as a stream error; the station
// did nothing wrong, in whatever phase the report lands.
func TestNetworkStreamErrorWhileTuningNoStrike(t *testing.T) {
	t.Run("tuning", func(t *testing.T) {
		s, _ := testState(t)
		s = drive(t, s, Discover{Query: "swedish death metal"})
		uuid := s.Station.UUID
		if s.Phase != Tuning {
			t.Fatalf("want Tuning, got %v", s.Phase)
		}
		s2, cmds := s.Update(evNetworkStreamError())
		if st, _ := s2.core.StationByUUID(uuid); st.FailCount != 0 {
			t.Fatalf("a network failure must not strike the station: FailCount=%d", st.FailCount)
		}
		if len(s2.failStreak) != 0 || s2.netFails != 1 {
			t.Fatalf("it counts toward offline without a strike: failStreak=%v netFails=%d", s2.failStreak, s2.netFails)
		}
		if len(cmds) < 2 || s2.Phase != Tuning {
			t.Fatalf("it should re-arm the listener and move on: cmds=%d phase=%v", len(cmds), s2.Phase)
		}
	})

	t.Run("a streak goes offline unstruck", func(t *testing.T) {
		s, _ := testState(t)
		s = playing(t, s)
		var seen []string
		for i := 0; i < offlineAfter; i++ {
			if s.Phase == Playing {
				s = drive(t, s, evStreamEnded()) // the stream dies; a reconnect is under way
			}
			seen = append(seen, s.Station.UUID)
			s = drive(t, s, evNetworkStreamError())
		}
		if s.Phase != Offline {
			t.Fatalf("three network failures in a row mean offline, got %v", s.Phase)
		}
		for _, uuid := range seen {
			if st, _ := s.core.StationByUUID(uuid); st.FailCount != 0 {
				t.Errorf("%s was struck for the network: FailCount=%d", uuid, st.FailCount)
			}
		}
	})

	t.Run("playing, once the reconnects run out", func(t *testing.T) {
		s, _ := testState(t)
		s = playing(t, s)
		uuid := s.Station.UUID
		s.drops = maxDrops // the next drop is one too many
		s2, cmds := s.Update(evNetworkStreamError())
		if st, _ := s2.core.StationByUUID(uuid); st.FailCount != 0 {
			t.Fatalf("giving up on the network is not a strike: FailCount=%d", st.FailCount)
		}
		if s2.netFails != 1 || len(cmds) < 2 {
			t.Fatalf("it should fail the tune without a strike: netFails=%d cmds=%d", s2.netFails, len(cmds))
		}
	})

	t.Run("an ordinary stream error still strikes", func(t *testing.T) {
		s, _ := testState(t)
		s = drive(t, s, Tuned{Pick: pickSeed(core.SeedStations()[0])})
		uuid := s.Station.UUID
		s2, _ := s.Update(evStreamError())
		if st, _ := s2.core.StationByUUID(uuid); st.FailCount != 1 {
			t.Fatalf("a dead stream is the station's strike: FailCount=%d", st.FailCount)
		}
	})
}

func TestPlayFailedNetworkTakesOfflinePath(t *testing.T) {
	netErr := fmt.Errorf("lookup: %w", player.ErrSourceNetwork)

	t.Run("no strike, but a retune", func(t *testing.T) {
		s, _ := testState(t)
		s = drive(t, s, Discover{Query: "swedish death metal"})
		uuid := s.Station.UUID
		s2, cmds := s.Update(PlayFailed{UUID: uuid, Err: netErr})
		if st, _ := s2.core.StationByUUID(uuid); st.FailCount != 0 {
			t.Fatalf("the network is not the station's fault: FailCount=%d", st.FailCount)
		}
		if len(cmds) == 0 || s2.Phase != Tuning {
			t.Fatalf("it should move on like a stream that never locked: cmds=%d phase=%v", len(cmds), s2.Phase)
		}
		if !strings.Contains(s2.Note, "moving on") {
			t.Fatalf("note = %q", s2.Note)
		}
	})

	t.Run("a streak goes offline and backs off", func(t *testing.T) {
		s, pl := testState(t)
		pl.failPlay(netErr)
		s = drive(t, s, Tuned{Pick: pickSeed(core.SeedStations()[0])})
		if s.Phase != Offline {
			t.Fatalf("three network failures in a row mean offline, got %v", s.Phase)
		}
		if d := s.RetryAt.Sub(s.Now); d < 4*time.Second || d > 6*time.Second {
			t.Fatalf("retryAt should be ~now+5s, got %v", d)
		}
		for _, st := range core.SeedStations() {
			if got, _ := s.core.StationByUUID(st.UUID); got.FailCount != 0 {
				t.Errorf("%s was struck for the network: FailCount=%d", st.UUID, got.FailCount)
			}
		}
	})

	t.Run("a failure inside a retry extends the backoff", func(t *testing.T) {
		s, pl := testState(t)
		s = playing(t, s)
		s, _ = offlineStreak(t, s)
		pl.failPlay(netErr)
		s = drive(t, s, Tick{Now: s.RetryAt.Add(time.Second)})
		if s.Phase != Offline {
			t.Fatalf("the retry hit the network again: want Offline, got %v", s.Phase)
		}
		if d := s.RetryAt.Sub(s.Now); d < 9*time.Second || d > 11*time.Second {
			t.Fatalf("the second outage failure backs off ~10s, got %v", d)
		}
	})

	t.Run("a rejected URL still strikes", func(t *testing.T) {
		s, _ := testState(t)
		s = drive(t, s, Tuned{Pick: pickSeed(core.SeedStations()[0])})
		uuid := s.Station.UUID
		s2, _ := s.Update(PlayFailed{UUID: uuid, Err: errors.New("bad URL")})
		if st, _ := s2.core.StationByUUID(uuid); st.FailCount != 1 {
			t.Fatalf("an ordinary rejection is the station's strike, FailCount=%d", st.FailCount)
		}
	})
}

// NEXT while paused on a discovery station is still next-song, not a station
// skip: it resumes, and what plays is the next song.
func TestNextWhilePausedOnDiscoveryResumesWithTheNextSong(t *testing.T) {
	s, pl := testState(t)
	s = playingDiscovery(t, s, "swedish death metal")
	s = paused(t, s)
	uuid, seq, plays := s.Station.UUID, s.PickSeq, pl.playCount()
	s.Now = s.Now.Add(10 * time.Second) // a short pause: the song is still loaded

	s = drive(t, s, Next{})

	if n := pl.nextSongCount(); n != 1 {
		t.Fatalf("NextSong should be called once, got %d (calls %v)", n, pl.callsList())
	}
	if s.Station.UUID != uuid || s.PickSeq != seq {
		t.Fatalf("NEXT while paused changed the station: seq %d->%d", seq, s.PickSeq)
	}
	if s.Phase != Tuning || !s.Resuming {
		t.Fatalf("want a resuming tune, got phase=%v resuming=%v", s.Phase, s.Resuming)
	}
	if s.Feedback != nextSongFeedback {
		t.Fatalf("feedback = %q, want %q", s.Feedback, nextSongFeedback)
	}
	if pl.playCount() != plays {
		t.Fatalf("a short pause resumes in place and must not reload: calls %v", pl.callsList())
	}
	// The old song stays held until its successor is on its way: asking for
	// the next song comes before the unpause.
	calls := pl.callsList()
	if len(calls) < 2 || calls[len(calls)-2] != "next-song" || calls[len(calls)-1] != "pause false" {
		t.Fatalf("want next-song then pause false, calls %v", calls)
	}
	if !s.core.ListenOpen() {
		t.Fatal("the listen must reopen as a continuation, so the station is not charged a skip")
	}

	s = drive(t, s, PlayerEvent(player.Event{Type: player.EventPlaying}))
	if s.Phase != Playing || s.Resuming {
		t.Fatalf("stream lock should land Playing, got %v resuming=%v", s.Phase, s.Resuming)
	}
}

// After a long pause the song is stale and resume reloads the station; a
// reload of a discovery station opens at the next song (the queue never
// repeats), so NEXT has nothing more to ask for.
func TestNextWhilePausedLongOnDiscoveryReloadsAtTheNextSong(t *testing.T) {
	s, pl := testState(t)
	s = playingDiscovery(t, s, "swedish death metal")
	s = paused(t, s)
	url, uuid, seq := s.Station.StreamURL(), s.Station.UUID, s.PickSeq
	s.Now = s.Now.Add(resumeInPlace + time.Second)

	s = drive(t, s, Next{})

	if lastCall(pl) != "play "+url {
		t.Fatalf("a long pause reloads the station, calls %v", pl.callsList())
	}
	if s.Station.UUID != uuid || s.PickSeq != seq {
		t.Fatalf("NEXT while paused changed the station: seq %d->%d", seq, s.PickSeq)
	}
	if s.Phase != Tuning || s.Feedback != nextSongFeedback {
		t.Fatalf("phase=%v feedback=%q", s.Phase, s.Feedback)
	}
	if !s.core.ListenOpen() {
		t.Fatal("the listen must reopen")
	}
}

// A refused NEXT SONG while paused still resumes: the listener pressed a key
// to hear something, and the refusal is a note.
func TestNextWhilePausedSongFailureStillResumes(t *testing.T) {
	s, pl := testState(t)
	pl.failNextSong(errors.New("no queue"))
	s = playingDiscovery(t, s, "swedish death metal")
	s = paused(t, s)
	s.Now = s.Now.Add(10 * time.Second)
	s = drive(t, s, Next{})
	if lastCall(pl) != "pause false" {
		t.Fatalf("calls %v", pl.callsList())
	}
	if !strings.Contains(s.Note, "no queue") {
		t.Fatalf("a refused NEXT SONG should say so, note=%q", s.Note)
	}
}

// On radio, and for a pause that is not on a discovery station, NEXT keeps
// its meaning: a station skip.
func TestNextWhilePausedOnRadioStillSkipsTheStation(t *testing.T) {
	s, pl := testState(t)
	s = playing(t, s)
	s = paused(t, s)
	seq := s.PickSeq
	s = drive(t, s, Next{})
	if pl.nextSongCount() != 0 {
		t.Fatal("a radio station has no queue to advance")
	}
	if s.PickSeq == seq {
		t.Fatal("NEXT on paused radio should still change station")
	}
}

// --- seeds that cannot start (F7) -----------------------------------------------------

// stationRows is how many stations the library holds: a refused seed must not
// add one, or it would sit in "pick up where you left off" with a strike.
func stationRows(s State) int { return s.core.StationCount() }

func TestDiscoverRefusesLinksThatAreNotAVideo(t *testing.T) {
	for _, seed := range []string{
		"https://www.youtube.com/playlist?list=PL1234567890",
		"https://www.youtube.com/@somechannel",
		"https://www.youtube.com/channel/UCabcdefghijklmnopqrstuv",
		"https://soundcloud.com/some/song",
		"https://example.com/stream.mp3",
		"ftp://example.com/x",
	} {
		t.Run(seed, func(t *testing.T) {
			s, pl := testState(t)
			rows := stationRows(s)

			s = drive(t, s, Discover{Query: seed})

			want := "can't start from that link · paste a YouTube video, or type a genre or artist"
			if s.Note != want {
				t.Errorf("note = %q, want %q", s.Note, want)
			}
			if s.Phase != Idle || s.HaveStation || pl.playCount() != 0 {
				t.Errorf("nothing may start: phase=%v station=%v plays=%d", s.Phase, s.HaveStation, pl.playCount())
			}
			if n := stationRows(s); n != rows {
				t.Errorf("a refused seed left a station row behind (%d -> %d)", rows, n)
			}
			if _, ok := s.core.StationByUUID(core.DiscoveryUUID(seed)); ok {
				t.Error("the refused seed has a station")
			}
		})
	}
}

func TestDiscoverYouTubeLinkNeedsYtdlp(t *testing.T) {
	const link = "https://www.youtube.com/watch?v=abcdefghijk&list=PL1"
	c, err := core.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	pl := newStubPlayer()
	s := New(c, pl, Options{NoYtdlp: true})
	rows := stationRows(s)

	s = drive(t, s, Discover{Query: link})

	want := "YouTube links need yt-dlp · see screech doctor"
	if s.Note != want {
		t.Errorf("note = %q, want %q", s.Note, want)
	}
	if s.Phase != Idle || s.HaveStation || pl.playCount() != 0 {
		t.Errorf("nothing may start: phase=%v station=%v plays=%d", s.Phase, s.HaveStation, pl.playCount())
	}
	if n := stationRows(s); n != rows {
		t.Errorf("a refused link left a station row behind (%d -> %d)", rows, n)
	}
	// Every spelling of a video link is a YouTube link.
	for _, spelled := range []string{"https://youtu.be/abcdefghijk?t=3", "music.youtube.com/watch?v=abcdefghijk", "https://www.youtube.com/shorts/abcdefghijk"} {
		s.Note = ""
		s = drive(t, s, Discover{Query: spelled})
		if s.Note != want || s.HaveStation {
			t.Errorf("%q: note=%q station=%v", spelled, s.Note, s.HaveStation)
		}
	}

	// A phrase needs no yt-dlp: Bandcamp carries it.
	s.Note = ""
	s = drive(t, s, Discover{Query: "swedish death metal"})
	if s.Note != "" || !s.HaveStation || s.Phase != Tuning {
		t.Errorf("a phrase without yt-dlp should still start: note=%q phase=%v", s.Note, s.Phase)
	}
}

func TestDiscoverYouTubeLinkStartsWithYtdlp(t *testing.T) {
	s, pl := testState(t)
	s = drive(t, s, Discover{Query: "https://youtu.be/abcdefghijk"})
	if s.Note != "" || !s.HaveStation || !core.IsDiscovery(s.Station.UUID) {
		t.Fatalf("a video link with yt-dlp should start: note=%q station=%v", s.Note, s.HaveStation)
	}
	if got := pl.lastPlay(); !strings.HasPrefix(got, core.DiscoverScheme) {
		t.Errorf("the player should be pointed at the sentinel, got %q", got)
	}
}

// The terminal's ASCII mode stays 7-bit: the notes ride the session's dot.
func TestDiscoverRefusalNotesUseTheSessionDot(t *testing.T) {
	c, err := core.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	s := New(c, newStubPlayer(), Options{Dot: "-", NoYtdlp: true})
	for _, seed := range []string{"https://example.com/x", "https://youtu.be/abcdefghijk"} {
		s.Note = ""
		s = drive(t, s, Discover{Query: seed})
		if s.Note == "" || !strings.Contains(s.Note, " - ") {
			t.Errorf("%q: note = %q, want the session's dot in it", seed, s.Note)
		}
		for _, r := range s.Note {
			if r > 0x7f {
				t.Errorf("%q: note %q leaks a non-ASCII rune into ascii mode", seed, s.Note)
			}
		}
	}
}

// A love that lands on the station alone (no song has been named yet) says
// nothing about the finds, so the queue is not told: only a loved song leans
// the mix.
func TestStationOnlyLoveOnDiscoveryLeavesQueueAlone(t *testing.T) {
	s, pl := testState(t)
	s = playingDiscovery(t, s, "swedish death metal")

	s = drive(t, s, Love{})

	if s.Feedback != "LOVED  Station" {
		t.Fatalf("feedback = %q, want a station-only love", s.Feedback)
	}
	if n := pl.songLovedCount(); n != 0 {
		t.Fatalf("a station-only love reached the queue %d times", n)
	}
}
