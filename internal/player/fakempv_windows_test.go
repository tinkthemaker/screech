//go:build windows

package player

import (
	"net"

	winio "github.com/Microsoft/go-winio"
)

// fakeListen serves the named pipe the client side dials via ipcPath.
func fakeListen(path string) (net.Listener, error) {
	return winio.ListenPipe(path, nil)
}
