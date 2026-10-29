package ytdlp

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// exeName is the file Find looks for.
func exeName() string {
	if runtime.GOOS == "windows" {
		return "yt-dlp.exe"
	}
	return "yt-dlp"
}

// Find answers "which yt-dlp?" without the listener setting anything up.
// configured is the ytdlp_path setting: "" means look (PATH, then beside the
// executable, then the data folder), "off" means don't use yt-dlp at all, and
// anything else is the listener's own word and is returned as given, even if
// it doesn't exist; doctor is where that gets said out loud.
func Find(configured, exeDir, dataDir string) (path string, ok bool) {
	configured = strings.TrimSpace(configured)
	if strings.EqualFold(configured, "off") {
		return "", false
	}
	if configured != "" {
		return configured, true
	}
	// A match in the working directory comes back with exec.ErrDot, so the
	// err == nil test is also what keeps a stray yt-dlp.exe there from being
	// the one Screech runs.
	if p, err := exec.LookPath(exeName()); err == nil {
		return p, true
	}
	for _, dir := range []string{exeDir, dataDir} {
		if dir == "" {
			continue
		}
		p := filepath.Join(dir, exeName())
		if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() {
			return p, true
		}
	}
	return "", false
}

// DetectJSRuntime picks the --js-runtimes value for yt-dlp, which needs a
// JavaScript runtime to solve YouTube's challenges. Deno is enabled by
// default, so with deno on PATH the answer is "" (nothing to pass). Failing
// that, the first of node and bun on PATH is returned as "name:path", the
// form --js-runtimes takes. No runtime at all is also "": yt-dlp still runs,
// and doctor warns.
func DetectJSRuntime() string {
	name, flag := jsRuntime()
	if name == "deno" || name == "none" {
		return ""
	}
	return flag
}

// JSRuntimeName names the runtime yt-dlp will have for its challenges, for
// doctor and the log: "deno", "node", "bun" or "none". It tells deno from
// nothing, which DetectJSRuntime can't: both need no flag.
func JSRuntimeName() string {
	name, _ := jsRuntime()
	return name
}

// jsRuntime is the one lookup behind both: the runtime's name and the
// --js-runtimes value that selects it.
func jsRuntime() (name, flag string) {
	if _, err := exec.LookPath("deno"); err == nil {
		return "deno", ""
	}
	for _, n := range []string{"node", "bun"} {
		if p, err := exec.LookPath(n); err == nil {
			return n, n + ":" + p
		}
	}
	return "none", ""
}
