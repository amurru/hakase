//go:build windows

package sandbox

import (
	"fmt"
	"os/exec"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Windows process-tree management uses Job Objects: every spawned command is
// assigned to its own job object created with JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE,
// so killing the tree is TerminateJobObject and an agent crash (which closes
// the handle) terminates everything assigned, mirroring Pdeathsig semantics.
//
// Known v1 limitation: a grandchild spawned by the child between Start() and
// AssignProcessToJobObject escapes the job. TerminateJobObject still kills
// everything assigned after the assignment completes.

// cmdJobs maps a started command to its per-process job handle. Guarded by
// jobMu because the background-process registry is polled and killed from
// independent tool handlers.
var (
	jobMu   sync.Mutex
	cmdJobs = map[*exec.Cmd]windows.Handle{}
)

// configureProcess applies platform process hardening to a command before
// Start. Windows has no process groups or parent-death signals; tree tracking
// is completed after Start by attachProcessTree.
func configureProcess(*exec.Cmd) {}

// attachProcessTree assigns the freshly started process to a new kill-on-close
// job object so a later killProcessTree reaps the whole tree and an agent
// crash terminates everything assigned. Best-effort: an error is returned (and
// killProcessTree falls back to killing only the direct child) but the caller
// does not need to abort the run.
func attachProcessTree(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	h, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return fmt.Errorf("CreateJobObject: %w", err)
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
			LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE,
		},
	}
	if ret, err := windows.SetInformationJobObject(
		h,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	); err != nil || ret == 0 {
		windows.CloseHandle(h)
		return fmt.Errorf("SetInformationJobObject: %w", err)
	}
	ph, err := windows.OpenProcess(
		windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE,
		false,
		uint32(cmd.Process.Pid),
	)
	if err != nil {
		windows.CloseHandle(h)
		return fmt.Errorf("OpenProcess(%d): %w", cmd.Process.Pid, err)
	}
	defer windows.CloseHandle(ph)
	if err := windows.AssignProcessToJobObject(h, ph); err != nil {
		windows.CloseHandle(h)
		return fmt.Errorf("AssignProcessToJobObject: %w", err)
	}
	jobMu.Lock()
	cmdJobs[cmd] = h
	jobMu.Unlock()
	return nil
}

// killProcessTree terminates the process and all of its assigned descendants
// via TerminateJobObject. Without a job assignment (attach failed or never
// ran) it falls back to killing just the direct child.
func killProcessTree(cmd *exec.Cmd) error {
	if cmd == nil {
		return nil
	}
	jobMu.Lock()
	h, ok := cmdJobs[cmd]
	delete(cmdJobs, cmd)
	jobMu.Unlock()
	if ok {
		err := windows.TerminateJobObject(h, 1)
		windows.CloseHandle(h)
		return err
	}
	if cmd.Process != nil {
		return cmd.Process.Kill()
	}
	return nil
}

// releaseProcessTree releases the per-process job handle after Wait. Safe to
// call for commands that were never attached, and after killProcessTree.
func releaseProcessTree(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	jobMu.Lock()
	h, ok := cmdJobs[cmd]
	delete(cmdJobs, cmd)
	jobMu.Unlock()
	if ok {
		windows.CloseHandle(h)
	}
}

// shellRoutingNote is appended to system_exec tool descriptions so the model
// knows the Windows shell semantics.
const shellRoutingNote = " On Windows the whole command line is run via cmd /D /C: POSIX-only constructs (globs, $(), backticks, VAR=x cmd) are NOT interpreted - use cmd syntax (%VAR% expansion, &&, |, >). Bare executable names resolve from PATH only, never the working directory."
