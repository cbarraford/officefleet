# SP4c — Agent Avatars Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give agents a visual identity per spec.md §6.1 — async image-backend headshot generation with a deterministic initials-SVG fallback, file storage served at `/avatars/`, regenerate/upload endpoints, and the SPA controls.

**Architecture:** A new `image_backends:` config list (one v1 kind: `openai-image-compatible`) plus `serve.avatar_backend/avatars_dir/avatar_prompt`. A new `internal/avatar` package owns generation (Generator interface → OpenAI-compatible impl), the initials fallback (same FNV-1a palette as the SPA's AvatarBubble, so server and client colors match), atomic file storage, public serving, and an async Service with a per-agent in-flight guard. The API gains two admin routes and a one-line hook in `handleCreateAgent`; `fleet serve` wires it all.

**Tech Stack:** Go stdlib only (`text/template`, `encoding/base64`, `net/http`, `os`) — zero new Go deps. Frontend: a raw-body helper on the existing client + two controls on AgentDetail.

**Spec:** `docs/superpowers/specs/2026-06-11-sp4c-avatars-design.md`

---

## Environment notes

1. **`NODE_OPTIONS` quirk (this session):** if `node`/`npm` fails with `Cannot find module ... restore-node-options.cjs`, prefix the command with `NODE_OPTIONS= ` (empty). Never commit that string anywhere.
2. Conventions: commit directly to `master`; TDD; gofmt + `go vet ./...` clean; zero new go.mod deps; fakes not Postgres; LSP diagnostics are often stale — trust `go test` output only.
3. Run Go commands from the repo root `/Users/cbarraford/workshop/office-fleet`.

## File map

| Path | Action | Responsibility |
|---|---|---|
| `internal/config/config.go` | modify | `ImageBackend` type, `Config.ImageBackends`, `ServeConfig.AvatarBackend/AvatarsDir/AvatarPrompt`, validation |
| `internal/config/config_test.go` | modify | validation table additions |
| `internal/avatar/initials.go` (+`initials_test.go`) | create | deterministic initials SVG (FNV-1a palette matching the SPA) |
| `internal/avatar/generator.go` (+`generator_test.go`) | create | `Generator` interface, `DefaultPrompt`, `OpenAIImageGenerator` |
| `internal/avatar/store.go` (+`store_test.go`) | create | atomic file `Store` + public `MountHTTP` route |
| `internal/avatar/service.go` (+`service_test.go`) | create | async `Service` (Assign/SetUpload/Wait, in-flight guard, fallback-on-error) |
| `internal/repo/agents.go` | modify | `UpdateAvatarURL` (narrow update — avoids clobbering concurrent PATCHes) |
| `internal/api/api.go` | modify | `AvatarService` interface, `Deps.Avatars`, two routes |
| `internal/api/avatar_handlers.go` (+`avatar_handlers_test.go`) | create | regenerate (202) + upload (PNG sniff, 1 MiB cap) handlers |
| `internal/api/entity_handlers.go` | modify | `handleCreateAgent` fires `Assign` (nil-safe) |
| `internal/api/api_test.go` | modify | `fakeAvatarService` |
| `cmd/fleet/main.go` | modify | serve wiring: store/generator/service, `/avatars` mount, `Deps.Avatars` |
| `configs/fleet.yaml` | modify | commented `image_backends` + serve examples |
| `.gitignore` | modify | `/avatars/` (serve creates it at runtime — worktree must stay clean) |
| `web/src/api/client.ts` (+`client.test.ts`) | modify | `api.putRaw` for binary uploads |
| `web/src/pages/AgentDetail.tsx` | modify | Regenerate + Upload controls, ~5s post-regenerate poll |

## Contract facts (verified against the codebase)

- `config.Load` expands `${env:VAR}` before YAML parse — an image backend's `api_key: ${env:OPENAI_API_KEY}` arrives as the literal key; no extra resolution step (parity with LLM endpoint backends).
- `config.BackendAuth` is reused for image backends (`mode: api_key|none`; `Validate` normalizes `""`→`none`).
- `repo.AgentRepo` has `Update` (full row) — SP4c adds `UpdateAvatarURL` so the async writer can't clobber a concurrent rename.
- `internal/api` fakes: `fakeAgentStore` (entity_handlers_test.go) with `newFakeAgentStore()`, `rows map[uuid.UUID]*domain.Agent`; `newMemSessionStore(role)` + `auth.NewSessions` for auth (middleware_test.go). `GetByID` on the fake returns an ERROR for missing agents (handlers treat any GetByID error as 404).
- Middleware: viewer non-GET → 403 (regenerate POST and upload PUT are admin-only for free). All `/api/v1/` routes require a session.
- The SPA `AvatarBubble` hashes names with FNV-1a over code points into the palette `['#e06c75','#e5c07b','#98c379','#56b6c2','#61afef','#c678dd','#d19a66','#be8c6c']` — `InitialsSVG` MUST use the same algorithm and palette so the generated fallback matches what the client showed before generation.
- `server.Handler(mounts ...func(*http.ServeMux))` — `/avatars/{file}` mounts as another func; Go 1.22 pattern precedence keeps it above the SPA's `/`.
- domain.Agent.AvatarURL is `*string` (`json:"avatar_url"`).

---

### Task 1: Config — image_backends + serve avatar fields + validation

**Files:**
- Modify: `internal/config/config.go`
- Test: `internal/config/config_test.go`
- Modify: `configs/fleet.yaml`

- [x] **Step 1: Write the failing validation tests**

Read `internal/config/config_test.go` first and follow its existing table/helper style (there is an established pattern of building a minimal valid Config and asserting `Validate` error substrings). Add:

```go
func TestValidateImageBackends(t *testing.T) {
	base := func() *Config {
		return &Config{
			ImageBackends: []ImageBackend{{
				Name: "dalle", Kind: "openai-image-compatible",
				BaseURI: "https://api.openai.com/v1", Model: "gpt-image-1",
				Auth: BackendAuth{Mode: "api_key", APIKey: "k"},
			}},
		}
	}

	t.Run("valid passes", func(t *testing.T) {
		if errs := Validate(base()); len(errs) != 0 {
			t.Fatalf("unexpected errors: %v", errs)
		}
	})

	cases := []struct {
		name    string
		mutate  func(*Config)
		wantSub string
	}{
		{"missing name", func(c *Config) { c.ImageBackends[0].Name = "" }, "image backend missing name"},
		{"duplicate name", func(c *Config) { c.ImageBackends = append(c.ImageBackends, c.ImageBackends[0]) }, "duplicate image backend name"},
		{"unknown kind", func(c *Config) { c.ImageBackends[0].Kind = "stable-diffusion" }, "unknown kind"},
		{"missing base_uri", func(c *Config) { c.ImageBackends[0].BaseURI = "" }, "requires base_uri"},
		{"missing model", func(c *Config) { c.ImageBackends[0].Model = "" }, "requires model"},
		{"bad auth mode", func(c *Config) { c.ImageBackends[0].Auth.Mode = "subscription" }, "auth mode"},
		{"api_key mode without key", func(c *Config) { c.ImageBackends[0].Auth = BackendAuth{Mode: "api_key"} }, "requires api_key"},
		{"avatar_backend undefined", func(c *Config) { c.Serve.AvatarBackend = "missing" }, "avatar_backend"},
		{"avatar_prompt unparsable", func(c *Config) { c.Serve.AvatarPrompt = "{{.Name" }, "avatar_prompt"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := base()
			tc.mutate(cfg)
			errs := Validate(cfg)
			found := false
			for _, e := range errs {
				if strings.Contains(e.Error(), tc.wantSub) {
					found = true
				}
			}
			if !found {
				t.Errorf("Validate() = %v, want an error containing %q", errs, tc.wantSub)
			}
		})
	}

	t.Run("avatar_backend referencing a defined backend passes", func(t *testing.T) {
		cfg := base()
		cfg.Serve.AvatarBackend = "dalle"
		if errs := Validate(cfg); len(errs) != 0 {
			t.Fatalf("unexpected errors: %v", errs)
		}
	})

	t.Run("auth mode none is valid and empty mode normalizes to none", func(t *testing.T) {
		cfg := base()
		cfg.ImageBackends[0].Auth = BackendAuth{}
		if errs := Validate(cfg); len(errs) != 0 {
			t.Fatalf("unexpected errors: %v", errs)
		}
		if cfg.ImageBackends[0].Auth.Mode != "none" {
			t.Errorf("mode = %q, want normalized none", cfg.ImageBackends[0].Auth.Mode)
		}
	})
}
```

(Add `"strings"` to the test imports if missing.)

- [x] **Step 2: Run to verify failure**

Run: `go test ./internal/config/ -run TestValidateImageBackends -v`
Expected: compile FAIL — `ImageBackend` undefined.

- [x] **Step 3: Implement**

In `internal/config/config.go`:

After the `Backend` type, add:

```go
// ImageBackend is a named image-generation provider (separate list from LLM
// backends — avatars only, spec §6.1 / SP4c). v1 supports one kind:
// openai-image-compatible (POST {base_uri}/images/generations, b64 response).
type ImageBackend struct {
	Name    string      `yaml:"name"`
	Kind    string      `yaml:"kind"` // openai-image-compatible
	BaseURI string      `yaml:"base_uri"`
	Model   string      `yaml:"model"`
	Auth    BackendAuth `yaml:"auth"`
	Size    string      `yaml:"size,omitempty"` // e.g. "256x256" (generator default when empty)
}
```

Extend `ServeConfig`:

```go
type ServeConfig struct {
	Addr           string `yaml:"addr,omitempty"`            // webhook listener; default ":8080"
	Workers        int    `yaml:"workers,omitempty"`         // dispatcher pool; default 4
	RescanInterval string `yaml:"rescan_interval,omitempty"` // Go duration; default "30s"
	SecureCookies  bool   `yaml:"secure_cookies,omitempty"`  // set true when serving behind TLS

	// Avatars (SP4c). AvatarBackend names an image_backends entry; unset
	// means initials-fallback only. AvatarPrompt is a Go text/template with
	// {{.Name}} and {{.Role}}; empty uses the built-in default.
	AvatarBackend string `yaml:"avatar_backend,omitempty"`
	AvatarsDir    string `yaml:"avatars_dir,omitempty"` // default "./avatars"
	AvatarPrompt  string `yaml:"avatar_prompt,omitempty"`
}
```

Extend `Config` (after `Backends`):

```go
	ImageBackends []ImageBackend `yaml:"image_backends,omitempty"`
```

In `Validate`, after the voter-panel loop and before the `cfg.Serve.Workers` check, add:

```go
	imageBackendNames := map[string]bool{}
	for i := range cfg.ImageBackends {
		ib := &cfg.ImageBackends[i]
		if ib.Name == "" {
			errs = append(errs, fmt.Errorf("image backend missing name"))
			continue
		}
		if imageBackendNames[ib.Name] {
			errs = append(errs, fmt.Errorf("duplicate image backend name: %q", ib.Name))
		}
		imageBackendNames[ib.Name] = true
		if ib.Kind != "openai-image-compatible" {
			errs = append(errs, fmt.Errorf("image backend %q: unknown kind %q", ib.Name, ib.Kind))
		}
		if ib.BaseURI == "" {
			errs = append(errs, fmt.Errorf("image backend %q: requires base_uri", ib.Name))
		}
		if ib.Model == "" {
			errs = append(errs, fmt.Errorf("image backend %q: requires model", ib.Name))
		}
		if ib.Auth.Mode == "" {
			ib.Auth.Mode = "none"
		}
		switch ib.Auth.Mode {
		case "api_key", "none":
		default:
			errs = append(errs, fmt.Errorf("image backend %q: unknown auth mode %q (api_key or none)", ib.Name, ib.Auth.Mode))
		}
		if ib.Auth.Mode == "api_key" && strings.TrimSpace(ib.Auth.APIKey) == "" {
			errs = append(errs, fmt.Errorf("image backend %q: auth mode api_key requires api_key to be set", ib.Name))
		}
	}
	if cfg.Serve.AvatarBackend != "" && !imageBackendNames[cfg.Serve.AvatarBackend] {
		errs = append(errs, fmt.Errorf("serve: avatar_backend %q is not a defined image backend", cfg.Serve.AvatarBackend))
	}
	if cfg.Serve.AvatarPrompt != "" {
		if _, err := template.New("avatar_prompt").Parse(cfg.Serve.AvatarPrompt); err != nil {
			errs = append(errs, fmt.Errorf("serve: invalid avatar_prompt: %v", err))
		}
	}
```

Add `"text/template"` to the imports.

- [x] **Step 4: Run tests**

Run: `go test ./internal/config/ -count=1`
Expected: PASS (new + existing).

- [x] **Step 5: Sample config**

In `configs/fleet.yaml`, add a commented block (adjacent to the `backends:` section; adapt placement to the file's existing comment style — read it first). IMPORTANT: write `$ {env:...}` style references carefully — `expandEnvRefs` runs on the RAW file bytes including comments, and an unset env var inside a comment would break loading (this bit us in SP2). Use a placeholder name that is safe, exactly as the existing file does for other commented secrets (check how it comments out `api_key` examples and copy that convention — if existing comments avoid `${env:...}` entirely, write `api_key: YOUR-API-KEY` instead):

```yaml
# Image backends generate agent avatars (spec §6.1). Separate from LLM backends.
# image_backends:
#   - name: dalle
#     kind: openai-image-compatible
#     base_uri: https://api.openai.com/v1
#     model: gpt-image-1
#     auth: { mode: api_key, api_key: YOUR-OPENAI-API-KEY }
#     size: 256x256            # optional; default 256x256

serve:
  # ... existing keys stay as-is; add commented examples:
  # avatar_backend: dalle      # unset => initials-SVG fallback only
  # avatars_dir: ./avatars     # default ./avatars
  # avatar_prompt: "Professional illustrated headshot of a {{.Role}} named {{.Name}}, neutral background, corporate style"
```

Verify the sample still loads: `go run ./cmd/fleet validate 2>/dev/null || true` — check what the validate/load command is named (`grep -n "Use:" cmd/fleet/main.go | head -20`) and run the appropriate one against `configs/fleet.yaml`; at minimum run the config tests again.

- [x] **Step 6: gofmt + vet + commit**

```bash
gofmt -l . && go vet ./...
git add internal/config configs/fleet.yaml
git commit -m "feat(sp4c): image_backends config and serve avatar settings"
```

---

### Task 2: internal/avatar — initials SVG fallback

**Files:**
- Create: `internal/avatar/initials.go`
- Test: `internal/avatar/initials_test.go`

- [x] **Step 1: Write the failing tests**

Create `internal/avatar/initials_test.go`:

```go
package avatar

import (
	"bytes"
	"encoding/xml"
	"strings"
	"testing"
)

func TestInitialsSVGDeterministic(t *testing.T) {
	a := InitialsSVG("Ada Lovelace")
	b := InitialsSVG("Ada Lovelace")
	if !bytes.Equal(a, b) {
		t.Error("same name produced different SVGs")
	}
	if !strings.Contains(string(a), ">AL<") {
		t.Errorf("SVG missing initials AL: %s", a)
	}
}

func TestInitialsSVGDistinctColors(t *testing.T) {
	a := string(InitialsSVG("Ada Lovelace"))
	g := string(InitialsSVG("Grace Hopper"))
	colorOf := func(svg string) string {
		i := strings.Index(svg, `fill="`)
		return svg[i+6 : i+13]
	}
	if colorOf(a) == colorOf(g) {
		t.Errorf("expected distinct palette colors, both got %s", colorOf(a))
	}
}

