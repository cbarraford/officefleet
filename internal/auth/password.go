// Package auth provides operator authentication: PBKDF2 password hashing
// (stdlib, versioned format) and opaque session tokens stored hashed.
package auth

import (
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
)

const (
	pbkdf2Iterations = 600_000
	saltLen          = 16
	derivedKeyLen    = 32
	hashScheme       = "pbkdf2-sha256"
)

// HashPassword derives a versioned PBKDF2-HMAC-SHA256 hash:
// pbkdf2-sha256$<iter>$<b64 salt>$<b64 key>. The scheme prefix lets a future
// KDF coexist and migrate on next login.
func HashPassword(password string) (string, error) {
	if password == "" {
		return "", fmt.Errorf("auth: password must not be empty")
	}
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("auth: salt: %w", err)
	}
	key, err := pbkdf2.Key(sha256.New, password, salt, pbkdf2Iterations, derivedKeyLen)
	if err != nil {
		return "", fmt.Errorf("auth: derive key: %w", err)
	}
	return fmt.Sprintf("%s$%d$%s$%s", hashScheme, pbkdf2Iterations,
		base64.StdEncoding.EncodeToString(salt),
		base64.StdEncoding.EncodeToString(key)), nil
}

// VerifyPassword reports whether password matches the stored versioned hash.
// Malformed or unknown-scheme hashes verify false (never panic).
func VerifyPassword(stored, password string) bool {
	parts := strings.Split(stored, "$")
	if len(parts) != 4 || parts[0] != hashScheme {
		return false
	}
	iter, err := strconv.Atoi(parts[1])
	if err != nil || iter < 1 {
		return false
	}
	salt, err := base64.StdEncoding.DecodeString(parts[2])
	if err != nil {
		return false
	}
	want, err := base64.StdEncoding.DecodeString(parts[3])
	if err != nil {
		return false
	}
	got, err := pbkdf2.Key(sha256.New, password, salt, iter, len(want))
	if err != nil {
		return false
	}
	return hmac.Equal(got, want)
}
