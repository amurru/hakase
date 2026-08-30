package sandbox

import "os/exec"

// Exported wrappers around the platform-specific process-lifecycle helpers
// (procattr_unix.go / procattr_windows.go) for packages that spawn hardened
// subprocesses but do not live in this package (internal/agent).

// ConfigureProcess applies platform process hardening to cmd before Start:
// process group + parent-death signal on Unix, nothing on Windows (the Job
// Object is assigned after Start by AttachProcessTree).
func ConfigureProcess(cmd *exec.Cmd) { configureProcess(cmd) }

// AttachProcessTree completes tree tracking after a successful Start
// (no-op on Unix; Job Object assignment on Windows). Best-effort: callers
// log the error and continue.
func AttachProcessTree(cmd *exec.Cmd) error { return attachProcessTree(cmd) }

// KillProcessTree terminates the process and all of its descendants.
func KillProcessTree(cmd *exec.Cmd) error { return killProcessTree(cmd) }

// ReleaseProcessTree releases per-process tracking resources after Wait.
func ReleaseProcessTree(cmd *exec.Cmd) { releaseProcessTree(cmd) }
