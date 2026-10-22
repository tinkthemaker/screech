//go:build windows

package main

// The Windows face's shell: the same shared startup the terminal uses,
// then a frameless Wails window over the same session engine. Startup
// failures become message boxes, a second launch becomes a handoff to
// the running window, and every quit path — Q, Alt+F4, taskbar close —
// funnels through one shutdown.

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime/debug"
	"time"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	wailsoptions "github.com/wailsapp/wails/v2/pkg/options/windows"
	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/tinkthemaker/screech/internal/app"
	"github.com/tinkthemaker/screech/internal/gui"
	"github.com/tinkthemaker/screech/internal/session"
)

// wailsEmitter adapts the runtime's EventsEmit to gui.Emitter.
type wailsEmitter struct{ ctx context.Context }

func (e wailsEmitter) Emit(event string, data any) {
	wailsruntime.EventsEmit(e.ctx, event, data)
}

// reducedMotion: --reduced-motion or a nonempty SCREECH_REDUCED_MOTION,
// same as the terminal parses them.
func reducedMotion() bool {
	if os.Getenv("SCREECH_REDUCED_MOTION") != "" {
		return true
	}
	for _, a := range os.Args[1:] {
		if a == "--reduced-motion" {
			return true
		}
	}
	return false
}

func main() {
	log := app.NewLog(app.DefaultLogPath())
	var rt *app.Runtime
	defer func() {
		if r := recover(); r != nil {
			// Linef buffers until the log is ours, so a panic before the
			// single-instance lock can't truncate a running screech's
			// file — the box still names where details would land.
			log.Linef("panic: %v\n%s", r, debug.Stack())
			messageBox(fmt.Sprintf("Screech crashed. Details were written to %s.", log.Path()))
			os.Exit(2)
		}
	}()

	var err error
	rt, err = app.Start(log)
	if err != nil {
		if errors.Is(err, app.ErrAlreadyRunning) {
			// Our own window may still be starting — give it the full
			// wait before a terminal takes the blame.
			if handoff(w32Finder{}, 3*time.Second) {
				return
			}
			messageBox("Screech is already playing in a terminal.")
			os.Exit(1)
		}
		messageBox(startMessage(err))
		os.Exit(1)
	}

	state := session.New(rt.Core, rt.Player, session.Options{
		SyncLimit:  rt.Config.SyncLimit,
		AutoHopAds: rt.Config.AutoHopAds,
		Notice:     rt.Notice,
		Dot:        "·",
	})
	sh := newShell(rt)
	br := gui.New(rt.Core, sh, gui.Prefs{ReducedMotion: reducedMotion()})
	runner := session.NewRunner(state, br.Sink())
	sh.runner = runner
	br.Attach(runner)

	// The window opens at the height the last run left it — the saved
	// fold state, in logical px Wails scales itself.
	height := stripH
	if saved := rt.Core.WindowPlacement(); saved.Saved && saved.Unfolded {
		height = consoleH
	}
	sh.unfolded = height == consoleH

	assets, err := fs.Sub(gui.Assets, "frontend")
	if err != nil {
		messageBox(err.Error())
		os.Exit(1)
	}

	err = wails.Run(&options.App{
		Title:            "Screech",
		Width:            stripW,
		Height:           height,
		Frameless:        true,
		DisableResize:    true,
		StartHidden:      true,
		BackgroundColour: options.NewRGB(0, 0, 0),
		AssetServer:      &assetserver.Options{Assets: assets},
		Bind:             []any{br},
		OnStartup: func(ctx context.Context) {
			sh.onStartup(ctx)
			br.SetEmitter(wailsEmitter{ctx})
			runner.Start()
		},
		OnDomReady: func(ctx context.Context) {
			sh.onDomReady()
		},
		OnBeforeClose: func(ctx context.Context) bool {
			sh.doBeforeClose()
			return false
		},
		OnShutdown: func(ctx context.Context) {
			sh.onShutdown()
		},
		Windows: &wailsoptions.Options{
			WindowClassName:      windowClass,
			WebviewUserDataPath:  filepath.Join(rt.Config.DataDir, "webview"),
			IsZoomControlEnabled: false,
			DisablePinchZoom:     true,
		},
	})
	if err != nil {
		log.Linef("FATAL: %v", err)
		sh.onShutdown()
		messageBox(err.Error())
		os.Exit(1)
	}
}

// startMessage is the message-box text for a startup failure: mpv gets
// install guidance a window user can act on, other stages stay terse.
// The TUI's MpvHelp checklist stays untouched for the terminal.
func startMessage(err error) string {
	var se *app.StartError
	if !errors.As(err, &se) {
		return err.Error()
	}
	if se.Stage == app.StageMpv {
		return fmt.Sprintf("Screech couldn't start mpv, which it needs for playback.\n\n%v\n\n"+
			"Install it with scoop install mpv (or choco install mpv, or from https://mpv.io), "+
			"then start Screech again. If mpv lives somewhere unusual, set mpv_path in config.toml.\n\n"+
			"screech doctor in a terminal checks everything.", se.Err)
	}
	return fmt.Sprintf("Screech couldn't start.\n\n%v\n\nscreech doctor in a terminal checks everything.", se)
}
