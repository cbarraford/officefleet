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

func TestProfile_GroupAFields(t *testing.T) {
	gl, _ := Profile("gitlab")
	if gl["listLimitFlag"] != "--per-page" {
		t.Errorf("gitlab listLimitFlag = %v", gl["listLimitFlag"])
	}
	if gl["issueCommentsHowto"] == nil || gl["closesIssuesHowto"] == nil {
		t.Errorf("gitlab missing howto fields: %v", gl)
	}
	gh, _ := Profile("github")
	if gh["listLimitFlag"] != "--limit" {
		t.Errorf("github listLimitFlag = %v", gh["listLimitFlag"])
	}
	if gh["issueCommentsHowto"] == nil || gh["closesIssuesHowto"] == nil {
		t.Errorf("github missing howto fields: %v", gh)
	}
}
