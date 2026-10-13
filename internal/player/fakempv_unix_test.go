//go:build !windows

package player

import (
	"net"
	"os"
)

// fakeListen serves the unix socket the client side dials via ipcPath. The
// socket file is removed on exit; the client's cleanupIPC does the same, so
// both racing removals are fine.
func fakeListen(path string) (net.Listener, error) {
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	return &unixCleanupListener{Listener: ln, path: path}, nil
}

type unixCleanupListener struct {
	net.Listener
	path string
}

func (l *unixCleanupListener) Close() error {
	err := l.Listener.Close()
	_ = os.Remove(l.path)
	return err
}
