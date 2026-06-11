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

// truncate bounds an error-body snippet (bytes suffice for JSON error logs).
func truncate(b []byte, n int) string {
	s := strings.TrimSpace(string(b))
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
