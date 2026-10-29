package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/tinkthemaker/screech/internal/config"
	"github.com/tinkthemaker/screech/internal/core"
	"github.com/tinkthemaker/screech/internal/discover"
	"github.com/tinkthemaker/screech/internal/player"
	versionpkg "github.com/tinkthemaker/screech/internal/version"
	"github.com/tinkthemaker/screech/internal/ytdlp"
)

const (
	ecConfig   = 1 << iota // config exists but won't parse
	ecDatabase             // database exists but won't open or fails checks
	ecMpv                  // mpv not on PATH
	ecIpc                  // mpv found but IPC handshake failed
	ecNetwork              // radio-browser unreachable
	ecYtdlp                // a yt-dlp was found or named but won't run
	ecBandcamp             // Bandcamp's discover feed didn't answer with tracks
)

// runDoctor checks every joint screech depends on and reports what it finds.
// It is read-only: it does not create files, directories, or database
// entries (the IPC probe runs inside a throwaway sandbox). The returned
// exit code is a bit mask of the failures it encountered. Files that simply
// haven't been created yet are reported as "--", not failures — a fresh
// install is not a broken one.
func runDoctor() int {
	ok := func(b bool) string {
		if b {
			return " ok "
		}
		return "FAIL"
	}
	fmt.Println("screech doctor " + version)
	fmt.Println()

	if exe, err := os.Executable(); err == nil {
		fmt.Printf("[ -- ] exe      %s\n", exe)
	}

	cfgFile := "config.toml"
	if base, err := os.UserConfigDir(); err == nil {
		cfgFile = filepath.Join(base, "screech", "config.toml")
	}
	cfg, dbPath, dataDir, cfgErr := config.Read()
	exitCode := reportConfig(os.Stdout, cfgFile, cfg, cfgErr)

	store, dbErr := core.OpenStoreReadOnly(dbPath)
	switch {
	case os.IsNotExist(dbErr):
		fmt.Printf("[ -- ] database %s\n", dbPath)
		fmt.Printf("        -> not created yet; first launch writes it\n")
	case dbErr != nil:
		fmt.Printf("[FAIL] database %s\n", dbPath)
		fmt.Printf("        -> %v\n", dbErr)
		exitCode |= ecDatabase
	default:
		schemaV, sErr := store.SchemaVersion()
		stations, cErr := store.StationCount()
		_ = store.Close()
		if sErr != nil || cErr != nil {
			fmt.Printf("[FAIL] database %s\n", dbPath)
			if sErr != nil {
				fmt.Printf("        -> %v\n", sErr)
			} else {
				fmt.Printf("        -> %v\n", cErr)
			}
			exitCode |= ecDatabase
		} else {
			fmt.Printf("[ ok ] database %s\n", dbPath)
			fmt.Printf("        -> schema v%d, %d stations cached\n", schemaV, stations)
		}
	}

	mpvPath := cfg.MpvPath
	if mpvPath == "" {
		mpvPath = "mpv"
	}
	resolved, lookErr := exec.LookPath(mpvPath)
	if resolved == "" {
		resolved = mpvPath
	} else {
		// Show the binary playback actually launches: on Windows LookPath
		// prefers the mpv.com wrapper over the real mpv.exe.
		resolved = player.ResolveMpv(resolved)
	}
	fmt.Printf("[%s] mpv      %s\n", ok(lookErr == nil), resolved)
	switch {
	case lookErr != nil:
		fmt.Printf("        -> %v\n        -> install mpv (scoop/choco/brew/apt) or set mpv_path in config.toml\n        -> just installed? PATH refreshes only in NEW terminals\n", lookErr)
		exitCode |= ecMpv
	default:
		if out, err := exec.Command(resolved, "--version").Output(); err == nil {
			fmt.Printf("        -> %s\n", strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0]))
		}
		// The joint that matters: spawn mpv and shake hands over IPC
		// (unix socket / Windows named pipe). The probe always runs in a
		// throwaway sandbox — read-only means the user's data directory
		// never gets created, chmodded, or even touched here.
		var ipcErr error
		ipcDataDir, err := os.MkdirTemp("", "screech-doctor-*")
		if err != nil {
			ipcErr = err
		} else {
			defer func() { _ = os.RemoveAll(ipcDataDir) }()
			pl, err := player.NewMPV(resolved, ipcDataDir)
			if err != nil {
				ipcErr = err
			} else {
				_ = pl.Close()
			}
		}
		fmt.Printf("[%s] mpv IPC  spawn + handshake\n", ok(ipcErr == nil))
		if ipcErr != nil {
			fmt.Printf("        -> %v\n", ipcErr)
			exitCode |= ecIpc
		}
	}

	exeDir := ""
	if exe, err := os.Executable(); err == nil {
		exeDir = filepath.Dir(exe)
	}
	exitCode |= reportYtdlp(os.Stdout, cfg.YtdlpPath, exeDir, dataDir, time.Now())
	exitCode |= reportBandcamp(os.Stdout, discover.BandcampClient{
		HTTP:      &http.Client{Timeout: 8 * time.Second},
		UserAgent: versionpkg.UserAgent(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://all.api.radio-browser.info/json/servers", nil)
	req.Header.Set("User-Agent", versionpkg.UserAgent())
	resp, netErr := http.DefaultClient.Do(req)
	if netErr == nil {
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			netErr = fmt.Errorf("status %s", resp.Status)
		}
	}
	fmt.Printf("[%s] network  radio-browser directory\n", ok(netErr == nil))
	if netErr != nil {
		fmt.Printf("        -> %v (seed stations still work offline)\n", netErr)
		exitCode |= ecNetwork
	}

	if exitCode != 0 {
		holdConsoleOnExit()
	}
	return exitCode
}

// staleAfter is how old a yt-dlp release may be before doctor suggests
// updating it. YouTube changes under yt-dlp every few weeks, and an old
// release is the usual reason it stops finding songs.
const staleAfter = 60 * 24 * time.Hour

// reportYtdlp checks the optional yt-dlp, which finds YouTube songs for
// discovery stations and resolves page links. Off, and none found, are not
// failures: discovery still plays from Bandcamp. A yt-dlp that is found or
// named but won't run is. configured is the ytdlp_path setting; it is looked
// for the way startup looks.
func reportYtdlp(w io.Writer, configured, exeDir, dataDir string, now time.Time) int {
	path, found := ytdlp.Find(configured, exeDir, dataDir)
	switch {
	case !found && strings.EqualFold(strings.TrimSpace(configured), "off"):
		fmt.Fprintf(w, "[ -- ] yt-dlp   off (ytdlp_path = \"off\")\n")
		fmt.Fprintf(w, "        -> discovery stations play from Bandcamp alone; page links won't play\n")
		return 0
	case !found:
		fmt.Fprintf(w, "[ -- ] yt-dlp   not found\n")
		fmt.Fprintf(w, "        -> discovery stations play from Bandcamp alone; YouTube songs and page links need yt-dlp\n")
		fmt.Fprintf(w, "        -> put yt-dlp on PATH, beside screech, or in %s, or set ytdlp_path in config.toml\n", core.CleanText(dataDir))
		return 0
	}

	resolved, err := exec.LookPath(path)
	var version string
	if err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		version, err = ytdlp.Version(ctx, resolved)
	}
	if err != nil {
		// Shown cleaned, run as written: a path is the user's to get right.
		fmt.Fprintf(w, "[FAIL] yt-dlp   %s\n", core.CleanText(path))
		fmt.Fprintf(w, "        -> %v\n        -> install yt-dlp, or fix ytdlp_path in config.toml (\"\" finds it, \"off\" turns it off)\n", err)
		return ecYtdlp
	}
	fmt.Fprintf(w, "[ ok ] yt-dlp   %s\n", resolved)
	fmt.Fprintf(w, "        -> %s\n", core.CleanText(version))
	if released, ok := releaseDate(version); ok && now.Sub(released) > staleAfter {
		fmt.Fprintf(w, "        -> released %s, over 60 days ago; run `yt-dlp -U` (YouTube changes often)\n", released.Format("2006-01-02"))
	}
	runtimeName := ytdlp.JSRuntimeName()
	fmt.Fprintf(w, "        -> js runtime: %s\n", runtimeName)
	if runtimeName == "none" {
		fmt.Fprintf(w, "        -> YouTube needs one: install deno, node or bun and put it on PATH\n")
	}
	return 0
}

