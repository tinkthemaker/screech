package ytdlp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// flipCtx is live for its first Err call and over from the second on: the
// shape of a deadline that lands between acquire's own check and the moment
// the turn is granted, which no timing-based test can hit on purpose.
type flipCtx struct {
	context.Context
	calls atomic.Int32
}

func (c *flipCtx) Err() error {
	if c.calls.Add(1) == 1 {
		return nil
	}
	return context.Canceled
}

// A context that ends right as the turn is granted must not start a
// process, and must hand the turn straight on.
func TestRunChecksTheContextAfterTheTurn(t *testing.T) {
	r, logPath := fake(t, "search")
	_, err := r.Songs(&flipCtx{Context: context.Background()}, "x", 1)
	if !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "gave up waiting its turn") {
		t.Fatalf("error = %v, want the gave-up-waiting error wrapping context.Canceled", err)
	}
	if lines := logLines(t, logPath); len(lines) != 0 {
		t.Fatalf("yt-dlp ran for a dead context: %v", lines)
	}
	free, stop := context.WithTimeout(context.Background(), 2*time.Second)
	defer stop()
	if err := lock.acquire(free, false); err != nil {
		t.Fatalf("the turn was not released: %v", err)
	}
	lock.release()
}

// hookLog collects what the process hooks saw, in order.
type hookLog struct {
	mu     sync.Mutex
	events []string
	// heldAtRelease is whether the app-wide turn was still held when the
	// release hook ran.
	heldAtRelease []bool
}

func (h *hookLog) add(s string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.events = append(h.events, s)
}

func (h *hookLog) hooks(r *Runner) {
	r.Protect = func(cmd *exec.Cmd) {
		h.add(fmt.Sprintf("protect started=%v", cmd.Process != nil))
	}
	r.Adopt = func(cmd *exec.Cmd) func() {
		h.add(fmt.Sprintf("adopt started=%v", cmd.Process != nil))
		return func() {
			lock.mu.Lock()
			held := lock.held
			lock.mu.Unlock()
			h.mu.Lock()
			h.heldAtRelease = append(h.heldAtRelease, held)
			h.mu.Unlock()
			h.add("release")
		}
	}
}

