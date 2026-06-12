package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestParseClaudeOutput_ValidJSON(t *testing.T) {
	input := []byte(`{"result":"task done","cost_usd":0.042,"usage":{"output_tokens":123}}`)
	res, err := parseClaudeOutput(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Summary != "task done" {
		t.Errorf("Summary = %q, want %q", res.Summary, "task done")
	}
	if res.Cost != 0.042 {
		t.Errorf("Cost = %v, want 0.042", res.Cost)
	}
	if res.Tokens != 123 {
		t.Errorf("Tokens = %d, want 123", res.Tokens)
	}
}

func TestParseClaudeOutput_NonJSON(t *testing.T) {
	input := []byte("just plain text")
	res, err := parseClaudeOutput(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Summary != string(input) {
		t.Errorf("Summary = %q, want %q", res.Summary, string(input))
	}
	if res.Status != 0 {
		t.Errorf("Status = %d, want 0", res.Status)
	}
}

func TestParseClaudeOutput_MultilineLastJSON(t *testing.T) {
	input := []byte("some prefix output\n{\"result\":\"multiline result\",\"cost_usd\":0.001,\"usage\":{\"output_tokens\":10}}")
	res, err := parseClaudeOutput(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Summary != "multiline result" {
		t.Errorf("Summary = %q, want %q", res.Summary, "multiline result")
	}
	if res.Tokens != 10 {
		t.Errorf("Tokens = %d, want 10", res.Tokens)
	}
}

func TestParseClaudeOutput_EmptyInput(t *testing.T) {
	_, err := parseClaudeOutput([]byte(""))
	if err == nil {
		t.Fatal("expected error for empty input, got nil")
	}
	if !strings.Contains(err.Error(), "empty output") {
		t.Errorf("error = %q, want it to contain \"empty output\"", err.Error())
	}
}

func TestBuildClaudePrompt_WithSystem(t *testing.T) {
	req := LLMRequest{
		SystemPrompt: "You are a helpful assistant.",
		Prompt:       "Do the task.",
	}
	got := buildClaudePrompt(req)
	if !strings.HasPrefix(got, "<system>\n") {
		t.Errorf("prompt does not start with <system> tag: %q", got)
	}
	if !strings.Contains(got, "You are a helpful assistant.") {
		t.Errorf("prompt missing system prompt content: %q", got)
	}
	if !strings.Contains(got, "</system>") {
		t.Errorf("prompt missing </system> closing tag: %q", got)
	}
	if !strings.HasSuffix(got, "Do the task.") {
		t.Errorf("prompt does not end with task prompt: %q", got)
	}
}

func TestBuildClaudePrompt_NoSystem(t *testing.T) {
	req := LLMRequest{
		SystemPrompt: "",
		Prompt:       "Do the task.",
	}
	got := buildClaudePrompt(req)
	if strings.Contains(got, "<system>") {
		t.Errorf("prompt contains unexpected <system> block: %q", got)
	}
	if got != "Do the task." {
		t.Errorf("prompt = %q, want %q", got, "Do the task.")
	}
}

func TestClaudeExecutorRestrictsEnvAndAllowedTools(t *testing.T) {
	dir := t.TempDir()
	binDir := filepath.Join(dir, "bin")
	if err := os.Mkdir(binDir, 0700); err != nil {
		t.Fatal(err)
	}
	argsFile := filepath.Join(dir, "args.txt")
	envFile := filepath.Join(dir, "env.txt")

	writeExecutable(t, filepath.Join(binDir, "claude"), fmt.Sprintf(`#!/bin/sh
printf '%%s\n' "$@" > %s
env > %s
printf '%%s\n' '{"result":"ok","usage":{"output_tokens":1}}'
`, shellQuote(argsFile), shellQuote(envFile)))
	writeExecutable(t, filepath.Join(binDir, "git"), "#!/bin/sh\nexit 0\n")
	writeExecutable(t, filepath.Join(binDir, "glab"), "#!/bin/sh\nexit 0\n")

	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FLEET_DATABASE_DSN", "postgres://user:secret@example/fleet")
	t.Setenv("UNRELATED_SECRET", "do-not-leak")
	t.Setenv("ANTHROPIC_API_KEY", "parent-env-key")

	_, err := NewClaudeExecutor("explicit-api-key").Run(context.Background(), LLMRequest{
		Prompt: "hello",
		Tools:  []string{"git", "glab"},
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	argsData, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	gotArgs := strings.Split(strings.TrimSpace(string(argsData)), "\n")
	wantArgs := []string{
		"--print",
		"--output-format",
		"json",
		"--allowed-tools",
		"Bash(git *)",
		"Bash(glab *)",
	}
	if !slices.Equal(gotArgs, wantArgs) {
		t.Fatalf("claude argv = %#v, want %#v", gotArgs, wantArgs)
	}

	envData, err := os.ReadFile(envFile)
	if err != nil {
		t.Fatal(err)
	}
	env := string(envData)
	if !strings.Contains(env, "ANTHROPIC_API_KEY=explicit-api-key\n") {
		t.Fatalf("claude env missing explicit API key: %s", env)
	}
	for _, leaked := range []string{"FLEET_DATABASE_DSN=", "UNRELATED_SECRET=", "ANTHROPIC_API_KEY=parent-env-key"} {
		if strings.Contains(env, leaked) {
			t.Fatalf("claude env leaked %q: %s", leaked, env)
		}
	}
}

func TestParseClaudeOutputExtractsFencedJSON(t *testing.T) {
	resultText := "I reviewed the MR.\n\n```json\n{\"summary\": \"2 issues found\", \"comments\": [{\"path\": \"a.go\", \"line\": 7, \"body\": \"nil deref\"}]}\n```"
	wrapper, _ := json.Marshal(map[string]any{"type": "result", "result": resultText})

	got, err := parseClaudeOutput(wrapper)
	if err != nil {
		t.Fatal(err)
	}
	if got.Summary != "2 issues found" {
		t.Errorf("Summary = %q, want the lifted output.summary", got.Summary)
	}
	comments, ok := got.Output["comments"].([]any)
	if !ok || len(comments) != 1 {
		t.Fatalf("Output[comments] = %#v, want 1-element array", got.Output["comments"])
	}
	first, _ := comments[0].(map[string]any)
	if first["path"] != "a.go" {
		t.Errorf("comment path = %v", first["path"])
	}
	if got.Output["raw"] != resultText {
		t.Errorf("raw text must still be preserved alongside the parsed object")
	}
}

func writeExecutable(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}

func TestParseClaudeOutputWholeTextJSON(t *testing.T) {
	resultText := `{"summary": "all clear", "issues": []}`
	wrapper, _ := json.Marshal(map[string]any{"type": "result", "result": resultText})

	got, err := parseClaudeOutput(wrapper)
	if err != nil {
		t.Fatal(err)
	}
	if got.Summary != "all clear" {
		t.Errorf("Summary = %q", got.Summary)
	}
	if _, ok := got.Output["issues"]; !ok {
		t.Error("Output missing issues key")
	}
}

func TestParseClaudeOutputPlainTextUnchanged(t *testing.T) {
	resultText := "Just prose, no JSON contract here."
	wrapper, _ := json.Marshal(map[string]any{"type": "result", "result": resultText})

	got, err := parseClaudeOutput(wrapper)
	if err != nil {
		t.Fatal(err)
	}
	if got.Summary != resultText {
		t.Errorf("Summary = %q, want the raw text", got.Summary)
	}
	if got.Output["raw"] != resultText {
		t.Errorf("Output[raw] = %v", got.Output["raw"])
	}
	if len(got.Output) != 1 {
		t.Errorf("plain text must not grow Output keys: %#v", got.Output)
	}
}

func TestParseClaudeOutputMalformedFenceFallsBack(t *testing.T) {
	resultText := "Findings:\n```json\n{not valid json]\n```"
	wrapper, _ := json.Marshal(map[string]any{"type": "result", "result": resultText})

	got, err := parseClaudeOutput(wrapper)
	if err != nil {
		t.Fatal(err)
	}
	if got.Summary != resultText {
		t.Errorf("malformed fence must leave Summary as raw text")
	}
	if len(got.Output) != 1 {
		t.Errorf("malformed fence must not grow Output: %#v", got.Output)
	}
}

func TestParseClaudeOutputJSONWithoutSummaryKeepsTextSummary(t *testing.T) {
	resultText := "Done.\n```json\n{\"comments\": []}\n```"
	wrapper, _ := json.Marshal(map[string]any{"type": "result", "result": resultText})

	got, err := parseClaudeOutput(wrapper)
	if err != nil {
		t.Fatal(err)
	}
	if got.Summary != resultText {
		t.Errorf("without output.summary the full text stays the summary, got %q", got.Summary)
	}
	if _, ok := got.Output["comments"]; !ok {
		t.Error("parsed object must still land in Output")
	}
}
