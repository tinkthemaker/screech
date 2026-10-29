//go:build windows

package player

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// init (which runs before TestMain, so before any of the fake-mpv routing)
// lets the test binary play two roles in a process tree: SCREECH_FAKE_TREE
// "parent:<dir>" is a launcher that, once <dir>/go exists, starts a child of
// its own, records the child's pid and waits, which is how a standalone
// yt-dlp is built; "child" just waits.
func init() {
	mode := os.Getenv("SCREECH_FAKE_TREE")
	switch {
	case mode == "child":
		time.Sleep(60 * time.Second)
		os.Exit(0)
	case strings.HasPrefix(mode, "parent:"):
		dir := strings.TrimPrefix(mode, "parent:")
		for i := 0; ; i++ {
			if _, err := os.Stat(filepath.Join(dir, "go")); err == nil {
				break
			}
			if i > 1000 {
				os.Exit(2)
			}
			time.Sleep(10 * time.Millisecond)
		}
		child := exec.Command(os.Args[0])
		child.Env = append(os.Environ(), "SCREECH_FAKE_TREE=child")
		if err := child.Start(); err != nil {
			os.Exit(3)
		}
		_ = os.WriteFile(filepath.Join(dir, "pid"), []byte(strconv.Itoa(child.Process.Pid)), 0o600)
		_ = child.Wait()
		os.Exit(0)
	}
}

// Releasing the group ends the launcher and the child it started. The test
// is the claim the timed-out lookup depends on: without the job object, the
// context kills the launcher and the child lives on holding the pipes.
func TestAdoptChildReleaseKillsTheWholeTree(t *testing.T) {
	dir := t.TempDir()
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), "SCREECH_FAKE_TREE=parent:"+dir)
	ProtectChild(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()
	release := AdoptChild(cmd)
	t.Cleanup(func() {
		release()
		_ = cmd.Process.Kill()
	})
	// Only now may the launcher start its child, so the child is born inside
	// the group the way it would be after a real Start.
	if err := os.WriteFile(filepath.Join(dir, "go"), nil, 0o600); err != nil {
		t.Fatal(err)
	}

	var pid uint32
	deadline := time.Now().Add(10 * time.Second)
	for pid == 0 {
		if b, err := os.ReadFile(filepath.Join(dir, "pid")); err == nil {
			n, _ := strconv.Atoi(strings.TrimSpace(string(b)))
			pid = uint32(n)
		}
		if pid == 0 {
			if time.Now().After(deadline) {
				t.Fatal("the launcher never started its child")
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	child, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		t.Fatalf("open child %d: %v", pid, err)
	}
	defer windows.CloseHandle(child)
	if ev, _ := windows.WaitForSingleObject(child, 0); ev != uint32(windows.WAIT_TIMEOUT) {
		t.Fatalf("the child is not running before the release (wait = %d)", ev)
	}

	release()
	release() // once is enough, and twice is harmless

	ev, err := windows.WaitForSingleObject(child, 5000)
	if err != nil || ev != windows.WAIT_OBJECT_0 {
		t.Fatalf("the child survived the release: wait = %d, %v", ev, err)
	}
	select {
	case <-waited:
	case <-time.After(5 * time.Second):
		t.Fatal("the launcher survived the release")
	}
}
