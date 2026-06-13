package repo

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/cbarraford/office-fleet/internal/domain"
	"github.com/google/uuid"
)

type rowValues []any

func (r rowValues) Scan(dest ...any) error {
	if len(dest) != len(r) {
		return fmt.Errorf("dest count = %d, want %d", len(dest), len(r))
	}
	for i := range dest {
		if r[i] == nil {
			reflect.ValueOf(dest[i]).Elem().Set(reflect.Zero(reflect.ValueOf(dest[i]).Elem().Type()))
			continue
		}
		dv := reflect.ValueOf(dest[i]).Elem()
		v := reflect.ValueOf(r[i])
		if !v.Type().AssignableTo(dv.Type()) {
			return fmt.Errorf("value %d type %s not assignable to %s", i, v.Type(), dv.Type())
		}
		dv.Set(v)
	}
	return nil
}

func TestScanAgentRejectsInvalidBackendJSON(t *testing.T) {
	_, err := scanAgent(rowValues{
		uuid.New(), "agent", "role", "prompt", []byte(`{`), true,
		nil, nil, time.Now(), time.Now(),
	})
	if err == nil {
		t.Fatal("expected invalid backend JSON error")
	}
	if !strings.Contains(err.Error(), "default_backend") {
		t.Fatalf("err = %v, want default_backend context", err)
	}
}

func TestScanAssignmentRejectsInvalidJSON(t *testing.T) {
	_, err := scanAssignment(rowValues{
		uuid.New(), uuid.New(), uuid.New(), true,
		[]byte(`{`), []byte(`[]`), []byte(`{}`), []byte(`null`),
		nil, nil, time.Now(), time.Now(),
	})
	if err == nil {
		t.Fatal("expected invalid trigger JSON error")
	}
	if !strings.Contains(err.Error(), "trigger") {
		t.Fatalf("err = %v, want trigger context", err)
	}
}

func TestScanAssignmentNullBackendDoesNotSetRef(t *testing.T) {
	a, err := scanAssignment(rowValues{
		uuid.New(), uuid.New(), uuid.New(), true,
		[]byte(`{"kind":"manual"}`), []byte(`[]`), []byte(`{}`), []byte(`null`),
		nil, nil, time.Now(), time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if a.Backend != nil {
		t.Fatalf("Backend = %+v, want nil for JSON null", a.Backend)
	}
}

func TestScanDutyRejectsInvalidBackendJSON(t *testing.T) {
	_, err := scanDuty(rowValues{
		uuid.New(), "duty", "role", "description", []string{"manual"}, "prompt",
		[]string{"bash"}, []byte(`[]`), []byte(`{}`), []byte(`{`),
		time.Now(), time.Now(),
	})
	if err == nil {
		t.Fatal("expected invalid backend JSON error")
	}
	if !strings.Contains(err.Error(), "backend") {
		t.Fatalf("err = %v, want backend context", err)
	}
}

func TestScanRunRejectsInvalidJSON(t *testing.T) {
	_, err := scanRun(rowValues{
		uuid.New(), uuid.New(), uuid.New(), uuid.New(),
		"manual", nil, "system", "prompt",
		[]byte(`{`), []byte(`[]`), domain.RunStatusRunning, 0, 0.0,
		time.Now(), nil, nil,
	})
	if err == nil {
		t.Fatal("expected invalid llm_result JSON error")
	}
	if !strings.Contains(err.Error(), "llm_result") {
		t.Fatalf("err = %v, want llm_result context", err)
	}
}

func TestScanEventRejectsInvalidPayloadNormJSON(t *testing.T) {
	_, err := scanEvent(rowValues{
		uuid.New(), "gitlab", "mr_opened", []byte(`{"raw":true}`), []byte(`{`),
		"alice", "mr:1", domain.EventStatusPending, time.Now(), nil,
	})
	if err == nil {
		t.Fatal("expected invalid payload_norm JSON error")
	}
	if !strings.Contains(err.Error(), "payload_norm") {
		t.Fatalf("err = %v, want payload_norm context", err)
	}
}
