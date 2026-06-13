package main

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/cbarraford/office-fleet/internal/domain"
	"github.com/google/uuid"
)

func TestNewDaemonLoggerWritesJSON(t *testing.T) {
	var buf bytes.Buffer
	logger := newDaemonLogger(&buf)

	logger.Info("serve started", "addr", ":8080")

	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("daemon log is not JSON: %v\n%s", err, buf.String())
	}
	if got["level"] != "INFO" {
		t.Fatalf("level = %v, want INFO", got["level"])
	}
	if got["msg"] != "serve started" {
		t.Fatalf("msg = %v, want serve started", got["msg"])
	}
	if got["addr"] != ":8080" {
		t.Fatalf("addr = %v, want :8080", got["addr"])
	}
}

func TestLogRunCompletionIncludesCorrelationFields(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	runID := uuid.New()
	assignmentID := uuid.New()
	agentID := uuid.New()
	dutyID := uuid.New()
	started := time.Date(2026, 6, 12, 9, 0, 0, 0, time.UTC)
	finished := started.Add(1500 * time.Millisecond)
	run := &domain.Run{
		ID:           runID,
		AssignmentID: assignmentID,
		AgentID:      agentID,
		DutyID:       dutyID,
		TriggerKind:  "cron",
		Status:       domain.RunStatusSucceeded,
		StartedAt:    started,
		FinishedAt:   &finished,
		Cost:         0.125,
	}

	logRunCompletion(logger, run, nil)

	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("run log is not JSON: %v\n%s", err, buf.String())
	}
	for key, want := range map[string]any{
		"run_id":        runID.String(),
		"assignment_id": assignmentID.String(),
		"agent":         agentID.String(),
		"duty":          dutyID.String(),
		"trigger_kind":  "cron",
		"status":        string(domain.RunStatusSucceeded),
	} {
		if got[key] != want {
			t.Fatalf("%s = %v, want %v", key, got[key], want)
		}
	}
	if got["cost"] != 0.125 {
		t.Fatalf("cost = %v, want 0.125", got["cost"])
	}
	if _, ok := got["duration"]; !ok {
		t.Fatalf("duration field missing from run log: %#v", got)
	}
}
