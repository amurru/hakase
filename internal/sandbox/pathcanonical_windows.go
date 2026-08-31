//go:build windows

package sandbox

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"golang.org/x/sys/windows"
)

// Windows path-alias canonicalization (WIN-005):
//
// Win32 path normalization resolves alias forms the Clean + EvalSymlinks
// string pipeline does not expand, so a path can string-match "allowed" while
// the OS opens a denied target. On Windows the string layer is the only
// enforcement (bwrap coerces to paths mode), and the deny roots protect
// config.json (provider API keys), credentials.json, and jwt-secret.
//
// Defense is two-layered:
//  1. Input rejection (string-only, cheap): trailing dots/spaces per
//     component (Win32 strips them and opens the base name), colons beyond
//     the drive specifier (NTFS alternate data streams), \\?\ and \\.\
//     prefixes (device/Win32 namespaces bypass normalization), and
//     drive-relative forms (C:foo resolves against the per-drive CWD).
//  2. Canonicalization: existing paths are opened with the caller's intended
//     access mode and resolved via GetFinalPathNameByHandle (folds junctions,
//     reparse points, 8.3 short names, and case in one step); not-yet-
//     existing paths canonicalize their deepest existing ancestor and append
//     the validated remainder.

// driveRelativeRe matches drive-relative paths like C:foo (but not C:\ or C:).
var driveRelativeRe = regexp.MustCompile(`^[A-Za-z]:[^\\/]`)

// envExpansionRe matches cmd.exe %VAR% environment-variable expansion and
// delayed-expansion !VAR! (cmd /V or a nested `cmd /V /C`) inside a command
// token. The audit sees the literal while cmd expands it to the OS path just
// before execution. Whitespace is allowed inside the delimiters because
// cmd.exe permits spaces in variable names (e.g. `!X Y!`); the previous
// `[^%\s]` form missed that case (CWE-22).
var envExpansionRe = regexp.MustCompile(`%[^%]+%|![^!]+!`)

// checkPathAlias returns an error naming the alias class when p is a Win32
// path alias that could bypass string-based confinement checks.
func checkPathAlias(p string) error {
	if p == "" {
		return nil
	}
	// Device / Win32 namespaces bypass normalization entirely.
	if strings.HasPrefix(p, `\\?\`) {
		return fmt.Errorf("device-namespace path (\\\\?\\ prefix) is not allowed")
	}
	if strings.HasPrefix(p, `\\.\`) {
		return fmt.Errorf("device-namespace path (\\\\.\\ prefix) is not allowed")
	}
	// Drive-relative form: the OS resolves C:foo against the per-drive CWD,
	// not the base the audit assumes.
	if driveRelativeRe.MatchString(p) {
		return fmt.Errorf("drive-relative path (C:name) is not allowed; use an absolute path")
	}
	// Per-component checks: trailing dots/spaces and stray colons.
	comps := splitPathComponents(p)
	for i, comp := range comps {
		if i == 0 && len(comp) == 2 && comp[1] == ':' {
			continue // drive component "C:"
		}
		// NTFS alternate data stream ("config.json:ads"). The path-like
		// qualifier (a dot in the component) keeps regex-style arguments
		// such as "foo:bar" working while still catching stream syntax
		// on file operands.
		if strings.ContainsRune(comp, ':') && strings.ContainsRune(comp, '.') {
			return fmt.Errorf("path contains an extra colon (NTFS alternate data stream), which is not allowed")
		}
		if comp != "." && comp != ".." && len(comp) > 0 &&
			(strings.HasSuffix(comp, ".") || strings.HasSuffix(comp, " ")) {
			return fmt.Errorf("path component %q ends with a trailing dot or space, which Win32 strips before opening the file", comp)
		}
	}
	return nil
}

// checkShellExpansionAlias rejects command tokens carrying cmd.exe
// environment-variable expansion (%VAR%) or delayed expansion (!VAR!). The
// audit would treat "%USERPROFILE%\.hakase\credentials.json" as a
// workspace-relative literal while cmd expands it to the denied absolute
// path just before execution - so sandbox mode refuses such tokens outright.
func checkShellExpansionAlias(tok string) error {
	if envExpansionRe.MatchString(tok) {
		return fmt.Errorf("token contains %%VAR%% or !VAR! shell expansion, which the path audit cannot resolve; use a literal path (add sandbox.read_roots entries for locations outside the workspace)")
	}
	return nil
}

// splitPathComponents splits p on both separators, dropping empty parts.
func splitPathComponents(p string) []string {
	return strings.FieldsFunc(p, func(r rune) bool {
		return r == '\\' || r == '/'
	})
}

// canonicalizePath resolves p to its final on-disk form using
// GetFinalPathNameByHandle. write selects the access mode used to open an
// existing file target (a write probe must not be laundered through a
// read-only open); directory targets and unreadable files fall back to an
// attributes-only open, which still resolves reparse points. For
// not-yet-existing paths the deepest existing ancestor is canonicalized and
// the validated remainder appended. Best-effort: when nothing can be opened
// the original string form is returned with the error.
func canonicalizePath(p string, write bool) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return p, err
	}
	abs = filepath.Clean(strings.TrimPrefix(abs, `\\?\`))

	// Split into the deepest existing ancestor plus the missing remainder.
	suffix := []string(nil)
	probe := abs
	for {
		if _, err := os.Stat(probe); err == nil {
			break
		}
		dir, base := filepath.Split(probe)
		if dir == "" || filepath.Clean(dir) == probe {
			break // reached the drive root
		}
		suffix = append([]string{base}, suffix...)
		probe = filepath.Clean(dir)
	}
	// Every appended component must itself be alias-free.
	for _, comp := range suffix {
		if err := checkPathAlias(comp); err != nil {
			return p, fmt.Errorf("cannot canonicalize %q: %w", p, err)
		}
	}

	final, err := finalPathByHandle(probe, write)
	if err != nil {
		return p, err
	}
	if len(suffix) == 0 {
		return final, nil
	}
	return filepath.Join(append([]string{final}, suffix...)...), nil
}

// finalPathByHandle opens path and returns its final form. Access mode:
// write targets files open with GENERIC_WRITE; directories (and anything the
// intended mode cannot open) use an attributes-only open, which needs no
// data access but still resolves junctions/reparse points for
// GetFinalPathNameByHandle.
func finalPathByHandle(path string, write bool) (string, error) {
	open := func(access uint32) (windows.Handle, error) {
		return windows.CreateFile(
			windows.StringToUTF16Ptr(path),
			access,
			windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
			nil,
			windows.OPEN_EXISTING,
			windows.FILE_FLAG_BACKUP_SEMANTICS,
			0,
		)
	}

	var h windows.Handle
	var err error
	if write {
		h, err = open(windows.GENERIC_WRITE)
	}
	if h == 0 {
		// Attributes-only fallback: directories, ACL-blocked files, and
		// read probes all resolve identically through it.
		h, err = open(windows.FILE_READ_ATTRIBUTES)
		if err != nil {
			return "", err
		}
	}
	defer windows.CloseHandle(h)

	for size := uint32(1024); ; size *= 2 {
		buf := make([]uint16, size)
		n, err := windows.GetFinalPathNameByHandle(h, &buf[0], size, 0)
		if err != nil {
			return "", err
		}
		if n < uint32(len(buf)) {
			// VOLUME_NAME_DOS returns a \\?\-prefixed path; the string
			// enforcement layer compares plain paths.
			return strings.TrimPrefix(windows.UTF16ToString(buf[:n]), `\\?\`), nil
		}
	}
}
