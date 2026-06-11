// Package api implements the /api/v1 REST surface and SSE stream consumed by
// the SP4b SPA. Handlers depend on narrow interfaces (satisfied by the repos,
// the Invoker, and the dispatcher) so the whole package tests with fakes.
package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/cbarraford/office-fleet/internal/auth"
	"github.com/cbarraford/office-fleet/internal/config"
	"github.com/cbarraford/office-fleet/internal/domain"
	"github.com/google/uuid"
)

type AgentStore interface {
	List(ctx context.Context) ([]*domain.Agent, error)
	GetByID(ctx context.Context, id uuid.UUID) (*domain.Agent, error)
	Insert(ctx context.Context, a *domain.Agent) error
	Update(ctx context.Context, a *domain.Agent) error
	Delete(ctx context.Context, id uuid.UUID) error
}

type DutyStore interface {
	List(ctx context.Context) ([]*domain.Duty, error)
	GetByID(ctx context.Context, id uuid.UUID) (*domain.Duty, error)
	Insert(ctx context.Context, d *domain.Duty) error
	Update(ctx context.Context, d *domain.Duty) error
	Delete(ctx context.Context, id uuid.UUID) error
}

type AssignmentStore interface {
	List(ctx context.Context) ([]*domain.Assignment, error)
	GetByID(ctx context.Context, id uuid.UUID) (*domain.Assignment, error)
	Insert(ctx context.Context, a *domain.Assignment) error
	Update(ctx context.Context, a *domain.Assignment) error
	Delete(ctx context.Context, id uuid.UUID) error
}

type RunStore interface {
	ListFiltered(ctx context.Context, status string, agentID uuid.UUID, limit int) ([]*domain.Run, error)
	GetByID(ctx context.Context, id uuid.UUID) (*domain.Run, error)
	AgentStats(ctx context.Context, agentID uuid.UUID) (*domain.AgentStats, error)
}

type EventStore interface {
	ListRecent(ctx context.Context, status string, limit int) ([]*domain.Event, error)
	GetByID(ctx context.Context, id uuid.UUID) (*domain.Event, error)
	MarkPending(ctx context.Context, id uuid.UUID) error
}

type SecretStore interface {
	Upsert(ctx context.Context, name string, value []byte) error
	List(ctx context.Context) (map[string][]byte, error)
	Delete(ctx context.Context, name string) error
}

type UserStore interface {
	GetByUsername(ctx context.Context, username string) (*domain.User, error)
	GetByID(ctx context.Context, id uuid.UUID) (*domain.User, error)
	Create(ctx context.Context, u *domain.User) error
	List(ctx context.Context) ([]*domain.User, error)
	Delete(ctx context.Context, username string) error
}

type Invoker interface {
	Invoke(ctx context.Context, assignmentID uuid.UUID, triggerKind string, eventID *string, params map[string]any) (*domain.Run, error)
}

// Encryptor seals secret values; nil means the master key is unset.
type Encryptor interface {
	Encrypt(plain []byte) ([]byte, error)
}

// API carries the dependencies for every handler.
type API struct {
	agents        AgentStore
	duties        DutyStore
	assignments   AssignmentStore
	runs          RunStore
	events        EventStore
	secretsRepo   SecretStore
	users         UserStore
	sessions      *auth.Sessions
	invoker       Invoker
	encryptor     Encryptor         // nil = no master key
	isEncrypted   func([]byte) bool // secrets.IsEncrypted
	notify        func(uuid.UUID)   // dispatcher nudge for replay; nil-safe
	cfg           *config.Config    // backends listing + validation parity
	broadcaster   *Broadcaster
	secureCookies bool
	logf          func(format string, args ...any)

	inner     *http.ServeMux // authenticated routes, built once
	innerOnce sync.Once
}

type Deps struct {
	Agents        AgentStore
	Duties        DutyStore
	Assignments   AssignmentStore
	Runs          RunStore
	Events        EventStore
	Secrets       SecretStore
	Users         UserStore
	Sessions      *auth.Sessions
	Invoker       Invoker
	Encryptor     Encryptor
	IsEncrypted   func([]byte) bool
	Notify        func(uuid.UUID)
	Config        *config.Config
	SecureCookies bool
}

