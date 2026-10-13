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
	exitCode := 0
	switch {
	case cfgErr == nil:
		fmt.Printf("[ ok ] config   %s\n", cfgFile)
		for _, w := range cfg.Warnings {
			fmt.Printf("        -> warning: %s\n", w)
		}
	case os.IsNotExist(cfgErr):
		fmt.Printf("[ -- ] config   %s\n", cfgFile)
		fmt.Printf("        -> not created yet; first launch writes it\n")
	default:
		fmt.Printf("[FAIL] config   %s\n", cfgFile)
		fmt.Printf("        -> %v\n", cfgErr)
		exitCode |= ecConfig
	}

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
