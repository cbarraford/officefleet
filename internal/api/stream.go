package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"

	"github.com/cbarraford/office-fleet/internal/domain"
)

// Broadcaster fans run updates out to SSE subscribers. Publishing never
// blocks: a slow subscriber's buffer fills and messages are dropped for that
// subscriber only — the feed is advisory, the runs table is truth.
type Broadcaster struct {
	mu   sync.Mutex
	subs map[chan []byte]struct{}
}

func NewBroadcaster() *Broadcaster {
	return &Broadcaster{subs: map[chan []byte]struct{}{}}
}

func (b *Broadcaster) Subscribe() chan []byte {
	ch := make(chan []byte, 16)
	b.mu.Lock()
	b.subs[ch] = struct{}{}
	b.mu.Unlock()
	return ch
}

func (b *Broadcaster) Unsubscribe(ch chan []byte) {
	b.mu.Lock()
	delete(b.subs, ch)
	b.mu.Unlock()
}

// PublishRun emits run_started (status running) or run_finished (terminal).
func (b *Broadcaster) PublishRun(run *domain.Run) {
	event := "run_finished"
	if run.Status == domain.RunStatusRunning {
		event = "run_started"
	}
	payload, err := json.Marshal(map[string]any{
		"event": event, "id": run.ID, "assignment_id": run.AssignmentID,
		"agent_id": run.AgentID, "duty_id": run.DutyID,
		"trigger_kind": run.TriggerKind, "status": run.Status,
		"tokens": run.Tokens, "cost": run.Cost,
	})
	if err != nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.subs {
		select {
		case ch <- payload:
		default: // slow consumer: drop
		}
	}
}

func (a *API) handleStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	ch := a.broadcaster.Subscribe()
	defer a.broadcaster.Unsubscribe(ch)
	for {
		select {
		case <-r.Context().Done():
			return
		case payload := <-ch:
			_, _ = fmt.Fprintf(w, "data: %s\n\n", payload)
			flusher.Flush()
		}
	}
}