func TestInitialsSVGValidXML(t *testing.T) {
	for _, name := range []string{"Ada Lovelace", "solo", "", "  ", "Ünïcôde Ñame", "a & <b>"} {
		var v struct{}
		if err := xml.Unmarshal(InitialsSVG(name), &v); err != nil {
			t.Errorf("InitialsSVG(%q) produced invalid XML: %v", name, err)
		}
	}
}

func TestInitialsExtraction(t *testing.T) {
	cases := []struct{ name, want string }{
		{"Ada Lovelace", "AL"},
		{"solo", "S"},
		{"three word name", "TN"},
		{"", "?"},
		{"   ", "?"},
		{"über cool", "ÜC"},
	}
	for _, tc := range cases {
		if got := initials(tc.name); got != tc.want {
			t.Errorf("initials(%q) = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestPaletteMatchesSPA(t *testing.T) {
	// The SPA's AvatarBubble (web/src/components/AvatarBubble.tsx) uses
	// FNV-1a over code points into this exact palette; server fallback must
	// produce the same color for the same name.
	want := []string{"#e06c75", "#e5c07b", "#98c379", "#56b6c2", "#61afef", "#c678dd", "#d19a66", "#be8c6c"}
	if len(palette) != len(want) {
		t.Fatalf("palette size %d, want %d", len(palette), len(want))
	}
	for i := range want {
		if palette[i] != want[i] {
			t.Errorf("palette[%d] = %s, want %s", i, palette[i], want[i])
		}
	}
}
```

- [x] **Step 2: Run to verify failure**

Run: `go test ./internal/avatar/ -v`
Expected: FAIL to build — package/functions undefined.

- [x] **Step 3: Implement `internal/avatar/initials.go`**

```go
// Package avatar generates, stores, and serves agent avatars (spec §6.1):
// an async image-backend pipeline with a deterministic initials-SVG fallback.
package avatar

import (
	"fmt"
	"html"
	"strings"
)

// palette mirrors web/src/components/AvatarBubble.tsx exactly — the same
// FNV-1a hash and colors keep the server-generated fallback identical to the
// bubble the client renders before avatar_url is set.
var palette = []string{"#e06c75", "#e5c07b", "#98c379", "#56b6c2", "#61afef", "#c678dd", "#d19a66", "#be8c6c"}

// fnv32 is FNV-1a over the name's code points (matches the TS implementation,
// which iterates code points and uses 32-bit Math.imul).
func fnv32(name string) uint32 {
	h := uint32(2166136261)
	for _, r := range name {
		h ^= uint32(r)
		h *= 16777619
	}
	return h
}

// initials extracts 1–2 uppercase initials: first rune of the first word and
// first rune of the last word ("?" when the name is blank).
func initials(name string) string {
	parts := strings.Fields(name)
	if len(parts) == 0 {
		return "?"
	}
	first := firstRune(parts[0])
	if len(parts) == 1 {
		return strings.ToUpper(first)
	}
	return strings.ToUpper(first + firstRune(parts[len(parts)-1]))
}

func firstRune(s string) string {
	for _, r := range s {
		return string(r)
	}
	return ""
}

// InitialsSVG renders the deterministic 256×256 fallback avatar.
func InitialsSVG(name string) []byte {
	color := palette[fnv32(name)%uint32(len(palette))]
	text := html.EscapeString(initials(name))
	return []byte(fmt.Sprintf(
		`<svg xmlns="http://www.w3.org/2000/svg" width="256" height="256" viewBox="0 0 256 256">`+
			`<rect width="256" height="256" fill="%s"/>`+
			`<text x="50%%" y="50%%" dy="0.35em" text-anchor="middle" font-family="sans-serif" font-size="102" font-weight="700" fill="#ffffff">%s</text>`+
			`</svg>`, color, text))
}
```

- [x] **Step 4: Run tests**

Run: `go test ./internal/avatar/ -count=1 -v`
Expected: PASS (5 tests).

- [x] **Step 5: Commit**

```bash
gofmt -l . && go vet ./...
git add internal/avatar
git commit -m "feat(sp4c): deterministic initials-SVG avatar fallback"
```

---

### Task 3: internal/avatar — OpenAI-compatible image generator

**Files:**
- Create: `internal/avatar/generator.go`
- Test: `internal/avatar/generator_test.go`

- [x] **Step 1: Write the failing tests**

Create `internal/avatar/generator_test.go`:

```go
package avatar

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"text/template"
)

func promptTmpl(t *testing.T) *template.Template {
	t.Helper()
	tmpl, err := template.New("avatar").Parse(DefaultPrompt)
	if err != nil {
		t.Fatal(err)
	}
	return tmpl
}

func TestGenerateRequestShape(t *testing.T) {
	fakePNG := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 1, 2, 3}
	var got map[string]any
	var auth, path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		auth = r.Header.Get("Authorization")
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("bad request body: %v", err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]string{{"b64_json": base64.StdEncoding.EncodeToString(fakePNG)}},
		})
	}))
	defer srv.Close()

	g := NewOpenAIImageGenerator(srv.URL, "gpt-image-1", "sk-test", "", promptTmpl(t))
	png, err := g.Generate(context.Background(), "Ada Lovelace", "Code Reviewer")
	if err != nil {
		t.Fatal(err)
	}
	if string(png) != string(fakePNG) {
		t.Error("decoded image does not match")
	}
	if path != "/images/generations" {
		t.Errorf("path = %q, want /images/generations", path)
	}
	if auth != "Bearer sk-test" {
		t.Errorf("auth = %q, want Bearer sk-test", auth)
	}
	if got["model"] != "gpt-image-1" {
		t.Errorf("model = %v", got["model"])
	}
	if got["size"] != "256x256" {
		t.Errorf("size = %v, want default 256x256", got["size"])
	}
	if got["response_format"] != "b64_json" {
		t.Errorf("response_format = %v", got["response_format"])
	}
	if got["n"] != float64(1) {
		t.Errorf("n = %v, want 1", got["n"])
	}
	prompt, _ := got["prompt"].(string)
	if !strings.Contains(prompt, "Ada Lovelace") || !strings.Contains(prompt, "Code Reviewer") {
		t.Errorf("prompt missing name/role: %q", prompt)
	}
}

