// Package openai is the HTTP transport for openai-compatible chat/completions
// endpoints (Ollama, vLLM, llama.cpp, hosted APIs). Transport only — the agent
// loop lives in package agentloop.
package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/cbarraford/office-fleet/internal/agentloop"
)

// Client speaks POST {BaseURL}/chat/completions.
type Client struct {
	BaseURL    string        // e.g. http://localhost:11434/v1 (no trailing slash)
	APIKey     string        // empty = no Authorization header
	HTTP       *http.Client  // nil = http.DefaultClient
	RetryDelay time.Duration // base backoff; default 1s (tests set ~1ms)
}

const maxRetries = 2 // retries after the initial attempt, on 429/5xx only

type wireFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type wireToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function wireFunction `json:"function"`
}

type wireMessage struct {
	Role       string         `json:"role"`
	Content    string         `json:"content"`
	ToolCalls  []wireToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
}

type wireResponse struct {
	Choices []struct {
		Message wireMessage `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

func (c *Client) Chat(ctx context.Context, req agentloop.ChatRequest) (agentloop.ChatResponse, error) {
	body := map[string]any{
		"model":    req.Model,
		"messages": encodeMessages(req.Messages),
	}
	if req.Tools != nil {
		body["tools"] = req.Tools
	}
	// Backend params passthrough; reserved keys are not overridable.
	for k, v := range req.Params {
		if k == "model" || k == "messages" || k == "tools" {
			continue
		}
		body[k] = v
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return agentloop.ChatResponse{}, fmt.Errorf("encode request: %w", err)
	}

	httpClient := c.HTTP
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	retryDelay := c.RetryDelay
	if retryDelay <= 0 {
		retryDelay = time.Second
	}

	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return agentloop.ChatResponse{}, ctx.Err()
			case <-time.After(retryDelay * time.Duration(1<<(attempt-1))):
			}
		}

		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
			c.BaseURL+"/chat/completions", bytes.NewReader(payload))
		if err != nil {
			return agentloop.ChatResponse{}, fmt.Errorf("build request: %w", err)
		}
		httpReq.Header.Set("Content-Type", "application/json")
		if c.APIKey != "" {
			httpReq.Header.Set("Authorization", "Bearer "+c.APIKey)
		}

		resp, err := httpClient.Do(httpReq)
		if err != nil {
			return agentloop.ChatResponse{}, fmt.Errorf("chat request: %w", err)
		}
		respBody, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			return agentloop.ChatResponse{}, fmt.Errorf("read response: %w", readErr)
		}

		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			lastErr = fmt.Errorf("chat endpoint returned %d: %s", resp.StatusCode, snippet(respBody))
			continue
		}
		if resp.StatusCode != http.StatusOK {
			return agentloop.ChatResponse{}, fmt.Errorf("chat endpoint returned %d: %s", resp.StatusCode, snippet(respBody))
		}
		return decodeResponse(respBody)
	}
	return agentloop.ChatResponse{}, fmt.Errorf("chat failed after %d attempts: %w", maxRetries+1, lastErr)
}

func encodeMessages(messages []agentloop.Message) []wireMessage {
	out := make([]wireMessage, len(messages))
	for i, m := range messages {
		wm := wireMessage{Role: m.Role, Content: m.Content, ToolCallID: m.ToolCallID}
		for _, tc := range m.ToolCalls {
			args, err := json.Marshal(tc.Args)
			if err != nil {
				args = []byte("{}")
			}
			wm.ToolCalls = append(wm.ToolCalls, wireToolCall{
				ID:   tc.ID,
				Type: "function",
				Function: wireFunction{
					Name:      tc.Name,
					Arguments: string(args),
				},
			})
		}
		out[i] = wm
	}
	return out
}

func decodeResponse(body []byte) (agentloop.ChatResponse, error) {
	var wire wireResponse
	if err := json.Unmarshal(body, &wire); err != nil {
		return agentloop.ChatResponse{}, fmt.Errorf("decode response: %w (body: %s)", err, snippet(body))
	}
	if len(wire.Choices) == 0 {
		return agentloop.ChatResponse{}, fmt.Errorf("chat response has no choices (body: %s)", snippet(body))
	}
	wm := wire.Choices[0].Message
	msg := agentloop.Message{Role: wm.Role, Content: wm.Content}
	for _, wtc := range wm.ToolCalls {
		tc := agentloop.ToolCall{ID: wtc.ID, Name: wtc.Function.Name}
		var args map[string]any
		if err := json.Unmarshal([]byte(wtc.Function.Arguments), &args); err != nil {
			tc.ArgsError = err.Error()
		} else {
			tc.Args = args
		}
		msg.ToolCalls = append(msg.ToolCalls, tc)
	}
	return agentloop.ChatResponse{
		Message: msg,
		Usage: agentloop.Usage{
			PromptTokens:     wire.Usage.PromptTokens,
			CompletionTokens: wire.Usage.CompletionTokens,
		},
	}, nil
}

// snippet bounds error-message bodies.
func snippet(b []byte) string {
	const max = 200
	if len(b) > max {
		return string(b[:max]) + "..."
	}
	return string(b)
}
