package main

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/tinkthemaker/screech/internal/config"
	"github.com/tinkthemaker/screech/internal/discover"
)

// TestMain doubles the test binary as a fake yt-dlp, as in the other
// packages: a configured ytdlp_path of os.Args[0] with SCREECH_FAKE_YTDLP set
// answers `--version` without yt-dlp installed.
func TestMain(m *testing.M) {
	if v := os.Getenv("SCREECH_FAKE_YTDLP"); v != "" {
		if v == "fail" {
			fmt.Fprintln(os.Stderr, "ERROR: not really yt-dlp")
			os.Exit(1)
		}
		fmt.Println(v)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func exe(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

// onlyPath leaves PATH holding one folder, so what the machine has installed
// can't answer for the test.
func onlyPath(t *testing.T, dir string) {
	t.Helper()
	t.Setenv("PATH", dir)
}

func touch(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
}

var today = time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)

// Doctor says which yt-dlp it found, its version, and the JS runtime yt-dlp
// will have; and counts discovery stations on a line of their own, apart from
// the custom ones.
func TestDoctorReportsYtdlpAndRuntime(t *testing.T) {
	bin := t.TempDir()
	touch(t, bin, exe("node"))
	onlyPath(t, bin)
	t.Setenv("SCREECH_FAKE_YTDLP", "2026.10.26")

	var out bytes.Buffer
	code := reportYtdlp(&out, os.Args[0], "", "", today)
	cfg := config.Config{Stations: []config.Station{
		{Name: "A", URL: "https://a.example/"},
		{Name: "D1", Discover: "dungeon synth"},
		{Name: "D2", Discover: "swedish death metal"},
	}}
	reportConfig(&out, "config.toml", cfg, nil)
	if code != 0 {
		t.Fatalf("exit bits %d, want 0\n%s", code, out.String())
	}
	for _, want := range []string{
		"[ ok ] yt-dlp   " + os.Args[0],
		"-> 2026.10.26\n",
		"-> js runtime: node\n",
		"-> 1 custom station\n",
		"-> discovery stations: 2 ",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("doctor output lacks %q:\n%s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "yt-dlp -U") {
		t.Errorf("a version three days old needs no update hint:\n%s", out.String())
	}

	// No runtime at all is said plainly, with what to do about it.
	onlyPath(t, t.TempDir())
	out.Reset()
	if code := reportYtdlp(&out, os.Args[0], "", "", today); code != 0 {
		t.Fatalf("exit bits %d, want 0\n%s", code, out.String())
	}
	if !strings.Contains(out.String(), "-> js runtime: none") || !strings.Contains(out.String(), "deno") {
		t.Errorf("no runtime:\n%s", out.String())
	}
}

// An old yt-dlp is the usual reason YouTube stops working, so doctor points
// at `yt-dlp -U` once the release is more than 60 days old.
func TestDoctorSuggestsUpdatingOldYtdlp(t *testing.T) {
	onlyPath(t, t.TempDir())
	for _, tc := range []struct {
		version string
		now     time.Time
		hint    bool
	}{
		{"2026.10.26", today, false},
		{"2026.10.26", time.Date(2026, 12, 25, 0, 0, 0, 0, time.UTC), false}, // 60 days
		{"2026.10.26", time.Date(2026, 12, 26, 0, 0, 0, 0, time.UTC), true},  // 61
		{"2026.10.26.232927", time.Date(2027, 3, 1, 0, 0, 0, 0, time.UTC), true},
		{"stable-build", time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC), false}, // not a date, no guess
	} {
		t.Setenv("SCREECH_FAKE_YTDLP", tc.version)
		var out bytes.Buffer
		if code := reportYtdlp(&out, os.Args[0], "", "", tc.now); code != 0 {
			t.Fatalf("%s: exit bits %d", tc.version, code)
		}
		if got := strings.Contains(out.String(), "yt-dlp -U"); got != tc.hint {
			t.Errorf("version %s on %s: hint = %v, want %v\n%s", tc.version, tc.now.Format("2006-01-02"), got, tc.hint, out.String())
		}
	}
}

func TestDoctorYtdlp(t *testing.T) {
	exeDir, dataDir := t.TempDir(), t.TempDir()
	onlyPath(t, t.TempDir())

	// Off is the listener's choice, not a failure.
	var out bytes.Buffer
	if code := reportYtdlp(&out, "off", exeDir, dataDir, today); code != 0 || !strings.Contains(out.String(), "[ -- ] yt-dlp   off") {
		t.Fatalf("off: exit %d\n%s", code, out.String())
	}

	// Finding none is the ordinary state of a fresh install: said, not failed.
	out.Reset()
	if code := reportYtdlp(&out, "", exeDir, dataDir, today); code != 0 ||
		!strings.Contains(out.String(), "[ -- ] yt-dlp   not found") || !strings.Contains(out.String(), "Bandcamp") {
		t.Fatalf("not found: exit %d\n%s", code, out.String())
	}

	// A path the listener wrote that doesn't run is a failure.
	out.Reset()
	if code := reportYtdlp(&out, "no-such-yt-dlp-anywhere", exeDir, dataDir, today); code != ecYtdlp || !strings.Contains(out.String(), "[FAIL] yt-dlp") {
		t.Fatalf("missing: exit %d\n%s", code, out.String())
	}
	t.Setenv("SCREECH_FAKE_YTDLP", "fail")
	out.Reset()
	if code := reportYtdlp(&out, os.Args[0], exeDir, dataDir, today); code != ecYtdlp || !strings.Contains(out.String(), "[FAIL] yt-dlp") {
		t.Fatalf("broken: exit %d\n%s", code, out.String())
	}

	// Found by looking in the data folder.
	touch(t, dataDir, exe("yt-dlp"))
	t.Setenv("SCREECH_FAKE_YTDLP", "2026.10.26")
	out.Reset()
	reportYtdlp(&out, "", exeDir, dataDir, today)
	if !strings.Contains(out.String(), filepath.Join(dataDir, exe("yt-dlp"))) {
		t.Errorf("the yt-dlp in the data folder was not the one reported:\n%s", out.String())
	}
}

func TestDoctorCountsCustomAndDiscoveryApart(t *testing.T) {
	cfg := config.Config{Stations: []config.Station{
		{Name: "A", URL: "https://a.example/"}, {Name: "B", URL: "https://b.example/"},
		{Name: "D", Discover: "dungeon synth"},
	}, Warnings: []string{`[[stations]] #4: needs a url or discover; skipped`}}
	var out bytes.Buffer
	if code := reportConfig(&out, "config.toml", cfg, nil); code != 0 {
		t.Fatalf("exit bits %d, want 0", code)
	}
	for _, want := range []string{"-> 2 custom stations\n", "-> discovery stations: 1 ", "needs a url or discover; skipped"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("doctor config section lacks %q:\n%s", want, out.String())
		}
	}
	out.Reset()
	reportConfig(&out, "config.toml", config.Config{Stations: []config.Station{{Name: "D", Discover: "x"}}}, nil)
	if !strings.Contains(out.String(), "no custom stations") || !strings.Contains(out.String(), "discovery stations: 1") {
		t.Errorf("only a discovery:\n%s", out.String())
	}
}

const bandcampAnswer = `{"results":[{"band_name":"B","featured_track":{"id":1,"title":"T","band_name":"B","stream_url":"https://t4.bcbits.com/stream/1","duration":100}}]}`

// Doctor asks Bandcamp's feed once, for a tag it should always have. The
// endpoint is the client's own, so the test points it at a local server and
// never at the network.
func TestDoctorBandcamp(t *testing.T) {
	var asked []string
	status, body := http.StatusOK, bandcampAnswer
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b := new(bytes.Buffer)
		_, _ = b.ReadFrom(r.Body)
		asked = append(asked, b.String())
		w.WriteHeader(status)
		fmt.Fprint(w, body)
	}))
	defer srv.Close()
	client := discover.BandcampClient{HTTP: srv.Client(), UserAgent: "screech-test", Endpoint: srv.URL}

	var out bytes.Buffer
	if code := reportBandcamp(&out, client); code != 0 || !strings.Contains(out.String(), "[ ok ] bandcamp") {
		t.Fatalf("healthy: exit %d\n%s", code, out.String())
	}
	if len(asked) != 1 || !strings.Contains(asked[0], `"ambient"`) || !strings.Contains(asked[0], `"top"`) {
		t.Errorf("asked %v, want one request for ambient/top", asked)
	}

	status = http.StatusServiceUnavailable
	out.Reset()
	if code := reportBandcamp(&out, client); code != ecBandcamp || !strings.Contains(out.String(), "[FAIL] bandcamp") || !strings.Contains(out.String(), "503") {
		t.Fatalf("503: exit %d\n%s", code, out.String())
	}

	// Answering with nothing to play means the feed's shape changed.
	status, body = http.StatusOK, `{"results":[]}`
	out.Reset()
	if code := reportBandcamp(&out, client); code != ecBandcamp || !strings.Contains(out.String(), "[FAIL] bandcamp") {
		t.Fatalf("empty: exit %d\n%s", code, out.String())
	}

	srv.Close()
	out.Reset()
	if code := reportBandcamp(&out, client); code != ecBandcamp {
		t.Fatalf("down: exit %d\n%s", code, out.String())
	}
}