// releaseDate reads the date out of a yt-dlp version, which is its release
// date: 2026.10.26, or 2026.10.26.232927 for a nightly. Anything else has no
// date to judge by.
func releaseDate(version string) (time.Time, bool) {
	parts := strings.SplitN(strings.TrimSpace(version), ".", 4)
	if len(parts) < 3 {
		return time.Time{}, false
	}
	t, err := time.Parse("2006.1.2", strings.Join(parts[:3], "."))
	return t, err == nil
}

// reportBandcamp asks Bandcamp's discover feed once for a tag it should
// always have. The feed is undocumented and can change without notice, and
// discovery stations quietly lean on YouTube alone when it does, so this is
// where that gets said out loud.
func reportBandcamp(w io.Writer, c discover.BandcampClient) int {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	items, err := c.Tag(ctx, "ambient", "top")
	if err == nil && len(items) == 0 {
		err = errors.New("answered, but with no tracks; the feed may have changed shape")
	}
	if err != nil {
		fmt.Fprintf(w, "[FAIL] bandcamp discover feed\n")
		fmt.Fprintf(w, "        -> %v (discovery stations lean on YouTube alone until it answers)\n", err)
		return ecBandcamp
	}
	fmt.Fprintf(w, "[ ok ] bandcamp discover feed\n")
	fmt.Fprintf(w, "        -> %d tracks for \"ambient\"\n", len(items))
	return 0
}

