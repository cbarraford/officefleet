package executor

import (
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
