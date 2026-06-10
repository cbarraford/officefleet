package bridge

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/cbarraford/office-fleet/internal/agentloop"
	"github.com/cbarraford/office-fleet/internal/domain"
)

func newTestBridge(t *testing.T, limits Limits) (*Bridge, string) {
	t.Helper()
	ws := t.TempDir()
	return New(ws, limits), ws
}

// execTool runs one non-terminal tool call and fails the test on
// bridge-internal errors. (Named execTool, not exec, to avoid clashing with
// the os/exec import used by the implementation file in this package.)
func execTool(t *testing.T, b *Bridge, name string, args map[string]any) (string, bool, *domain.LLMResult) {
	t.Helper()
	obs, done, result, err := b.Execute(context.Background(), agentloop.ToolCall{ID: "t", Name: name, Args: args})
	if err != nil {
		t.Fatalf("Execute(%s) bridge-internal error: %v", name, err)
	}
	return obs, done, result
}

func TestSpecs_IncludesAllTools(t *testing.T) {
	b, _ := newTestBridge(t, Limits{})
	specs := b.Specs()
	want := map[string]bool{"run_command": false, "read_file": false, "write_file": false, "list_dir": false, "submit_result": false}
	for _, s := range specs {
		if _, ok := want[s.Name]; !ok {
			t.Errorf("unexpected tool %q", s.Name)
		}
		want[s.Name] = true
		if s.Parameters == nil {
			t.Errorf("tool %q has nil Parameters schema", s.Name)
		}
	}
	for name, seen := range want {
		if !seen {
			t.Errorf("missing tool %q", name)
		}
	}
}

func TestRunCommand_CwdIsWorkspace(t *testing.T) {
	b, ws := newTestBridge(t, Limits{})
	obs, done, _ := execTool(t, b, "run_command", map[string]any{"cmd": "pwd"})
	if done {
		t.Fatal("run_command must not terminate the loop")
	}
	// macOS tempdirs may be symlinked (/var -> /private/var); resolve before comparing.
	resolved, err := filepath.EvalSymlinks(ws)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(obs, resolved) && !strings.Contains(obs, ws) {
		t.Errorf("pwd observation %q does not contain workspace %q", obs, ws)
	}
	if !strings.Contains(obs, "exit code: 0") {
		t.Errorf("observation missing exit code: %q", obs)
	}
}

func TestRunCommand_NonzeroExit(t *testing.T) {
	b, _ := newTestBridge(t, Limits{})
	obs, _, _ := execTool(t, b, "run_command", map[string]any{"cmd": "exit 3"})
	if !strings.Contains(obs, "exit code: 3") {
		t.Errorf("observation = %q, want exit code: 3", obs)
	}
}

func TestRunCommand_Timeout(t *testing.T) {
	b, _ := newTestBridge(t, Limits{CommandTimeout: 50 * time.Millisecond})
	obs, _, _ := execTool(t, b, "run_command", map[string]any{"cmd": "sleep 2"})
	if !strings.Contains(obs, "timed out") {
		t.Errorf("observation = %q, want timeout notice", obs)
	}
}

func TestRunCommand_OutputTruncated(t *testing.T) {
	b, _ := newTestBridge(t, Limits{MaxOutputBytes: 100})
	obs, _, _ := execTool(t, b, "run_command", map[string]any{"cmd": "yes x | head -c 10000"})
	if len(obs) > 300 { // 100 bytes + exit-code line + truncation marker headroom
		t.Errorf("observation length = %d, want truncated to ~100 bytes", len(obs))
	}
	if !strings.Contains(obs, "[truncated]") {
		t.Errorf("observation missing truncation marker: %q", obs)
	}
}

func TestRunCommand_AllowlistDeny(t *testing.T) {
	b, _ := newTestBridge(t, Limits{CommandAllowlist: []string{"echo"}})
	obs, done, _ := execTool(t, b, "run_command", map[string]any{"cmd": "rm -rf /tmp/x"})
	if done {
		t.Fatal("denial must not terminate the loop")
	}
	if !strings.Contains(obs, "not in the allowlist") {
		t.Errorf("observation = %q, want allowlist denial", obs)
	}
}

