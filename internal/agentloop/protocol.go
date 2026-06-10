package agentloop

// nativeProtocol implements ToolProtocol using the openai-compatible native
// function-calling wire shape: tools as {"type":"function","function":{...}}
// in the request, tool calls decoded by the transport into Message.ToolCalls.
type nativeProtocol struct{}

// Native is the openai-compatible function-calling protocol.
var Native ToolProtocol = nativeProtocol{}

func (nativeProtocol) Encode(specs []ToolSpec) any {
	out := make([]map[string]any, len(specs))
	for i, s := range specs {
		out[i] = map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        s.Name,
				"description": s.Description,
				"parameters":  s.Parameters,
			},
		}
	}
	return out
}

func (nativeProtocol) Decode(resp ChatResponse) []ToolCall {
	return resp.Message.ToolCalls
}
