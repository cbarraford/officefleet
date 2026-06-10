package executor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cbarraford/office-fleet/internal/domain"
)

// slowFake is a FakeExecutor with a delay and cancellation awareness.
type slowFake struct {
	result    domain.LLMResult
	err       error
	delay     time.Duration
	gotReq    LLMRequest
	cancelled bool
}

func (s *slowFake) Kind() string { return "fake" }

func (s *slowFake) Run(ctx context.Context, req LLMRequest) (domain.LLMResult, error) {
	s.gotReq = req
	select {
	case <-time.After(s.delay):
	case <-ctx.Done():
		s.cancelled = true
		return domain.LLMResult{}, ctx.Err()
	}
	return s.result, s.err
}

func TestVoter_FirstSuccess_CompletionOrder(t *testing.T) {
	slow := &slowFake{result: domain.LLMResult{Status: 0, Summary: "slow-winner"}, delay: 500 * time.Millisecond}
	fast := &slowFake{result: domain.LLMResult{Status: 0, Summary: "fast-winner"}, delay: 5 * time.Millisecond}
	v := &VotingExecutor{
		Strategy: "first_success",
		Panel: []PanelMember{
			{Name: "slow", Exec: slow, Model: "m-slow"},
			{Name: "fast", Exec: fast, Model: "m-fast"},
		},
	}
	res, err := v.Run(context.Background(), LLMRequest{Prompt: "p"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Summary != "fast-winner" {
		t.Errorf("Summary = %q, want fast-winner (completion order)", res.Summary)
	}
}

func TestVoter_FirstSuccess_SkipsFailures(t *testing.T) {
	failing := &slowFake{result: domain.LLMResult{Status: 1, Summary: "failed"}, delay: time.Millisecond}
	ok := &slowFake{result: domain.LLMResult{Status: 0, Summary: "ok"}, delay: 20 * time.Millisecond}
	v := &VotingExecutor{
		Strategy: "first_success",
		Panel:    []PanelMember{{Name: "f", Exec: failing}, {Name: "ok", Exec: ok}},
	}
	res, err := v.Run(context.Background(), LLMRequest{Prompt: "p"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Summary != "ok" {
		t.Errorf("Summary = %q, want ok", res.Summary)
	}
}

func TestVoter_FirstSuccess_AllFail(t *testing.T) {
	f1 := &slowFake{result: domain.LLMResult{Status: 1, Summary: "f1"}, delay: time.Millisecond}
	f2 := &slowFake{result: domain.LLMResult{Status: 2, Summary: "f2"}, delay: 5 * time.Millisecond}
	v := &VotingExecutor{
		Strategy: "first_success",
		Panel:    []PanelMember{{Name: "f1", Exec: f1}, {Name: "f2", Exec: f2}},
	}
	res, err := v.Run(context.Background(), LLMRequest{Prompt: "p"})
	if err != nil {
		t.Fatal("all-failed (non-error) panel should not return an error; pipeline handles Status != 0")
	}
	if res.Status == 0 {
		t.Error("expected nonzero status when no member succeeds")
	}
}

func TestVoter_AllErrored(t *testing.T) {
	e1 := &slowFake{err: errors.New("boom1"), delay: time.Millisecond}
	e2 := &slowFake{err: errors.New("boom2"), delay: time.Millisecond}
	v := &VotingExecutor{
		Strategy: "first_success",
		Panel:    []PanelMember{{Name: "e1", Exec: e1}, {Name: "e2", Exec: e2}},
	}
	_, err := v.Run(context.Background(), LLMRequest{Prompt: "p"})
	if err == nil {
		t.Fatal("expected an error when every panel member errors")
	}
}

func TestVoter_Majority_PluralityOnStatus(t *testing.T) {
	a := &slowFake{result: domain.LLMResult{Status: 0, Summary: "a", Tokens: 10, Cost: 0.1}, delay: 30 * time.Millisecond}
	b := &slowFake{result: domain.LLMResult{Status: 1, Summary: "b", Tokens: 20, Cost: 0.2}, delay: time.Millisecond}
	c := &slowFake{result: domain.LLMResult{Status: 0, Summary: "c", Tokens: 30, Cost: 0.3}, delay: time.Millisecond}
	v := &VotingExecutor{
		Strategy: "majority",
		Panel:    []PanelMember{{Name: "a", Exec: a}, {Name: "b", Exec: b}, {Name: "c", Exec: c}},
	}
	res, err := v.Run(context.Background(), LLMRequest{Prompt: "p"})
	if err != nil {
		t.Fatal(err)
	}
	// Status 0 wins 2-1; representative = lowest panel index in group = "a".
	if res.Summary != "a" {
		t.Errorf("Summary = %q, want a (lowest panel index in winning group)", res.Summary)
	}
	if res.Status != 0 {
		t.Errorf("Status = %d", res.Status)
	}
	// Tokens/cost summed across ALL members.
	if res.Tokens != 60 {
		t.Errorf("Tokens = %d, want 60", res.Tokens)
	}
	if res.Cost < 0.59 || res.Cost > 0.61 {
		t.Errorf("Cost = %v, want 0.6", res.Cost)
	}
}

func TestVoter_Majority_TieLowestPanelIndexGroup(t *testing.T) {
	a := &slowFake{result: domain.LLMResult{Status: 1, Summary: "a"}, delay: time.Millisecond}
	b := &slowFake{result: domain.LLMResult{Status: 0, Summary: "b"}, delay: time.Millisecond}
	v := &VotingExecutor{
		Strategy: "majority",
		Panel:    []PanelMember{{Name: "a", Exec: a}, {Name: "b", Exec: b}},
	}
	res, err := v.Run(context.Background(), LLMRequest{Prompt: "p"})
	if err != nil {
		t.Fatal(err)
	}
	// 1-1 tie; group containing panel index 0 (status 1) wins.
	if res.Summary != "a" {
		t.Errorf("Summary = %q, want a (tie -> lowest panel index group)", res.Summary)
	}
}

func TestVoter_MemberModelEffortOverride(t *testing.T) {
	m1 := &slowFake{result: domain.LLMResult{Status: 0, Summary: "x"}, delay: time.Millisecond}
	m2 := &slowFake{result: domain.LLMResult{Status: 0, Summary: "y"}, delay: time.Millisecond}
	v := &VotingExecutor{
		Strategy: "majority",
		Panel: []PanelMember{
			{Name: "m1", Exec: m1, Model: "model-one", Effort: "low"},
			{Name: "m2", Exec: m2, Model: "model-two", Effort: "high"},
		},
	}
	if _, err := v.Run(context.Background(), LLMRequest{Prompt: "p", Model: "voter-level-model"}); err != nil {
		t.Fatal(err)
	}
	if m1.gotReq.Model != "model-one" || m1.gotReq.Effort != "low" {
		t.Errorf("m1 req = model %q effort %q", m1.gotReq.Model, m1.gotReq.Effort)
	}
	if m2.gotReq.Model != "model-two" || m2.gotReq.Effort != "high" {
		t.Errorf("m2 req = model %q effort %q", m2.gotReq.Model, m2.gotReq.Effort)
	}
}

func TestVoter_WorkspaceSubdirs(t *testing.T) {
	m1 := &slowFake{result: domain.LLMResult{Status: 0, Summary: "x"}, delay: time.Millisecond}
	m2 := &slowFake{result: domain.LLMResult{Status: 0, Summary: "y"}, delay: time.Millisecond}
	v := &VotingExecutor{
		Strategy: "majority",
		Panel:    []PanelMember{{Name: "m1", Exec: m1}, {Name: "m2", Exec: m2}},
	}
	ws := t.TempDir()
	if _, err := v.Run(context.Background(), LLMRequest{Prompt: "p", Workspace: ws}); err != nil {
		t.Fatal(err)
	}
	if m1.gotReq.Workspace != filepath.Join(ws, "panel-0") {
		t.Errorf("m1 workspace = %q", m1.gotReq.Workspace)
	}
	if m2.gotReq.Workspace != filepath.Join(ws, "panel-1") {
		t.Errorf("m2 workspace = %q", m2.gotReq.Workspace)
	}
	for _, sub := range []string{"panel-0", "panel-1"} {
		if fi, err := os.Stat(filepath.Join(ws, sub)); err != nil || !fi.IsDir() {
			t.Errorf("workspace subdir %s missing: %v", sub, err)
		}
	}
}

func TestVoter_KindAndTranscriptPrefix(t *testing.T) {
	m1 := &slowFake{result: domain.LLMResult{Status: 0, Summary: "x", Transcript: "INNER", Tokens: 5}, delay: time.Millisecond}
	v := &VotingExecutor{Strategy: "majority", Panel: []PanelMember{{Name: "m1", Exec: m1}}}
	if v.Kind() != "voter" {
		t.Errorf("Kind = %q", v.Kind())
	}
	res, err := v.Run(context.Background(), LLMRequest{Prompt: "p"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Transcript, "m1") || !strings.Contains(res.Transcript, "INNER") {
		t.Errorf("Transcript = %q, want panel summary + representative transcript", res.Transcript)
	}
}
