//go:build windows

package player

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"syscall"
	"time"

	winio "github.com/Microsoft/go-winio"
)

func prepareIPCDir(dir string) error { return nil }

func ipcPath(dir string) string {
	return fmt.Sprintf(`\\.\pipe\screech-mpv-%d`, os.Getpid())
}

func dialIPC(path string) (net.Conn, error) {
	timeout := 2 * time.Second
	return winio.DialPipe(path, &timeout)
}

func configureCmd(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
}

func cleanupIPC(path string) error { return nil }
