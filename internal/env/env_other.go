//go:build !linux

// env_other.go - non-Linux stubs for the platform-specific environment
// helpers. The portable fields in env.go (GOOS/GOARCH, shell, locale,
// timezone, user/home, hostname, toolchains) still populate; the
// Linux-specific facts (kernel, distro, disk, memory) simply degrade to empty
// so darwin/windows builds compile and run without extra platform files.
package env

// kernelRelease is unsupported off Linux.
func kernelRelease() string { return "" }

// osRelease is unsupported off Linux.
func osRelease() (id, version, codename, pretty string) { return "", "", "", "" }

// diskFreeBytes is unsupported off Linux.
func diskFreeBytes(path string) uint64 { return 0 }

// memTotalBytes is unsupported off Linux.
func memTotalBytes() uint64 { return 0 }

// memAvailableBytes is unsupported off Linux.
func memAvailableBytes() uint64 { return 0 }
