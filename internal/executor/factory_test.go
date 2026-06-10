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
