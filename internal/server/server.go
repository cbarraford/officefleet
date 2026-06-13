// Package server hosts OfficeFleet's HTTP ingestion surface: plugin webhooks
// and a health check. SP4 mounts the REST API/UI into this same server.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/cbarraford/office-fleet/internal/domain"
	"github.com/cbarraford/office-fleet/internal/plugin"
)

// Ingestor persists inbound events; events.Ingestor satisfies it.
type Ingestor interface {
	Ingest(ctx context.Context, evs []domain.Event) (int, error)
}

type Server struct {
	ingestor Ingestor
	health   *Health
	logf     func(format string, args ...any)
}

func New(ingestor Ingestor) *Server {
	return &Server{
		ingestor: ingestor,
		health:   NewHealth(),
		logf:     func(format string, args ...any) { fmt.Fprintf(os.Stderr, format+"\n", args...) },
	}
}

func (s *Server) WithHealth(health *Health) *Server {
	if health != nil {
		s.health = health
	}
	return s
}

func (s *Server) WithLogger(logger *slog.Logger) *Server {
	if logger != nil {
		s.logf = func(format string, args ...any) {
			logger.Error("server error", "message", fmt.Sprintf(format, args...))
		}
	}
	return s
}

type Health struct {
	mu       sync.RWMutex
	lastTick time.Time
}

func NewHealth() *Health {
	h := &Health{}
	h.Beat()
	return h
}

func (h *Health) Beat() {
	h.BeatAt(time.Now().UTC())
}

func (h *Health) BeatAt(t time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.lastTick = t.UTC()
}

func (h *Health) Snapshot() map[string]any {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return map[string]any{
		"status":    "ok",
		"last_tick": h.lastTick.Format(time.RFC3339Nano),
	}
}

// Handler builds the HTTP mux: webhooks + healthz, plus any extra mounts
// (the /api/v1 surface in fleet serve).
func (s *Server) Handler(mounts ...func(*http.ServeMux)) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, s.health.Snapshot())
	})
	mux.HandleFunc("POST /webhooks/{plugin}", s.handleWebhook)
	for _, mount := range mounts {
		mount(mux)
	}
	return mux
}

func (s *Server) handleWebhook(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("plugin")
	p, ok := plugin.Get(name)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "unknown plugin"})
		return
	}
	ws, ok := p.(plugin.WebhookSource)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "plugin has no webhook source"})
		return
	}

	evs, err := ws.HandleWebhook(r.Context(), r)
	var authErr *plugin.AuthError
	if errors.As(err, &authErr) {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": authErr.Msg})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}

	n, err := s.ingestor.Ingest(r.Context(), evs)
	if err != nil {
		s.logf("server: ingest webhook %s: %v", name, err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "ingest failed"})
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"accepted": n})
}

func writeJSON(w http.ResponseWriter, status int, body map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