func TestGenerateNoAuthHeaderWhenKeyEmpty(t *testing.T) {
	var auth string
	hit := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = true
		auth = r.Header.Get("Authorization")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]string{{"b64_json": base64.StdEncoding.EncodeToString([]byte("x"))}},
		})
	}))
	defer srv.Close()

	g := NewOpenAIImageGenerator(srv.URL, "m", "", "512x512", promptTmpl(t))
	if _, err := g.Generate(context.Background(), "A", "B"); err != nil {
		t.Fatal(err)
	}
	if !hit {
		t.Fatal("server not hit")
	}
	if auth != "" {
		t.Errorf("unexpected Authorization header %q", auth)
	}
}

func TestGenerateNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"error":{"message":"billing hard limit reached"}}`, http.StatusForbidden)
	}))
	defer srv.Close()

	g := NewOpenAIImageGenerator(srv.URL, "m", "k", "", promptTmpl(t))
	_, err := g.Generate(context.Background(), "A", "B")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "billing hard limit") {
		t.Errorf("error should carry a body snippet: %v", err)
	}
}

func TestGenerateEmptyData(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{}})
	}))
	defer srv.Close()

	g := NewOpenAIImageGenerator(srv.URL, "m", "k", "", promptTmpl(t))
	if _, err := g.Generate(context.Background(), "A", "B"); err == nil {
		t.Fatal("expected error for empty data")
	}
}
```

- [x] **Step 2: Run to verify failure**

Run: `go test ./internal/avatar/ -run TestGenerate -v`
Expected: FAIL to build — `NewOpenAIImageGenerator`/`DefaultPrompt` undefined.

- [x] **Step 3: Implement `internal/avatar/generator.go`**

```go
package avatar

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"text/template"
	"time"
)

// Generator produces a PNG image for an agent persona.
type Generator interface {
	Generate(ctx context.Context, name, role string) ([]byte, error)
}

// DefaultPrompt is used when serve.avatar_prompt is unset.
const DefaultPrompt = "Professional illustrated headshot of a {{.Role}} named {{.Name}}, neutral background, corporate style"

const (
	defaultImageSize     = "256x256"
	imageRequestTimeout  = 60 * time.Second // generation is slow; callers are async
	maxErrorBodySnippet  = 512
	maxImageResponseSize = 16 << 20
)

// OpenAIImageGenerator calls a generic OpenAI-compatible images endpoint:
// POST {base_uri}/images/generations with response_format b64_json.
type OpenAIImageGenerator struct {
	baseURI string
	model   string
	apiKey  string // empty = no Authorization header
	size    string
	prompt  *template.Template
	client  *http.Client
}

func NewOpenAIImageGenerator(baseURI, model, apiKey, size string, prompt *template.Template) *OpenAIImageGenerator {
	if size == "" {
		size = defaultImageSize
	}
	return &OpenAIImageGenerator{
		baseURI: strings.TrimRight(baseURI, "/"),
		model:   model,
		apiKey:  apiKey,
		size:    size,
		prompt:  prompt,
		client:  &http.Client{Timeout: imageRequestTimeout},
	}
}

func (g *OpenAIImageGenerator) Generate(ctx context.Context, name, role string) ([]byte, error) {
	var prompt bytes.Buffer
	if err := g.prompt.Execute(&prompt, struct{ Name, Role string }{name, role}); err != nil {
		return nil, fmt.Errorf("render avatar prompt: %w", err)
	}
	body, err := json.Marshal(map[string]any{
		"model":           g.model,
		"prompt":          prompt.String(),
		"n":               1,
		"size":            g.size,
		"response_format": "b64_json",
	})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.baseURI+"/images/generations", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if g.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+g.apiKey)
	}
	resp, err := g.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("image API request: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxImageResponseSize))
	if err != nil {
		return nil, fmt.Errorf("image API read response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("image API %s: %s", resp.Status, truncate(raw, maxErrorBodySnippet))
	}
	var out struct {
		Data []struct {
			B64JSON string `json:"b64_json"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("image API decode response: %w", err)
	}
	if len(out.Data) == 0 || out.Data[0].B64JSON == "" {
		return nil, fmt.Errorf("image API returned no image data")
	}
	png, err := base64.StdEncoding.DecodeString(out.Data[0].B64JSON)
	if err != nil {
		return nil, fmt.Errorf("image API decode b64: %w", err)
	}
	return png, nil
}

