// Package secrets provides AES-256-GCM encryption-at-rest for the secrets
// table. Stored format: ASCII magic "FSEC1" + 12-byte nonce + ciphertext+tag.
// Rows lacking the magic are legacy plaintext (pre-SP4a), detectable without
// schema changes; `fleet secrets encrypt-existing` migrates them.
package secrets

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"fmt"
)

// MasterKeyEnv names the environment variable holding the base64 32-byte key.
const MasterKeyEnv = "FLEET_MASTER_KEY"

var magic = []byte("FSEC1")

const nonceLen = 12

// Cipher encrypts/decrypts secret values with a fixed master key.
type Cipher struct {
	aead cipher.AEAD
}

// NewCipher builds a Cipher from a base64-encoded 32-byte key.
func NewCipher(keyB64 string) (*Cipher, error) {
	if keyB64 == "" {
		return nil, fmt.Errorf("secrets: master key is empty (set %s to a base64-encoded 32-byte key)", MasterKeyEnv)
	}
	key, err := base64.StdEncoding.DecodeString(keyB64)
	if err != nil {
		return nil, fmt.Errorf("secrets: master key is not valid base64: %w", err)
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("secrets: master key must be 32 bytes, got %d", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("secrets: init cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("secrets: init gcm: %w", err)
	}
	return &Cipher{aead: aead}, nil
}

// Encrypt seals plain into the stored format with a fresh random nonce.
func (c *Cipher) Encrypt(plain []byte) ([]byte, error) {
	nonce := make([]byte, nonceLen)
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("secrets: nonce: %w", err)
	}
	out := make([]byte, 0, len(magic)+nonceLen+len(plain)+c.aead.Overhead())
	out = append(out, magic...)
	out = append(out, nonce...)
	return c.aead.Seal(out, nonce, plain, nil), nil
}

// Decrypt opens a stored value. It errors on legacy plaintext (no magic),
// truncated values, and authentication failures (tamper / wrong key).
func (c *Cipher) Decrypt(stored []byte) ([]byte, error) {
	if !IsEncrypted(stored) {
		return nil, fmt.Errorf("secrets: value is not encrypted (legacy plaintext?)")
	}
	rest := stored[len(magic):]
	if len(rest) < nonceLen+c.aead.Overhead() {
		return nil, fmt.Errorf("secrets: encrypted value is truncated")
	}
	nonce, ct := rest[:nonceLen], rest[nonceLen:]
	plain, err := c.aead.Open(nil, nonce, ct, nil)
	if err != nil {
		return nil, fmt.Errorf("secrets: decrypt failed (wrong key or corrupted value): %w", err)
	}
	return plain, nil
}

// IsEncrypted reports whether a stored value carries the FSEC1 format.
func IsEncrypted(stored []byte) bool {
	return bytes.HasPrefix(stored, magic)
}
