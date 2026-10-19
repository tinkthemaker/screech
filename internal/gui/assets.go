// Package gui is the platform-neutral bridge between the LCARS window and
// the shared listening engine. It knows nothing about Wails; the
// Windows-only shell in cmd/screech-gui binds it.
package gui

import "embed"

// Assets is the window's front end, served verbatim from the binary.
//
//go:embed all:frontend
var Assets embed.FS
