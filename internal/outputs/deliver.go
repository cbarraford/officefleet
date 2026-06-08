package outputs

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/cbarraford/office-fleet/internal/domain"
	"github.com/cbarraford/office-fleet/internal/plugin"
	"github.com/cbarraford/office-fleet/internal/prompt"
)

// mustJSON encodes v as a JSON string. If marshalling fails it returns an
// empty JSON object so templates always receive a valid JSON string rather
// than Go's default map literal syntax.
func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// Deliver executes each configured output binding: renders params, calls plugin.Do.
// Returns one OutputDelivery per binding; never aborts early on individual failures.
func Deliver(
	ctx context.Context,
	outputs []domain.OutputBinding,
	result domain.LLMResult,
	promptCtx prompt.Context,
) []domain.OutputDelivery {
	deliveries := make([]domain.OutputDelivery, 0, len(outputs))
	for _, out := range outputs {
		d := domain.OutputDelivery{Plugin: out.Plugin, Action: out.Action}
		rendered, err := renderParams(out.Params, result, promptCtx)
		if err != nil {
			d.Status = "failed"
			d.Error = fmt.Sprintf("render params: %v", err)
			deliveries = append(deliveries, d)
			continue
		}
		d.Params = rendered
		p, ok := plugin.Get(out.Plugin)
		if !ok {
			d.Status = "failed"
			d.Error = fmt.Sprintf("plugin %q not registered", out.Plugin)
			deliveries = append(deliveries, d)
			continue
		}
		if _, err = p.Do(ctx, out.Action, rendered); err != nil {
			d.Status = "failed"
			d.Error = err.Error()
		} else {
			d.Status = "delivered"
		}
		deliveries = append(deliveries, d)
	}
	return deliveries
}

// renderParams resolves each string param value as a Go template.
func renderParams(params map[string]any, result domain.LLMResult, promptCtx prompt.Context) (map[string]any, error) {
	// Enrich the context with LLM result fields so templates can reference {{.Event.llm_summary}}.
	enriched := promptCtx
	enriched.Event = make(map[string]any, len(promptCtx.Event)+3)
	for k, v := range promptCtx.Event {
		enriched.Event[k] = v
	}
	enriched.Event["llm_summary"] = result.Summary
	enriched.Event["llm_transcript"] = result.Transcript
	enriched.Event["llm_output"] = mustJSON(result.Output)

	out := make(map[string]any, len(params))
	for k, v := range params {
		str, ok := v.(string)
		if !ok {
			out[k] = v
			continue
		}
		rendered, err := prompt.Render(str, enriched)
		if err != nil {
			return nil, fmt.Errorf("param %q: %w", k, err)
		}
		out[k] = rendered
	}
	return out, nil
}
