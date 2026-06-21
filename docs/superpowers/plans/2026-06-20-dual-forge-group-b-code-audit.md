# Dual-forge Group B: code-audit — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. Steps use checkbox (`- [ ]`) syntax.

**Goal:** Make the continuous `code-audit` fleet skill run on both GitLab and GitHub. It files an issue per verified finding via the `create_issue` output action — GitLab has that action; GitHub does not yet, so this group adds it.

**Architecture:** code-audit emits a JSON list of issues and an `outputs:` binding delivers each via `plugin.Do("create_issue", params)`. GitLab's `create_issue` already exists. We add a GitHub `create_issue` that accepts the SAME param keys the binding already uses (`project`/`repo`, `title`, `description`, `labels` as a comma-separated string) and maps them to GitHub's Issues API (`description`→`body`, comma-`labels`→string array). Then the prompt/config are forge-ized like the other skills.

**Tech Stack:** Go (`github.com/cbarraford/office-fleet`), `text/template` prompts, YAML config, `httptest` + table tests.

## Global Constraints

- Module path: `github.com/cbarraford/office-fleet`. Test command: `go test ./...` (render tests set `FLEET_DATABASE_DSN` via `t.Setenv`; verify with it unset).
- The GitHub `create_issue` action must accept the lingua-franca param keys the existing binding uses: `project` (or `repo`), `title`, `description` (NOT `body`), `labels` (comma-separated string, NOT array). It maps `description`→GitHub `body` and splits `labels` into GitHub's string array. This keeps one `outputs:` block usable on both forges.
- Reuse the existing GitHub helpers `firstParam` and `apiJSON(ctx, method, url, payload) (map, int, error)` (added in the code-review slice). Do not reimplement them.
- Back-compat: existing GitLab code-audit assignment unchanged in behavior; absent `forge` defaults to `gitlab`.
- Secrets reachable ONLY via `{{secret .Forge.tokenSecret}}` inside the prompt.
- New GitHub assignment defaults `enabled: false` (the GitLab `security-loop` is enabled and points at the `myorg/myrepo` placeholder; two enabled loops on the same placeholder would double-file).
- Work continues on branch `feat/dual-forge-code-review` (PR #51). Do NOT create a new branch.

---

### Task 1: GitHub `create_issue` action

**Files:**
- Modify: `internal/plugins/github/github.go` (add `createIssue`; register in `Do` + `Actions`)
- Modify: `internal/plugins/github/github_test.go` (add tests)

**Interfaces:**
- Consumes: `firstParam`, `apiJSON`, `paramToString` (already in the package).
- Produces: action `create_issue` accepting `project`/`repo`, `title`, `description`, `labels` (comma-separated); POSTs to `/repos/{repo}/issues` with `{title, body, labels:[...]}`.

- [ ] **Step 1: Write the failing tests**

Add to `internal/plugins/github/github_test.go`:

```go
func TestCreateIssue_Github(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"number":42}`))
	}))
	defer srv.Close()

	g := &GitHubPlugin{baseURL: srv.URL, token: "t"}
	_, err := g.Do(context.Background(), "create_issue", map[string]any{
		"project": "org/repo", "title": "[Security] bug", "description": "found at x.go:10",
		"labels": "security, huginn-code-audit, general-security",
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/repos/org/repo/issues" {
		t.Errorf("path = %q", gotPath)
	}
	if gotBody["title"] != "[Security] bug" || gotBody["body"] != "found at x.go:10" {
		t.Errorf("title/body = %v", gotBody)
	}
	labels, ok := gotBody["labels"].([]any)
	if !ok || len(labels) != 3 || labels[0] != "security" || labels[1] != "huginn-code-audit" {
		t.Errorf("labels not split into trimmed array: %v", gotBody["labels"])
	}
}

