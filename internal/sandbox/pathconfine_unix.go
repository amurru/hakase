//go:build unix

package sandbox

// checkPathAlias rejects Win32 path-alias escapes (trailing dots/spaces,
// NTFS alternate data streams, device namespaces, drive-relative forms).
// No-op on Unix: those aliases do not exist there and the Linux test
// expectations stay byte-identical.
func checkPathAlias(string) error { return nil }

// checkShellExpansionAlias rejects command tokens that a Windows shell would
// expand (%VAR%, delayed !VAR!) after the audit ran. No-op on Unix.
func checkShellExpansionAlias(string) error { return nil }

// canonicalizePath resolves p to its final on-disk form via an open handle.
// Identity on Unix: symlink resolution already happens in the callers
// (EvalSymlinks + securejoin).
func canonicalizePath(p string, _ bool) (string, error) {
	return p, nil
}
