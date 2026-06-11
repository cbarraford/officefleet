package secrets

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"strings"
	"testing"
)

func testKey(t *testing.T) string {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(key)
}

func TestRoundTrip(t *testing.T) {
	c, err := NewCipher(testKey(t))
	if err != nil {
		t.Fatal(err)
	}
	plain := []byte("glpat-supersecret-token")
	stored, err := c.Encrypt(plain)
	if err != nil {
		t.Fatal(err)
	}
	if !IsEncrypted(stored) {
		t.Error("encrypted value not detected by IsEncrypted")
	}
	if bytes.Contains(stored, plain) {
		t.Error("ciphertext contains plaintext")
	}
	got, err := c.Decrypt(stored)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, plain) {
		t.Errorf("round trip = %q, want %q", got, plain)
	}
}

func TestEncryptIsNondeterministic(t *testing.T) {
	c, _ := NewCipher(testKey(t))
	a, _ := c.Encrypt([]byte("x"))
	b, _ := c.Encrypt([]byte("x"))
	if bytes.Equal(a, b) {
		t.Error("two encryptions of the same plaintext must differ (random nonce)")
	}
}

func TestTamperDetected(t *testing.T) {
	c, _ := NewCipher(testKey(t))
	stored, _ := c.Encrypt([]byte("secret"))
	stored[len(stored)-1] ^= 0xFF
	if _, err := c.Decrypt(stored); err == nil {
		t.Error("tampered ciphertext must fail to decrypt (GCM auth)")
	}
}

func TestWrongKeyFails(t *testing.T) {
	c1, _ := NewCipher(testKey(t))
	c2, _ := NewCipher(testKey(t))
	stored, _ := c1.Encrypt([]byte("secret"))
	if _, err := c2.Decrypt(stored); err == nil {
		t.Error("decrypt with wrong key must fail")
	}
}

func TestIsEncryptedOnLegacyPlaintext(t *testing.T) {
	for _, legacy := range [][]byte{[]byte("glpat-plain"), []byte(""), []byte("FSEC"), nil} {
		if IsEncrypted(legacy) {
			t.Errorf("IsEncrypted(%q) = true, want false", legacy)
		}
	}
}

func TestDecryptRejectsNonEncrypted(t *testing.T) {
	c, _ := NewCipher(testKey(t))
	if _, err := c.Decrypt([]byte("plain-bytes")); err == nil {
		t.Error("Decrypt of non-FSEC1 bytes must error")
	}
	if _, err := c.Decrypt([]byte("FSEC1short")); err == nil {
		t.Error("Decrypt of truncated FSEC1 value must error, not panic")
	}
}

func TestNewCipherKeyValidation(t *testing.T) {
	cases := map[string]string{
		"empty":      "",
		"not-base64": "!!!not-base64!!!",
		"short key":  base64.StdEncoding.EncodeToString(make([]byte, 16)),
	}
	for name, key := range cases {
		if _, err := NewCipher(key); err == nil {
			t.Errorf("%s: expected error", name)
		} else if name == "short key" && !strings.Contains(err.Error(), "32") {
			t.Errorf("short-key error should mention 32 bytes: %v", err)
		}
	}
}