func TestCreateIssue_Github_NoLabels(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"number":1}`))
	}))
	defer srv.Close()
	g := &GitHubPlugin{baseURL: srv.URL, token: "t"}
	if _, err := g.Do(context.Background(), "create_issue", map[string]any{
		"project": "org/repo", "title": "t",
	}); err != nil {
		t.Fatal(err)
	}
	if _, present := gotBody["labels"]; present {
		t.Errorf("labels should be omitted when empty, got %v", gotBody["labels"])
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/plugins/github/ -run TestCreateIssue`
Expected: FAIL — `github: unknown action "create_issue"`.

- [ ] **Step 3: Implement `createIssue` and register it**

In `internal/plugins/github/github.go`, add the `create_issue` case to `Do()` (keep existing cases):

```go
	case "create_issue":
		return g.createIssue(ctx, params)
```

Add to `Actions()` (keep existing entries):

```go
		{Name: "create_issue", Description: "Create a GitHub issue"},
```

Add the function:

```go
func (g *GitHubPlugin) createIssue(ctx context.Context, params map[string]any) (map[string]any, error) {
	repo := firstParam(params, "project", "repo")
	title := paramToString(params["title"])
	description := paramToString(params["description"])
	labels := paramToString(params["labels"]) // comma-separated, optional
	if repo == "" || title == "" {
		return nil, fmt.Errorf("github create_issue: project and title are required")
	}
	payload := map[string]any{"title": title, "body": description}
	var labelList []string
	for _, l := range strings.Split(labels, ",") {
		if t := strings.TrimSpace(l); t != "" {
			labelList = append(labelList, t)
		}
	}
	if len(labelList) > 0 {
		payload["labels"] = labelList
	}
	url := fmt.Sprintf("%s/repos/%s/issues", g.baseURL, repo)
	result, _, err := g.apiJSON(ctx, http.MethodPost, url, payload)
	return result, err
}
```

