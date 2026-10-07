//go:build windows

package player

import (
	"fmt"
	"net"
	"os"
	"time"

	winio "github.com/Microsoft/go-winio"
)

func prepareIPCDir(dir string) error { return nil }

func ipcPath(dir string, gen int) string {
	return fmt.Sprintf(`\\.\pipe\screech-mpv-%d-%d`, os.Getpid(), gen)
}

func dialIPC(path string) (net.Conn, error) {
	timeout := 2 * time.Second
	return winio.DialPipe(path, &timeout)
}

func cleanupIPC(path string) error { return nil }
