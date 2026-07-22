package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func withConfigHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", dir)
	return dir
}

func TestLogPath(t *testing.T) {
	base := withConfigHome(t)
	want := filepath.Join(base, "screech", "screech.log")
	if got := logPath(); got != want {
		t.Errorf("logPath() = %q, want %q", got, want)
	}
}

func TestOpenLogCreatesFile(t *testing.T) {
	withConfigHome(t)
	// Reset package state so this test is independent of others.
	logF = nil
	t.Cleanup(func() {
		if logF != nil {
			_ = logF.Close()
			logF = nil
		}
	})

	openLog()
	if logF == nil {
		t.Fatal("openLog did not open a file")
	}
	if _, err := os.Stat(logPath()); err != nil {
		t.Fatalf("log file not created: %v", err)
	}
}

func TestLogLineWrites(t *testing.T) {
	withConfigHome(t)
	logF = nil
	t.Cleanup(func() {
		if logF != nil {
			_ = logF.Close()
			logF = nil
		}
	})

	openLog()
	logLine("hello %s %d", "world", 42)

	data, err := os.ReadFile(logPath())
	if err != nil {
		t.Fatalf("reading log: %v", err)
	}
	if got := string(data); !strings.Contains(got, "hello world 42\n") {
		t.Errorf("log contents = %q, want it to contain formatted line", got)
	}
}

func TestLogLineNoFileIsNoOp(t *testing.T) {
	logF = nil
	// Must not panic when no log file is open.
	logLine("this should be dropped")
}

func TestVersionSet(t *testing.T) {
	if version == "" {
		t.Error("version must not be empty")
	}
}
