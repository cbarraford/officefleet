package agentloop

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/cbarraford/office-fleet/internal/domain"
)

// harnessPreamble is appended to the user prompt. It is deliberately minimal:
// persona and task remain the operator's domain (the rendered prompts are
// recorded on the Run verbatim; this preamble is part of loop mechanics).
const harnessPreamble = `You are working in an isolated workspace directory (your current working directory).
Available CLI tools on PATH: %s.
Work step by step using the provided tools to complete the task.
When finished, you MUST call the submit_result tool. Its "output" object is consumed by automation — include any fields the task asks for.`

const nudgeMessage = "You must finish by calling the submit_result tool. Use the provided tools to complete the task."

func preamble(requiredTools []string) string {
	tools := "(none declared)"
	if len(requiredTools) > 0 {
		tools = strings.Join(requiredTools, ", ")
	}
	return fmt.Sprintf(harnessPreamble, tools)
}

// Run drives the generic agent loop: send the conversation, decode tool calls,
// execute them through the bridge, feed observations back, and terminate when
// the model calls submit_result (or limits are hit).
//
// Failure semantics: model-level failures (max iterations, model never calls
// submit_result) return (LLMResult{Status: nonzero, ...}, nil) so the caller
// records the full result including the transcript. A non-nil error is
// reserved for transport errors, bridge-internal errors, and cancellation.
func Run(ctx context.Context, client ChatClient, bridge ToolBridge, proto ToolProtocol,
	systemPrompt, userPrompt string, requiredTools []string, opts Opts) (domain.LLMResult, error) {

	maxIter := opts.MaxIterations
	if maxIter <= 0 {
		maxIter = DefaultMaxIterations
	}

	specs := bridge.Specs()
	encodedTools := proto.Encode(specs)

	var transcript strings.Builder
	messages := []Message{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: userPrompt + "\n\n" + preamble(requiredTools)},
	}
	for _, m := range messages {
		writeTranscript(&transcript, m)
	}

	tokens := 0
	nudged := false

	for i := 0; i < maxIter; i++ {
		resp, err := client.Chat(ctx, ChatRequest{
			Model:    opts.Model,
			Messages: messages,
			Tools:    encodedTools,
			Params:   opts.Params,
		})
		if err != nil {
			return domain.LLMResult{
				Status:     1,
				Summary:    "chat transport error: " + err.Error(),
				Output:     map[string]any{},
				Transcript: transcript.String(),
				Tokens:     tokens,
			}, fmt.Errorf("chat: %w", err)
		}
		tokens += resp.Usage.PromptTokens + resp.Usage.CompletionTokens
		writeTranscript(&transcript, resp.Message)

		calls := proto.Decode(resp)
		if len(calls) == 0 {
			if nudged {
				// The model was already told to call submit_result and still
				// produced plain text: finalize as a model-level failure,
				// preserving its last words and the transcript.
				return domain.LLMResult{
					Status:     1,
					Summary:    resp.Message.Content,
					Output:     map[string]any{},
					Transcript: transcript.String(),
					Tokens:     tokens,
				}, nil
			}
			nudged = true
			messages = append(messages, resp.Message)
			nudge := Message{Role: "user", Content: nudgeMessage}
			messages = append(messages, nudge)
			writeTranscript(&transcript, nudge)
			continue
		}

		messages = append(messages, resp.Message)
		for _, call := range calls {
			var obs string
			if call.ArgsError != "" {
				obs = "tool call arguments were not valid JSON: " + call.ArgsError
			} else {
				var done bool
				var result *domain.LLMResult
				var bridgeErr error
				obs, done, result, bridgeErr = bridge.Execute(ctx, call)
				if bridgeErr != nil {
					return domain.LLMResult{
						Status:     1,
						Summary:    "tool bridge error: " + bridgeErr.Error(),
						Output:     map[string]any{},
						Transcript: transcript.String(),
						Tokens:     tokens,
					}, fmt.Errorf("tool bridge: %w", bridgeErr)
				}
				if done {
					result.Transcript = transcript.String()
					result.Tokens = tokens
					if result.Output == nil {
						result.Output = map[string]any{}
					}
					return *result, nil
				}
			}
			toolMsg := Message{Role: "tool", ToolCallID: call.ID, Content: obs}
			messages = append(messages, toolMsg)
			writeTranscript(&transcript, toolMsg)
		}
	}

	return domain.LLMResult{
		Status:     1,
		Summary:    fmt.Sprintf("max iterations (%d) reached without submit_result", maxIter),
		Output:     map[string]any{},
		Transcript: transcript.String(),
		Tokens:     tokens,
	}, nil
}

// writeTranscript appends one message as a JSON line.
func writeTranscript(sb *strings.Builder, m Message) {
	b, err := json.Marshal(m)
	if err != nil {
		sb.WriteString(fmt.Sprintf(`{"role":%q,"marshal_error":%q}`, m.Role, err.Error()))
	} else {
		sb.Write(b)
	}
	sb.WriteString("\n")
}