func TestRunCommand_AllowlistAllow(t *testing.T) {
	b, _ := newTestBridge(t, Limits{CommandAllowlist: []string{"echo"}})
	obs, _, _ := execTool(t, b, "run_command", map[string]any{"cmd": "echo hello"})
	if !strings.Contains(obs, "hello") {
		t.Errorf("observation = %q, want command output", obs)
	}
}

func TestRunCommand_MissingCmd(t *testing.T) {
	b, _ := newTestBridge(t, Limits{})
	obs, done, _ := execTool(t, b, "run_command", map[string]any{})
	if done {
		t.Fatal("must not terminate")
	}
	if !strings.Contains(obs, "cmd") {
		t.Errorf("observation = %q, want missing-arg notice", obs)
	}
}

func TestReadWriteListFile(t *testing.T) {
	b, ws := newTestBridge(t, Limits{})

	obs, done, _ := execTool(t, b, "write_file", map[string]any{"path": "sub/note.txt", "content": "hello world"})
	if done || !strings.Contains(obs, "wrote") {
		t.Fatalf("write_file obs = %q done=%v", obs, done)
	}
	data, err := os.ReadFile(filepath.Join(ws, "sub", "note.txt"))
	if err != nil || string(data) != "hello world" {
		t.Fatalf("file content = %q err=%v", data, err)
	}

	obs, _, _ = execTool(t, b, "read_file", map[string]any{"path": "sub/note.txt"})
	if obs != "hello world" {
		t.Errorf("read_file obs = %q", obs)
	}

	obs, _, _ = execTool(t, b, "list_dir", map[string]any{"path": "sub"})
	if !strings.Contains(obs, "note.txt") {
		t.Errorf("list_dir obs = %q", obs)
	}

	// list_dir with no path defaults to workspace root.
	obs, _, _ = execTool(t, b, "list_dir", map[string]any{})
	if !strings.Contains(obs, "sub/") {
		t.Errorf("list_dir root obs = %q, want sub/ entry", obs)
	}
}

func TestReadFile_Missing(t *testing.T) {
	b, _ := newTestBridge(t, Limits{})
	obs, done, _ := execTool(t, b, "read_file", map[string]any{"path": "nope.txt"})
	if done {
		t.Fatal("must not terminate")
	}
	if !strings.Contains(obs, "no such file") && !strings.Contains(obs, "cannot read") {
		t.Errorf("observation = %q", obs)
	}
}

func TestPathEscapeDenied(t *testing.T) {
	b, _ := newTestBridge(t, Limits{})
	for _, path := range []string{"../outside.txt", "/etc/passwd", "sub/../../outside"} {
		obs, done, _ := execTool(t, b, "read_file", map[string]any{"path": path})
		if done {
			t.Fatalf("path %q: must not terminate", path)
		}
		if !strings.Contains(obs, "escapes the workspace") {
			t.Errorf("path %q: observation = %q, want escape denial", path, obs)
		}
		obs, _, _ = execTool(t, b, "write_file", map[string]any{"path": path, "content": "x"})
		if !strings.Contains(obs, "escapes the workspace") {
			t.Errorf("write path %q: observation = %q", path, obs)
		}
	}
}

