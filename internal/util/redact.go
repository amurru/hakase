// redact.go - secret-shaped string redaction for anything that leaves the
// process toward an LLM provider or lands in a persisted artifact
// (tasks.json, evidence.jsonl, reports, diagnostics).
//
// This is the canonical engine (plan SL-004). It is deliberately dependency
// free (stdlib only) so both internal/skill (mutator prompts) and
// internal/sleep (harvest/mine/replay/staging) share one implementation with
// no import cycle. The sleep-facing API (WrapSleepExcerpt, UnredactedAllowed)
// lives in internal/sleep and forwards here.
//
// Defense in depth, not a guarantee: run redaction BEFORE truncation and
// BEFORE any provider call, and again at staging/report writers. Patterns
// below are the floor, not the ceiling.
package util

import (
	"regexp"
)

// redactionRule pairs a secret-shaped pattern with its replacement token.
// Specific shapes come first; the generic credential-assignment rule runs
// last so typed tokens win (e.g. a GitHub PAT after "token=" keeps its
// github-token label).
var redactionRules = []struct {
	name  string
	re    *regexp.Regexp
	token string
}{
	{"aws-key", regexp.MustCompile(`\b(?:AKIA|ASIA|ABIA|ACCA)[0-9A-Z]{16}`), "[REDACTED:aws-key]"},
	{"github-token", regexp.MustCompile(`(?:gh[pousr]_|github_pat_)[A-Za-z0-9_]{20,}`), "[REDACTED:github-token]"},
	{"openai-key", regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{20,}`), "[REDACTED:openai-key]"},
	{"slack-token", regexp.MustCompile(`\bxox[abceoprs]-[A-Za-z0-9-]+`), "[REDACTED:slack-token]"},
	{"bearer", regexp.MustCompile(`(?i)\bbearer\s+[A-Za-z0-9\-._~+/=]{10,}`), "[REDACTED:bearer]"},
	// Full PEM block first (RE2 non-greedy is linear-time), header-only as
	// fallback for truncated excerpts (audit B1: body alone matches nothing).
	{"private-key", regexp.MustCompile(`(?s)-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----.*?-----END [A-Z0-9 ]*PRIVATE KEY-----`), "[REDACTED:private-key]"},
	{"private-key-header", regexp.MustCompile(`-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----`), "[REDACTED:private-key]"},
	{"jwt", regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}`), "[REDACTED:jwt]"},
	// SCREAMING_SNAKE assignments (audit B2): DATABASE_PASSWORD=...,
	// AWS_SECRET_ACCESS_KEY=... - the bare-name rule below cannot see these
	// because `_` is a word char, so no boundary exists before PASSWORD/KEY.
	{"env-credential", regexp.MustCompile(`(?i)(?:^|[\s;"',])([A-Z0-9_]*?(?:KEY|SECRET|TOKEN|PASSWORD|PASSWD)\s*=\s*\S{8,})`), "[REDACTED:credential]"},
	// Generic credential assignment: `name = value`, `"name": "value"`
	// (JSON), `name:value`. The name boundary excludes `_`-prefixed names
	// (covered above); the optional quote covers JSON keys (audit B2).
	{"credential", regexp.MustCompile(`(?i)(?:^|[^A-Za-z0-9_])((?:api[_-]?key|api[_-]?secret|secret[_-]?key|auth[_-]?token|access[_-]?token|client[_-]?secret|secret|token|password|passwd|pwd))["']?\s*[:=]\s*['"]?\S{8,}`), "[REDACTED:credential]"},
}

// RedactSecrets replaces secret-shaped substrings with typed
// [REDACTED:<kind>] tokens. It returns the redacted string and whether any
// rule fired. The same rule set doubles as the test oracle: fixtures must
// contain zero matches of any rule after redaction.
func RedactSecrets(s string) (string, bool) {
	redacted := false
	for _, r := range redactionRules {
		if r.re.MatchString(s) {
			s = r.re.ReplaceAllString(s, r.token)
			redacted = true
		}
	}
	return s, redacted
}

// RedactionRuleCount reports how many redaction rules are active. Tests use
// it to assert the oracle covers every rule (a rule added without a fixture
// fails the coverage test).
func RedactionRuleCount() int {
	return len(redactionRules)
}

// RedactedPatterns returns one synthetic secret per rule for oracle tests.
// Each sample must redact to a string containing no rule match.
func RedactedPatterns() []string {
	return []string{
		"AKIAIOSFODNN7EXAMPLE",
		"ASIAIOSFODNN7EXAMPLE",
		"ghp_abcdefghijklmnopqrstuvwxyz0123456789ABCD",
		"sk-ant-anthropic-test-key-0123456789abcdef",
		"xoxb-12345-67890-abcdefghijklmnopqrstuv",
		"xoxe-1-23456-abcdef",
		"Bearer abcdefghij1234567890",
		"-----BEGIN RSA PRIVATE KEY-----\nMIIEpAIBAAKCAQEA7bq cor rupted body line\n-----END RSA PRIVATE KEY-----",
		"-----BEGIN RSA PRIVATE KEY----- (truncated excerpt, no footer)",
		"eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.SflKxwRJSMeKKF2QT4fwpMeJf36POk6yJVadQssw5c",
		"DATABASE_PASSWORD=supersecret123",
		`{"user": "bot", "password": "hunter2hunter12"}`,
		"token=supersecret123456",
		"AWS_SECRET_ACCESS_KEY=wJalrXUtnFEMI/K7MDENG/bPxRfiCY12345678",
		"api_key=supersecretvalue123",
	}
}
