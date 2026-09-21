package auth

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestSetAndVerifyPassword(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "credentials.json")

	// SetPassword hashes and saves.
	err := SetPassword(path, "admin", "correct-horse-battery-staple")
	if err != nil {
		t.Fatalf("SetPassword failed: %v", err)
	}

	// Load back and verify with correct password.
	creds, err := LoadCredentials(path)
	if err != nil {
		t.Fatalf("LoadCredentials failed: %v", err)
	}
	if creds.Username != "admin" {
		t.Errorf("expected username 'admin', got %q", creds.Username)
	}
	if !VerifyPassword(creds, "correct-horse-battery-staple") {
		t.Error("VerifyPassword should return true for correct password")
	}
}

func TestVerifyPasswordWrong(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "credentials.json")

	err := SetPassword(path, "admin", "correct-horse-battery-staple")
	if err != nil {
		t.Fatalf("SetPassword failed: %v", err)
	}

	creds, err := LoadCredentials(path)
	if err != nil {
		t.Fatalf("LoadCredentials failed: %v", err)
	}

	if VerifyPassword(creds, "wrong-password") {
		t.Error("VerifyPassword should return false for incorrect password")
	}
	if VerifyPassword(creds, "") {
		t.Error("VerifyPassword should return false for empty password")
	}
}

func TestCredentialsFilePermissions(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "credentials.json")

	err := SetPassword(path, "admin", "secure-password")
	if err != nil {
		t.Fatalf("SetPassword failed: %v", err)
	}

	// Also test direct SaveCredentials via LoadCredentials re-save.
	creds, err := LoadCredentials(path)
	if err != nil {
		t.Fatalf("LoadCredentials failed: %v", err)
	}

	repath := filepath.Join(tmp, "credentials2.json")
	err = SaveCredentials(repath, creds)
	if err != nil {
		t.Fatalf("SaveCredentials failed: %v", err)
	}

	// Check permissions with stat. Windows has no POSIX mode bits (Stat
	// always reports 0666), so the assertions are POSIX-only.
	if runtime.GOOS != "windows" {
		info, err := os.Stat(repath)
		if err != nil {
			t.Fatalf("stat failed: %v", err)
		}
		if perm := info.Mode().Perm(); perm != 0600 {
			t.Errorf("expected 0600 permissions, got %04o", perm)
		}

		// Also verify the SetPassword-created file has 0600.
		info, err = os.Stat(path)
		if err != nil {
			t.Fatalf("stat failed: %v", err)
		}
		if perm := info.Mode().Perm(); perm != 0600 {
			t.Errorf("SetPassword file expected 0600 permissions, got %04o", perm)
		}
	}
}

func TestLoadCredentialsMissingFile(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "nonexistent.json")

	_, err := LoadCredentials(path)
	if err == nil {
		t.Error("LoadCredentials should return error for missing file")
	}
}

func TestGenerateOrLoadSecret(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "jwt-secret")

	// First call generates a new secret.
	secret, err := GenerateOrLoadSecret(path)
	if err != nil {
		t.Fatalf("GenerateOrLoadSecret failed: %v", err)
	}
	if len(secret) != 32 {
		t.Errorf("expected 32-byte secret, got %d bytes", len(secret))
	}

	// Second call loads the same secret.
	secret2, err := GenerateOrLoadSecret(path)
	if err != nil {
		t.Fatalf("second GenerateOrLoadSecret failed: %v", err)
	}
	if len(secret2) != 32 {
		t.Errorf("expected 32-byte secret on second call, got %d bytes", len(secret2))
	}

	// Verify the two secrets are byte-identical.
	for i := range secret {
		if secret[i] != secret2[i] {
			t.Fatalf("secret mismatch at byte %d: first=%d, second=%d", i, secret[i], secret2[i])
		}
	}
}
