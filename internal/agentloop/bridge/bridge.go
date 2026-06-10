// Package bridge implements the workspace ToolBridge for the generic agent
// loop: a sandboxed shell (cwd-anchored, time-limited, output-capped,
// optionally allowlisted) plus file tools and the submit_result terminator.
package bridge

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/cbarraford/office-fleet/internal/agentloop"
	"github.com/cbarraford/office-fleet/internal/domain"
)

// Limits are the safety rails for tool execution. Zero values get defaults.
type Limits struct {
	CommandTimeout time.Duration // per run_command; default 120s
	MaxOutputBytes int           // observation truncation; default 64 KiB
	// CommandAllowlist, when non-empty, gates the basename of the FIRST
	// whitespace-separated token of cmd only — shell chaining (;, &&, |, $())
	// is not inspected. It is a nudge for well-behaved models, not a sandbox.
	CommandAllowlist []string // empty = allow all commands
}

const (
	DefaultCommandTimeout = 120 * time.Second
	DefaultMaxOutputBytes = 64 * 1024
)

// Bridge executes the whole-computer toolset inside one run's workspace.
type Bridge struct {
	workspace string
	limits    Limits
}

// New builds a Bridge for the given workspace, applying limit defaults.
func New(workspace string, limits Limits) *Bridge {
	if limits.CommandTimeout <= 0 {
		limits.CommandTimeout = DefaultCommandTimeout
	}
	if limits.MaxOutputBytes <= 0 {
		limits.MaxOutputBytes = DefaultMaxOutputBytes
	}
	return &Bridge{workspace: workspace, limits: limits}
}

func (b *Bridge) Specs() []agentloop.ToolSpec {
	return []agentloop.ToolSpec{
		{
			Name:        "run_command",
			Description: "Run a shell command in the workspace. Returns the exit code and combined stdout/stderr.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"cmd": map[string]any{"type": "string", "description": "shell command to execute"},
				},
				"required": []string{"cmd"},
			},
		},
		{
			Name:        "read_file",
			Description: "Read a file in the workspace and return its contents.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{"type": "string", "description": "file path, relative to the workspace"},
				},
				"required": []string{"path"},
			},
		},
		{
			Name:        "write_file",
			Description: "Write (create or overwrite) a file in the workspace.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path":    map[string]any{"type": "string", "description": "file path, relative to the workspace"},
					"content": map[string]any{"type": "string", "description": "full file content"},
				},
				"required": []string{"path", "content"},
			},
		},
		{
			Name:        "list_dir",
			Description: "List a directory in the workspace. Directories have a trailing slash.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{"type": "string", "description": "directory path, relative to the workspace; defaults to the workspace root"},
				},
			},
		},
		{
			Name:        "submit_result",
			Description: "Finish the task and report the result. You MUST call this exactly once when done. The output object is consumed by automation.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"summary": map[string]any{"type": "string", "description": "concise summary of what was done"},
					"status":  map[string]any{"type": "integer", "description": "0 on success, nonzero on failure"},
					"output":  map[string]any{"type": "object", "description": "structured result fields the task asked for"},
				},
				"required": []string{"summary", "status"},
			},
		},
	}
}

// Execute dispatches one tool call. Tool-level failures are observations
// (nil error); a non-nil error means the bridge itself is broken.
func (b *Bridge) Execute(ctx context.Context, call agentloop.ToolCall) (string, bool, *domain.LLMResult, error) {
	switch call.Name {
	case "run_command":
		return b.runCommand(ctx, call.Args), false, nil, nil
	case "read_file":
		return b.readFile(call.Args), false, nil, nil
	case "write_file":
		return b.writeFile(call.Args), false, nil, nil
	case "list_dir":
		return b.listDir(call.Args), false, nil, nil
	case "submit_result":
		return b.submitResult(call.Args)
	default:
		return fmt.Sprintf("unknown tool %q", call.Name), false, nil, nil
	}
}

