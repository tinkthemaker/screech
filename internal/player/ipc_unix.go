//go:build !windows

package player

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
)

func prepareIPCDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return os.Chmod(dir, 0o700)
}

func ipcPath(dir string, gen int) string {
	return filepath.Join(dir, fmt.Sprintf("mpv-%d-%d.sock", os.Getpid(), gen))
}

func dialIPC(path string) (net.Conn, error) {
	return net.Dial("unix", path)
}

func cleanupIPC(path string) error {
	return os.Remove(path)
}
