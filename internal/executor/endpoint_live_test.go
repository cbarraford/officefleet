package executor

import (
	"context"
	"flag"
	"os"
	"testing"

	"github.com/cbarraford/office-fleet/internal/config"
)

var liveOllamaFlag = flag.Bool("live-ollama", false, "run live smoke against a local Ollama (needs a tool-capable model)")

// TestEndpointExecutor_LiveOllamaSmoke exercises the real loop against a local
// Ollama. Requires `ollama serve` and a function-calling-capable model.
// Run: go test ./internal/executor/ -run LiveOllama -live-ollama -v
// Override the model with OLLAMA_MODEL (default llama3.1).
func TestEndpointExecutor_LiveOllamaSmoke(t *testing.T) {
	if !*liveOllamaFlag {
		t.Skip("skipping live test; pass -live-ollama to enable")
	}
	model := os.Getenv("OLLAMA_MODEL")
	if model == "" {
		model = "llama3.1"
	}
	ex, err := NewEndpointExecutor(&config.Backend{
		Name:    "live-ollama",
		Kind:    "openai-compatible",
		BaseURI: "http://localhost:11434/v1",
		Model:   model,
		Auth:    config.BackendAuth{Mode: "none"},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := ex.Run(context.Background(), LLMRequest{
		Prompt:    "Create a file named hello.txt containing the word hello, then submit a result summarizing what you did.",
		Workspace: t.TempDir(),
		Model:     model,
	})
	if err != nil {
		t.Fatalf("live run failed: %v", err)
	}
	if result.Summary == "" {
		t.Error("expected non-empty summary")
	}
	t.Logf("status=%d tokens=%d summary=%s", result.Status, result.Tokens, result.Summary)
}
