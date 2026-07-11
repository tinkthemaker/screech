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

// mpv on Windows serves JSON IPC over a named pipe, not a unix socket.
func ipcPath() string {
	return fmt.Sprintf(`\\.\pipe\screech-mpv-%d`, os.Getpid())
}

func dialIPC(path string) (net.Conn, error) {
	timeout := 2 * time.Second
	return winio.DialPipe(path, &timeout)
}

// configureCmd hides the mpv console window.
func configureCmd(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
}
