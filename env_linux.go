//go:build linux

// env_linux.go - Linux-specific environment detection used by env.go:
// kernel release (syscall.Uname), distro identity (/etc/os-release), disk free
// (syscall.Statfs) and total memory (/proc/meminfo). All functions degrade to
// empty/zero on failure and are never fatal.
package main

import (
	"os"
	"strconv"
	"strings"
	"syscall"
)

// kernelRelease returns the kernel release string (uname -r), e.g.
// "6.18.39-1-lts". Returns "" when unavailable.
func kernelRelease() string {
	var u syscall.Utsname
	if err := syscall.Uname(&u); err != nil {
		return ""
	}
	// u.Release is a fixed [65]int8 array; copy until the NUL terminator.
	rel := make([]byte, 0, len(u.Release))
	for _, c := range u.Release {
		if c == 0 {
			break
		}
		rel = append(rel, byte(c))
	}
	return string(rel)
}

// osRelease reads /etc/os-release and returns (ID, VERSION_ID,
// VERSION_CODENAME, PRETTY_NAME). Returns empty values when the file is
// missing or unreadable (container images, unusual setups).
func osRelease() (id, version, codename, pretty string) {
	data, err := os.ReadFile("/etc/os-release")
	if err != nil {
		return "", "", "", ""
	}
	return parseOSReleaseContent(string(data))
}

// parseOSReleaseContent parses /etc/os-release KEY=VALUE content. Values may
// be single- or double-quoted; `#` lines are comments. Only the four fields
// used by env.go are extracted.
func parseOSReleaseContent(data string) (id, version, codename, pretty string) {
	for _, line := range strings.Split(data, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		val = strings.Trim(strings.TrimSpace(val), `"'`)
		switch strings.TrimSpace(key) {
		case "ID":
			id = val
		case "VERSION_ID":
			version = val
		case "VERSION_CODENAME":
			codename = val
		case "PRETTY_NAME":
			pretty = val
		}
	}
	return id, version, codename, pretty
}

// diskFreeBytes returns the free space (in bytes) available to the calling
// user on the filesystem containing path. Returns 0 when statfs fails.
func diskFreeBytes(path string) uint64 {
	if path == "" {
		return 0
	}
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0
	}
	return st.Bavail * uint64(st.Bsize)
}

// memTotalBytes returns total physical memory in bytes, from /proc/meminfo.
// Returns 0 when unavailable.
func memTotalBytes() uint64 {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "MemTotal:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) >= 2 {
			if kb, err := strconv.ParseUint(fields[1], 10, 64); err == nil {
				return kb * 1024
			}
		}
	}
	return 0
}

// memAvailableBytes returns currently available memory in bytes, from
// /proc/meminfo (MemAvailable, falling back to MemFree). Used by the
// environment-staleness notice. Returns 0 when unavailable.
func memAvailableBytes() uint64 {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "MemAvailable:") && !strings.HasPrefix(line, "MemFree:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) >= 2 {
			if kb, err := strconv.ParseUint(fields[1], 10, 64); err == nil {
				return kb * 1024
			}
		}
	}
	return 0
}
