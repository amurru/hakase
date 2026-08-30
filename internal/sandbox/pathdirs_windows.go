//go:build windows

package sandbox

// trustedExecDirs are host paths a system_exec command may reference without
// requiring an explicit read root. The Windows set covers the
// machine-managed system and program locations the Unix list mirrors;
// user-writable locations (PATH entries under %LOCALAPPDATA%, scoop, etc.)
// are NOT trusted - add them to sandbox.read_roots explicitly.
var trustedExecDirs = []string{
	`C:\Windows\System32`,
	`C:\Windows`,
	`C:\Program Files`,
	`C:\Program Files (x86)`,
	`C:\ProgramData`,
}
