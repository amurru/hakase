// auth_test.go - Tier 4 auth slice: non-interactive `auth set-password`
// (first-run provisioning only; overwriting an existing login stays
// interactive behind the current-password challenge) and the unattended
// web boot provisioning from HAKASE_ADMIN_* env vars.
package cli

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"amurru/hakase/internal/auth"
	"amurru/hakase/internal/config"
)

// withStdin swaps os.Stdin for a file with the given content.
func withStdin(t *testing.T, content string) {
	t.Helper()
	f := filepath.Join(t.TempDir(), "stdin")
	if err := os.WriteFile(f, []byte(content), 0o600); err != nil {
		t.Fatalf("write stdin fixture: %v", err)
	}
	file, err := os.Open(f)
	if err != nil {
		t.Fatalf("open stdin fixture: %v", err)
	}
	old := os.Stdin
	os.Stdin = file
	t.Cleanup(func() {
		os.Stdin = old
		file.Close()
	})
}

func TestSetPasswordNonInteractiveCreates(t *testing.T) {
	isolateHome(t)
	credsPath := filepath.Join(config.HakaseHome(), "credentials.json")
	withStdin(t, "hunter2 secret\n")

	code := runSetPassword([]string{"--username", "admin", "--password-stdin"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	creds, err := auth.LoadCredentials(credsPath)
	if err != nil {
		t.Fatalf("credentials not created: %v", err)
	}
	if creds.Username != "admin" {
		t.Errorf("username = %q, want admin", creds.Username)
	}
	if !auth.VerifyPassword(creds, "hunter2 secret") {
		t.Error("stored password does not verify")
	}
	if runtime.GOOS != "windows" {
		// SaveCredentials pins 0600; Windows synthesizes 0666 for regular
		// files, so the pin is only observable on POSIX.
		info, err := os.Stat(credsPath)
		if err != nil {
			t.Fatalf("stat: %v", err)
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Errorf("credentials perms = %o, want 600", perm)
		}
	}
}

func TestSetPasswordNonInteractiveRefusesOverwrite(t *testing.T) {
	isolateHome(t)
	credsPath := filepath.Join(config.HakaseHome(), "credentials.json")
	if err := auth.SetPassword(credsPath, "admin", "original"); err != nil {
		t.Fatalf("seed credentials: %v", err)
	}
	withStdin(t, "newpass\n")

	code := runSetPassword([]string{"--username", "admin", "--password-stdin"})
	if code != 1 {
		t.Errorf("exit code = %d, want 1 (non-interactive must not overwrite)", code)
	}
	creds, err := auth.LoadCredentials(credsPath)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !auth.VerifyPassword(creds, "original") {
		t.Error("original password no longer verifies; the refusal must leave credentials untouched")
	}
}

func TestSetPasswordNonInteractiveRequiresUsername(t *testing.T) {
	isolateHome(t)
	withStdin(t, "x")
	if code := runSetPassword([]string{"--password-stdin"}); code != 2 {
		t.Errorf("exit code = %d, want 2 without --username", code)
	}
}

func TestSetPasswordNonInteractiveEmptyPassword(t *testing.T) {
	isolateHome(t)
	withStdin(t, "\n")
	if code := runSetPassword([]string{"--username", "admin", "--password-stdin"}); code != 2 {
		t.Errorf("exit code = %d, want 2 for an empty password", code)
	}
}

func TestSetPasswordInteractiveUsernameFlag(t *testing.T) {
	// --username alone keeps the interactive password flow (two prompts via
	// piped stdin, since the test harness is not a terminal).
	isolateHome(t)
	withStdin(t, "pass1\npass1\n")
	if code := runSetPassword([]string{"--username", "admin"}); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	creds, err := auth.LoadCredentials(filepath.Join(config.HakaseHome(), "credentials.json"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !auth.VerifyPassword(creds, "pass1") {
		t.Error("password does not verify")
	}
}

func TestProvisionAdminFromEnv(t *testing.T) {
	t.Run("no env no credentials: refused, caller keeps its path", func(t *testing.T) {
		isolateHome(t)
		provisioned, err := ProvisionAdminFromEnv(filepath.Join(config.HakaseHome(), "credentials.json"))
		if provisioned || err != nil {
			t.Errorf("provisioned=%v err=%v, want false/nil", provisioned, err)
		}
	})

	t.Run("both env vars provision first run", func(t *testing.T) {
		isolateHome(t)
		credsPath := filepath.Join(config.HakaseHome(), "credentials.json")
		t.Setenv(AdminUsernameEnv, "root")
		t.Setenv(AdminPasswordEnv, "boot-secret")
		provisioned, err := ProvisionAdminFromEnv(credsPath)
		if err != nil || !provisioned {
			t.Fatalf("provisioned=%v err=%v, want true/nil", provisioned, err)
		}
		creds, err := auth.LoadCredentials(credsPath)
		if err != nil || creds.Username != "root" || !auth.VerifyPassword(creds, "boot-secret") {
			t.Errorf("provisioned credentials wrong: %+v, %v", creds, err)
		}
	})

	t.Run("half-configured env is an error", func(t *testing.T) {
		isolateHome(t)
		t.Setenv(AdminUsernameEnv, "root")
		t.Setenv(AdminPasswordEnv, "")
		provisioned, err := ProvisionAdminFromEnv(filepath.Join(config.HakaseHome(), "credentials.json"))
		if provisioned || err == nil {
			t.Errorf("provisioned=%v err=%v, want false/error for exactly one var", provisioned, err)
		}
		if err != nil && !strings.Contains(err.Error(), AdminUsernameEnv) {
			t.Errorf("error %v should name the env vars", err)
		}
	})

	t.Run("existing credentials untouched", func(t *testing.T) {
		isolateHome(t)
		credsPath := filepath.Join(config.HakaseHome(), "credentials.json")
		if err := auth.SetPassword(credsPath, "admin", "original"); err != nil {
			t.Fatalf("seed: %v", err)
		}
		t.Setenv(AdminUsernameEnv, "root")
		t.Setenv(AdminPasswordEnv, "boot-secret")
		provisioned, err := ProvisionAdminFromEnv(credsPath)
		if provisioned || err != nil {
			t.Errorf("provisioned=%v err=%v, want false/nil when credentials exist", provisioned, err)
		}
		creds, _ := auth.LoadCredentials(credsPath)
		if !auth.VerifyPassword(creds, "original") {
			t.Error("existing credentials were modified by the provisioning path")
		}
	})
}