func TestSubmitResult(t *testing.T) {
	b, _ := newTestBridge(t, Limits{})
	obs, done, result, err := b.Execute(context.Background(), agentloop.ToolCall{
		ID: "t", Name: "submit_result",
		Args: map[string]any{
			"summary": "all done",
			"status":  float64(0),
			"output":  map[string]any{"review_body": "LGTM"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !done {
		t.Fatal("submit_result must terminate the loop")
	}
	_ = obs
	if result.Summary != "all done" || result.Status != 0 {
		t.Errorf("result = %+v", result)
	}
	if result.Output["review_body"] != "LGTM" {
		t.Errorf("Output = %v", result.Output)
	}
}

func TestSubmitResult_NonzeroStatus(t *testing.T) {
	b, _ := newTestBridge(t, Limits{})
	_, done, result, err := b.Execute(context.Background(), agentloop.ToolCall{
		ID: "t", Name: "submit_result",
		Args: map[string]any{"summary": "could not finish", "status": float64(2)},
	})
	if err != nil || !done {
		t.Fatalf("done=%v err=%v", done, err)
	}
	if result.Status != 2 {
		t.Errorf("Status = %d, want 2", result.Status)
	}
	if result.Output == nil {
		t.Error("Output must default to an empty map")
	}
}

func TestSubmitResult_MissingSummary(t *testing.T) {
	b, _ := newTestBridge(t, Limits{})
	obs, done, result, err := b.Execute(context.Background(), agentloop.ToolCall{
		ID: "t", Name: "submit_result", Args: map[string]any{"status": float64(0)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if done || result != nil {
		t.Fatal("submit_result without summary must NOT terminate")
	}
	if !strings.Contains(obs, "summary") {
		t.Errorf("observation = %q, want summary-required notice", obs)
	}
}

func TestUnknownTool(t *testing.T) {
	b, _ := newTestBridge(t, Limits{})
	obs, done, _ := execTool(t, b, "frobnicate", map[string]any{})
	if done {
		t.Fatal("must not terminate")
	}
	if !strings.Contains(obs, "unknown tool") {
		t.Errorf("observation = %q", obs)
	}
}

func TestRunCommand_TimeoutKillsBackgroundChildren(t *testing.T) {
	b, _ := newTestBridge(t, Limits{CommandTimeout: 200 * time.Millisecond})
	start := time.Now()
	obs, _, _ := execTool(t, b, "run_command", map[string]any{"cmd": "sleep 5 & echo started"})
	elapsed := time.Since(start)
	// Without group-kill the held pipe blocks until the child exits (~5s).
	if elapsed > 3*time.Second {
		t.Fatalf("run_command blocked for %v; timeout defeated by backgrounded child", elapsed)
	}
	if !strings.Contains(obs, "timed out") {
		t.Errorf("observation = %q, want timeout notice", obs)
	}
}

func TestRunCommand_ParentCancelObservation(t *testing.T) {
	b, _ := newTestBridge(t, Limits{})
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	obs, done, _, err := b.Execute(ctx, agentloop.ToolCall{
		ID: "t", Name: "run_command", Args: map[string]any{"cmd": "sleep 5"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if done {
		t.Fatal("must not terminate")
	}
	if !strings.Contains(obs, "cancelled") {
		t.Errorf("observation = %q, want cancellation notice", obs)
	}
	if strings.Contains(obs, "exit code") {
		t.Errorf("observation = %q, must not report a phantom exit code on cancellation", obs)
	}
}

func TestRunCommand_ReapsOrphanedBackgroundChildren(t *testing.T) {
	b, _ := newTestBridge(t, Limits{}) // default 120s timeout: WaitDelay path, not deadline
	start := time.Now()
	// Unique sleep duration so pgrep can find this exact orphan if it leaks.
	obs, _, _ := execTool(t, b, "run_command", map[string]any{"cmd": "sleep 397 & echo started"})
	elapsed := time.Since(start)
	if elapsed > 5*time.Second {
		t.Fatalf("run_command blocked for %v", elapsed)
	}
	if !strings.Contains(obs, "started") {
		t.Errorf("observation = %q, want command output", obs)
	}
	if !strings.Contains(obs, "exit code: 0") {
		t.Errorf("observation = %q, want exit code 0 (command itself succeeded)", obs)
	}
	// The backgrounded child must have been reaped, not leaked.
	time.Sleep(100 * time.Millisecond)
	out, _ := exec.Command("pgrep", "-f", "sleep 397").Output()
	if len(strings.TrimSpace(string(out))) > 0 {
		t.Errorf("orphaned child survived: pgrep output %q", out)
	}
}

func TestTruncate_RuneSafe(t *testing.T) {
	b, _ := newTestBridge(t, Limits{MaxOutputBytes: 7})
	// "héllo wörld" — é and ö are 2 bytes each; byte 7 lands mid-ö... construct
	// a string where the cap falls inside a multi-byte rune.
	s := "abéöéöéö" // 2 + 6*2 = 14 bytes
	got := b.truncate(s)
	if !strings.HasSuffix(got, "\n[truncated]") {
		t.Fatalf("missing marker: %q", got)
	}
	body := strings.TrimSuffix(got, "\n[truncated]")
	if !utf8.ValidString(body) {
		t.Errorf("truncated body is not valid UTF-8: %q", body)
	}
	if len(body) > 7 {
		t.Errorf("body length = %d, want <= 7", len(body))
	}
}
