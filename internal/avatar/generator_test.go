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
