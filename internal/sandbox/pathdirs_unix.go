//go:build unix

package sandbox

// trustedExecDirs are host paths a system_exec command may reference without
// requiring an explicit read root. They mirror the read-only system bindings
// bubblewrap provides (sandboxexec.go: systemROBindDirs) plus the minimal
// virtual/scratch filesystems bwrap mounts (/proc, /dev, /tmp, /run) and
// /sys, which common diagnostic commands read. Everything else must live
// under a sandbox read root.
var trustedExecDirs = []string{
	"/usr", "/lib", "/lib64", "/bin", "/sbin", "/etc", "/nix",
	"/proc", "/dev", "/sys", "/tmp", "/run",
}
