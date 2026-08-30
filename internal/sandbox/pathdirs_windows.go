//go:build windows

package sandbox

// trustedExecDirs are host paths a system_exec command may reference without
// requiring an explicit read root. The Windows set covers only locations
// standard users cannot write to: C:\ProgramData is deliberately excluded
// (its default ACL lets ordinary users create files and subdirectories, so a
// planted executable would pass the path audit) - add a sandbox.read_roots
// entry explicitly if a ProgramData executable is genuinely required.
var trustedExecDirs = []string{
	`C:\Windows\System32`,
	`C:\Windows`,
	`C:\Program Files`,
	`C:\Program Files (x86)`,
}