func (b *Bridge) runCommand(ctx context.Context, args map[string]any) string {
	cmdStr, _ := args["cmd"].(string)
	if strings.TrimSpace(cmdStr) == "" {
		return "run_command requires a non-empty 'cmd' string argument"
	}
	if len(b.limits.CommandAllowlist) > 0 {
		first := strings.Fields(cmdStr)[0]
		base := filepath.Base(first)
		if !slices.Contains(b.limits.CommandAllowlist, base) {
			return fmt.Sprintf("command %q is not in the allowlist", base)
		}
	}

	cmdCtx, cancel := context.WithTimeout(ctx, b.limits.CommandTimeout)
	defer cancel()
	cmd := exec.CommandContext(cmdCtx, "sh", "-c", cmdStr)
	cmd.Dir = b.workspace
	// Run in a dedicated process group so cancellation kills background
	// children too (sh alone would die while its children keep the output
	// pipe open and outlive the deadline). WaitDelay unblocks CombinedOutput
	// if something survives the group kill while holding the pipe.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = 2 * time.Second
	out, err := cmd.CombinedOutput()

	if ctx.Err() != nil {
		return "command cancelled: " + ctx.Err().Error()
	}
	if cmdCtx.Err() == context.DeadlineExceeded {
		return fmt.Sprintf("command timed out after %s", b.limits.CommandTimeout)
	}
	exitCode := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			exitCode = ee.ExitCode()
		} else {
			return "command failed to start: " + err.Error()
		}
	}
	return fmt.Sprintf("exit code: %d\n%s", exitCode, b.truncate(string(out)))
}

func (b *Bridge) readFile(args map[string]any) string {
	path, _ := args["path"].(string)
	resolved, deny := b.resolve(path)
	if deny != "" {
		return deny
	}
	data, err := os.ReadFile(resolved)
	if err != nil {
		return "cannot read file: " + err.Error()
	}
	return b.truncate(string(data))
}

func (b *Bridge) writeFile(args map[string]any) string {
	path, _ := args["path"].(string)
	content, ok := args["content"].(string)
	if !ok {
		return "write_file requires a 'content' string argument"
	}
	resolved, deny := b.resolve(path)
	if deny != "" {
		return deny
	}
	if err := os.MkdirAll(filepath.Dir(resolved), 0o755); err != nil {
		return "cannot create parent directory: " + err.Error()
	}
	if err := os.WriteFile(resolved, []byte(content), 0o644); err != nil {
		return "cannot write file: " + err.Error()
	}
	return fmt.Sprintf("wrote %d bytes to %s", len(content), path)
}

func (b *Bridge) listDir(args map[string]any) string {
	path, _ := args["path"].(string)
	if path == "" {
		path = "."
	}
	resolved, deny := b.resolve(path)
	if deny != "" {
		return deny
	}
	entries, err := os.ReadDir(resolved)
	if err != nil {
		return "cannot list directory: " + err.Error()
	}
	var sb strings.Builder
	for _, e := range entries {
		sb.WriteString(e.Name())
		if e.IsDir() {
			sb.WriteString("/")
		}
		sb.WriteString("\n")
	}
	if sb.Len() == 0 {
		return "(empty directory)"
	}
	return b.truncate(sb.String())
}

func (b *Bridge) submitResult(args map[string]any) (string, bool, *domain.LLMResult, error) {
	summary, _ := args["summary"].(string)
	if strings.TrimSpace(summary) == "" {
		return "submit_result requires a non-empty 'summary' string argument", false, nil, nil
	}
	status := 0
	if v, ok := args["status"].(float64); ok {
		status = int(v)
	}
	output, _ := args["output"].(map[string]any)
	if output == nil {
		output = map[string]any{}
	}
	return "", true, &domain.LLMResult{Status: status, Summary: summary, Output: output}, nil
}

// resolve maps a tool path argument to an absolute path inside the workspace.
// The second return value is a non-empty denial observation on failure.
func (b *Bridge) resolve(raw string) (string, string) {
	if raw == "" {
		return "", "a non-empty 'path' argument is required"
	}
	p := raw
	if !filepath.IsAbs(p) {
		p = filepath.Join(b.workspace, p)
	}
	wsAbs, err := filepath.Abs(b.workspace)
	if err != nil {
		return "", "cannot resolve workspace path: " + err.Error()
	}
	pAbs, err := filepath.Abs(p)
	if err != nil {
		return "", "cannot resolve path: " + err.Error()
	}
	if pAbs != wsAbs && !strings.HasPrefix(pAbs, wsAbs+string(filepath.Separator)) {
		return "", fmt.Sprintf("path %q escapes the workspace", raw)
	}
	return pAbs, ""
}

func (b *Bridge) truncate(s string) string {
	if len(s) <= b.limits.MaxOutputBytes {
		return s
	}
	return s[:b.limits.MaxOutputBytes] + "\n[truncated]"
}
