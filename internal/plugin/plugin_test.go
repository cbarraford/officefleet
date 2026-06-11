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
