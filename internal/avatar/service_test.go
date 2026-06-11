package avatar

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cbarraford/office-fleet/internal/domain"
	"github.com/google/uuid"
)

type fakeURLStore struct {
	mu   sync.Mutex
	urls map[uuid.UUID]string
	err  error
}

func newFakeURLStore() *fakeURLStore { return &fakeURLStore{urls: map[uuid.UUID]string{}} }

func (f *fakeURLStore) UpdateAvatarURL(_ context.Context, id uuid.UUID, url string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.urls[id] = url
	return nil
}

func (f *fakeURLStore) get(id uuid.UUID) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.urls[id]
}

type fakeGen struct {
	mu    sync.Mutex
	calls int
	data  []byte
	err   error
	block chan struct{} // when non-nil, Generate waits for a receive
}

func (g *fakeGen) Generate(_ context.Context, _, _ string) ([]byte, error) {
	g.mu.Lock()
	g.calls++
	block := g.block
	g.mu.Unlock()
	if block != nil {
		<-block
	}
	return g.data, g.err
}

func (g *fakeGen) callCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.calls
}

func testService(t *testing.T, gen Generator) (*Service, *fakeURLStore) {
	t.Helper()
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	urls := newFakeURLStore()
	svc := NewService(gen, store, urls, func(format string, args ...any) { t.Logf("svc: "+format, args...) })
	svc.now = func() time.Time { return time.Unix(1700000000, 0) }
	return svc, urls
}

func agentFor(name string) *domain.Agent {
	return &domain.Agent{ID: uuid.New(), Name: name, Role: "Tester"}
}

func TestAssignGeneratesPNG(t *testing.T) {
	gen := &fakeGen{data: []byte("png-bytes")}
	svc, urls := testService(t, gen)
	a := agentFor("Ada Lovelace")

	svc.Assign(a)
	svc.Wait()

	want := "/avatars/" + a.ID.String() + ".png?v=1700000000"
	if got := urls.get(a.ID); got != want {
		t.Errorf("avatar_url = %q, want %q", got, want)
	}
	if gen.callCount() != 1 {
		t.Errorf("generator called %d times, want 1", gen.callCount())
	}
}

func TestAssignFallsBackToSVGOnGeneratorError(t *testing.T) {
	gen := &fakeGen{err: fmt.Errorf("rate limited")}
	svc, urls := testService(t, gen)
	a := agentFor("Grace Hopper")

	svc.Assign(a)
	svc.Wait()

	if got := urls.get(a.ID); !strings.HasSuffix(got, ".svg?v=1700000000") {
		t.Errorf("avatar_url = %q, want an .svg fallback", got)
	}
}

func TestAssignNilGeneratorUsesSVG(t *testing.T) {
	svc, urls := testService(t, nil)
	a := agentFor("No Backend")

	svc.Assign(a)
	svc.Wait()

	if got := urls.get(a.ID); !strings.Contains(got, ".svg") {
		t.Errorf("avatar_url = %q, want svg", got)
	}
}

func TestAssignInFlightGuard(t *testing.T) {
	gen := &fakeGen{data: []byte("png"), block: make(chan struct{})}
	svc, _ := testService(t, gen)
	a := agentFor("Busy Agent")

	svc.Assign(a)
	svc.Assign(a) // second call while the first is blocked: must be a no-op
	gen.block <- struct{}{}
	svc.Wait()

	if gen.callCount() != 1 {
		t.Errorf("generator called %d times, want 1 (in-flight guard)", gen.callCount())
	}

	// After completion the guard clears: a new Assign generates again.
	gen.mu.Lock()
	gen.block = nil
	gen.mu.Unlock()
	svc.Assign(a)
	svc.Wait()
	if gen.callCount() != 2 {
		t.Errorf("generator called %d times after re-assign, want 2", gen.callCount())
	}
}

func TestAssignNilSafe(t *testing.T) {
	var svc *Service
	svc.Assign(agentFor("x")) // must not panic
	svc2, _ := testService(t, nil)
	svc2.Assign(nil) // must not panic
	svc2.Wait()
}

func TestSetUpload(t *testing.T) {
	svc, urls := testService(t, nil)
	id := uuid.New()

	url, err := svc.SetUpload(context.Background(), id, []byte("png-bytes"))
	if err != nil {
		t.Fatal(err)
	}
	want := "/avatars/" + id.String() + ".png?v=1700000000"
	if url != want {
		t.Errorf("url = %q, want %q", url, want)
	}
	if urls.get(id) != want {
		t.Errorf("store not updated: %q", urls.get(id))
	}
}

func TestAssignUpdateErrorIsLoggedNotFatal(t *testing.T) {
	urlsErr := newFakeURLStore()
	urlsErr.err = fmt.Errorf("db down")
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var logged []string
	svc := NewService(nil, store, urlsErr, func(format string, args ...any) {
		mu.Lock()
		logged = append(logged, fmt.Sprintf(format, args...))
		mu.Unlock()
	})
	svc.Assign(agentFor("Log Me"))
	svc.Wait()
	mu.Lock()
	defer mu.Unlock()
	if len(logged) == 0 {
		t.Error("expected the update failure to be logged")
	}
}
