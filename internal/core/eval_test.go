//go:build eval

package core

// The simulated-listener harness: does the picker actually learn?
//
// A synthetic catalog (genres, artists, stations that play them) and a
// listener with hidden taste drive the real Core through the calls the TUI
// makes — Tune, StartListen, NoteTitle, Love, EndListen — on a simulated
// clock. The listener stays on stations it likes and skips the rest, so the
// model only ever sees what it sees in production: dwell time and loves.
// The measure is E[like] of the picks over time, against what a uniformly
// random pick would get.
//
//	go test -tags eval -run TestEval -v -timeout 30m ./internal/core
//
// It asserts only that learning happens at all; the table is the point.

import (
	"fmt"
	"math"
	"math/rand"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

const (
	evalGenres          = 24
	evalArtistsPerGenre = 30
	evalStations        = 2000
	evalPicks           = 400
	evalSeeds           = 12
	evalWindow          = 50
)

type evalStation struct {
	primary int // the genre the music actually is
	artists []string
	quality float64 // hidden per-station appeal in [0,1)
}

type evalWorld struct {
	st       map[string]*evalStation
	catalog  []Station
	liked    map[int]bool    // genres the listener likes
	gems     map[string]bool // stations the listener likes regardless of genre
	custom   map[string]bool // listener-added stations -> whether they like it
	songSeq  int
	genreTag []string
}

// newEvalWorld builds the catalog. Genre popularity is Zipf-like, a fifth of
// stations carry no tags at all, tagged ones usually list their real genre
// plus some noise, and a few are mislabelled outright — roughly the shape
// of the real directory. custom adds that many listener-added stations
// after everything else is drawn, so the other scenarios' worlds are
// unchanged by it.
func newEvalWorld(seed int64, custom int) *evalWorld {
	rng := rand.New(rand.NewSource(seed))
	w := &evalWorld{st: map[string]*evalStation{}, liked: map[int]bool{}, gems: map[string]bool{}, custom: map[string]bool{}}
	for g := 0; g < evalGenres; g++ {
		w.genreTag = append(w.genreTag, fmt.Sprintf("genre%02d", g))
	}
	weights := make([]float64, evalGenres)
	total := 0.0
	for g := range weights {
		weights[g] = 1 / (float64(g) + 1.5)
		total += weights[g]
	}
	drawGenre := func() int {
		x := rng.Float64() * total
		for g, wt := range weights {
			if x < wt {
				return g
			}
			x -= wt
		}
		return evalGenres - 1
	}
	artist := func(g, i int) string { return fmt.Sprintf("Artist %s %02d", strings.ToUpper(w.genreTag[g]), i) }

	for i := 0; i < evalStations; i++ {
		uuid := fmt.Sprintf("eval-%04d", i)
		primary := drawGenre()
		var tags []string
		if rng.Float64() >= 0.2 {
			if rng.Float64() < 0.85 {
				tags = append(tags, w.genreTag[primary])
			}
			for n := 0; n < 2; n++ {
				if rng.Float64() < 0.35 {
					if g := rng.Intn(evalGenres); g != primary {
						tags = append(tags, w.genreTag[g])
					}
				}
			}
			if len(tags) == 0 {
				tags = append(tags, w.genreTag[rng.Intn(evalGenres)])
			}
		}
		es := &evalStation{primary: primary, quality: rng.Float64()}
		for _, j := range rng.Perm(evalArtistsPerGenre)[:12] {
			es.artists = append(es.artists, artist(primary, j))
		}
		for n := 0; n < 3; n++ {
			es.artists = append(es.artists, artist(rng.Intn(evalGenres), rng.Intn(evalArtistsPerGenre)))
		}
		w.st[uuid] = es
		w.catalog = append(w.catalog, Station{
			UUID: uuid, Name: fmt.Sprintf("Eval Station %d", i),
			URL: "http://eval.test/" + uuid, URLResolved: "http://eval.test/" + uuid,
			Tags: strings.Join(tags, ","), Codec: "MP3", Bitrate: 128,
			Votes: rng.Intn(500), LastCheckOK: true,
		})
	}
	// Liked genres come from the middle of the popularity curve: liking
	// only the most common genre would let a popularity-biased picker look
	// smart without learning anything.
	for _, g := range rng.Perm(12)[:3] {
		w.liked[g+4] = true
	}
	for _, i := range rng.Perm(evalStations)[:evalStations/20] {
		w.gems[fmt.Sprintf("eval-%04d", i)] = true
	}
	// Links the listener pasted in: untagged, so tag affinity and the
	// directory's metadata know nothing about them. All but the last are
	// stations the listener likes; one is a link that turned out dull.
	for i := 0; i < custom; i++ {
		uuid := fmt.Sprintf("custom:eval-%d", i)
		primary := rng.Intn(evalGenres)
		es := &evalStation{primary: primary, quality: rng.Float64()}
		for _, j := range rng.Perm(evalArtistsPerGenre)[:12] {
			es.artists = append(es.artists, artist(primary, j))
		}
		w.st[uuid] = es
		w.custom[uuid] = i < custom-1
		w.catalog = append(w.catalog, Station{
			UUID: uuid, Name: fmt.Sprintf("My Link %d", i),
			URL: "http://eval.test/" + uuid, URLResolved: "http://eval.test/" + uuid,
			Codec: "MP3", Bitrate: 128, LastCheckOK: true,
		})
	}
	return w
}

// title is the next ICY title for a station: a real artist from its
// rotation and a never-repeating song, so a love can never toggle an
// earlier one off.
func (w *evalWorld) title(uuid string, rng *rand.Rand) string {
	w.songSeq++
	es := w.st[uuid]
	if es == nil {
		return fmt.Sprintf("Seed Artist - Seed Song %d", w.songSeq)
	}
	return fmt.Sprintf("%s - Song %d", es.artists[rng.Intn(len(es.artists))], w.songSeq)
}

type evalScenario struct {
	name string
	// custom is how many listener-added stations the world carries.
	custom int
	// like is the probability the listener stays on a station. It judges
	// the music, never the tags.
	like func(w *evalWorld, uuid string) float64
}

func likeGenre(w *evalWorld, uuid string) float64 {
	if es := w.st[uuid]; es != nil && w.liked[es.primary] {
		return 0.8
	}
	return 0.04
}

var evalScenarios = []evalScenario{
	{"genre", 0, likeGenre},
	{"station", 0, func(w *evalWorld, uuid string) float64 {
		if w.gems[uuid] {
			return 0.85
		}
		return 0.04
	}},
	{"genre+quality", 0, func(w *evalWorld, uuid string) float64 {
		es := w.st[uuid]
		switch {
		case es == nil || !w.liked[es.primary]:
			return 0.03
		case es.quality >= 0.5:
			return 0.85
		default:
			return 0.2
		}
	}},
	// Genre taste plus four pasted links, three of them liked: whether a
	// link the listener added ever comes round, and whether a dull one
	// fades instead of being forced on them.
	{"genre+links", 4, func(w *evalWorld, uuid string) float64 {
		if liked, ok := w.custom[uuid]; ok {
			if liked {
				return 0.85
			}
			return 0.04
		}
		return likeGenre(w, uuid)
	}},
}

func (sc evalScenario) baseRate(w *evalWorld) float64 {
	sum := 0.0
	for _, st := range w.catalog {
		sum += sc.like(w, st.UUID)
	}
	return sum / float64(len(w.catalog))
}

type evalPick struct {
	like   float64
	stayed bool
	repeat bool // the listener had already stayed on this station before
	reason string
	uuid   string
	link   string // "liked" or "dull" for a listener-added station
}

func reasonClass(r string) string {
	switch {
	case strings.HasPrefix(r, "plays "):
		return "artist"
	case strings.HasSuffix(r, " favorite"):
		return "favorite"
	case strings.HasPrefix(r, "tag:") || r == "matches your tags":
		return "tag"
	case r == "wildcard":
		return "wildcard"
	case r == "added by you":
		return "link"
	}
	return "other"
}

// runEval drives one simulated listener for evalPicks tunes.
func runEval(t *testing.T, sc evalScenario, seed int64, loveP float64) ([]evalPick, float64) {
	t.Helper()
	c := openTestCore(t)
	c.rng = rand.New(rand.NewSource(seed))
	w := newEvalWorld(seed, sc.custom)
	start := time.Date(2026, 1, 5, 8, 0, 0, 0, time.Local)
	if err := c.store.UpsertStations(w.catalog, start); err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	err := c.reload()
	c.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}

	rng := rand.New(rand.NewSource(seed*7919 + 1))
	now := start
	stayedOn := map[string]bool{}
	out := make([]evalPick, 0, evalPicks)
	for i := 0; i < evalPicks; i++ {
		p, err := c.Tune(now)
		if err != nil {
			t.Fatalf("pick %d: %v", i, err)
		}
		uuid := p.Station.UUID
		c.StartListen(uuid, now)
		like := sc.like(w, uuid)
		stayed := rng.Float64() < like
		link := ""
		if liked, ok := w.custom[uuid]; ok {
			link = "dull"
			if liked {
				link = "liked"
			}
		}
		out = append(out, evalPick{like: like, stayed: stayed, repeat: stayedOn[uuid], reason: reasonClass(p.Reason), uuid: uuid, link: link})
		if !stayed {
			c.NoteTitle(w.title(uuid, rng), now.Add(5*time.Second))
			now = now.Add(time.Duration(10+rng.Intn(50)) * time.Second)
			continue
		}
		// A stay: 15-90 minutes, a new title every four minutes, and a
		// love on one in four stays.
		dwell := 15*time.Minute + time.Duration(rng.ExpFloat64()*float64(25*time.Minute))
		if dwell > 90*time.Minute {
			dwell = 90 * time.Minute
		}
		end := now.Add(dwell)
		loveAt := -1
		if rng.Float64() < loveP {
			loveAt = rng.Intn(int(dwell/(4*time.Minute)) + 1)
		}
		k := 0
		for at := now.Add(5 * time.Second); at.Before(end); at = at.Add(4 * time.Minute) {
			c.NoteTitle(w.title(uuid, rng), at)
			if k == loveAt {
				if _, _, _, err := c.Love(at.Add(time.Minute)); err != nil {
					t.Fatal(err)
				}
			}
			k++
		}
		now = end
		stayedOn[uuid] = true
		if rng.Float64() < 0.15 {
			// The radio goes off for a few hours: the next tune lands in
			// another daypart, the way a real week of listening does.
			c.EndListen(now)
			now = now.Add(time.Duration(4+rng.Intn(12)) * time.Hour)
		}
	}
	c.EndListen(now)
	return out, sc.baseRate(w)
}

