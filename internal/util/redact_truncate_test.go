// redact_truncate_test.go - S10/S12/S13 unified redact-then-truncate helper.
package util

import (
	"strings"
	"testing"
)

// Redaction must run BEFORE truncation: a secret straddling the cut
// point must not survive it.
func TestRedactTruncate_SecretAcrossCut(t *testing.T) {
	secret := "token=supersecret123456"
	pad := strings.Repeat("x", 500)
	in := pad + " " + secret + " " + pad
	out := RedactTruncate(in, 512)
	if strings.Contains(out, "supersecret123456") {
		t.Errorf("secret survived redact-then-truncate: %q", out)
	}
	if !strings.Contains(out, "REDACTED") {
		t.Errorf("no redaction token in %q", out)
	}
	if n := len([]rune(out)); n > 512 {
		t.Errorf("output %d runes, want <= 512", n)
	}
}

// Clean text keeps the telegram-style … truncation.
func TestRedactTruncate_CleanTruncates(t *testing.T) {
	in := strings.Repeat("a", 100)
	out := RedactTruncate(in, 10)
	if out != strings.Repeat("a", 9)+"…" {
		t.Errorf("got %q, want 9 a's + ellipsis", out)
	}
}

// Short text passes through byte-identical; n <= 0 disables truncation.
func TestRedactTruncate_Passthrough(t *testing.T) {
	if got := RedactTruncate("echo hi", 512); got != "echo hi" {
		t.Errorf("clean text changed: %q", got)
	}
	long := "token=supersecret123456"
	red, _ := RedactSecrets(long)
	if got := RedactTruncate(long, 0); got != red {
		t.Errorf("n=0 must redact without truncating: %q", got)
	}
	if got := RedactTruncate(long, -1); got != red {
		t.Errorf("n<0 must redact without truncating: %q", got)
	}
}

// Token-shape guard: the bearer/basic scheme with a common word is prose,
// not a credential; a shaped token still redacts.
func TestRedactSecrets_SchemeShapeGuard(t *testing.T) {
	if out, found := RedactSecrets("use bearer information wisely"); found || out != "use bearer information wisely" {
		t.Errorf("prose redacted: found=%v out=%q", found, out)
	}
	if out, found := RedactSecrets("Bearer abcdefgh12345678"); !found || !strings.Contains(out, "REDACTED:bearer") {
		t.Errorf("shaped bearer missed: found=%v out=%q", found, out)
	}
	if out, found := RedactSecrets("Basic dXNlcjpwYXNz"); !found || !strings.Contains(out, "REDACTED:basic") {
		t.Errorf("shaped basic missed: found=%v out=%q", found, out)
	}
	// H3 loop 4: unicode whitespace splits; long opaque tokens redact even
	// all-lowercase; short lowercase prose never redacts.
	if out, found := RedactSecrets("Bearer\tabcdefghijklmnop"); !found || !strings.Contains(out, "REDACTED:bearer") {
		t.Errorf("tab-split bearer missed: found=%v out=%q", found, out)
	}
	if out, found := RedactSecrets("Bearer abcdefghijklmnop"); !found || !strings.Contains(out, "REDACTED:bearer") {
		t.Errorf("long lowercase bearer missed: found=%v out=%q", found, out)
	}
	if out, found := RedactSecrets("bearer info"); found || out != "bearer info" {
		t.Errorf("short lowercase prose redacted: found=%v out=%q", found, out)
	}
}
