// path_resources_test.go - PathResources dual-leg discipline (S5/H6).
package permissions

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestPathResourcesSymlinkKeepsBothLegs pins the POSIX rule: a symlinked
// path and its canonical target are textually distinct spellings, so both
// legs are kept and rules authored against either spelling still match.
// SameFile collapsing is Windows-only (8.3 short vs long names).
func TestPathResourcesSymlinkKeepsBothLegs(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX dual-leg: Windows collapses SameFile spellings by design")
	}
	dir := t.TempDir()
	real := filepath.Join(dir, "real")
	if err := os.Mkdir(real, 0755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(real, "secret.md")
	if err := os.WriteFile(target, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	legs := PathResources(filepath.Join(link, "secret.md"))
	if len(legs) != 2 {
		t.Fatalf("symlinked path legs = %v, want [canonical raw]", legs)
	}
	if legs[0] == legs[1] {
		t.Fatalf("legs collapsed on POSIX: %v", legs)
	}
}