(`strings`, `fmt`, `net/http` are already imported.)

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/plugins/github/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/plugins/github/github.go internal/plugins/github/github_test.go
git commit -m "feat(github): add create_issue action (description->body, comma labels->array)"
```

---

### Task 2: Forge-ize the code-audit skill + assignments

**Files:**
- Modify: `configs/huginn.yaml` (code-audit skill `prompt`, `config_schema`, `output_actions`; existing assignment; add github assignment)
- Modify: `internal/config/huginn_render_test.go` (add a render test)

**Interfaces:**
- Consumes: `skillPrompt(t, name)` helper (already in the test file from Group A); Forge fields `cli`, `host`, `cloneUser`, `tokenSecret`, `tokenEnv`, `listLimitFlag`.

- [ ] **Step 1: Write the failing test**

Add to `internal/config/huginn_render_test.go`:

```go
func TestCodeAuditPrompt_RendersBothForges(t *testing.T) {
	tmpl := skillPrompt(t, "code-audit")
	secrets := map[string]string{"gitlab_token": "glt", "github_token": "ght"}
	cases := []struct{ forge, wantHost, wantList, wantLimit string }{
		{"gitlab", "gitlab.com", "glab issue list", "--per-page"},
		{"github", "github.com", "gh issue list", "--limit"},
	}
	for _, tc := range cases {
		fp, _ := forge.Profile(tc.forge)
		ctx := prompt.Context{
			Assignment: map[string]any{"project": "o/r", "category": "general-security"},
			Forge:      fp,
		}
		out, err := prompt.Render(tmpl, ctx, secrets)
		if err != nil {
			t.Fatalf("forge %s render: %v", tc.forge, err)
		}
		for _, want := range []string{tc.wantHost, tc.wantList, tc.wantLimit} {
			if !strings.Contains(out, want) {
				t.Errorf("forge %s: missing %q", tc.forge, want)
			}
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/config/ -run TestCodeAuditPrompt`
Expected: FAIL — github case lacks `github.com`/`gh issue list`/`--limit`.

- [ ] **Step 3: Forge-ize the code-audit `prompt:` — only the Workspace + issue-list lines change**

In `configs/huginn.yaml`, in the code-audit skill `prompt:`, replace the Workspace + existing-findings block (the first lines) with:

```yaml
      Audit {{.Assignment.project}} for {{.Assignment.category}} defects.

      Workspace:
        git clone --depth 1 https://{{.Forge.cloneUser}}:{{secret .Forge.tokenSecret}}@{{.Forge.host}}/{{.Assignment.project}}.git repo
        cd repo && export {{.Forge.tokenEnv}}={{secret .Forge.tokenSecret}}
      Load existing findings first so you never duplicate them (huginn labels its
      audit issues "huginn-code-audit"):
        {{.Forge.cli}} issue list --label huginn-code-audit --repo {{.Assignment.project}} {{.Forge.listLimitFlag}} 100
```

Leave the entire rest of the prompt (Category focus, Discipline, the JSON result format with the `labels` field) UNCHANGED — it is forge-neutral.

- [ ] **Step 4: Add `forge` to `config_schema.properties` and a github `output_actions` entry**

Add to `config_schema.properties` (keep existing):

```yaml
        forge: { type: string, description: "Which forge this assignment targets", enum: [gitlab, github], default: "gitlab" }
```

Replace the code-audit `output_actions:` block with both forges:

```yaml
    output_actions:
      - plugin: gitlab
        action: create_issue
      - plugin: github
        action: create_issue
```

- [ ] **Step 5: Run the render test to verify it passes**

Run: `go test ./internal/config/ -run TestCodeAuditPrompt`
Expected: PASS.

- [ ] **Step 6: Add `forge: gitlab` to the existing assignment + add a GitHub assignment**

Add `forge: gitlab` as the first key of the existing `security-loop` assignment's `config:`. Then add a parallel GitHub assignment after it:

```yaml
  - agent: huginn
    skill: code-audit
    name: gh-security-loop
    enabled: false               # opt-in: enable + point at a GitHub repo
    trigger:
      kind: continuous
      delay: 6h
    config:
      forge: github
      project: "myorg/myrepo"
      category: "general-security"
    outputs:
      - plugin: github
        action: create_issue
        for_each: issues
        params:
          project: "{{.Assignment.project}}"
          title: "{{.Item.title}}"
          description: "{{.Item.description}}"
          labels: "{{.Item.labels}}"
```

- [ ] **Step 7: Validate config + full sweep**

Run:
```bash
go run ./cmd/fleet config validate --config configs/huginn.yaml
go build ./... && env -u FLEET_DATABASE_DSN go test ./...
```
Expected: validate exit 0; all tests pass.

- [ ] **Step 8: Commit**

```bash
git add configs/huginn.yaml internal/config/huginn_render_test.go
git commit -m "feat(huginn): dual-forge code-audit prompt + GitHub create_issue assignment"
```

---

### Task 3: Group verification sweep

**Files:** none.

- [ ] **Step 1: Build, vet, test (env-independent)**

Run:
```bash
go build ./... && go vet ./... && env -u FLEET_DATABASE_DSN go test ./...
```
Expected: all PASS.

- [ ] **Step 2: Confirm the code-audit prompt renders for both forges**

Run: `env -u FLEET_DATABASE_DSN go test ./internal/config/ -run TestCodeAuditPrompt -v`
Expected: PASS for both subcases.

---

## Self-Review

**Spec coverage:** Group B (code-audit) of the design's follow-on list → Task 2; the required GitHub `create_issue` action the design's §4 lists → Task 1. ✅

**Placeholder scan:** complete code/YAML in every step; `<...>` only inside prompt prose (agent-fill), matching existing style. ✅

**Type consistency:** `create_issue` accepts `project`/`title`/`description`/`labels` in Task 1, and the Task 2 `outputs:` binding sends exactly those keys. `skillPrompt` is reused from Group A (not redefined). ✅