func TestEval(t *testing.T) {
	for _, sc := range evalScenarios {
		for _, loveP := range []float64{0.25, 0} {
			t.Run(fmt.Sprintf("%s/love=%.2f", sc.name, loveP), func(t *testing.T) {
				windows := evalPicks / evalWindow
				like := make([]float64, windows)
				hits := make([]float64, windows)
				repeats := make([]float64, windows)
				distinct := make([]float64, windows)
				reasons := make([]map[string]int, windows)
				for i := range reasons {
					reasons[i] = map[string]int{}
				}
				base := 0.0
				links := map[string]float64{}     // link kind -> picks, whole run
				lateLinks := map[string]float64{} // the same, last window only
				for s := evalSeedBase(); s < evalSeedBase()+evalSeeds; s++ {
					picks, b := runEval(t, sc, s, loveP)
					base += b / evalSeeds
					seen := map[int]map[string]bool{}
					for i, p := range picks {
						if p.link != "" {
							links[p.link]++
							if i/evalWindow == windows-1 {
								lateLinks[p.link]++
							}
						}
						if seen[i/evalWindow] == nil {
							seen[i/evalWindow] = map[string]bool{}
						}
						if !seen[i/evalWindow][p.uuid] {
							seen[i/evalWindow][p.uuid] = true
							distinct[i/evalWindow]++
						}
						wi := i / evalWindow
						like[wi] += p.like
						if p.stayed {
							hits[wi]++
						}
						if p.repeat {
							repeats[wi]++
						}
						reasons[wi][p.reason]++
					}
				}
				n := float64(evalWindow * evalSeeds)
				var b strings.Builder
				fmt.Fprintf(&b, "\nscenario %-14s love %.2f  random pick E[like] = %.3f  (%d seeds x %d picks)\n", sc.name, loveP, base, evalSeeds, evalPicks)
				fmt.Fprintf(&b, "  picks      E[like]  lift   stayed  repeat  distinct  reasons\n")
				for wi := 0; wi < windows; wi++ {
					keys := make([]string, 0, len(reasons[wi]))
					for k := range reasons[wi] {
						keys = append(keys, k)
					}
					sort.Strings(keys)
					var rs []string
					for _, k := range keys {
						rs = append(rs, fmt.Sprintf("%s %d%%", k, int(math.Round(100*float64(reasons[wi][k])/n))))
					}
					fmt.Fprintf(&b, "  %3d-%-3d    %.3f   %4.1fx   %.2f    %.2f    %.2f      %s\n",
						wi*evalWindow+1, (wi+1)*evalWindow, like[wi]/n, like[wi]/n/base, hits[wi]/n, repeats[wi]/n, distinct[wi]/n, strings.Join(rs, ", "))
				}
				if sc.custom > 0 {
					all := float64(evalPicks * evalSeeds)
					fmt.Fprintf(&b, "  links: liked %.1f%% of all picks (%.1f%% late), dull %.1f%% (%.1f%% late)\n",
						100*links["liked"]/all, 100*lateLinks["liked"]/n, 100*links["dull"]/all, 100*lateLinks["dull"]/n)
				}
				t.Log(b.String())
				if late := like[windows-1] / n; late < 2*base {
					t.Errorf("no learning: last-window E[like] %.3f is under twice the random rate %.3f", late, base)
				}
			})
		}
	}
}

// evalSeedBase lets a second run check a result on fresh worlds
// (EVAL_SEED_BASE=101) instead of tuning against the same twelve.
func evalSeedBase() int64 {
	if n, err := strconv.ParseInt(os.Getenv("EVAL_SEED_BASE"), 10, 64); err == nil {
		return n
	}
	return 1
}
