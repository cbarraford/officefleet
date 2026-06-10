package executor

import (
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

func TestFromBackend_UnknownKind(t *testing.T) {
	_, err := FromBackend(&config.Config{}, &config.Backend{Name: "x", Kind: "carrier-pigeon"})
	if err == nil || !strings.Contains(err.Error(), "carrier-pigeon") {
		t.Fatalf("err = %v", err)
	}
}
