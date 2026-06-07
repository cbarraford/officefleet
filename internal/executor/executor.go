package executor

import (
	"context"

	"github.com/cbarraford/office-fleet/internal/domain"
)

// LLMRequest is the input to an Executor.Run call.
type LLMRequest struct {
	SystemPrompt string
	Prompt       string
	Workspace    string
	Tools        []string // CLI names expected on PATH; sufficient for CLI agentic backends
	Model        string   // resolved: BackendRef.Model ?? Backend.Model
	Effort       string   // resolved: BackendRef.Effort ?? Backend.DefaultEffort
}

// Executor performs a Run given a resolved Backend.
// SP1 ships one impl: the claude CLI agentic backend.
// Endpoint backends + the generic agent loop are SP2; this interface is shaped so they slot in.
type Executor interface {
	Kind() string
	Run(ctx context.Context, req LLMRequest) (domain.LLMResult, error)
}
