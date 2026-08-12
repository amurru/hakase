// web_test.go - tests for the web/serve bootstrap flag and config plumbing
// added by Task 1 of the security-hardening plan (--insecure-cookie).
package main

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"amurru/hakase/internal/config"
)

// TestInsecureCookieFlag asserts the three plumbing behaviors: (a) go.mod
// lists golang.org/x/time as a direct require, (b) the --insecure-cookie flag
// parses to a boolean, (c) the auth.allow_insecure_cookie config key loads
// with the correct default (false).
func TestInsecureCookieFlag(t *testing.T) {
	// (a) go.mod must carry x/time as a direct dependency (promoted from
	// go.sum-only). Tests run with the package dir as the working directory,
	// so go.mod lives two levels up.
	mod, err := os.ReadFile(filepath.Join("..", "..", "go.mod"))
	if err != nil {
		t.Fatalf("read go.mod: %v", err)
	}
	if !strings.Contains(string(mod), "\tgolang.org/x/time v0.15.0") {
		t.Errorf("go.mod must contain direct require line %q", "\tgolang.org/x/time v0.15.0")
	}

	// (b) the flag parses to a boolean. Bare --insecure-cookie means true.
	fs := flag.NewFlagSet("web", flag.ContinueOnError)
	_, _, insecureCookie := registerWebFlags(fs)
	if err := fs.Parse([]string{"--insecure-cookie"}); err != nil {
		t.Fatalf("parse --insecure-cookie: %v", err)
	}
	if !insecureCookie.set || insecureCookie.value != true {
		t.Errorf("--insecure-cookie must parse to set=true value=true, got set=%v value=%v", insecureCookie.set, insecureCookie.value)
	}

	// Explicit false is distinguishable from an absent flag.
	fs = flag.NewFlagSet("web", flag.ContinueOnError)
	_, _, insecureCookie = registerWebFlags(fs)
	if err := fs.Parse([]string{"--insecure-cookie=false"}); err != nil {
		t.Fatalf("parse --insecure-cookie=false: %v", err)
	}
	if !insecureCookie.set || insecureCookie.value != false {
		t.Errorf("--insecure-cookie=false must parse to set=true value=false, got set=%v value=%v", insecureCookie.set, insecureCookie.value)
	}

	// Absent flag: unset, so the config file value stands.
	fs = flag.NewFlagSet("web", flag.ContinueOnError)
	_, _, insecureCookie = registerWebFlags(fs)
	if err := fs.Parse(nil); err != nil {
		t.Fatalf("parse empty args: %v", err)
	}
	if insecureCookie.set {
		t.Errorf("absent --insecure-cookie must leave the flag unset, got set=%v value=%v", insecureCookie.set, insecureCookie.value)
	}

	// (c) the config key loads with the correct default (false).
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"provider":"gemini"}`), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Auth.AllowInsecureCookie {
		t.Error("allow_insecure_cookie must default to false when absent")
	}
}

// TestInsecureCookiePrecedence asserts CLI flag > config file > default
// (false): an explicitly-set --insecure-cookie=false must override a config
// file that sets allow_insecure_cookie: true.
func TestInsecureCookiePrecedence(t *testing.T) {
	// Config file: allow_insecure_cookie = true.
	path := filepath.Join(t.TempDir(), "config.json")
	body := `{"provider":"gemini","auth":{"allow_insecure_cookie":true}}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if !cfg.Auth.AllowInsecureCookie {
		t.Fatal("config value must load as true")
	}

	// CLI flag explicitly false overrides the config value.
	fs := flag.NewFlagSet("web", flag.ContinueOnError)
	_, _, insecureCookie := registerWebFlags(fs)
	if err := fs.Parse([]string{"--insecure-cookie=false"}); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if resolved := applyInsecureCookiePrecedence(cfg.Auth.AllowInsecureCookie, insecureCookie.set, insecureCookie.value); resolved {
		t.Error("CLI flag false must override config true; resolved true")
	}

	// CLI flag absent: config value stands.
	fs = flag.NewFlagSet("web", flag.ContinueOnError)
	_, _, insecureCookie = registerWebFlags(fs)
	if err := fs.Parse(nil); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if resolved := applyInsecureCookiePrecedence(cfg.Auth.AllowInsecureCookie, insecureCookie.set, insecureCookie.value); !resolved {
		t.Error("absent flag must defer to config true; resolved false")
	}

	// Neither flag nor config: default false.
	zeroCfg := config.Config{}
	fs = flag.NewFlagSet("web", flag.ContinueOnError)
	_, _, insecureCookie = registerWebFlags(fs)
	if err := fs.Parse(nil); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if resolved := applyInsecureCookiePrecedence(zeroCfg.Auth.AllowInsecureCookie, insecureCookie.set, insecureCookie.value); resolved {
		t.Error("default must be false when nothing sets it")
	}
}
