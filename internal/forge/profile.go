// Package forge holds the static per-provider (GitLab/GitHub) profile that lets
// one fleet skill prompt drive either forge. Every place the two CLIs, hosts,
// nouns, or flags differ is captured here as DATA, so prompts carry no provider
// branching — they read {{.Forge.*}}.
package forge

// Profiles maps a forge name to its template profile, exposed to prompts as
// {{.Forge.*}} (see internal/prompt.Context.Forge). This slice carries only the
// fields code-review needs plus the change-create flags reused by later skills;
// ci* fields land with the ci-fix slice.
var Profiles = map[string]map[string]any{
	"gitlab": {
		"name":             "gitlab",
		"cli":              "glab",
		"host":             "gitlab.com",
		"cloneUser":        "oauth2",
		"tokenSecret":      "gitlab_token",
		"tokenEnv":         "GITLAB_TOKEN",
		"changeCmd":        "mr",
		"changeNoun":       "merge request",
		"changeAbbr":       "MR",
		"changeSigil":      "!",
		"headFlag":         "--source-branch",
		"baseFlag":         "--target-branch",
		"bodyFlag":         "--description",
		"removeBranchFlag": "--remove-source-branch",
		"listLimitFlag":      "--per-page",
		"issueCommentsHowto": "glab issue note list <iid> --repo <project>",
		"closesIssuesHowto":  "glab api \"projects/<project, url-encoded with / as %2F>/merge_requests/<change_iid>/closes_issues\"",
	},
	"github": {
		"name":             "github",
		"cli":              "gh",
		"host":             "github.com",
		"cloneUser":        "x-access-token",
		"tokenSecret":      "github_token",
		"tokenEnv":         "GITHUB_TOKEN",
		"changeCmd":        "pr",
		"changeNoun":       "pull request",
		"changeAbbr":       "PR",
		"changeSigil":      "#",
		"headFlag":         "--head",
		"baseFlag":         "--base",
		"bodyFlag":         "--body",
		"removeBranchFlag": "--delete-branch",
		"listLimitFlag":      "--limit",
		"issueCommentsHowto": "gh issue view <iid> --repo <project> --comments",
		"closesIssuesHowto":  "gh pr view <change_iid> --repo <project> --json closingIssuesReferences --jq '.closingIssuesReferences[].number'",
	},
}

// Profile returns the profile for name, or (nil, false) if unknown.
func Profile(name string) (map[string]any, bool) {
	p, ok := Profiles[name]
	return p, ok
}
