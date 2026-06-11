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

// maxFanOutItems caps a single for_each binding so a hallucinating model
// cannot file thousands of issues; the truncation is recorded, not silent.
const maxFanOutItems = 50

// Deliver executes each configured output binding: renders params, calls plugin.Do.
// Returns OutputDelivery records (one per plain binding; one PER ITEM for
// for_each bindings); never aborts early on individual failures.
func Deliver(
	ctx context.Context,
	outputs []domain.OutputBinding,
	result domain.LLMResult,
	promptCtx prompt.Context,
) []domain.OutputDelivery {
	deliveries := make([]domain.OutputDelivery, 0, len(outputs))
	for _, out := range outputs {
		if out.ForEach != "" {
			deliveries = append(deliveries, deliverFanOut(ctx, out, result, promptCtx)...)
			continue
		}
		deliveries = append(deliveries, deliverOne(ctx, out, result, promptCtx, nil))
	}
	return deliveries
}

// deliverFanOut delivers out once per element of result.Output[out.ForEach].
func deliverFanOut(
	ctx context.Context,
	out domain.OutputBinding,
	result domain.LLMResult,
	promptCtx prompt.Context,
) []domain.OutputDelivery {
	raw, ok := result.Output[out.ForEach]
	if !ok || raw == nil {
		return nil // healthy "no findings": zero deliveries
	}
	list, ok := raw.([]any)
	if !ok {
		return []domain.OutputDelivery{{
			Plugin: out.Plugin, Action: out.Action, Status: "failed",
			Error: fmt.Sprintf("for_each key %q is not an array", out.ForEach),
		}}
	}
	if len(list) == 0 {
		return nil
	}
	n := len(list)
	truncated := n > maxFanOutItems
	if truncated {
		n = maxFanOutItems
	}
	deliveries := make([]domain.OutputDelivery, 0, n+1)
	for i := 0; i < n; i++ {
		item, ok := list[i].(map[string]any)
		if !ok {
			deliveries = append(deliveries, domain.OutputDelivery{
				Plugin: out.Plugin, Action: out.Action, Status: "failed",
				Error: fmt.Sprintf("for_each item %d is not an object", i),
			})
			continue
		}
		deliveries = append(deliveries, deliverOne(ctx, out, result, promptCtx, item))
	}
	if truncated {
		deliveries = append(deliveries, domain.OutputDelivery{
			Plugin: out.Plugin, Action: out.Action, Status: "failed",
			Error: fmt.Sprintf("for_each list truncated: %d items exceeds the cap of %d", len(list), maxFanOutItems),
		})
	}
	return deliveries
}

// deliverOne renders params (item non-nil during fan-out) and calls plugin.Do.
func deliverOne(
	ctx context.Context,
	out domain.OutputBinding,
	result domain.LLMResult,
	promptCtx prompt.Context,
	item map[string]any,
) domain.OutputDelivery {
	d := domain.OutputDelivery{Plugin: out.Plugin, Action: out.Action}
	rendered, err := renderParams(out.Params, result, promptCtx, item)
	if err != nil {
		d.Status = "failed"
		d.Error = fmt.Sprintf("render params: %v", err)
		return d
	}
	d.Params = rendered
	p, ok := plugin.Get(out.Plugin)
	if !ok {
		d.Status = "failed"
		d.Error = fmt.Sprintf("plugin %q not registered", out.Plugin)
		return d
	}
	if _, err = p.Do(ctx, out.Action, rendered); err != nil {
		d.Status = "failed"
		d.Error = err.Error()
	} else {
		d.Status = "delivered"
	}
	return d
}

// renderParams resolves each string param value as a Go template. item, when
// non-nil, is exposed as {{.Item.*}} (fan-out element).
func renderParams(params map[string]any, result domain.LLMResult, promptCtx prompt.Context, item map[string]any) (map[string]any, error) {
	// Enrich the context with LLM result fields so templates can reference {{.Event.llm_summary}}.
	enriched := promptCtx
	enriched.Event = make(map[string]any, len(promptCtx.Event)+3)
	for k, v := range promptCtx.Event {
		enriched.Event[k] = v
	}
	enriched.Event["llm_summary"] = result.Summary
	enriched.Event["llm_transcript"] = result.Transcript
	enriched.Event["llm_output"] = mustJSON(result.Output)
	enriched.Item = item

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
