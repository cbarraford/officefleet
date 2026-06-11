package api

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/cbarraford/office-fleet/internal/domain"
	"github.com/google/uuid"
)

func TestBroadcaster_TwoSubscribersReceiveRunStarted(t *testing.T) {
	b := NewBroadcaster()
	ch1 := b.Subscribe()
	ch2 := b.Subscribe()

	run := &domain.Run{ID: uuid.New(), Status: domain.RunStatusRunning}
	b.PublishRun(run)

	for i, ch := range []chan []byte{ch1, ch2} {
		select {
		case payload := <-ch:
			var m map[string]any
			if err := json.Unmarshal(payload, &m); err != nil {
				t.Fatalf("subscriber %d: invalid JSON: %v", i+1, err)
			}
			if got, _ := m["event"].(string); got != "run_started" {
				t.Errorf("subscriber %d: event = %q, want run_started", i+1, got)
			}
			idStr, _ := m["id"].(string)
			if !strings.Contains(idStr, run.ID.String()) {
				// UUID may be marshalled in various forms; check the string contains the run ID
				t.Errorf("subscriber %d: id = %q, want %s", i+1, idStr, run.ID)
			}
		case <-time.After(time.Second):
			t.Fatalf("subscriber %d: no message received within 1s", i+1)
		}
	}
}

func TestBroadcaster_RunFinishedEvent(t *testing.T) {
	b := NewBroadcaster()
	ch := b.Subscribe()

	run := &domain.Run{ID: uuid.New(), Status: domain.RunStatusSucceeded}
	b.PublishRun(run)

	select {
	case payload := <-ch:
		var m map[string]any
		if err := json.Unmarshal(payload, &m); err != nil {
			t.Fatalf("invalid JSON: %v", err)
		}
		if got, _ := m["event"].(string); got != "run_finished" {
			t.Errorf("event = %q, want run_finished", got)
		}
	case <-time.After(time.Second):
		t.Fatal("no message received within 1s")
	}
}

func TestBroadcaster_SlowSubscriberDoesNotBlock(t *testing.T) {
	b := NewBroadcaster()
	// Never-reading subscriber (buffer size 16, we'll publish 20 messages).
	_ = b.Subscribe()
	// Reading subscriber.
	chReading := b.Subscribe()

	run := &domain.Run{ID: uuid.New(), Status: domain.RunStatusRunning}

	done := make(chan struct{})
	go func() {
		for i := 0; i < 20; i++ {
			b.PublishRun(run)
		}
		close(done)
	}()

	select {
	case <-done:
		// Publisher completed without blocking — good.
	case <-time.After(time.Second):
		t.Fatal("20 publishes did not complete within 1s; publisher appears blocked by slow subscriber")
	}

	// The reading subscriber should have received at least as many messages as
	// fit in the buffer (16) since it never read during publishing.
	// Drain whatever is available.
	received := 0
	for {
		select {
		case <-chReading:
			received++
		default:
			goto done
		}
	}
done:
	if received == 0 {
		t.Error("reading subscriber received no messages")
	}
}

func TestBroadcaster_UnsubscribeStopsDelivery(t *testing.T) {
	b := NewBroadcaster()
	ch := b.Subscribe()
	b.Unsubscribe(ch)

	run := &domain.Run{ID: uuid.New(), Status: domain.RunStatusRunning}
	b.PublishRun(run)

	select {
	case msg := <-ch:
		t.Errorf("unsubscribed channel received message: %s", msg)
	case <-time.After(50 * time.Millisecond):
		// Correct: no delivery after unsubscribe.
	}
}

func TestBroadcaster_RunIDInPayload(t *testing.T) {
	b := NewBroadcaster()
	ch := b.Subscribe()

	runID := uuid.New()
	run := &domain.Run{ID: runID, Status: domain.RunStatusRunning}
	b.PublishRun(run)

	select {
	case payload := <-ch:
		if !strings.Contains(string(payload), runID.String()) {
			t.Errorf("payload does not contain run id %s: %s", runID, payload)
		}
	case <-time.After(time.Second):
		t.Fatal("no message received")
	}
}
