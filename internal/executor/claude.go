package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"syscall"
	"time"

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

	// The system prompt travels inside stdin via buildClaudePrompt's
	// <system> envelope — NOT as a CLI flag. (Dogfood finding: the previous
	// `--system` flag does not exist on the claude CLI — the real flag is
	// `--system-prompt` — so every run carrying a persona died on an unknown
	// option; the stdin envelope works across CLI versions.)
	args := []string{"--print", "--output-format", "json"}
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

	// Run claude in its own process group and, on context cancellation (daemon
	// shutdown), kill the WHOLE group so its children (glab/git) are reaped too,
	// not just claude itself (issue #7). WaitDelay bounds the grace period.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = 5 * time.Second

	// The agent is driven by untrusted content (MR diffs, comments), so it does
	// NOT inherit the daemon's full environment — that carries FLEET_DATABASE_DSN
	// (DB password) and FLEET_MASTER_KEY (secrets key), a prompt-injection
	// exfiltration vector. Pass only what the toolchain needs (issue #13).
	cmd.Env = minimalChildEnv(c.APIKey)

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
		// --output-format json guarantees a JSON object on the final line.
		// Unparseable output (a warning, partial output, a crash) is a failure,
		// not a success — fabricating Status:0 here would post the garbage to
		// the integration and record the run as succeeded (issue #2). Return
		// Status:1 + a non-nil error; the raw output is preserved for audit.
		return domain.LLMResult{
			Status: 1, Summary: string(data),
			Output: map[string]any{"raw": string(data)}, Transcript: string(data),
		}, fmt.Errorf("claude: output is not valid JSON (%d bytes)", len(data))
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
		// SP5 structured-result contract: when the final text carries a JSON
		// object (whole text or the last fenced ```json block), expose it as
		// the structured Output so output fan-out can iterate its lists —
		// parity with what submit_result gives endpoint backends.
		if obj := extractJSONObject(v); obj != nil {
			for k, val := range obj {
				result.Output[k] = val
			}
			if s, ok := obj["summary"].(string); ok && s != "" {
				result.Summary = s
			}
		}
	}
	// The claude CLI emits total_cost_usd; older versions used cost_usd. Prefer
	// the current field and fall back to the legacy one (issue #1) — reading the
	// wrong field recorded every run's cost as $0.
	if v, ok := raw["total_cost_usd"].(float64); ok {
		result.Cost = v
	} else if v, ok := raw["cost_usd"].(float64); ok {
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

// childEnvAllow is the exact set of environment variables passed to the agent
// child. The daemon's full env is NOT inherited because it carries fleet
// secrets the untrusted-content-driven agent must never see (issue #13).
var childEnvAllow = map[string]bool{
	"PATH": true, "HOME": true, "USER": true, "LOGNAME": true,
	"SHELL": true, "LANG": true, "LANGUAGE": true, "LC_ALL": true,
	"TERM": true, "TZ": true, "TMPDIR": true,
	"SSL_CERT_FILE": true, "SSL_CERT_DIR": true,
}

// childEnvAllowPrefix passes through the families the toolchain needs: the LLM
// provider's own credentials (ANTHROPIC_/CLAUDE_), git config (GIT_), locale
// (LC_), and XDG config dirs (XDG_). FLEET_* is deliberately NOT listed.
var childEnvAllowPrefix = []string{"LC_", "ANTHROPIC_", "CLAUDE_", "GIT_", "XDG_"}

// minimalChildEnv builds an allow-listed environment for the claude child and
// appends the resolved API key (which wins over any inherited ANTHROPIC_API_KEY).
func minimalChildEnv(apiKey string) []string {
	var out []string
	for _, kv := range os.Environ() {
		i := strings.IndexByte(kv, '=')
		if i < 0 {
			continue
		}
		k := kv[:i]
		if childEnvAllow[k] || hasAllowedEnvPrefix(k) {
			out = append(out, kv)
		}
	}
	if apiKey != "" {
		out = append(out, "ANTHROPIC_API_KEY="+apiKey)
	}
	return out
}

func hasAllowedEnvPrefix(k string) bool {
	for _, p := range childEnvAllowPrefix {
		if strings.HasPrefix(k, p) {
			return true
		}
	}
	return false
}

func verifyTools(tools []string) error {
	for _, tool := range tools {
		if _, err := exec.LookPath(tool); err != nil {
			return fmt.Errorf("required tool %q not found on PATH", tool)
		}
	}
	return nil
}

// jsonFenceRe captures the contents of ```json ... ``` blocks.
var jsonFenceRe = regexp.MustCompile("(?s)```json\\s*(.*?)```")

// extractJSONObject pulls a structured result object from the model's final
// text: the whole text when it is a JSON object, else the LAST fenced
// ```json block that parses to an object. Returns nil when there is none.
func extractJSONObject(text string) map[string]any {
	trimmed := strings.TrimSpace(text)
	if strings.HasPrefix(trimmed, "{") {
		var obj map[string]any
		if err := json.Unmarshal([]byte(trimmed), &obj); err == nil {
			return obj
		}
	}
	matches := jsonFenceRe.FindAllStringSubmatch(text, -1)
	for i := len(matches) - 1; i >= 0; i-- {
		var obj map[string]any
		if err := json.Unmarshal([]byte(strings.TrimSpace(matches[i][1])), &obj); err == nil {
			return obj
		}
	}
	return nil
}