func New(d Deps) *API {
	return &API{
		agents: d.Agents, duties: d.Duties, assignments: d.Assignments,
		runs: d.Runs, events: d.Events, secretsRepo: d.Secrets, users: d.Users,
		sessions: d.Sessions, invoker: d.Invoker, encryptor: d.Encryptor,
		isEncrypted: d.IsEncrypted, notify: d.Notify, cfg: d.Config,
		broadcaster:   NewBroadcaster(),
		secureCookies: d.SecureCookies,
		logf:          func(format string, args ...any) { fmt.Fprintf(os.Stderr, format+"\n", args...) },
	}
}

// RunUpdateSink exposes the run-update sink for serve wiring (pipeline hook).
func (a *API) RunUpdateSink() func(*domain.Run) { return a.broadcaster.PublishRun }

// Mount registers all routes on mux. Every route except POST /api/v1/login
// passes through the auth middleware.
func (a *API) Mount(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/login", a.handleLogin)
	mux.Handle("/api/v1/", a.requireAuth(http.HandlerFunc(a.route)))
}

// authedMux builds the inner mux with the full authenticated route table.
// Constructed in Mount-time order; Go 1.22 patterns handle method+path.
func (a *API) authedMux() *http.ServeMux {
	m := http.NewServeMux()
	m.HandleFunc("POST /api/v1/logout", a.handleLogout)
	m.HandleFunc("GET /api/v1/me", a.handleMe)

	m.HandleFunc("GET /api/v1/agents", a.handleListAgents)
	m.HandleFunc("POST /api/v1/agents", a.handleCreateAgent)
	m.HandleFunc("GET /api/v1/agents/{id}", a.handleGetAgent)
	m.HandleFunc("PATCH /api/v1/agents/{id}", a.handlePatchAgent)
	m.HandleFunc("DELETE /api/v1/agents/{id}", a.handleDeleteAgent)
	m.HandleFunc("GET /api/v1/agents/{id}/stats", a.handleAgentStats)

	m.HandleFunc("GET /api/v1/duties", a.handleListDuties)
	m.HandleFunc("POST /api/v1/duties", a.handleCreateDuty)
	m.HandleFunc("GET /api/v1/duties/{id}", a.handleGetDuty)
	m.HandleFunc("PATCH /api/v1/duties/{id}", a.handlePatchDuty)
	m.HandleFunc("DELETE /api/v1/duties/{id}", a.handleDeleteDuty)

	m.HandleFunc("GET /api/v1/assignments", a.handleListAssignments)
	m.HandleFunc("POST /api/v1/assignments", a.handleCreateAssignment)
	m.HandleFunc("GET /api/v1/assignments/{id}", a.handleGetAssignment)
	m.HandleFunc("PATCH /api/v1/assignments/{id}", a.handlePatchAssignment)
	m.HandleFunc("DELETE /api/v1/assignments/{id}", a.handleDeleteAssignment)
	m.HandleFunc("POST /api/v1/assignments/{id}/run", a.handleRunNow)

	m.HandleFunc("GET /api/v1/runs", a.handleListRuns)
	m.HandleFunc("GET /api/v1/runs/{id}", a.handleGetRun)
	m.HandleFunc("GET /api/v1/events", a.handleListEvents)
	m.HandleFunc("POST /api/v1/events/{id}/replay", a.handleReplayEvent)
	m.HandleFunc("GET /api/v1/backends", a.handleListBackends)
	m.HandleFunc("GET /api/v1/secrets", a.handleListSecrets)
	m.HandleFunc("PUT /api/v1/secrets/{name}", a.handlePutSecret)
	m.HandleFunc("DELETE /api/v1/secrets/{name}", a.handleDeleteSecret)
	m.HandleFunc("GET /api/v1/users", a.handleListUsers)
	m.HandleFunc("POST /api/v1/users", a.handleCreateUser)
	m.HandleFunc("DELETE /api/v1/users/{username}", a.handleDeleteUser)
	m.HandleFunc("GET /api/v1/stream", a.handleStream)
	return m
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func parseIDParam(r *http.Request) (uuid.UUID, error) {
	return uuid.Parse(r.PathValue("id"))
}

const defaultListLimit = 50

func parseLimit(r *http.Request) int {
	if v := r.URL.Query().Get("limit"); v != "" {
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil && n > 0 && n <= 500 {
			return n
		}
	}
	return defaultListLimit
}

// sessionCookie builds the session cookie consistently.
func (a *API) sessionCookie(value string, maxAge time.Duration) *http.Cookie {
	return &http.Cookie{
		Name: auth.CookieName, Value: value, Path: "/",
		HttpOnly: true, SameSite: http.SameSiteLaxMode,
		Secure: a.secureCookies, MaxAge: int(maxAge.Seconds()),
	}
}
