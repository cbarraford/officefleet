// Package agentloop implements OfficeFleet's generic tool-using agent loop for
// endpoint backends (raw chat/completions APIs with no built-in agent loop).
// The package is pure orchestration: all I/O is injected via ChatClient
// (transport) and ToolBridge (effects), so the loop is fully testable with fakes.
package agentloop

import (
	"context"

	"github.com/cbarraford/office-fleet/internal/domain"
)

// Message is one chat message in the provider-agnostic shape the loop works with.
type Message struct {
	Role       string     `json:"role"` // "system" | "user" | "assistant" | "tool"
	Content    string     `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`   // assistant messages carrying tool calls
	ToolCallID string     `json:"tool_call_id,omitempty"` // tool messages: which call this observes
}

// ToolSpec describes one tool offered to the model.
type ToolSpec struct {
	Name        string
	Description string
	Parameters  map[string]any // JSON schema for the arguments object
}

// ToolCall is one tool invocation requested by the model.
type ToolCall struct {
	ID   string         `json:"id"`
	Name string         `json:"name"`
	Args map[string]any `json:"args,omitempty"`
	// ArgsError is set by the transport when the provider's argument payload
	// could not be decoded (e.g. malformed JSON). The loop feeds it back to
	// the model as an observation instead of dispatching to the bridge.
	ArgsError string `json:"args_error,omitempty"`
}

// ChatRequest is one round-trip request to the model.
type ChatRequest struct {
	Model    string
	Messages []Message
	Tools    any            // protocol-encoded tool specs (provider wire shape)
	Params   map[string]any // backend params passthrough (e.g. num_ctx)
}

// Usage reports token consumption for one round-trip.
type Usage struct {
	PromptTokens     int
	CompletionTokens int
}

// ChatResponse is the model's reply to one ChatRequest.
type ChatResponse struct {
	Message Message
	Usage   Usage
}

// ChatClient performs one chat round-trip. Implemented by agentloop/openai.
type ChatClient interface {
	Chat(ctx context.Context, req ChatRequest) (ChatResponse, error)
}

// ToolBridge executes tool calls. Implemented by agentloop/bridge.
type ToolBridge interface {
	// Specs lists the tools offered to the model, including submit_result.
	Specs() []ToolSpec
	// Execute runs one tool call. For submit_result it returns done=true and
	// the finished result. Tool-level failures (bad command, missing file,
	// denial) are returned as the observation string with a nil error; a
	// non-nil error means the bridge itself is broken and fails the run.
	Execute(ctx context.Context, call ToolCall) (observation string, done bool, result *domain.LLMResult, err error)
}

// ToolProtocol encodes tool specs into the provider request and decodes tool
// calls out of the response. Only the native openai function-calling
// implementation ships in SP2; a text/ReAct adapter can slot in later.
type ToolProtocol interface {
	Encode(specs []ToolSpec) any
	Decode(resp ChatResponse) []ToolCall
}

// Opts configures one loop run.
type Opts struct {
	Model string
	// Params are passed through to the provider request body. The keys
	// "model", "messages", and "tools" are reserved and ignored by the transport.
	Params        map[string]any
	MaxIterations int // <=0 means DefaultMaxIterations
}

// DefaultMaxIterations bounds the loop when the backend config does not set it.
const DefaultMaxIterations = 25
