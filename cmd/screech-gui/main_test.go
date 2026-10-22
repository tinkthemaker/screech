//go:build windows

package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/tinkthemaker/screech/internal/app"
)

// The window's message boxes must not print the terminal's install
// checklist ("open a NEW terminal", brew/apt lines) — a face gets text
// written for it.
func TestStartMessageMpvStage(t *testing.T) {
	err := &app.StartError{Stage: app.StageMpv, Err: errors.New("exec: no mpv")}
	got := startMessage(err)
	for _, want := range []string{
		"Screech couldn't start mpv, which it needs for playback.",
		"exec: no mpv",
		"scoop install mpv",
		"mpv_path in config.toml",
		"screech doctor in a terminal",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("mpv message missing %q:\n%s", want, got)
		}
	}
	for _, banned := range []string{"NEW terminal", "brew install", "pacman"} {
		if strings.Contains(got, banned) {
			t.Fatalf("mpv message has terminal-only text %q:\n%s", banned, got)
		}
	}
}

func TestStartMessageOtherStage(t *testing.T) {
	err := &app.StartError{Stage: app.StageDatabase, Err: errors.New("file is locked")}
	got := startMessage(err)
	if got != "Screech couldn't start.\n\nopening database: file is locked\n\nscreech doctor in a terminal checks everything." {
		t.Fatalf("got %q", got)
	}
}

func TestStartMessagePlainError(t *testing.T) {
	if got := startMessage(errors.New("boom")); got != "boom" {
		t.Fatalf("got %q", got)
	}
}