// reportConfig is doctor's config check: the file, how many of the
// listener's own stations it carries, and every repair the loader made.
// A [[stations]] entry it had to skip shows up here as a warning, so the
// count and the warnings together say what the next launch will play.
func reportConfig(w io.Writer, cfgFile string, cfg config.Config, cfgErr error) int {
	switch {
	case cfgErr == nil:
		fmt.Fprintf(w, "[ ok ] config   %s\n", cfgFile)
		// Two kinds of the listener's own stations, counted apart: a
		// stream or page link they play, and a discovery that finds songs.
		discoveries := 0
		for _, st := range cfg.Stations {
			if st.Discover != "" {
				discoveries++
			}
		}
		switch n := len(cfg.Stations) - discoveries; n {
		case 0:
			fmt.Fprintf(w, "        -> no custom stations (screech add <link> adds one)\n")
		case 1:
			fmt.Fprintf(w, "        -> 1 custom station\n")
		default:
			fmt.Fprintf(w, "        -> %d custom stations\n", n)
		}
		fmt.Fprintf(w, "        -> discovery stations: %d (in config.toml; the ones started from FIND are kept in the database)\n", discoveries)
		for _, warn := range cfg.Warnings {
			fmt.Fprintf(w, "        -> warning: %s\n", warn)
		}
	case os.IsNotExist(cfgErr):
		fmt.Fprintf(w, "[ -- ] config   %s\n", cfgFile)
		fmt.Fprintf(w, "        -> not created yet; first launch writes it\n")
	default:
		fmt.Fprintf(w, "[FAIL] config   %s\n", cfgFile)
		fmt.Fprintf(w, "        -> %v\n", cfgErr)
		return ecConfig
	}
	return 0
}
