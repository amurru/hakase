package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// Claims is the JWT claims payload.
type Claims struct {
	Username string `json:"username"`
	Exp      int64  `json:"exp"`
}

// JWTManager signs and validates JWT tokens using HMAC-SHA256.
type JWTManager struct {
	secret []byte
}

// NewJWTManager creates a JWTManager with the given signing key.
func NewJWTManager(secret []byte) *JWTManager {
	return &JWTManager{secret: secret}
}

// header is the static JWT header for HS256.
const jwtHeader = `{"alg":"HS256","typ":"JWT"}`

// GenerateToken creates a signed JWT token for the given username and expiry duration.
func (m *JWTManager) GenerateToken(username string, expiry time.Duration) (string, error) {
	now := time.Now()
	claims := Claims{
		Username: username,
		Exp:      now.Add(expiry).Unix(),
	}

	claimsJSON, err := json.Marshal(claims)
	if err != nil {
		return "", fmt.Errorf("marshal claims: %w", err)
	}

	headerB64 := base64.RawURLEncoding.EncodeToString([]byte(jwtHeader))
	claimsB64 := base64.RawURLEncoding.EncodeToString(claimsJSON)
	signingInput := headerB64 + "." + claimsB64

	signature := m.sign(signingInput)
	sigB64 := base64.RawURLEncoding.EncodeToString(signature)

	return signingInput + "." + sigB64, nil
}

// ValidateToken parses and validates a JWT token, checking signature and expiry.
func (m *JWTManager) ValidateToken(tokenString string) (*Claims, error) {
	parts := splitJWTParts(tokenString)
	if len(parts) != 3 {
		return nil, fmt.Errorf("invalid token: expected 3 parts, got %d", len(parts))
	}

	headerB64, claimsB64, sigB64 := parts[0], parts[1], parts[2]
	signingInput := headerB64 + "." + claimsB64

	// Verify signature.
	expectedSig, err := base64.RawURLEncoding.DecodeString(sigB64)
	if err != nil {
		return nil, fmt.Errorf("decode signature: %w", err)
	}
	actualSig := m.sign(signingInput)
	if !hmac.Equal(actualSig, expectedSig) {
		return nil, fmt.Errorf("invalid signature")
	}

	// Decode claims.
	claimsJSON, err := base64.RawURLEncoding.DecodeString(claimsB64)
	if err != nil {
		return nil, fmt.Errorf("decode claims: %w", err)
	}

	var claims Claims
	if err := json.Unmarshal(claimsJSON, &claims); err != nil {
		return nil, fmt.Errorf("unmarshal claims: %w", err)
	}

	// Check expiry.
	if time.Now().Unix() > claims.Exp {
		return nil, fmt.Errorf("token expired")
	}

	return &claims, nil
}

// sign computes HMAC-SHA256 of the signing input.
func (m *JWTManager) sign(input string) []byte {
	mac := hmac.New(sha256.New, m.secret)
	mac.Write([]byte(input))
	return mac.Sum(nil)
}

// splitJWTParts splits a JWT into its three dot-separated parts.
func splitJWTParts(token string) []string {
	parts := make([]string, 0, 3)
	start := 0
	for i := 0; i < len(token); i++ {
		if token[i] == '.' {
			parts = append(parts, token[start:i])
			start = i + 1
		}
	}
	parts = append(parts, token[start:])
	return parts
}

// GenerateOrLoadSecret loads a 32-byte JWT secret from path if it exists,
// otherwise generates a new random secret, writes it with 0600, and returns it.
func GenerateOrLoadSecret(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err == nil {
		if len(data) != 32 {
			return nil, fmt.Errorf("invalid secret file: expected 32 bytes, got %d", len(data))
		}
		return data, nil
	}

	if !os.IsNotExist(err) {
		return nil, fmt.Errorf("read secret file: %w", err)
	}

	// Generate new 32-byte random secret.
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return nil, fmt.Errorf("generate secret: %w", err)
	}

	if err := os.WriteFile(path, secret, 0600); err != nil {
		return nil, fmt.Errorf("save secret: %w", err)
	}

	return secret, nil
}
