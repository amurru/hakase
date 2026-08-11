package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"golang.org/x/crypto/argon2"
)

const (
	// argon2id parameters (PHC standard).
	argonMemory  = 64 * 1024 // 64 MB
	argonTime    = 3
	argonThreads = 2
	saltLength   = 16
	keyLength    = 32
)

// Credentials is the single-admin user credential model.
type Credentials struct {
	Username     string `json:"username"`
	PasswordHash string `json:"password_hash"` // PHC-format argon2id hash
}

// LoadCredentials reads credentials from a JSON file.
func LoadCredentials(path string) (*Credentials, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("load credentials: %w", err)
	}
	var c Credentials
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("parse credentials: %w", err)
	}
	return &c, nil
}

// SaveCredentials writes credentials to a JSON file with 0600 permissions.
func SaveCredentials(path string, c *Credentials) error {
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal credentials: %w", err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("save credentials: %w", err)
	}
	return nil
}

// SetPassword hashes the password with argon2id and saves credentials to disk.
func SetPassword(path, username, password string) error {
	salt := make([]byte, saltLength)
	if _, err := rand.Read(salt); err != nil {
		return fmt.Errorf("generate salt: %w", err)
	}

	hash := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, keyLength)

	// Encode in PHC string format:
	// $argon2id$v=19$m=65536,t=3,p=2$<salt-b64>$<hash-b64>
	saltB64 := base64.RawStdEncoding.EncodeToString(salt)
	hashB64 := base64.RawStdEncoding.EncodeToString(hash)

	phc := fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s",
		argonMemory, argonTime, argonThreads, saltB64, hashB64)

	creds := &Credentials{
		Username:     username,
		PasswordHash: phc,
	}
	return SaveCredentials(path, creds)
}

// VerifyPassword compares a plaintext password against the stored argon2id hash.
func VerifyPassword(c *Credentials, password string) bool {
	if c == nil || c.PasswordHash == "" {
		return false
	}

	salt, expectedHash, err := parsePHCHash(c.PasswordHash)
	if err != nil {
		return false
	}

	if len(salt) != saltLength || len(expectedHash) != keyLength {
		return false
	}

	computedHash := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, keyLength)
	return subtle.ConstantTimeCompare(computedHash, expectedHash) == 1
}

// parsePHCHash extracts salt and hash bytes from a PHC-format argon2id string.
// Format: $argon2id$v=19$m=65536,t=3,p=2$<salt-b64>$<hash-b64>
func parsePHCHash(phc string) (salt, hash []byte, err error) {
	// Split by "$"
	parts := strings.Split(phc, "$")
	// Expected: ["", "argon2id", "v=19", "m=65536,t=3,p=2", salt, hash]
	// The leading empty string is from split on leading "$".
	if len(parts) < 6 {
		return nil, nil, fmt.Errorf("invalid PHC format: expected at least 6 parts, got %d", len(parts))
	}

	if parts[1] != "argon2id" {
		return nil, nil, fmt.Errorf("expected argon2id, got %s", parts[1])
	}

	salt, err = base64.RawStdEncoding.DecodeString(parts[len(parts)-2])
	if err != nil {
		return nil, nil, fmt.Errorf("decode salt: %w", err)
	}

	hash, err = base64.RawStdEncoding.DecodeString(parts[len(parts)-1])
	if err != nil {
		return nil, nil, fmt.Errorf("decode hash: %w", err)
	}

	return salt, hash, nil
}
