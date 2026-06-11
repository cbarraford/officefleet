package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cbarraford/office-fleet/internal/domain"
	"github.com/cbarraford/office-fleet/internal/plugin"
)

// stubPlugin implements plugin.Plugin (+ optionally WebhookSource).
type stubPlugin struct {
	name    string
	events  []domain.Event
	err     error
	webhook bool
}

func (s *stubPlugin) Name() string                       { return s.name }
func (s *stubPlugin) EventSources() []plugin.EventSource { return nil }
func (s *stubPlugin) Actions() []plugin.Action           { return nil }
func (s *stubPlugin) ConfigSchema() plugin.Schema        { return nil }
func (s *stubPlugin) Init(_ context.Context, _ map[string]any, _ plugin.SecretLookup) error {
	return nil
}
func (s *stubPlugin) Do(_ context.Context, _ string, _ map[string]any) (map[string]any, error) {
	return nil, nil
}

type webhookStub struct{ stubPlugin }

func (w *webhookStub) HandleWebhook(_ context.Context, _ *http.Request) ([]domain.Event, error) {
	return w.events, w.err
}

type fakeIngestor struct {
	got []domain.Event
	n   int
	err error
}

func (f *fakeIngestor) Ingest(_ context.Context, evs []domain.Event) (int, error) {
	f.got = append(f.got, evs...)
	if f.err != nil {
		return 0, f.err
	}
	return f.n, nil
}

func TestHealthz(t *testing.T) {
	srv := httptest.NewServer(New(&fakeIngestor{}).Handler())
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("status = %d", resp.StatusCode)
	}
}

func TestWebhook_Accepted(t *testing.T) {
	p := &webhookStub{stubPlugin: stubPlugin{name: "hooktest-ok"}}
	p.events = []domain.Event{{SourcePlugin: "hooktest-ok", EventType: "t", DedupKey: "k"}}
	plugin.Register(p)
	ing := &fakeIngestor{n: 1}
	srv := httptest.NewServer(New(ing).Handler())
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/webhooks/hooktest-ok", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", resp.StatusCode)
	}
	var body map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if body["accepted"] != float64(1) {
		t.Errorf("accepted = %v", body["accepted"])
	}
	if len(ing.got) != 1 {
		t.Errorf("ingested = %d", len(ing.got))
	}
}

func TestWebhook_StatusCodes(t *testing.T) {
	plugin.Register(&stubPlugin{name: "hooktest-nosrc"}) // no WebhookSource
	authFail := &webhookStub{stubPlugin: stubPlugin{name: "hooktest-auth"}}
	authFail.err = &plugin.AuthError{Msg: "bad token"}
	plugin.Register(authFail)
	parseFail := &webhookStub{stubPlugin: stubPlugin{name: "hooktest-parse"}}
	parseFail.err = fmt.Errorf("garbled payload")
	plugin.Register(parseFail)

	ingFail := &webhookStub{stubPlugin: stubPlugin{name: "hooktest-ingfail"}}
	ingFail.events = []domain.Event{{SourcePlugin: "hooktest-ingfail", EventType: "t", DedupKey: "k"}}
	plugin.Register(ingFail)

	srv := httptest.NewServer(New(&fakeIngestor{}).Handler())
	defer srv.Close()
	srvIngFail := httptest.NewServer(New(&fakeIngestor{err: fmt.Errorf("db down")}).Handler())
	defer srvIngFail.Close()

	cases := []struct {
		url  string
		want int
	}{
		{srv.URL + "/webhooks/no-such-plugin", 404},
		{srv.URL + "/webhooks/hooktest-nosrc", 404},
		{srv.URL + "/webhooks/hooktest-auth", 401},
		{srv.URL + "/webhooks/hooktest-parse", 400},
		{srvIngFail.URL + "/webhooks/hooktest-ingfail", 500},
	}
	for _, c := range cases {
		resp, err := http.Post(c.url, "application/json", strings.NewReader("{}"))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != c.want {
			t.Errorf("POST %s = %d, want %d", c.url, resp.StatusCode, c.want)
		}
	}
}
