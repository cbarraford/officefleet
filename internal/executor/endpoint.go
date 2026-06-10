package executor

import (
	"context"
	"fmt"
	"time"

	"github.com/cbarraford/office-fleet/internal/agentloop"
	"github.com/cbarraford/office-fleet/internal/agentloop/bridge"
	"github.com/cbarraford/office-fleet/internal/agentloop/openai"
	"github.com/cbarraford/office-fleet/internal/config"
	"github.com/cbarraford/office-fleet/internal/domain"
)

// EndpointExecutor drives an openai-compatible chat endpoint through
// OfficeFleet's generic agent loop. Cost is always 0 in SP2 (no price table).
type EndpointExecutor struct {
	BaseURI       string
	APIKey        string // empty for auth mode "none"
	Params        map[string]any
	MaxIterations int
	Limits        bridge.Limits
	RetryDelay    time.Duration // 0 = transport default (1s); tests shrink it

	// client overrides the transport for tests; nil builds an openai.Client.
	client agentloop.ChatClient
}

// NewEndpointExecutor builds an EndpointExecutor from a validated backend config.
func NewEndpointExecutor(b *config.Backend) (*EndpointExecutor, error) {
	limits := bridge.Limits{
		MaxOutputBytes:   b.MaxOutputBytes,
		CommandAllowlist: b.CommandAllowlist,
	}
	if b.CommandTimeout != "" {
		d, err := time.ParseDuration(b.CommandTimeout)
		if err != nil {
			return nil, fmt.Errorf("backend %q: invalid command_timeout %q: %w", b.Name, b.CommandTimeout, err)
		}
		limits.CommandTimeout = d
	}
	var apiKey string
	if b.Auth.Mode == "api_key" {
		apiKey = b.Auth.APIKey
	}
	return &EndpointExecutor{
		BaseURI:       b.BaseURI,
		APIKey:        apiKey,
		Params:        b.Params,
		MaxIterations: b.MaxIterations,
		Limits:        limits,
	}, nil
}

func (e *EndpointExecutor) Kind() string { return "openai-compatible" }

// Run executes one agent-loop run in the request's workspace.
// req.Effort is accepted but unused: raw chat APIs have no portable effort
// semantics in SP2 (see the spec's open questions).
func (e *EndpointExecutor) Run(ctx context.Context, req LLMRequest) (domain.LLMResult, error) {
	client := e.client
	if client == nil {
		client = &openai.Client{BaseURL: e.BaseURI, APIKey: e.APIKey, RetryDelay: e.RetryDelay}
	}
	br := bridge.New(req.Workspace, e.Limits)
	return agentloop.Run(ctx, client, br, agentloop.Native,
		req.SystemPrompt, req.Prompt, req.Tools,
		agentloop.Opts{Model: req.Model, Params: e.Params, MaxIterations: e.MaxIterations})
}
