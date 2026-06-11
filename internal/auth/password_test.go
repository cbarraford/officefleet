package auth

import (
	"strings"
	"testing"
)

func TestHashVerifyRoundTrip(t *testing.T) {
	hash, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(hash, "pbkdf2-sha256$600000$") {
		t.Errorf("hash format = %q", hash)
	}
	if !VerifyPassword(hash, "correct horse battery staple") {
		t.Error("correct password rejected")
	}
	if VerifyPassword(hash, "wrong password") {
		t.Error("wrong password accepted")
	}
}

func TestHashesAreSalted(t *testing.T) {
	a, _ := HashPassword("same")
	b, _ := HashPassword("same")
	if a == b {
		t.Error("two hashes of the same password must differ (random salt)")
	}
}

func TestVerifyRejectsMalformed(t *testing.T) {
	for _, h := range []string{"", "nonsense", "pbkdf2-sha256$notanumber$AA$BB",
		"pbkdf2-sha256$600000$!badb64!$AA", "argon2id$future$x$y"} {
		if VerifyPassword(h, "pw") {
			t.Errorf("malformed hash %q verified", h)
		}
	}
}

func TestEmptyPasswordRejected(t *testing.T) {
	if _, err := HashPassword(""); err == nil {
		t.Error("empty password must be rejected")
	}
}
