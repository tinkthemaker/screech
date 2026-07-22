//go:build !windows

package player

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
)

func ipcPath() string {
	// mpv creates this control socket, and anyone who can connect to it can
	// drive mpv (load files, run commands). A world-writable temp dir would
	// let other local users reach it, so keep it inside a per-user directory
	// only its owner can enter.
	dir := filepath.Join(os.TempDir(), fmt.Sprintf("screech-%d", os.Getuid()))
	_ = os.MkdirAll(dir, 0o700)
	return filepath.Join(dir, fmt.Sprintf("screech-mpv-%d.sock", os.Getpid()))
}

func dialIPC(path string) (net.Conn, error) {
	return net.Dial("unix", path)
}

func configureCmd(cmd *exec.Cmd) {}
