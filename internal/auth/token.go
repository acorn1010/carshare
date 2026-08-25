// Package auth holds session token minting and the Google OAuth provider.
// Session tokens are opaque random strings, the database only ever sees their
// SHA-256, so a leaked database cannot replay sessions.
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
)

// NewSessionToken mints a raw session token and its storable hash.
func NewSessionToken() (raw, hash string, err error) {
	buffer := make([]byte, 32)
	if _, err := rand.Read(buffer); err != nil {
		return "", "", fmt.Errorf("auth: token entropy: %w", err)
	}
	raw = base64.RawURLEncoding.EncodeToString(buffer)
	return raw, HashToken(raw), nil
}

// HashToken maps a raw token to the hash stored in cars.sessions.
func HashToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// NewState mints the anti-CSRF state for an OAuth round trip.
func NewState() (string, error) {
	buffer := make([]byte, 16)
	if _, err := rand.Read(buffer); err != nil {
		return "", fmt.Errorf("auth: state entropy: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buffer), nil
}