// truncate bounds an error-body snippet (rune-safe is overkill for logs of
// JSON error bodies; bytes suffice and never split the message mid-escape).
func truncate(b []byte, n int) string {
	s := strings.TrimSpace(string(b))
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
```

- [x] **Step 4: Run tests**

Run: `go test ./internal/avatar/ -count=1`
Expected: PASS.

- [x] **Step 5: Commit**

```bash
gofmt -l . && go vet ./...
git add internal/avatar
git commit -m "feat(sp4c): openai-image-compatible avatar generator"
```

---

### Task 4: internal/avatar — atomic Store + public /avatars serving

**Files:**
- Create: `internal/avatar/store.go`
- Test: `internal/avatar/store_test.go`
- Modify: `.gitignore` (add `/avatars/` — serve creates the dir at runtime; the worktree must stay clean)

- [x] **Step 1: Write the failing tests**

Create `internal/avatar/store_test.go`:

```go
package avatar

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestStoreSaveAndOverwrite(t *testing.T) {
	dir := t.TempDir()
	s, err := NewStore(filepath.Join(dir, "avatars")) // exercises MkdirAll
	if err != nil {
		t.Fatal(err)
	}
	id := uuid.New()

	url, err := s.Save(id, "svg", []byte("<svg/>"))
	if err != nil {
		t.Fatal(err)
	}
	if url != "/avatars/"+id.String()+".svg" {
		t.Errorf("url = %q", url)
	}
	got, err := os.ReadFile(filepath.Join(dir, "avatars", id.String()+".svg"))
	if err != nil || string(got) != "<svg/>" {
		t.Fatalf("read back: %q, %v", got, err)
	}

	// Overwrite with new content.
	if _, err := s.Save(id, "svg", []byte("<svg>2</svg>")); err != nil {
		t.Fatal(err)
	}
	got, _ = os.ReadFile(filepath.Join(dir, "avatars", id.String()+".svg"))
	if string(got) != "<svg>2</svg>" {
		t.Errorf("overwrite failed: %q", got)
	}

	// No temp files left behind.
	entries, _ := os.ReadDir(filepath.Join(dir, "avatars"))
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			t.Errorf("leftover temp file %s", e.Name())
		}
	}
}

func TestStoreRejectsBadExtension(t *testing.T) {
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Save(uuid.New(), "html", []byte("x")); err == nil {
		t.Fatal("expected error for non-png/svg extension")
	}
}

func TestNewStoreFailsOnUnwritableDir(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("running as root; permissions not enforced")
	}
	parent := t.TempDir()
	locked := filepath.Join(parent, "locked")
	if err := os.MkdirAll(locked, 0o555); err != nil {
		t.Fatal(err)
	}
	if _, err := NewStore(filepath.Join(locked, "avatars")); err == nil {
		t.Fatal("expected error for unwritable dir")
	}
}

