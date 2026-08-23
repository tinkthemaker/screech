//go:build !windows

package player

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
)

func prepareIPCDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return os.Chmod(dir, 0o700)
}

func ipcPath(dir string) string {
	return filepath.Join(dir, fmt.Sprintf("mpv-%d.sock", os.Getpid()))
}

func dialIPC(path string) (net.Conn, error) {
	return net.Dial("unix", path)
}

func configureCmd(cmd *exec.Cmd) {}

func cleanupIPC(path string) error {
	return os.Remove(path)
}
