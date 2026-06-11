# SP4c — Agent Avatars (Image Backend + Fallback) — Design Spec

**Status:** Draft for review
**Date:** 2026-06-11
**Author:** brainstormed with Claude Code
**Parent:** `spec.md` §6.1 (avatar generation); SP4a (schema columns `avatar_url`/`hired_at`
already exist); SP4b (the SPA's AvatarBubble renders `avatar_url` when set, initials
otherwise — SP4c adds the generation pipeline and the regenerate/upload controls' endpoints).

---

## 1. Summary

SP4c gives agents a visual identity per §6.1: a headshot generated asynchronously by a
configured **image backend** (the named-backend pattern, kept separate from LLM backends),
stored as a file under a configurable avatars directory and served at `/avatars/<id>.png`,
with a deterministic **initials-SVG fallback** when no backend is configured or generation
fails. Operators can regenerate or upload an override. Agent creation never blocks on image
generation.

### Decisions locked during brainstorming

1. **Separate `image_backends:` config list** (not mixed into `backends:` — avoids tangling
   LLM validation such as voter rules). v1 ships ONE kind: **`openai-image-compatible`**
   (generic `POST {base_uri}/images/generations`, b64 response — covers OpenAI's image API
   and compatible local servers). Stability/Flux-specific kinds are deferred.
2. **`serve.avatar_backend`** names the backend to use; unset → fallback-only mode.
3. **Initials fallback** is server-generated deterministic SVG (color derived from a name
   hash) written to the same avatars directory — the UI never needs special-casing beyond
   "render avatar_url".
4. **Async generation** (§6.1): create/regenerate return immediately; the UI polls (no new
   SSE event kind this slice).
5. **`/avatars/` is served unauthenticated** (like the SPA's static assets) so plain `<img>`
   tags work; avatar images are not sensitive. Recorded explicitly.

---

## 2. Goals & non-goals

### Goals
- `image_backends:` config + validation; `serve.avatar_backend` + `serve.avatars_dir`
  (default `./avatars`) + `serve.avatar_prompt` (template, `{{.Name}}`/`{{.Role}}`).
- `internal/avatar`: `Generator` interface, the OpenAI-compatible generator, the initials
  SVG fallback, and the async orchestration (generate → write file → update `avatar_url`).
- API: `POST /api/v1/agents/{id}/avatar/regenerate` (admin, 202) and
  `PUT /api/v1/agents/{id}/avatar` (admin, PNG upload ≤ 1 MiB, replaces the file, sets
  `avatar_url`).
- Hook into agent creation: when the API creates an agent, generation fires asynchronously
  (backend configured → image; else fallback SVG immediately).
- Serving: `/avatars/{file}` static file route in serve.
- SPA additions: Regenerate + Upload controls on the agent detail page; AvatarBubble already
  renders `avatar_url` (SP4b) — detail view polls the agent once ~5s after create/regenerate.

### Non-goals (SP4c)
- Stability/Flux/dedicated-provider kinds (deferred; the generic kind covers compatible
  endpoints).
- SSE `agent_updated` events (polling suffices this slice).
- Image moderation/validation beyond PNG sniffing + size cap on upload.
- Avatars for anything other than agents.
- CLI-driven generation (`fleet` gains no avatar commands; this is a UI feature).

---

## 3. Config (`internal/config`)

```yaml
image_backends:
  - name: dalle
    kind: openai-image-compatible
    base_uri: https://api.openai.com/v1
    model: gpt-image-1
    auth: { mode: api_key, api_key: ${env:OPENAI_API_KEY} }
    size: 256x256            # optional; default 256x256

serve:
  avatar_backend: dalle      # optional; unset => initials fallback only
  avatars_dir: ./avatars     # optional; default ./avatars
  avatar_prompt: "Professional illustrated headshot of a {{.Role}} named {{.Name}}, neutral background, corporate style"  # optional; default exactly this
```

Validation: `image_backends` names unique; kind ∈ {`openai-image-compatible`}; base_uri +
model required; auth mode ∈ {api_key, none}; `serve.avatar_backend`, when set, must name a
defined image backend; `avatar_prompt`, when set, must parse as a Go text/template.

---

## 4. `internal/avatar`

```go
// Generator produces a PNG image for an agent persona.
type Generator interface {
	Generate(ctx context.Context, name, role string) ([]byte, error)
}
```

- **`OpenAIImageGenerator`** — renders the prompt template, `POST {base_uri}/images/generations`
  with `{model, prompt, n: 1, size, response_format: "b64_json"}`, bearer auth when api_key
  mode, decodes `data[0].b64_json`. Non-2xx → error with bounded body snippet; 60s request
  timeout (image generation is slow; the call is already async).
- **`InitialsAvatar(name string) []byte`** — deterministic SVG: 1–2 initials extracted from
  the name, background color picked from a fixed palette by FNV hash of the name, white
  text, 256×256. Written with an `.svg` extension and served with `image/svg+xml`.
- **`Store`** — writes `<agent_id>.png` / `<agent_id>.svg` atomically (temp file + rename)
  under `avatars_dir` (created with 0755 on startup), returns the public URL path
  (`/avatars/<file>`). Regeneration/upload overwrites; the URL is stable per format, and the
  agent's `avatar_url` update is what the UI keys off (cache-busted with `?v=<unix>` query
  appended by the API when it updates the field).
- **`Service`** — orchestrates: `Assign(agent)` runs in a goroutine: pick generator (backend
  configured → image generator with fallback-on-error; else fallback immediately) → store
  file → `agents.Update` with the new `avatar_url`. Errors are logged, never propagated to
  the creating request (§6.1 non-blocking). A per-agent in-flight guard (mutex + set)
  prevents concurrent double-generation.

---

## 5. API & serving

| Route | Method | Behavior |
|---|---|---|
| `/api/v1/agents/{id}/avatar/regenerate` | POST | admin; 404 unknown agent; 202 `{status: "generating"}`; fires Service.Assign async |
| `/api/v1/agents/{id}/avatar` | PUT | admin; body raw PNG (Content-Type image/png, ≤ 1 MiB, magic-sniffed); stores + updates avatar_url; 200 with the agent |
| `/avatars/{file}` | GET | unauthenticated static file serve from avatars_dir (no directory listing; name sanitized to a single path element) |

- `handleCreateAgent` (SP4a) gains one line after Insert: `avatarSvc.Assign(agent)` (nil-safe —
  serve wires the service; tests pass nil).
- serve wiring: build the Service from config (generator per `avatar_backend`, store from
  `avatars_dir`), mount the file route, pass the service into `api.Deps`.

## 6. Error handling

| Failure | Behavior |
|---|---|
| No image backend configured | fallback SVG generated immediately on create/regenerate |
| Image API error/timeout | logged; fallback SVG written (agent always ends with SOME avatar); regenerate retries the image path |
| Upload wrong type/too large | 400 with reason |
| avatars_dir unwritable | serve startup error (fail fast) |
| Unknown agent on regenerate/upload | 404 |
| Path traversal on /avatars/{file} | sanitized; 404 |

## 7. Testing strategy

- **Unit — initials SVG:** deterministic output for a name (golden), distinct colors for
  distinct names, valid XML, 1–2 initials logic (single word, two words, unicode).
- **Unit — OpenAI generator:** httptest — request shape (model/prompt/size/b64 flag, bearer),
  b64 decode, non-2xx error, prompt template rendering with name/role.
- **Unit — Store:** atomic write, overwrite, URL path, traversal-safe filenames.
- **Unit — Service:** fake generator + fake agent store: async assign updates avatar_url;
  image-error falls back to SVG; in-flight guard prevents double-generation; nil service
  no-ops in handleCreateAgent.
- **API:** regenerate 202/404 + admin-only; upload happy/wrong-type/oversize/404; /avatars
  static route traversal tests.
- **Config:** validation table for image_backends + serve fields.
- **SPA:** Regenerate/Upload controls call the endpoints; tsc+build gate as in SP4b.

## 8. Acceptance criteria

1. With no image backend configured, creating an agent via the API yields an initials SVG at
   `/avatars/<id>.svg` and a populated `avatar_url` within ~1s (async), with no impact on the
   create response time.
2. With an `openai-image-compatible` backend configured (httptest in CI), create/regenerate
   produce a PNG at `/avatars/<id>.png` and update `avatar_url` (cache-busted); image-API
   failure falls back to the SVG and logs.
3. Upload replaces the avatar (PNG sniffed, ≤1 MiB enforced) and updates `avatar_url`.
4. `/avatars/` serves files publicly, rejects traversal, and the SPA renders generated
   avatars in the directory grid and detail header.
5. Regenerate is admin-only (viewer 403), 202s immediately, and is idempotent under
   concurrent calls (in-flight guard).
6. Config validation covers the §3 rules; sample fleet.yaml gains commented examples; all
   existing suites pass; gofmt/vet clean; zero new Go module dependencies.

## 9. Open questions / deferred

- Stability/Flux dedicated kinds; image style presets beyond the prompt template.
- SSE `agent_updated` push (polling suffices).
- Avatar cleanup on agent delete (files orphan harmlessly; revisit with retention work).
- Serving avatars behind auth (public by decision #5; revisit if personas become sensitive).
