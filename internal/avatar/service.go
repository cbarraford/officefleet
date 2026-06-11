package avatar

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/cbarraford/office-fleet/internal/domain"
	"github.com/google/uuid"
)

// AgentURLStore is the narrow write the async worker needs. A dedicated
// UPDATE (not a full-row Update) means a slow generation can never clobber a
// concurrent rename/PATCH.
type AgentURLStore interface {
	UpdateAvatarURL(ctx context.Context, id uuid.UUID, avatarURL string) error
}

// generateTimeout bounds one full generation attempt (the HTTP client inside
// the generator has its own 60s timeout; this is the outer belt).
const generateTimeout = 90 * time.Second

// Service orchestrates avatar generation: pick generator (image backend with
// fallback-on-error, else initials immediately) → store file → update
// avatar_url. Generation is async and never blocks or fails agent creation.
type Service struct {
	gen    Generator // nil = initials-fallback only
	store  *Store
	agents AgentURLStore
	logf   func(format string, args ...any)
	now    func() time.Time

	mu       sync.Mutex
	inFlight map[uuid.UUID]struct{}
	wg       sync.WaitGroup
}

func NewService(gen Generator, store *Store, agents AgentURLStore, logf func(format string, args ...any)) *Service {
	if logf == nil {
		logf = func(format string, args ...any) { fmt.Fprintf(os.Stderr, format+"\n", args...) }
	}
	return &Service{
		gen: gen, store: store, agents: agents, logf: logf,
		now: time.Now, inFlight: map[uuid.UUID]struct{}{},
	}
}

// Assign generates an avatar for the agent asynchronously. It never blocks
// and never reports an error to the caller (failures are logged; the agent
// always ends up with SOME avatar — §6.1 non-blocking). Safe on a nil
// receiver and nil agent. A per-agent in-flight guard makes concurrent calls
// idempotent.
func (s *Service) Assign(agent *domain.Agent) {
	if s == nil || agent == nil {
		return
	}
	s.mu.Lock()
	if _, busy := s.inFlight[agent.ID]; busy {
		s.mu.Unlock()
		return
	}
	s.inFlight[agent.ID] = struct{}{}
	s.mu.Unlock()

	// Copy what we need — the caller's pointer belongs to its request.
	id, name, role := agent.ID, agent.Name, agent.Role
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer func() {
			s.mu.Lock()
			delete(s.inFlight, id)
			s.mu.Unlock()
		}()
		s.generate(context.Background(), id, name, role)
	}()
}

// Wait blocks until all in-flight generations finish (tests; optional at
// shutdown — generations are atomic on disk, so interrupting one is safe).
func (s *Service) Wait() { s.wg.Wait() }

func (s *Service) generate(ctx context.Context, id uuid.UUID, name, role string) {
	var data []byte
	ext := "png"
	if s.gen != nil {
		genCtx, cancel := context.WithTimeout(ctx, generateTimeout)
		d, err := s.gen.Generate(genCtx, name, role)
		cancel()
		if err != nil {
			s.logf("avatar: generate for %q: %v (falling back to initials)", name, err)
		} else {
			data = d
		}
	}
	if data == nil {
		data, ext = InitialsSVG(name), "svg"
	}
	if err := s.publish(ctx, id, ext, data); err != nil {
		s.logf("avatar: %v", err)
	}
}

// publish stores the bytes and points avatar_url at them (cache-busted so
// the browser refetches after regeneration overwrites the same filename).
func (s *Service) publish(ctx context.Context, id uuid.UUID, ext string, data []byte) error {
	urlPath, err := s.store.Save(id, ext, data)
	if err != nil {
		return fmt.Errorf("store avatar for %s: %w", id, err)
	}
	busted := fmt.Sprintf("%s?v=%d", urlPath, s.now().Unix())
	if err := s.agents.UpdateAvatarURL(ctx, id, busted); err != nil {
		return fmt.Errorf("update avatar_url for %s: %w", id, err)
	}
	return nil
}

// SetUpload synchronously stores an operator-uploaded PNG and updates
// avatar_url, returning the new (cache-busted) URL.
func (s *Service) SetUpload(ctx context.Context, id uuid.UUID, png []byte) (string, error) {
	if s == nil {
		return "", fmt.Errorf("avatar service not configured")
	}
	urlPath, err := s.store.Save(id, "png", png)
	if err != nil {
		return "", err
	}
	busted := fmt.Sprintf("%s?v=%d", urlPath, s.now().Unix())
	if err := s.agents.UpdateAvatarURL(ctx, id, busted); err != nil {
		return "", err
	}
	return busted, nil
}
