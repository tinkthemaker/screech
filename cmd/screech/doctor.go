package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"screech/internal/config"
	"screech/internal/core"
	"screech/internal/player"
)

const (
	ecConfig   = 1 << iota // bad or missing config
	ecDatabase             // database missing or unreadable
	ecMpv                  // mpv not on PATH
	ecIpc                  // mpv found but IPC handshake failed
	ecNetwork              // radio-browser unreachable
)

// runDoctor checks every joint screech depends on and reports what it finds.
// It is read-only: it does not create files, directories, or database entries.
// The returned exit code is a bit mask of the failures it encountered.
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
	exitCode := 0
	fmt.Printf("[%s] config   %s\n", ok(cfgErr == nil), cfgFile)
	if cfgErr != nil {
		fmt.Printf("        -> %v\n", cfgErr)
		exitCode |= ecConfig
	}

	stations := 0
	store, dbErr := core.OpenStoreReadOnly(dbPath)
	if dbErr == nil {
		stations, _ = store.StationCount()
		_ = store.Close()
	}
	fmt.Printf("[%s] database %s\n", ok(dbErr == nil), dbPath)
	if dbErr != nil {
		fmt.Printf("        -> %v\n", dbErr)
		exitCode |= ecDatabase
	}

	// The doctor command must not touch the user's data directory. The IPC
	// test uses a temporary sandbox that is created and removed in one go.
	ipcDataDir := dataDir
	var cleanupDataDir func()
	cleanupDataDir = func() {}
	if cfgErr != nil {
		if d, err := os.MkdirTemp("", "screech-doctor-*"); err == nil {
			ipcDataDir = d
			cleanupDataDir = func() { _ = os.RemoveAll(d) }
		}
	}
	defer cleanupDataDir()

	mpvPath := cfg.MpvPath
	if mpvPath == "" {
		mpvPath = "mpv"
	}
	resolved, lookErr := exec.LookPath(mpvPath)
	if resolved == "" {
		resolved = mpvPath
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
		// (unix socket / Windows named pipe).
		var ipcErr error
		if ipcDataDir != "" {
			pl, err := player.NewMPV(resolved, ipcDataDir)
			if err != nil {
				ipcErr = err
			} else {
				_ = pl.Close()
			}
		} else {
			ipcErr = fmt.Errorf("no data directory available for IPC test")
		}
		fmt.Printf("[%s] mpv IPC  spawn + handshake\n", ok(ipcErr == nil))
		if ipcErr != nil {
			fmt.Printf("        -> %v\n", ipcErr)
			exitCode |= ecIpc
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://all.api.radio-browser.info/json/servers", nil)
	req.Header.Set("User-Agent", "screech/"+version)
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

	if stations > 0 {
		fmt.Printf("\nstations cached: %d\n", stations)
	}
	if exitCode != 0 {
		holdConsoleOnExit()
	}
	return exitCode
}
