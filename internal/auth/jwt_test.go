package auth

import (
	"testing"
	"time"
)

func TestJWTGenerateValidate(t *testing.T) {
	secret := make([]byte, 32)
	for i := range secret {
		secret[i] = byte(i + 1)
	}

	mgr := NewJWTManager(secret)

	token, err := mgr.GenerateToken("admin", 1*time.Hour)
	if err != nil {
		t.Fatalf("GenerateToken failed: %v", err)
	}
	if token == "" {
		t.Fatal("GenerateToken returned empty token")
	}

	claims, err := mgr.ValidateToken(token)
	if err != nil {
		t.Fatalf("ValidateToken failed: %v", err)
	}
	if claims.Username != "admin" {
		t.Errorf("expected username 'admin', got %q", claims.Username)
	}
}

func TestJWTExpiredToken(t *testing.T) {
	secret := make([]byte, 32)
	mgr := NewJWTManager(secret)

	// Generate an already-expired token (negative duration).
	token, err := mgr.GenerateToken("admin", -1*time.Hour)
	if err != nil {
		t.Fatalf("GenerateToken failed: %v", err)
	}

	_, err = mgr.ValidateToken(token)
	if err == nil {
		t.Error("ValidateToken should fail for expired token")
	}
}

func TestJWTInvalidSignature(t *testing.T) {
	secret := make([]byte, 32)
	for i := range secret {
		secret[i] = byte(i + 1)
	}

	mgr := NewJWTManager(secret)
	token, err := mgr.GenerateToken("admin", 1*time.Hour)
	if err != nil {
		t.Fatalf("GenerateToken failed: %v", err)
	}

	// Validate with a different manager (different key).
	differentSecret := make([]byte, 32)
	for i := range differentSecret {
		differentSecret[i] = byte(255 - i)
	}
	differentMgr := NewJWTManager(differentSecret)

	_, err = differentMgr.ValidateToken(token)
	if err == nil {
		t.Error("ValidateToken should fail for token signed with different key")
	}
}

func TestJWTMalformedToken(t *testing.T) {
	secret := make([]byte, 32)
	mgr := NewJWTManager(secret)

	_, err := mgr.ValidateToken("not-a-valid-token")
	if err == nil {
		t.Error("ValidateToken should fail for malformed token")
	}

	_, err = mgr.ValidateToken("")
	if err == nil {
		t.Error("ValidateToken should fail for empty token")
	}
}

func TestGenerateOrLoadSecret(t *testing.T) {
	tmp := t.TempDir()
	path := tmp + "/jwt-secret"

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
