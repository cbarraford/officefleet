package executor

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/cbarraford/office-fleet/internal/domain"
)

// PanelMember is one voter panel entry. Model/Effort are captured from the
// member's own backend config at factory time, because the voter backend has
// no model of its own and the pipeline resolves req.Model from the voter.
type PanelMember struct {
	Name   string
	Exec   Executor
	Model  string
	Effort string
}

// VotingExecutor fans one LLMRequest out to a panel of executors and
// aggregates by strategy. Minimal voter (SP2): majority votes on the integer
// Status code; semantic/judge strategies are deferred.
type VotingExecutor struct {
	Panel    []PanelMember
	Strategy string // first_success | majority
}

func NewVotingExecutor(panel []PanelMember, strategy string) *VotingExecutor {
	return &VotingExecutor{Panel: panel, Strategy: strategy}
}

func (v *VotingExecutor) Kind() string { return "voter" }

type memberOutcome struct {
	idx    int
	result domain.LLMResult
	hadErr bool
}

func (v *VotingExecutor) Run(ctx context.Context, req LLMRequest) (domain.LLMResult, error) {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	ch := make(chan memberOutcome, len(v.Panel))
	for i, m := range v.Panel {
		mreq := req
		mreq.Model = m.Model
		mreq.Effort = m.Effort
		if req.Workspace != "" {
			ws := filepath.Join(req.Workspace, fmt.Sprintf("panel-%d", i))
			if err := os.MkdirAll(ws, 0o755); err != nil {
				return domain.LLMResult{}, fmt.Errorf("create panel workspace: %w", err)
			}
			mreq.Workspace = ws
		}
		go func(i int, m PanelMember, mreq LLMRequest) {
			res, err := m.Exec.Run(runCtx, mreq)
			if err != nil {
				// Synthesize a failure result so aggregation is uniform.
				res = domain.LLMResult{
					Status:  1,
					Summary: fmt.Sprintf("panel member %s: %v", m.Name, err),
					Output:  map[string]any{},
				}
			}
			ch <- memberOutcome{idx: i, result: res, hadErr: err != nil}
		}(i, m, mreq)
	}

	switch v.Strategy {
	case "first_success":
		return v.firstSuccess(ch)
	default: // "majority" — config validation guarantees the strategy is valid
		return v.majority(ch)
	}
}

// firstSuccess returns the first completed result with Status == 0, cancelling
// the rest. Tokens/cost are summed over results received up to and including
// the winner (cancelled members never report). If no member succeeds, the
// last-completed failure is returned; if every member also errored, an error.
func (v *VotingExecutor) firstSuccess(ch <-chan memberOutcome) (domain.LLMResult, error) {
	tokens, cost := 0, 0.0
	var last memberOutcome
	allErrored := true
	for range v.Panel {
		out := <-ch
		tokens += out.result.Tokens
		cost += out.result.Cost
		last = out
		if !out.hadErr {
			allErrored = false
		}
		if !out.hadErr && out.result.Status == 0 {
			win := out.result
			win.Tokens = tokens
			win.Cost = cost
			return win, nil
		}
	}
	res := last.result
	res.Tokens = tokens
	res.Cost = cost
	if allErrored {
		return res, fmt.Errorf("all %d panel members failed", len(v.Panel))
	}
	return res, nil
}

// majority waits for all members, then takes a plurality vote on Status. Ties
// go to the group containing the lowest panel index; the representative is the
// lowest panel index within the winning group. Tokens/cost sum over all members.
func (v *VotingExecutor) majority(ch <-chan memberOutcome) (domain.LLMResult, error) {
	outcomes := make([]memberOutcome, 0, len(v.Panel))
	tokens, cost := 0, 0.0
	allErrored := true
	for range v.Panel {
		out := <-ch
		outcomes = append(outcomes, out)
		tokens += out.result.Tokens
		cost += out.result.Cost
		if !out.hadErr {
			allErrored = false
		}
	}

	// Group by status: count and lowest panel index per group.
	counts := map[int]int{}
	lowestIdx := map[int]int{}
	for _, out := range outcomes {
		s := out.result.Status
		counts[s]++
		if cur, ok := lowestIdx[s]; !ok || out.idx < cur {
			lowestIdx[s] = out.idx
		}
	}
	winStatus, winCount, winLowest := 0, -1, len(v.Panel)
	for s, n := range counts {
		if n > winCount || (n == winCount && lowestIdx[s] < winLowest) {
			winStatus, winCount, winLowest = s, n, lowestIdx[s]
		}
	}

	// Representative: lowest panel index within the winning group.
	var rep memberOutcome
	repIdx := len(v.Panel)
	for _, out := range outcomes {
		if out.result.Status == winStatus && out.idx < repIdx {
			rep = out
			repIdx = out.idx
		}
	}

	res := rep.result
	res.Tokens = tokens
	res.Cost = cost
	res.Transcript = v.panelSummary(outcomes) + res.Transcript
	if allErrored {
		return res, fmt.Errorf("all %d panel members failed", len(v.Panel))
	}
	return res, nil
}

// panelSummary renders one line per member: name -> status/tokens.
func (v *VotingExecutor) panelSummary(outcomes []memberOutcome) string {
	lines := make([]string, len(v.Panel))
	for _, out := range outcomes {
		lines[out.idx] = fmt.Sprintf("panel %s: status=%d tokens=%d", v.Panel[out.idx].Name, out.result.Status, out.result.Tokens)
	}
	return "=== voter panel ===\n" + strings.Join(lines, "\n") + "\n=== representative transcript ===\n"
}
