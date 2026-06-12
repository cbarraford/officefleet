package executor

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/cbarraford/office-fleet/internal/config"
)

func TestFromBackend_Claude(t *testing.T) {
	cfg := &config.Config{}
	exec, err := FromBackend(cfg, &config.Backend{
		Name: "c", Kind: "claude",
		Auth: config.BackendAuth{Mode: "api_key", APIKey: "sk-x"},
	})
	if err != nil {
		t.Fatal(err)
	}
	ce, ok := exec.(*ClaudeExecutor)
	if !ok {
		t.Fatalf("got %T, want *ClaudeExecutor", exec)
	}
	if ce.APIKey != "sk-x" {
		t.Errorf("APIKey = %q", ce.APIKey)
	}
}

func TestFromBackend_ClaudeSubscriptionNoKey(t *testing.T) {
	exec, err := FromBackend(&config.Config{}, &config.Backend{
		Name: "c", Kind: "claude", Auth: config.BackendAuth{Mode: "subscription"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if exec.(*ClaudeExecutor).APIKey != "" {
		t.Error("subscription backend must not set an API key")
	}
}

func TestFromBackend_SecretRefRequiresLookup(t *testing.T) {
	_, err := FromBackend(&config.Config{}, &config.Backend{
		Name: "c", Kind: "claude",
		Auth: config.BackendAuth{Mode: "api_key", APIKey: "${secret:anthropic_key}"},
	})
	if err == nil {
		t.Fatal("expected missing lookup error")
	}
	if !strings.Contains(err.Error(), "requires a secret lookup") {
		t.Fatalf("err = %v, want missing lookup error", err)
	}
}

func TestFromBackendWithSecrets_ClaudeAPIKeySecretRef(t *testing.T) {
	backend := &config.Backend{
		Name: "c", Kind: "claude",
		Auth: config.BackendAuth{Mode: "api_key", APIKey: "${secret:anthropic_key}"},
	}
	exec, err := FromBackendWithSecrets(context.Background(), &config.Config{}, backend, func(_ context.Context, name string) (string, error) {
		if name != "anthropic_key" {
			t.Fatalf("lookup name = %q, want anthropic_key", name)
		}
		return "sk-resolved", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	ce, ok := exec.(*ClaudeExecutor)
	if !ok {
		t.Fatalf("got %T, want *ClaudeExecutor", exec)
	}
	if ce.APIKey != "sk-resolved" {
		t.Fatalf("APIKey = %q, want resolved secret", ce.APIKey)
	}
	if strings.Contains(ce.APIKey, "${secret:") {
		t.Fatalf("secret ref reached Claude executor: %q", ce.APIKey)
	}
	if backend.Auth.APIKey != "${secret:anthropic_key}" {
		t.Fatalf("backend config mutated to %q", backend.Auth.APIKey)
	}
}

func TestFromBackend_Endpoint(t *testing.T) {
	exec, err := FromBackend(&config.Config{}, &config.Backend{
		Name: "e", Kind: "openai-compatible", BaseURI: "http://localhost:11434/v1",
		Model: "llama3.1", Auth: config.BackendAuth{Mode: "none"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := exec.(*EndpointExecutor); !ok {
		t.Fatalf("got %T, want *EndpointExecutor", exec)
	}
}

func TestFromBackendWithSecrets_EndpointAPIKeySecretRef(t *testing.T) {
	exec, err := FromBackendWithSecrets(context.Background(), &config.Config{}, &config.Backend{
		Name: "e", Kind: "openai-compatible", BaseURI: "http://localhost:11434/v1",
		Model: "llama3.1", Auth: config.BackendAuth{Mode: "api_key", APIKey: "${secret:openai_key}"},
	}, func(_ context.Context, name string) (string, error) {
		if name != "openai_key" {
			t.Fatalf("lookup name = %q, want openai_key", name)
		}
		return "endpoint-secret", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	ee, ok := exec.(*EndpointExecutor)
	if !ok {
		t.Fatalf("got %T, want *EndpointExecutor", exec)
	}
	if ee.APIKey != "endpoint-secret" {
		t.Fatalf("APIKey = %q, want resolved secret", ee.APIKey)
	}
}

func TestFromBackendWithSecrets_MissingSecretFails(t *testing.T) {
	_, err := FromBackendWithSecrets(context.Background(), &config.Config{}, &config.Backend{
		Name: "e", Kind: "openai-compatible", BaseURI: "http://localhost:11434/v1",
		Model: "llama3.1", Auth: config.BackendAuth{Mode: "api_key", APIKey: "${secret:missing_key}"},
	}, func(context.Context, string) (string, error) {
		return "", errors.New("not found")
	})
	if err == nil {
		t.Fatal("expected secret lookup error")
	}
	if !strings.Contains(err.Error(), "missing_key") {
		t.Fatalf("error = %v, want secret name", err)
	}
}

func TestFromBackend_UnknownKind(t *testing.T) {
	_, err := FromBackend(&config.Config{}, &config.Backend{Name: "x", Kind: "carrier-pigeon"})
	if err == nil || !strings.Contains(err.Error(), "carrier-pigeon") {
		t.Fatalf("err = %v", err)
	}
}

func TestFromBackend_Voter(t *testing.T) {
	cfg := &config.Config{
		Backends: []config.Backend{
			{Name: "c", Kind: "claude", Auth: config.BackendAuth{Mode: "subscription"}, Model: "claude-x", DefaultEffort: "high"},
			{Name: "e", Kind: "openai-compatible", BaseURI: "http://x/v1", Model: "llama3.1", Auth: config.BackendAuth{Mode: "none"}},
		},
	}
	exec, err := FromBackend(cfg, &config.Backend{
		Name: "panel", Kind: "voter", Strategy: "majority", Panel: []string{"c", "e"},
	})
	if err != nil {
		t.Fatal(err)
	}
	v, ok := exec.(*VotingExecutor)
	if !ok {
		t.Fatalf("got %T", exec)
	}
	if len(v.Panel) != 2 {
		t.Fatalf("panel size = %d", len(v.Panel))
	}
	if v.Panel[0].Model != "claude-x" || v.Panel[0].Effort != "high" {
		t.Errorf("member 0 model/effort = %q/%q", v.Panel[0].Model, v.Panel[0].Effort)
	}
	if v.Panel[1].Model != "llama3.1" {
		t.Errorf("member 1 model = %q", v.Panel[1].Model)
	}
}

func TestFromBackendWithSecrets_VoterPanelSecretRef(t *testing.T) {
	cfg := &config.Config{
		Backends: []config.Backend{
			{Name: "c", Kind: "claude", Auth: config.BackendAuth{Mode: "api_key", APIKey: "${secret:anthropic_key}"}},
		},
	}
	exec, err := FromBackendWithSecrets(context.Background(), cfg, &config.Backend{
		Name: "panel", Kind: "voter", Strategy: "first_success", Panel: []string{"c"},
	}, func(_ context.Context, name string) (string, error) {
		if name != "anthropic_key" {
			t.Fatalf("lookup name = %q, want anthropic_key", name)
		}
		return "panel-secret", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	v := exec.(*VotingExecutor)
	if got := v.Panel[0].Exec.(*ClaudeExecutor).APIKey; got != "panel-secret" {
		t.Fatalf("panel APIKey = %q, want resolved secret", got)
	}
}

func TestFromBackend_VoterUnknownMember(t *testing.T) {
	_, err := FromBackend(&config.Config{}, &config.Backend{
		Name: "panel", Kind: "voter", Strategy: "majority", Panel: []string{"ghost"},
	})
	if err == nil || !strings.Contains(err.Error(), "ghost") {
		t.Fatalf("err = %v", err)
	}
}

func TestFromBackend_VoterBadStrategy(t *testing.T) {
	cfg := &config.Config{
		Backends: []config.Backend{
			{Name: "c", Kind: "claude", Auth: config.BackendAuth{Mode: "subscription"}},
		},
	}
	_, err := FromBackend(cfg, &config.Backend{
		Name: "panel", Kind: "voter", Strategy: "consensus", Panel: []string{"c"},
	})
	if err == nil || !strings.Contains(err.Error(), "consensus") {
		t.Fatalf("err = %v, want unknown-strategy error", err)
	}
}
