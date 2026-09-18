// redact_test.go - oracle tests for RedactSecrets (plan SL-004).
package util

import (
	"strings"
	"testing"
)

// TestRedactSecrets_Oracle asserts every synthetic sample redacts to a
// string that matches NO rule (the rule set is its own oracle), and that
// every rule fires on at least one sample so a new rule without a fixture
// fails loudly. Multiple fixtures per rule are welcome.
func TestRedactSecrets_Oracle(t *testing.T) {
	samples := RedactedPatterns()
	if len(samples) < RedactionRuleCount() {
		t.Fatalf("RedactedPatterns() has %d samples for %d rules: need >= 1 fixture per rule",
			len(samples), RedactionRuleCount())
	}
	fired := make([]bool, len(redactionRules))
	for i, s := range samples {
		out, found := RedactSecrets(s)
		if !found {
			t.Errorf("sample %d (%q) did not trigger redaction", i, s)
		}
		for ri, r := range redactionRules {
			if r.re.MatchString(s) {
				fired[ri] = true
			}
			if r.re.MatchString(out) {
				t.Errorf("sample %d still matches rule %q after redaction: %q", i, r.name, out)
			}
		}
		if strings.Contains(out, "[REDACTED:") == false {
			t.Errorf("sample %d has no redaction token: %q", i, out)
		}
	}
	for ri, r := range redactionRules {
		if !fired[ri] {
			t.Errorf("rule %q fires on no fixture: add a sample", r.name)
		}
	}
}

// TestRedactSecrets_MixedContent redacts secrets embedded in prose while
// leaving surrounding text intact.
func TestRedactSecrets_MixedContent(t *testing.T) {
	in := "deploy with GITHUB_PAT=ghp_abcdefghijklmnopqrstuvwxyz0123456789ABCD then run make test"
	out, found := RedactSecrets(in)
	if !found {
		t.Fatal("expected redaction to fire")
	}
	if strings.Contains(out, "ghp_") {
		t.Errorf("secret survived: %q", out)
	}
	if !strings.Contains(out, "then run make test") {
		t.Errorf("surrounding text damaged: %q", out)
	}
}

// TestRedactSecrets_CleanPassthrough leaves ordinary text byte-identical.
func TestRedactSecrets_CleanPassthrough(t *testing.T) {
	in := "the quick brown fox jumps over 13 lazy dogs; password policy reviewed"
	if out, found := RedactSecrets(in); found || out != in {
		t.Errorf("clean text altered: found=%v out=%q", found, out)
	}
}
