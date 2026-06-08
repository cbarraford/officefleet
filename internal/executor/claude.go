package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/cbarraford/office-fleet/internal/domain"
)

// ClaudeExecutor shells out to the "claude" CLI agentic backend.
// The claude CLI manages its own tool-use loop.
type ClaudeExecutor struct {
	APIKey string // if non-empty, set as ANTHROPIC_API_KEY
}

func NewClaudeExecutor(apiKey string) *ClaudeExecutor {
	return &ClaudeExecutor{APIKey: apiKey}
}

func (c *ClaudeExecutor) Kind() string { return "claude" }

func (c *ClaudeExecutor) Run(ctx context.Context, req LLMRequest) (domain.LLMResult, error) {
	if _, err := exec.LookPath("claude"); err != nil {
		return domain.LLMResult{}, fmt.Errorf("claude binary not found on PATH: %w", err)
	}

	if err := verifyTools(req.Tools); err != nil {
		return domain.LLMResult{}, err
	}

	args := []string{"--print", "--output-format", "json"}
	if req.SystemPrompt != "" {
		args = append(args, "--system", req.SystemPrompt)
	}
	if req.Model != "" {
		args = append(args, "--model", req.Model)
	}
	if req.Effort != "" {
		args = append(args, "--effort", req.Effort)
	}

	combinedPrompt := buildClaudePrompt(req)
	cmd := exec.CommandContext(ctx, "claude", args...)
	if req.Workspace != "" {
		cmd.Dir = req.Workspace
	}
	cmd.Stdin = strings.NewReader(combinedPrompt)

	env := os.Environ()
	if c.APIKey != "" {
		env = append(env, "ANTHROPIC_API_KEY="+c.APIKey)
	}
	cmd.Env = env

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		errMsg := strings.TrimSpace(stderr.String())
		return domain.LLMResult{Status: 1, Summary: errMsg},
			fmt.Errorf("claude CLI: %w\nstderr: %s", err, errMsg)
	}

	return parseClaudeOutput(stdout.Bytes())
}

func buildClaudePrompt(req LLMRequest) string {
	if req.SystemPrompt == "" {
		return req.Prompt
	}
	return "<system>\n" + req.SystemPrompt + "\n</system>\n" + req.Prompt
}

// parseClaudeOutput extracts LLMResult from claude CLI --output-format json output.
func parseClaudeOutput(data []byte) (domain.LLMResult, error) {
	lines := bytes.Split(bytes.TrimSpace(data), []byte("\n"))
	var last []byte
	for i := len(lines) - 1; i >= 0; i-- {
		if len(bytes.TrimSpace(lines[i])) > 0 {
			last = lines[i]
			break
		}
	}
	if len(last) == 0 {
		return domain.LLMResult{}, fmt.Errorf("claude: empty output")
	}
	var raw map[string]any
	if err := json.Unmarshal(last, &raw); err != nil {
		return domain.LLMResult{
			Status: 0, Summary: string(data),
			Output: map[string]any{"raw": string(data)}, Transcript: string(data),
		}, nil
	}
	result := domain.LLMResult{Output: map[string]any{}}
	if isErr, ok := raw["is_error"].(bool); ok && isErr {
		result.Status = 1
	}
	if t, ok := raw["type"].(string); ok && t == "error" {
		result.Status = 1
	}
	if v, ok := raw["result"].(string); ok {
		result.Summary = v
		result.Output["raw"] = v
	}
	if v, ok := raw["cost_usd"].(float64); ok {
		result.Cost = v
	}
	if v, ok := raw["usage"].(map[string]any); ok {
		var total float64
		for _, key := range []string{"input_tokens", "output_tokens", "cache_read_input_tokens", "cache_creation_input_tokens"} {
			if tok, ok := v[key].(float64); ok {
				total += tok
			}
		}
		result.Tokens = int(total)
	}
	result.Transcript = string(data)
	return result, nil
}

func verifyTools(tools []string) error {
	for _, tool := range tools {
		if _, err := exec.LookPath(tool); err != nil {
			return fmt.Errorf("required tool %q not found on PATH", tool)
		}
	}
	return nil
}
