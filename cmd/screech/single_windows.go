//go:build windows

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/windows"
)

// acquireSingleInstance uses a named mutex whose name is derived from the
// data directory, so two users with different data directories do not block
// one another. The mutex is destroyed when the returned cleanup runs.
func acquireSingleInstance(dataDir string) (func(), error) {
	name, _ := syscall.UTF16PtrFromString("screech-" + hashDataDir(dataDir))
	h, err := windows.CreateMutex(nil, false, name)
	if err != nil {
		if err == windows.ERROR_ALREADY_EXISTS {
			return nil, fmt.Errorf("another screech is already running")
		}
		return nil, err
	}
	return func() { _ = windows.CloseHandle(h) }, nil
}

func hashDataDir(d string) string {
	s := sha256.Sum256([]byte(strings.ToLower(filepath.Clean(d))))
	return hex.EncodeToString(s[:8])
}
