package main

import (
	"context"
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
	"github.com/tinkthemaker/screech/internal/player"
	versionpkg "github.com/tinkthemaker/screech/internal/version"
)

const (
	ecConfig   = 1 << iota // config exists but won't parse
	ecDatabase             // database exists but won't open or fails checks
	ecMpv                  // mpv not on PATH
	ecIpc                  // mpv found but IPC handshake failed
	ecNetwork              // radio-browser unreachable
	ecYtdlp                // ytdlp_path set but yt-dlp won't run
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
	cfg, dbPath, _, cfgErr := config.Read()
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

	exitCode |= reportYtdlp(os.Stdout, cfg.YtdlpPath)

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

// reportYtdlp checks the optional page-link resolver. Off is not a
// failure: only page links (YouTube, SoundCloud…) need it.
func reportYtdlp(w io.Writer, path string) int {
	if path == "" {
		fmt.Fprintf(w, "[ -- ] yt-dlp   off (ytdlp_path is empty; page links won't play)\n")
		return 0
	}
	resolved, err := exec.LookPath(path)
	var version []byte
	if err == nil {
		version, err = exec.Command(resolved, "--version").Output()
	}
	if err != nil {
		// Shown cleaned, run as written: a path is the user's to get right.
		fmt.Fprintf(w, "[FAIL] yt-dlp   %s\n", core.CleanText(path))
		fmt.Fprintf(w, "        -> %v\n        -> install yt-dlp, or fix or empty ytdlp_path in config.toml\n", err)
		return ecYtdlp
	}
	fmt.Fprintf(w, "[ ok ] yt-dlp   %s\n", resolved)
	fmt.Fprintf(w, "        -> %s\n", strings.TrimSpace(string(version)))
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
		switch n := len(cfg.Stations); n {
		case 0:
			fmt.Fprintf(w, "        -> no custom stations (screech add <link> adds one)\n")
		case 1:
			fmt.Fprintf(w, "        -> 1 custom station\n")
		default:
			fmt.Fprintf(w, "        -> %d custom stations\n", n)
		}
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