func TestMountHTTPServing(t *testing.T) {
	dir := t.TempDir()
	s, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	id := uuid.New()
	if _, err := s.Save(id, "svg", []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`)); err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	MountHTTP(mux, dir)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// Served with the right content type.
	resp, err := http.Get(srv.URL + "/avatars/" + id.String() + ".svg")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "image/svg") {
		t.Errorf("Content-Type = %q, want image/svg+xml", ct)
	}

	// Missing file → 404.
	resp, _ = http.Get(srv.URL + "/avatars/nope.png")
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("missing file = %d, want 404", resp.StatusCode)
	}

	// Traversal and dotfiles → 404 (never escape the avatars dir).
	for _, path := range []string{"/avatars/..%2f..%2fetc%2fpasswd", "/avatars/.hidden", "/avatars/"} {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+path, nil)
		resp, err := http.DefaultTransport.RoundTrip(req) // no redirect following, raw path preserved
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			t.Errorf("GET %s = 200, want non-200", path)
		}
	}
}
```

- [x] **Step 2: Run to verify failure**

Run: `go test ./internal/avatar/ -run 'TestStore|TestMount|TestNewStore' -v`
Expected: FAIL to build — `NewStore`/`MountHTTP` undefined.

- [x] **Step 3: Implement `internal/avatar/store.go`**

```go
package avatar

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
)

// Store writes avatar files atomically (temp file + rename) under one
// directory and reports their public URL paths.
type Store struct {
	dir string
}

// NewStore creates the directory (0755) and fails fast when it is not
// writable — serve startup surfaces the misconfiguration immediately.
func NewStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create avatars dir %s: %w", dir, err)
	}
	probe := filepath.Join(dir, ".write-probe")
	if err := os.WriteFile(probe, nil, 0o644); err != nil {
		return nil, fmt.Errorf("avatars dir %s not writable: %w", dir, err)
	}
	_ = os.Remove(probe)
	return &Store{dir: dir}, nil
}

// Save writes <agentID>.<ext> atomically and returns the public URL path
// (/avatars/<file>, no cache-bust — the Service appends ?v=).
func (s *Store) Save(id uuid.UUID, ext string, data []byte) (string, error) {
	if ext != "png" && ext != "svg" {
		return "", fmt.Errorf("unsupported avatar extension %q", ext)
	}
	name := id.String() + "." + ext
	tmp, err := os.CreateTemp(s.dir, ".tmp-avatar-*")
	if err != nil {
		return "", fmt.Errorf("create temp avatar: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		_ = os.Remove(tmp.Name())
		return "", fmt.Errorf("write avatar: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return "", fmt.Errorf("close avatar: %w", err)
	}
	if err := os.Rename(tmp.Name(), filepath.Join(s.dir, name)); err != nil {
		_ = os.Remove(tmp.Name())
		return "", fmt.Errorf("publish avatar: %w", err)
	}
	return "/avatars/" + name, nil
}

// MountHTTP registers the public, unauthenticated avatar route (decision #5
// in the SP4c spec: plain <img> tags must work; images are not sensitive).
// {file} is a single path element by ServeMux contract; the extra checks are
// belt and braces against dotfiles and separators.
func MountHTTP(mux *http.ServeMux, dir string) {
	mux.HandleFunc("GET /avatars/{file}", func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("file")
		if name == "" || name != filepath.Base(name) || strings.HasPrefix(name, ".") {
			http.NotFound(w, r)
			return
		}
		full := filepath.Join(dir, name)
		info, err := os.Stat(full)
		if err != nil || info.IsDir() {
			http.NotFound(w, r)
			return
		}
		http.ServeFile(w, r, full)
	})
}
```

- [x] **Step 4: Run tests + update .gitignore**

Run: `go test ./internal/avatar/ -count=1`
Expected: PASS.

Append to `.gitignore`:

```gitignore
/avatars/
```

- [x] **Step 5: Commit**

```bash
gofmt -l . && go vet ./...
git add internal/avatar .gitignore
git commit -m "feat(sp4c): atomic avatar store and public /avatars route"
```

---

### Task 5: internal/avatar — async Service + repo UpdateAvatarURL

**Files:**
- Create: `internal/avatar/service.go`
- Test: `internal/avatar/service_test.go`
- Modify: `internal/repo/agents.go` (add `UpdateAvatarURL`)

- [x] **Step 1: Write the failing tests**

Create `internal/avatar/service_test.go`:

```go
package avatar

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cbarraford/office-fleet/internal/domain"
	"github.com/google/uuid"
)

type fakeURLStore struct {
	mu   sync.Mutex
	urls map[uuid.UUID]string
	err  error
}

func newFakeURLStore() *fakeURLStore { return &fakeURLStore{urls: map[uuid.UUID]string{}} }

func (f *fakeURLStore) UpdateAvatarURL(_ context.Context, id uuid.UUID, url string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.urls[id] = url
	return nil
}

func (f *fakeURLStore) get(id uuid.UUID) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.urls[id]
}

type fakeGen struct {
	mu      sync.Mutex
	calls   int
	data    []byte
	err     error
	block   chan struct{} // when non-nil, Generate waits for a receive
}

func (g *fakeGen) Generate(_ context.Context, _, _ string) ([]byte, error) {
	g.mu.Lock()
	g.calls++
	block := g.block
	g.mu.Unlock()
	if block != nil {
		<-block
	}
	return g.data, g.err
}

func (g *fakeGen) callCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.calls
}

func testService(t *testing.T, gen Generator) (*Service, *fakeURLStore) {
	t.Helper()
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	urls := newFakeURLStore()
	svc := NewService(gen, store, urls, func(format string, args ...any) { t.Logf("svc: "+format, args...) })
	svc.now = func() time.Time { return time.Unix(1700000000, 0) }
	return svc, urls
}

func agentFor(name string) *domain.Agent {
	return &domain.Agent{ID: uuid.New(), Name: name, Role: "Tester"}
}

func TestAssignGeneratesPNG(t *testing.T) {
	gen := &fakeGen{data: []byte("png-bytes")}
	svc, urls := testService(t, gen)
	a := agentFor("Ada Lovelace")

	svc.Assign(a)
	svc.Wait()

	want := "/avatars/" + a.ID.String() + ".png?v=1700000000"
	if got := urls.get(a.ID); got != want {
		t.Errorf("avatar_url = %q, want %q", got, want)
	}
	if gen.callCount() != 1 {
		t.Errorf("generator called %d times, want 1", gen.callCount())
	}
}

func TestAssignFallsBackToSVGOnGeneratorError(t *testing.T) {
	gen := &fakeGen{err: fmt.Errorf("rate limited")}
	svc, urls := testService(t, gen)
	a := agentFor("Grace Hopper")

	svc.Assign(a)
	svc.Wait()

	if got := urls.get(a.ID); !strings.HasSuffix(got, ".svg?v=1700000000") {
		t.Errorf("avatar_url = %q, want an .svg fallback", got)
	}
}

func TestAssignNilGeneratorUsesSVG(t *testing.T) {
	svc, urls := testService(t, nil)
	a := agentFor("No Backend")

	svc.Assign(a)
	svc.Wait()

	if got := urls.get(a.ID); !strings.Contains(got, ".svg") {
		t.Errorf("avatar_url = %q, want svg", got)
	}
}

func TestAssignInFlightGuard(t *testing.T) {
	gen := &fakeGen{data: []byte("png"), block: make(chan struct{})}
	svc, _ := testService(t, gen)
	a := agentFor("Busy Agent")

	svc.Assign(a)
	svc.Assign(a) // second call while the first is blocked: must be a no-op
	gen.block <- struct{}{}
	svc.Wait()

	if gen.callCount() != 1 {
		t.Errorf("generator called %d times, want 1 (in-flight guard)", gen.callCount())
	}

	// After completion the guard clears: a new Assign generates again.
	gen.mu.Lock()
	gen.block = nil
	gen.mu.Unlock()
	svc.Assign(a)
	svc.Wait()
	if gen.callCount() != 2 {
		t.Errorf("generator called %d times after re-assign, want 2", gen.callCount())
	}
}

func TestAssignNilSafe(t *testing.T) {
	var svc *Service
	svc.Assign(agentFor("x")) // must not panic
	svc2, _ := testService(t, nil)
	svc2.Assign(nil) // must not panic
	svc2.Wait()
}

func TestSetUpload(t *testing.T) {
	svc, urls := testService(t, nil)
	id := uuid.New()

	url, err := svc.SetUpload(context.Background(), id, []byte("png-bytes"))
	if err != nil {
		t.Fatal(err)
	}
	want := "/avatars/" + id.String() + ".png?v=1700000000"
	if url != want {
		t.Errorf("url = %q, want %q", url, want)
	}
	if urls.get(id) != want {
		t.Errorf("store not updated: %q", urls.get(id))
	}
}

func TestAssignUpdateErrorIsLoggedNotFatal(t *testing.T) {
	urlsErr := newFakeURLStore()
	urlsErr.err = fmt.Errorf("db down")
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var logged []string
	svc := NewService(nil, store, urlsErr, func(format string, args ...any) {
		mu.Lock()
		logged = append(logged, fmt.Sprintf(format, args...))
		mu.Unlock()
	})
	svc.Assign(agentFor("Log Me"))
	svc.Wait()
	mu.Lock()
	defer mu.Unlock()
	if len(logged) == 0 {
		t.Error("expected the update failure to be logged")
	}
}
```

- [x] **Step 2: Run to verify failure**

Run: `go test ./internal/avatar/ -run 'TestAssign|TestSetUpload' -v`
Expected: FAIL to build — `Service` undefined.

- [x] **Step 3: Implement `internal/avatar/service.go`**

```go
package avatar

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/cbarraford/office-fleet/internal/domain"
	"github.com/google/uuid"
)

// AgentURLStore is the narrow write the async worker needs. A dedicated
// UPDATE (not a full-row Update) means a slow generation can never clobber a
// concurrent rename/PATCH.
type AgentURLStore interface {
	UpdateAvatarURL(ctx context.Context, id uuid.UUID, avatarURL string) error
}

// generateTimeout bounds one full generation attempt (the HTTP client inside
// the generator has its own 60s timeout; this is the outer belt).
const generateTimeout = 90 * time.Second

// Service orchestrates avatar generation: pick generator (image backend with
// fallback-on-error, else initials immediately) → store file → update
// avatar_url. Generation is async and never blocks or fails agent creation.
type Service struct {
	gen    Generator // nil = initials-fallback only
	store  *Store
	agents AgentURLStore
	logf   func(format string, args ...any)
	now    func() time.Time

	mu       sync.Mutex
	inFlight map[uuid.UUID]struct{}
	wg       sync.WaitGroup
}

func NewService(gen Generator, store *Store, agents AgentURLStore, logf func(format string, args ...any)) *Service {
	if logf == nil {
		logf = func(format string, args ...any) { fmt.Fprintf(os.Stderr, format+"\n", args...) }
	}
	return &Service{
		gen: gen, store: store, agents: agents, logf: logf,
		now: time.Now, inFlight: map[uuid.UUID]struct{}{},
	}
}

// Assign generates an avatar for the agent asynchronously. It never blocks
// and never reports an error to the caller (failures are logged; the agent
// always ends up with SOME avatar — §6.1 non-blocking). Safe on a nil
// receiver and nil agent. A per-agent in-flight guard makes concurrent calls
// idempotent.
func (s *Service) Assign(agent *domain.Agent) {
	if s == nil || agent == nil {
		return
	}
	s.mu.Lock()
	if _, busy := s.inFlight[agent.ID]; busy {
		s.mu.Unlock()
		return
	}
	s.inFlight[agent.ID] = struct{}{}
	s.mu.Unlock()

	// Copy what we need — the caller's pointer belongs to its request.
	id, name, role := agent.ID, agent.Name, agent.Role
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer func() {
			s.mu.Lock()
			delete(s.inFlight, id)
			s.mu.Unlock()
		}()
		s.generate(context.Background(), id, name, role)
	}()
}

// Wait blocks until all in-flight generations finish (tests; optional at
// shutdown — generations are atomic on disk, so interrupting one is safe).
func (s *Service) Wait() { s.wg.Wait() }

func (s *Service) generate(ctx context.Context, id uuid.UUID, name, role string) {
	var data []byte
	ext := "png"
	if s.gen != nil {
		genCtx, cancel := context.WithTimeout(ctx, generateTimeout)
		d, err := s.gen.Generate(genCtx, name, role)
		cancel()
		if err != nil {
			s.logf("avatar: generate for %q: %v (falling back to initials)", name, err)
		} else {
			data = d
		}
	}
	if data == nil {
		data, ext = InitialsSVG(name), "svg"
	}
	if err := s.publish(ctx, id, ext, data); err != nil {
		s.logf("avatar: %v", err)
	}
}

// publish stores the bytes and points avatar_url at them (cache-busted so
// the browser refetches after regeneration overwrites the same filename).
func (s *Service) publish(ctx context.Context, id uuid.UUID, ext string, data []byte) error {
	urlPath, err := s.store.Save(id, ext, data)
	if err != nil {
		return fmt.Errorf("store avatar for %s: %w", id, err)
	}
	busted := fmt.Sprintf("%s?v=%d", urlPath, s.now().Unix())
	if err := s.agents.UpdateAvatarURL(ctx, id, busted); err != nil {
		return fmt.Errorf("update avatar_url for %s: %w", id, err)
	}
	return nil
}

// SetUpload synchronously stores an operator-uploaded PNG and updates
// avatar_url, returning the new (cache-busted) URL.
func (s *Service) SetUpload(ctx context.Context, id uuid.UUID, png []byte) (string, error) {
	if s == nil {
		return "", fmt.Errorf("avatar service not configured")
	}
	urlPath, err := s.store.Save(id, "png", png)
	if err != nil {
		return "", err
	}
	busted := fmt.Sprintf("%s?v=%d", urlPath, s.now().Unix())
	if err := s.agents.UpdateAvatarURL(ctx, id, busted); err != nil {
		return "", err
	}
	return busted, nil
}
```

- [x] **Step 4: Add `UpdateAvatarURL` to `internal/repo/agents.go`** (after `Update`)

```go
// UpdateAvatarURL sets only avatar_url — the async avatar worker must not
// clobber concurrent full-row updates.
func (r *AgentRepo) UpdateAvatarURL(ctx context.Context, id uuid.UUID, avatarURL string) error {
	tag, err := r.db.Exec(ctx,
		"UPDATE agents SET avatar_url=$2, updated_at=now() WHERE id=$1", id, avatarURL)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("agent %s not found", id)
	}
	return nil
}
```

- [x] **Step 5: Run the full avatar suite + race detector**

Run: `go test ./internal/avatar/ -count=1 -race`
Expected: PASS, no races (the in-flight guard test exercises concurrency).

- [x] **Step 6: Commit**

```bash
gofmt -l . && go vet ./...
git add internal/avatar internal/repo
git commit -m "feat(sp4c): async avatar service with in-flight guard and fallback"
```

---

### Task 6: API — regenerate/upload handlers + create-agent hook

**Files:**
- Modify: `internal/api/api.go` (AvatarService interface, Deps, routes)
- Create: `internal/api/avatar_handlers.go`
- Test: `internal/api/avatar_handlers_test.go`
- Modify: `internal/api/entity_handlers.go` (`handleCreateAgent` hook)
- Modify: `internal/api/api_test.go` (`fakeAvatarService`)

- [x] **Step 1: Write the failing tests**

Create `internal/api/avatar_handlers_test.go`:

```go
package api

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/cbarraford/office-fleet/internal/auth"
	"github.com/cbarraford/office-fleet/internal/domain"
	"github.com/google/uuid"
)

// avatarTestAPI builds an API with agents + a fake avatar service.
func avatarTestAPI(t *testing.T, role string) (*API, *fakeAgentStore, *fakeAvatarService, string) {
	t.Helper()
	sessions := auth.NewSessions(newMemSessionStore(role))
	agents := newFakeAgentStore()
	avatars := newFakeAvatarService()
	a := New(Deps{Sessions: sessions, Agents: agents, Avatars: avatars})
	token, err := sessions.Start(context.Background(), uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	return a, agents, avatars, token
}

func avatarReq(t *testing.T, a *API, method, path, token, contentType string, body []byte) *http.Response {
	t.Helper()
	mux := http.NewServeMux()
	a.Mount(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	req, err := http.NewRequest(method, srv.URL+path, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if token != "" {
		req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: token})
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func seedAgentForAvatar(t *testing.T, agents *fakeAgentStore) *domain.Agent {
	t.Helper()
	agent := &domain.Agent{ID: uuid.New(), Name: "Ada", Role: "Reviewer", Enabled: true}
	if err := agents.Insert(context.Background(), agent); err != nil {
		t.Fatal(err)
	}
	return agent
}

var testPNG = append([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}, []byte("fake-image-data")...)

func TestRegenerateAvatar(t *testing.T) {
	a, agents, avatars, token := avatarTestAPI(t, domain.RoleAdmin)
	agent := seedAgentForAvatar(t, agents)

	resp := avatarReq(t, a, http.MethodPost, "/api/v1/agents/"+agent.ID.String()+"/avatar/regenerate", token, "", nil)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", resp.StatusCode)
	}
	if got := avatars.assignedIDs(); len(got) != 1 || got[0] != agent.ID {
		t.Errorf("Assign calls = %v, want [%s]", got, agent.ID)
	}
}

func TestRegenerateAvatarUnknownAgent(t *testing.T) {
	a, _, _, token := avatarTestAPI(t, domain.RoleAdmin)
	resp := avatarReq(t, a, http.MethodPost, "/api/v1/agents/"+uuid.NewString()+"/avatar/regenerate", token, "", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

func TestRegenerateAvatarViewerForbidden(t *testing.T) {
	a, agents, _, token := avatarTestAPI(t, domain.RoleViewer)
	agent := seedAgentForAvatar(t, agents)
	resp := avatarReq(t, a, http.MethodPost, "/api/v1/agents/"+agent.ID.String()+"/avatar/regenerate", token, "", nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.StatusCode)
	}
}

func TestUploadAvatar(t *testing.T) {
	a, agents, avatars, token := avatarTestAPI(t, domain.RoleAdmin)
	agent := seedAgentForAvatar(t, agents)

	resp := avatarReq(t, a, http.MethodPut, "/api/v1/agents/"+agent.ID.String()+"/avatar", token, "image/png", testPNG)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if got := avatars.uploadFor(agent.ID); !bytes.Equal(got, testPNG) {
		t.Errorf("uploaded bytes mismatch (got %d bytes)", len(got))
	}
}

func TestUploadAvatarRejectsNonPNG(t *testing.T) {
	a, agents, _, token := avatarTestAPI(t, domain.RoleAdmin)
	agent := seedAgentForAvatar(t, agents)

	resp := avatarReq(t, a, http.MethodPut, "/api/v1/agents/"+agent.ID.String()+"/avatar", token, "image/png", []byte("GIF89a-not-a-png"))
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestUploadAvatarRejectsWrongContentType(t *testing.T) {
	a, agents, _, token := avatarTestAPI(t, domain.RoleAdmin)
	agent := seedAgentForAvatar(t, agents)

	resp := avatarReq(t, a, http.MethodPut, "/api/v1/agents/"+agent.ID.String()+"/avatar", token, "image/jpeg", testPNG)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestUploadAvatarRejectsOversize(t *testing.T) {
	a, agents, _, token := avatarTestAPI(t, domain.RoleAdmin)
	agent := seedAgentForAvatar(t, agents)

	big := make([]byte, (1<<20)+1)
	copy(big, testPNG)
	resp := avatarReq(t, a, http.MethodPut, "/api/v1/agents/"+agent.ID.String()+"/avatar", token, "image/png", big)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestUploadAvatarUnknownAgent(t *testing.T) {
	a, _, _, token := avatarTestAPI(t, domain.RoleAdmin)
	resp := avatarReq(t, a, http.MethodPut, "/api/v1/agents/"+uuid.NewString()+"/avatar", token, "image/png", testPNG)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

func TestCreateAgentTriggersAvatarAssign(t *testing.T) {
	a, _, avatars, token := avatarTestAPI(t, domain.RoleAdmin)
	body := []byte(`{"name": "Newbie", "role": "Tester"}`)
	resp := avatarReq(t, a, http.MethodPost, "/api/v1/agents", token, "application/json", body)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", resp.StatusCode)
	}
	if got := avatars.assignedIDs(); len(got) != 1 {
		t.Errorf("Assign calls = %d, want 1 (create must fire generation)", len(got))
	}
}

func TestCreateAgentNilAvatarServiceIsSafe(t *testing.T) {
	// Existing entity tests construct the API without Avatars — creation
	// must not panic when the service is absent.
	sessions := auth.NewSessions(newMemSessionStore(domain.RoleAdmin))
	agents := newFakeAgentStore()
	a := New(Deps{Sessions: sessions, Agents: agents})
	token, err := sessions.Start(context.Background(), uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	resp := avatarReq(t, a, http.MethodPost, "/api/v1/agents", token, "application/json", []byte(`{"name":"X"}`))
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", resp.StatusCode)
	}
}
```

Add to `internal/api/api_test.go` (near the other fakes):

```go
type fakeAvatarService struct {
	mu       sync.Mutex
	assigned []uuid.UUID
	uploads  map[uuid.UUID][]byte
}

func newFakeAvatarService() *fakeAvatarService {
	return &fakeAvatarService{uploads: map[uuid.UUID][]byte{}}
}

func (f *fakeAvatarService) Assign(agent *domain.Agent) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.assigned = append(f.assigned, agent.ID)
}

func (f *fakeAvatarService) SetUpload(_ context.Context, id uuid.UUID, png []byte) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := make([]byte, len(png))
	copy(cp, png)
	f.uploads[id] = cp
	return "/avatars/" + id.String() + ".png?v=1", nil
}

func (f *fakeAvatarService) assignedIDs() []uuid.UUID {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]uuid.UUID(nil), f.assigned...)
}

func (f *fakeAvatarService) uploadFor(id uuid.UUID) []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.uploads[id]
}
```

- [x] **Step 2: Run to verify failure**

Run: `go test ./internal/api/ -run 'TestRegenerate|TestUpload|TestCreateAgentTriggers|TestCreateAgentNilAvatar' -v`
Expected: FAIL to build — `Deps.Avatars`/`fakeAvatarService` undefined.

- [x] **Step 3: Wire the interface and routes in `internal/api/api.go`**

After the `Encryptor` interface, add:

```go
// AvatarService triggers avatar generation/storage; nil means avatars are
// not wired (tests). Implemented by avatar.Service.
type AvatarService interface {
	Assign(agent *domain.Agent)
	SetUpload(ctx context.Context, id uuid.UUID, png []byte) (string, error)
}
```

Add `avatars AvatarService` to the `API` struct, `Avatars AvatarService` to `Deps`, and `avatars: d.Avatars,` in `New`.

In `authedMux()`, after the agents routes:

```go
	m.HandleFunc("POST /api/v1/agents/{id}/avatar/regenerate", a.handleRegenerateAvatar)
	m.HandleFunc("PUT /api/v1/agents/{id}/avatar", a.handleUploadAvatar)
```

- [x] **Step 4: Create `internal/api/avatar_handlers.go`**

```go
package api

import (
	"bytes"
	"errors"
	"io"
	"net/http"
)

const maxAvatarUploadBytes = 1 << 20 // 1 MiB

var pngMagic = []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}

func (a *API) handleRegenerateAvatar(w http.ResponseWriter, r *http.Request) {
	id, err := parseIDParam(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	agent, err := a.agents.GetByID(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "agent not found")
		return
	}
	if a.avatars == nil {
		writeError(w, http.StatusInternalServerError, "avatars not configured")
		return
	}
	a.avatars.Assign(agent)
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "generating"})
}

func (a *API) handleUploadAvatar(w http.ResponseWriter, r *http.Request) {
	id, err := parseIDParam(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	if _, err := a.agents.GetByID(r.Context(), id); err != nil {
		writeError(w, http.StatusNotFound, "agent not found")
		return
	}
	if a.avatars == nil {
		writeError(w, http.StatusInternalServerError, "avatars not configured")
		return
	}
	if ct := r.Header.Get("Content-Type"); ct != "" && ct != "image/png" {
		writeError(w, http.StatusBadRequest, "avatar must be uploaded as image/png")
		return
	}
	body := http.MaxBytesReader(w, r.Body, maxAvatarUploadBytes)
	data, err := io.ReadAll(body)
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeError(w, http.StatusBadRequest, "avatar must be at most 1 MiB")
			return
		}
		writeError(w, http.StatusBadRequest, "could not read upload body")
		return
	}
	if len(data) < len(pngMagic) || !bytes.Equal(data[:len(pngMagic)], pngMagic) {
		writeError(w, http.StatusBadRequest, "body must be a PNG image")
		return
	}
	if _, err := a.avatars.SetUpload(r.Context(), id, data); err != nil {
		a.logf("api: upload avatar: %v", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	agent, err := a.agents.GetByID(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "agent not found")
		return
	}
	writeJSON(w, http.StatusOK, agent)
}
```

- [x] **Step 5: Hook agent creation in `internal/api/entity_handlers.go`**

In `handleCreateAgent`, after the successful `a.agents.Insert(...)` block and before `writeJSON(w, http.StatusCreated, agent)`:

```go
	if a.avatars != nil {
		a.avatars.Assign(agent) // async per §6.1 — creation never blocks on imagery
	}
```

- [x] **Step 6: Run the API suite**

Run: `go test ./internal/api/ -count=1`
Expected: PASS (new avatar tests + entire existing suite — the nil-Avatars Deps in older tests stay valid because the hook nil-checks).

- [x] **Step 7: Commit**

```bash
gofmt -l . && go vet ./...
git add internal/api
git commit -m "feat(sp4c): avatar regenerate/upload endpoints and create-agent hook"
```

---

### Task 7: fleet serve wiring

**Files:**
- Modify: `cmd/fleet/main.go` (serve command + imports)

- [x] **Step 1: Build the avatar service in `serveCmd`**

In `cmd/fleet/main.go`, inside `serveCmd`'s `RunE`, after `inv, pipeline := buildInvoker(...)` and before the `apiSrv := api.New(...)` block, add:

```go
			// Avatars (SP4c): store + optional image generator + async service.
			avatarsDir := cfg.Serve.AvatarsDir
			if avatarsDir == "" {
				avatarsDir = "./avatars"
			}
			avatarStore, err := avatar.NewStore(avatarsDir)
			if err != nil {
				return fmt.Errorf("avatars: %w", err)
			}
			var avatarGen avatar.Generator
			if cfg.Serve.AvatarBackend != "" {
				var ib *config.ImageBackend
				for i := range cfg.ImageBackends {
					if cfg.ImageBackends[i].Name == cfg.Serve.AvatarBackend {
						ib = &cfg.ImageBackends[i]
						break
					}
				}
				if ib == nil {
					// unreachable: loadValidatedConfig enforces the reference
					return fmt.Errorf("avatar_backend %q not defined", cfg.Serve.AvatarBackend)
				}
				promptText := cfg.Serve.AvatarPrompt
				if promptText == "" {
					promptText = avatar.DefaultPrompt
				}
				promptTmpl, perr := template.New("avatar").Parse(promptText)
				if perr != nil {
					return fmt.Errorf("avatar_prompt: %w", perr) // unreachable: validated at load
				}
				avatarGen = avatar.NewOpenAIImageGenerator(ib.BaseURI, ib.Model, ib.Auth.APIKey, ib.Size, promptTmpl)
				fmt.Printf("avatar generation via image backend %q\n", ib.Name)
			}
			avatarSvc := avatar.NewService(avatarGen, avatarStore, repo.NewAgentRepo(pool), nil)
```

Add imports: `"text/template"` and `"github.com/cbarraford/office-fleet/internal/avatar"`.

- [x] **Step 2: Pass the service into the API and mount /avatars**

In the `api.New(api.Deps{...})` literal, add:

```go
				Avatars:       avatarSvc,
```

Change the handler line from:

```go
			httpSrv := &http.Server{Addr: addr, Handler: server.New(ingestor).Handler(apiSrv.Mount, web.Mount)}
```

to:

```go
			httpSrv := &http.Server{Addr: addr, Handler: server.New(ingestor).Handler(
				apiSrv.Mount,
				func(mux *http.ServeMux) { avatar.MountHTTP(mux, avatarsDir) },
				web.Mount,
			)}
```

(`web.Mount` registers bare `/` which is lowest precedence regardless of order, but keep it last for readability.)

- [x] **Step 3: Build + full suite**

Run: `go build ./... && go test ./... -count=1`
Expected: PASS. (Note: `*avatar.Service` is always non-nil in serve — no typed-nil interface hazard here, unlike the SP4a Encryptor case where a nil pointer was conditionally assigned.)

- [x] **Step 4: Commit**

```bash
gofmt -l . && go vet ./...
git add cmd/fleet/main.go
git commit -m "feat(sp4c): wire avatar service, /avatars route into fleet serve"
```

---

### Task 8: SPA — client putRaw + AgentDetail avatar controls

**Files:**
- Modify: `web/src/api/client.ts` (raw-body helper)
- Test: `web/src/api/client.test.ts`
- Modify: `web/src/pages/AgentDetail.tsx` (Regenerate + Upload controls, ~5s poll)

- [x] **Step 1: Write the failing client test**

Append to the `describe('api client', ...)` block in `web/src/api/client.test.ts`:

```ts
  it('putRaw sends the body verbatim with the given content type', async () => {
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse(200, { ok: true }))
    vi.stubGlobal('fetch', fetchMock)

    const blob = new Uint8Array([0x89, 0x50, 0x4e, 0x47])
    await api.putRaw('/api/v1/agents/x/avatar', blob, 'image/png')
    const init = fetchMock.mock.calls[0][1] as RequestInit
    expect(init.method).toBe('PUT')
    expect((init.headers as Record<string, string>)['Content-Type']).toBe('image/png')
    expect(init.body).toBe(blob)
    expect(init.credentials).toBe('same-origin')
  })

  it('putRaw surfaces error envelopes like JSON requests', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(jsonResponse(400, { error: 'body must be a PNG image' })))

    const err = await api.putRaw('/api/v1/agents/x/avatar', new Uint8Array([1]), 'image/png').then(
      () => null,
      (e: unknown) => e,
    )
    expect(err).toBeInstanceOf(ApiError)
    expect((err as ApiError).message).toBe('body must be a PNG image')
  })
```

- [x] **Step 2: Run to verify failure**

Run: `cd web && NODE_OPTIONS= npx vitest run src/api/client.test.ts`
Expected: FAIL — `putRaw` is not a function.

- [x] **Step 3: Implement `putRaw` in `web/src/api/client.ts`**

Refactor so JSON and raw requests share the response handling. Replace the `request` function and `api` export with:

```ts
async function handle<T>(res: Response): Promise<T> {
  if (!res.ok) {
    let msg = `request failed (${res.status})`
    try {
      const data: unknown = await res.json()
      if (data && typeof data === 'object' && typeof (data as { error?: unknown }).error === 'string') {
        msg = (data as { error: string }).error
      }
    } catch {
      // non-JSON error body: keep the generic message
    }
    if (res.status === 401) {
      cfg.onUnauthorized()
      throw new ApiError(401, msg === `request failed (401)` ? 'authentication required' : msg)
    }
    throw new ApiError(res.status, msg)
  }
  return res.json() as Promise<T>
}

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  const init: RequestInit = { method, credentials: 'same-origin' }
  if (body !== undefined) {
    init.headers = { 'Content-Type': 'application/json' }
    init.body = JSON.stringify(body)
  }
  return handle<T>(await fetch(path, init))
}

async function requestRaw<T>(method: string, path: string, body: BodyInit, contentType: string): Promise<T> {
  return handle<T>(
    await fetch(path, {
      method,
      credentials: 'same-origin',
      headers: { 'Content-Type': contentType },
      body,
    }),
  )
}

export const api = {
  get: <T>(path: string) => request<T>('GET', path),
  post: <T>(path: string, body?: unknown) => request<T>('POST', path, body),
  patch: <T>(path: string, body: unknown) => request<T>('PATCH', path, body),
  put: <T>(path: string, body: unknown) => request<T>('PUT', path, body),
  del: <T>(path: string) => request<T>('DELETE', path),
  putRaw: <T>(path: string, body: BodyInit, contentType: string) => requestRaw<T>('PUT', path, body, contentType),
}
```

IMPORTANT: preserve the current 401 semantics exactly (the existing tests pin them — the error-envelope message wins over the generic 'authentication required' fallback).

- [x] **Step 4: Run the client tests**

Run: `cd web && NODE_OPTIONS= npx vitest run src/api/`
Expected: PASS (all client + sse tests, including the two new ones).

- [x] **Step 5: Add the controls to `web/src/pages/AgentDetail.tsx`**

Add imports: change the react import to include `useRef` and `type ChangeEvent`:

```tsx
import { useCallback, useEffect, useMemo, useRef, useState, type ChangeEvent, type FormEvent } from 'react'
```

Inside the `AgentDetail` component (after the `toggleAssignment` handler), add:

```tsx
  const fileInput = useRef<HTMLInputElement>(null)

  const regenerateAvatar = async () => {
    if (!detail) return
    try {
      await api.post(`/api/v1/agents/${detail.agent.id}/avatar/regenerate`)
      toast('info', 'avatar generating — refreshing shortly…')
      window.setTimeout(load, 5000) // §6.1: async generation; poll once after ~5s
    } catch (err) {
      toast('error', err instanceof ApiError ? err.message : 'regenerate failed')
    }
  }

  const uploadAvatar = async (e: ChangeEvent<HTMLInputElement>) => {
    const file = e.target.files?.[0]
    e.target.value = '' // allow re-selecting the same file later
    if (!file || !detail) return
    if (file.type !== 'image/png') {
      toast('error', 'avatar must be a PNG')
      return
    }
    if (file.size > 1024 * 1024) {
      toast('error', 'avatar must be at most 1 MiB')
      return
    }
    try {
      await api.putRaw(`/api/v1/agents/${detail.agent.id}/avatar`, file, 'image/png')
      load()
    } catch (err) {
      toast('error', err instanceof ApiError ? err.message : 'upload failed')
    }
  }
```

In the header JSX, next to the existing Pause/Resume button (inside the `{isAdmin && (...)}` area — restructure that conditional to hold all three controls):

```tsx
        {isAdmin && (
          <>
            <button className="small" onClick={regenerateAvatar}>
              Regenerate avatar
            </button>
            <button className="small" onClick={() => fileInput.current?.click()}>
              Upload avatar
            </button>
            <input ref={fileInput} type="file" accept="image/png" style={{ display: 'none' }} onChange={uploadAvatar} />
            <button className="small" onClick={toggleAgent}>
              {agent.enabled ? 'Pause' : 'Resume'}
            </button>
          </>
        )}
```

- [x] **Step 6: Gate + commit**

```bash
cd web && NODE_OPTIONS= npm run build && NODE_OPTIONS= npm run test
cd .. && git add web/src
git commit -m "feat(sp4c): avatar regenerate/upload controls and raw-body client helper"
```

---

### Task 9: Final gate

**Files:** none new — verification only (plus fixes it surfaces).

- [x] **Step 1: Full automated gate**

```bash
NODE_OPTIONS= make test          # go test ./... + tsc + vitest
gofmt -l .                        # nothing
go vet ./...                      # clean
go test ./internal/avatar/ -race -count=1
git diff --stat 04467fd -- go.mod go.sum   # 04467fd = last SP4b commit (pre-SP4c base); must be EMPTY
```

- [x] **Step 2: Worktree invariant**

```bash
NODE_OPTIONS= make build && git status --short   # nothing (avatars/, fleet, dist all ignored)
```

- [x] **Step 3: Acceptance criteria spot-check (map to spec §8)**

1. No-backend create → initials SVG + populated avatar_url: covered by `TestAssignNilGeneratorUsesSVG` + `TestCreateAgentTriggersAvatarAssign`.
2. Backend create/regenerate → PNG + cache-busted URL; API failure → SVG fallback + log: `TestAssignGeneratesPNG`, `TestAssignFallsBackToSVGOnGeneratorError`, generator httptest suite.
3. Upload replaces (sniffed, capped): `TestUploadAvatar*`.
4. `/avatars/` public + traversal-safe: `TestMountHTTPServing` (+ SPA renders via existing AvatarBubble).
5. Regenerate admin-only, 202, idempotent under concurrency: `TestRegenerateAvatarViewerForbidden`, `TestRegenerateAvatar`, `TestAssignInFlightGuard`.
6. Config validation + sample yaml + suites green + zero new deps: Task 1 table, Step 1 gates.

Live-browser smoke against a real image backend stays in the deferred dogfood pass (no local Postgres in this environment) — note it in the final report.

- [x] **Step 4: Commit any fixes**

```bash
git add -A && git status --short   # review carefully
git commit -m "test(sp4c): final gate fixes"   # only if there were fixes
```

---

## Self-review checklist

- **Spec coverage:** §3 config → Task 1; §4 Generator/InitialsAvatar/Store/Service → Tasks 2–5; §5 API & serving (routes table, create hook, serve wiring) → Tasks 6–7; §6 error handling (fallbacks, 400s, fail-fast dir, 404s, traversal) → Tasks 4–6 tests + NewStore probe; §7 testing strategy → mirrored per task (initials golden/determinism, generator httptest, store atomicity, service fakes + in-flight, API matrix, config table, SPA tsc/build gate); §8 ACs → Task 9 map.
- **Type consistency:** `Generator.Generate(ctx, name, role) ([]byte, error)` used by fakeGen + OpenAIImageGenerator + Service; `Store.Save(id uuid.UUID, ext string, data []byte) (string, error)`; `AgentURLStore.UpdateAvatarURL(ctx, id, url)` matches repo method; api.AvatarService {Assign(*domain.Agent), SetUpload(ctx, uuid.UUID, []byte) (string, error)} matches avatar.Service's method set (Assign + SetUpload — Wait is extra on the concrete type, fine for interface satisfaction).
- **Spec deviation (intentional, recorded):** spec §2 names the fallback `InitialsAvatar(name) []byte`; the plan names it `InitialsSVG` (clearer about output format). Same contract.
- **Known simplification:** SPA grid does not poll after create (client-side initials bubble shows until the next natural refetch); only the detail page polls post-regenerate, per spec §2 Goals wording.


