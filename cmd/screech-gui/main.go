//go:build windows

package main

import (
	"io/fs"
	"log"
	"os"
	"path/filepath"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/windows"

	"github.com/tinkthemaker/screech/internal/gui"
)

func main() {
	assets, err := fs.Sub(gui.Assets, "frontend")
	if err != nil {
		log.Fatal(err)
	}
	// Spike: browser state kept inside the repo sandbox, never AppData.
	webviewData := filepath.Join(".review", "webview-data")
	_ = os.MkdirAll(webviewData, 0o755)
	err = wails.Run(&options.App{
		Title:            "Screech",
		Width:            480,
		Height:           128,
		Frameless:        true,
		DisableResize:    true,
		BackgroundColour: options.NewRGB(0, 0, 0),
		AssetServer:      &assetserver.Options{Assets: assets},
		Windows: &windows.Options{
			WindowClassName:      "ScreechLCARS",
			WebviewUserDataPath:  webviewData,
			IsZoomControlEnabled: false,
			DisablePinchZoom:     true,
		},
	})
	if err != nil {
		log.Fatal(err)
	}
}
