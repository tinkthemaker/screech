//go:build windows

package player

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

func configureCmd(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
}

// ResolveMpv prefers the real binary over the mpv.com console wrapper: the
// wrapper re-spawns mpv.exe, and killing the wrapper leaves the real player
// running headless. PATHEXT resolution finds .com first, so swap when the
// sibling exe exists.
func ResolveMpv(path string) string {
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

// newKillJob makes a job object that terminates every process assigned to it
// when its last handle closes.
func newKillJob() (windows.Handle, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0, err
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info))); err != nil {
		windows.CloseHandle(job)
		return 0, err
	}
	return job, nil
}

// assignToJob puts the running process pid into job, reporting whether it
// took. cmd.Process.Handle lacks SET_QUOTA/TERMINATE, so the child is
// re-opened by pid.
func assignToJob(job windows.Handle, pid int) bool {
	h, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE,
		false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)
	return windows.AssignProcessToJobObject(job, h) == nil
}

func init() {
	if job, err := newKillJob(); err == nil {
		killJob = job
	} // non-fatal: worst case is today's behavior
}

func afterStart(cmd *exec.Cmd) {
	if killJob == 0 {
		return
	}
	assignToJob(killJob, cmd.Process.Pid)
}

// adoptChild is AdoptChild on Windows: a job object of the child's own, so
// that closing it ends that child's whole tree and nothing else. If the job
// can't be made or the child can't be put in it (it already exited, or the
// system refuses), the release has nothing to close and the caller is left
// with plain process kills, as before.
func adoptChild(cmd *exec.Cmd) (release func()) {
	none := func() {}
	job, err := newKillJob()
	if err != nil {
		return none
	}
	if !assignToJob(job, cmd.Process.Pid) {
		windows.CloseHandle(job)
		return none
	}
	var once sync.Once
	return func() { once.Do(func() { windows.CloseHandle(job) }) }
}
