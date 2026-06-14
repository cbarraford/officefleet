package plugin

import (
	"errors"
	"testing"
)

func TestAuthError(t *testing.T) {
	var err error = &AuthError{Msg: "bad token"}
	if err.Error() != "bad token" {
		t.Errorf("Error() = %q", err.Error())
	}
	var ae *AuthError
	if !errors.As(err, &ae) {
		t.Error("errors.As failed to match *AuthError")
	}
}

func TestInitErrorTracking(t *testing.T) {
	defer RecordInit("track-test", nil) // keep global registry clean

	if InitError("track-test") != nil {
		t.Fatal("a plugin with no recorded init must report no error")
	}

	boom := errors.New("missing token")
	RecordInit("track-test", boom)
	if !errors.Is(InitError("track-test"), boom) {
		t.Errorf("InitError = %v, want the recorded failure", InitError("track-test"))
	}

	// A later successful Init clears the recorded failure.
	RecordInit("track-test", nil)
	if InitError("track-test") != nil {
		t.Error("a successful re-init must clear the recorded error")
	}
}
