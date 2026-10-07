//go:build windows

package player

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

func configureCmd(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
}

// resolveMpv prefers the real binary over the mpv.com console wrapper: the
// wrapper re-spawns mpv.exe, and killing the wrapper leaves the real player
// running headless. PATHEXT resolution finds .com first, so swap when the
// sibling exe exists.
func resolveMpv(path string) string {
	resolved, err := exec.LookPath(path)
	if err != nil || !strings.EqualFold(filepath.Ext(resolved), ".com") {
		return path
	}
	exe := resolved[:len(resolved)-len(filepath.Ext(resolved))] + ".exe"
	if _, err := os.Stat(exe); err != nil {
		return path
	}
	return exe
}

// killJob carries JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE: when the last handle
// closes — including when screech is killed by Task Manager or crashes —
// Windows terminates every process still assigned, so mpv can't be
// orphaned playing with no controller. The handle is deliberately never
// closed; the process exiting is what performs the cleanup.
var killJob = windows.Handle(0)

func init() {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return // non-fatal: worst case is today's behavior
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info))); err != nil {
		windows.CloseHandle(job)
		return
	}
	killJob = job
}

func afterStart(cmd *exec.Cmd) {
	if killJob == 0 {
		return
	}
	// cmd.Process.Handle lacks SET_QUOTA/TERMINATE, so re-open the child.
	h, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE,
		false, uint32(cmd.Process.Pid))
	if err != nil {
		return
	}
	defer windows.CloseHandle(h)
	_ = windows.AssignProcessToJobObject(killJob, h)
}
