package app

import (
	"fmt"
	"os"
	"path/filepath"
)

// Log is a buffered last-run log: one launch, one file, easy to paste.
// Lines written before Open (startup, config) still land, but only once
// Open proves this process is allowed to write — after the single-instance
// lock, or when failing for any other reason — so a second screech never
// truncates the running instance's log.
type Log struct {
	path string
	f    *os.File
	buf  []string
}

func NewLog(path string) *Log { return &Log{path: path} }

func (l *Log) Path() string { return l.path }

func (l *Log) Open() {
	if l.f != nil {
		return
	}
	_ = os.MkdirAll(filepath.Dir(l.path), 0o700)
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return
	}
	l.f = f
	for _, line := range l.buf {
		fmt.Fprintln(l.f, line)
	}
	l.buf = nil
	_ = l.f.Sync()
}

func (l *Log) Close() {
	if l.f != nil {
		_ = l.f.Close()
		l.f = nil
	}
}

// Linef buffers until the log file exists. Lines written before the
// single-instance lock (startup, config) still land, but only once Open
// proves this process is allowed to write.
func (l *Log) Linef(format string, args ...any) {
	line := fmt.Sprintf(format, args...)
	if l.f == nil {
		l.buf = append(l.buf, line)
		return
	}
	fmt.Fprintln(l.f, line)
	_ = l.f.Sync()
}

// DefaultLogPath is where the last-run log lives when the caller has no
// better answer: the OS config dir, beside the executable when that
// fails, and the working directory as a last resort.
func DefaultLogPath() string {
	if base, err := os.UserConfigDir(); err == nil {
		return filepath.Join(base, "screech", "screech.log")
	}
	if exe, err := os.Executable(); err == nil {
		return filepath.Join(filepath.Dir(exe), "screech.log")
	}
	return "screech.log"
}
