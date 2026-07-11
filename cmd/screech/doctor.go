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

// runDoctor checks every joint screech depends on and reports what it finds.
// All checks run even after failures; the output is made to be pasted.
func runDoctor() {
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
	cfg, dbPath, cfgErr := config.Load()
	fmt.Printf("[%s] config   %s\n", ok(cfgErr == nil), cfgFile)
	if cfgErr != nil {
		fmt.Printf("        -> %v\n", cfgErr)
	}

	stations := 0
	var dbErr error
	if cfgErr == nil {
		var c *core.Core
		c, dbErr = core.Open(dbPath)
		if dbErr == nil {
			stations = c.StationCount()
			_ = c.Close()
		}
		fmt.Printf("[%s] database %s (%d stations)\n", ok(dbErr == nil), dbPath, stations)
		if dbErr != nil {
			fmt.Printf("        -> %v\n", dbErr)
		}
	}

	mpvPath := "mpv"
	if cfgErr == nil && cfg.MpvPath != "" {
		mpvPath = cfg.MpvPath
	}
	resolved, lookErr := exec.LookPath(mpvPath)
	if resolved == "" {
		resolved = mpvPath
	}
	fmt.Printf("[%s] mpv      %s\n", ok(lookErr == nil), resolved)
	switch {
	case lookErr != nil:
		fmt.Printf("        -> %v\n        -> install mpv (scoop/choco/brew/apt) or set mpv_path in config.toml\n        -> just installed? PATH refreshes only in NEW terminals\n", lookErr)
	default:
		if out, err := exec.Command(resolved, "--version").Output(); err == nil {
			fmt.Printf("        -> %s\n", strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0]))
		}
		// The joint that matters: spawn mpv and shake hands over IPC
		// (unix socket / Windows named pipe).
		pl, ipcErr := player.NewMPV(resolved)
		if ipcErr == nil {
			_ = pl.Close()
		}
		fmt.Printf("[%s] mpv IPC  spawn + handshake\n", ok(ipcErr == nil))
		if ipcErr != nil {
			fmt.Printf("        -> %v\n", ipcErr)
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
	}

	fmt.Println()
	fmt.Println("log of last run: " + logPath())
	holdConsoleOnExit()
}
