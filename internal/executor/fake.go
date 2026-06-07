package executor

import (
	"context"

	"github.com/cbarraford/office-fleet/internal/domain"
)

// FakeExecutor returns a canned LLMResult for tests.
// It records the last request it received for assertion.
type FakeExecutor struct {
	Result  domain.LLMResult
	LastReq LLMRequest
	Err     error
}

func NewFakeExecutor(result domain.LLMResult) *FakeExecutor {
	return &FakeExecutor{Result: result}
}

func (f *FakeExecutor) Kind() string { return "fake" }

func (f *FakeExecutor) Run(_ context.Context, req LLMRequest) (domain.LLMResult, error) {
	f.LastReq = req
	return f.Result, f.Err
}