// The app installs the player's child protection through these: Protect
// before the process exists, Adopt the moment it does, and the release it
// hands back runs once the run is over, still inside the app-wide turn, so
// no later yt-dlp starts while a child of this one is alive.
func TestRunnerHooksBracketTheProcess(t *testing.T) {
	r, _ := fake(t, "search")
	var h hookLog
	h.hooks(&r)
	if _, err := r.Songs(context.Background(), "x", 1); err != nil {
		t.Fatal(err)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	want := []string{"protect started=false", "adopt started=true", "release"}
	if !slices.Equal(h.events, want) {
		t.Fatalf("hooks ran as %v, want %v", h.events, want)
	}
	if !slices.Equal(h.heldAtRelease, []bool{true}) {
		t.Fatalf("release ran with the turn held = %v, want it held", h.heldAtRelease)
	}
}

// A timed-out yt-dlp is the case the release exists for: the launcher dies
// with the context but its child lives on holding the pipes, so the group
// is closed the moment the context ends, and the turn is not handed on
// before that.
func TestRunnerReleasesOnceWhenTheContextEnds(t *testing.T) {
	r, _ := fake(t, "hang")
	var h hookLog
	h.hooks(&r)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if _, err := r.Songs(ctx, "x", 1); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want context.DeadlineExceeded", err)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	releases := 0
	for _, e := range h.events {
		if e == "release" {
			releases++
		}
	}
	if releases != 1 {
		t.Fatalf("release ran %d times, want exactly once: %v", releases, h.events)
	}
	if !slices.Equal(h.heldAtRelease, []bool{true}) {
		t.Fatalf("release ran with the turn held = %v, want it held", h.heldAtRelease)
	}
}

// A process that never started has nothing to adopt.
func TestRunnerHooksSkipAFailedStart(t *testing.T) {
	var h hookLog
	r := Runner{Path: filepath.Join(t.TempDir(), "no-such-yt-dlp")}
	h.hooks(&r)
	if _, err := r.Songs(context.Background(), "x", 1); err == nil {
		t.Fatal("a missing executable must fail")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if want := []string{"protect started=false"}; !slices.Equal(h.events, want) {
		t.Fatalf("hooks ran as %v, want only %v", h.events, want)
	}
}

// yt-dlp reports a dead connection in words, not in a status; a lookup that
// failed for want of a network must be told apart from a video that is gone,
// because the first must not strike a station.
func TestNetworkFailuresAreMarked(t *testing.T) {
	for _, msg := range []string{
		"ERROR: [youtube:search] x: Unable to download API page: <urlopen error [Errno 11001] getaddrinfo failed> (caused by URLError(gaierror(11001, 'getaddrinfo failed')))",
		"ERROR: Unable to download webpage: <urlopen error [Errno -3] Temporary failure in name resolution>",
		"ERROR: unable to download video data: [Errno 101] Network is unreachable",
		"ERROR: [youtube] x: Unable to download API page: HTTPSConnectionPool(host='www.youtube.com', port=443): Read timed out.",
		"ERROR: <urlopen error [WinError 10061] No connection could be made because the target machine actively refused it> Connection refused",
	} {
		if !looksOffline(msg) {
			t.Errorf("looksOffline(%q) = false", msg)
		}
	}
	for _, msg := range []string{
		"ERROR: [youtube] gone: Video unavailable",
		"ERROR: [youtube] x: Sign in to confirm you're not a bot",
		"ERROR: Unable to download webpage: HTTP Error 429: Too Many Requests",
		"ERROR: [youtube] x: Private video",
		"",
	} {
		if looksOffline(msg) {
			t.Errorf("looksOffline(%q) = true", msg)
		}
	}
}

func TestOfflineRunWrapsErrNetwork(t *testing.T) {
	r, _ := fake(t, "offline")
	_, err := r.Songs(context.Background(), "x", 1)
	if !errors.Is(err, ErrNetwork) {
		t.Fatalf("error = %v, want one wrapping ErrNetwork", err)
	}
	if !strings.Contains(err.Error(), "getaddrinfo failed") {
		t.Errorf("error = %v, want yt-dlp's own words kept", err)
	}
	// A gone video is not the network.
	r, _ = fake(t, "fail")
	if _, err := r.Resolve(context.Background(), "https://www.youtube.com/watch?v=gone"); errors.Is(err, ErrNetwork) {
		t.Errorf("a missing video is not a network failure: %v", err)
	}
}

// doctor says which runtime yt-dlp will have, including deno, which needs no
// flag and so is invisible to DetectJSRuntime.
func TestJSRuntimeName(t *testing.T) {
	exe := func(name string) string {
		if runtime.GOOS == "windows" {
			return name + ".exe"
		}
		return name
	}
	denoDir, nodeDir, bunDir := t.TempDir(), t.TempDir(), t.TempDir()
	touchExe(t, denoDir, exe("deno"))
	touchExe(t, nodeDir, exe("node"))
	touchExe(t, bunDir, exe("bun"))
	join := func(dirs ...string) string { return strings.Join(dirs, string(os.PathListSeparator)) }
	for _, c := range []struct{ name, path, want string }{
		{"nothing", t.TempDir(), "none"},
		{"node", join(nodeDir), "node"},
		{"bun", join(bunDir), "bun"},
		{"node beats bun", join(bunDir, nodeDir), "node"},
		{"deno wins", join(bunDir, nodeDir, denoDir), "deno"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("PATH", c.path)
			if got := JSRuntimeName(); got != c.want {
				t.Fatalf("JSRuntimeName = %q, want %q", got, c.want)
			}
		})
	}
}
