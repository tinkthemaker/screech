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
	return filepath.Join(os.TempDir(), fmt.Sprintf("screech-mpv-%d.sock", os.Getpid()))
}

func dialIPC(path string) (net.Conn, error) {
	return net.Dial("unix", path)
}

func configureCmd(cmd *exec.Cmd) {}
