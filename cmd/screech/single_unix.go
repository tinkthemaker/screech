//go:build !windows

package main

import (
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// acquireSingleInstance takes an advisory flock on a file in the data
// directory. The lock is released when the returned cleanup function runs
// (process exit also drops it).
func acquireSingleInstance(dataDir string) (func(), error) {
	p := filepath.Join(dataDir, "screech.lock")
	f, err := os.OpenFile(p, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("another screech is already running")
	}
	return func() { _ = f.Close() }, nil
}
