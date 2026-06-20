package forge

import "testing"

func TestProfile_KnownForges(t *testing.T) {
	gl, ok := Profile("gitlab")
	if !ok {
		t.Fatal("gitlab profile missing")
	}
	if gl["cli"] != "glab" || gl["host"] != "gitlab.com" || gl["tokenSecret"] != "gitlab_token" {
		t.Errorf("gitlab profile = %v", gl)
	}
	gh, ok := Profile("github")
	if !ok {
		t.Fatal("github profile missing")
	}
	if gh["cli"] != "gh" || gh["changeAbbr"] != "PR" || gh["headFlag"] != "--head" {
		t.Errorf("github profile = %v", gh)
	}
}

func TestProfile_Unknown(t *testing.T) {
	if _, ok := Profile("bitbucket"); ok {
		t.Error("expected unknown forge to return ok=false")
	}
}
