package avatar

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestStoreSaveAndOverwrite(t *testing.T) {
	dir := t.TempDir()
	s, err := NewStore(filepath.Join(dir, "avatars")) // exercises MkdirAll
	if err != nil {
		t.Fatal(err)
	}
	id := uuid.New()

	url, err := s.Save(id, "svg", []byte("<svg/>"))
	if err != nil {
		t.Fatal(err)
	}
	if url != "/avatars/"+id.String()+".svg" {
		t.Errorf("url = %q", url)
	}
	got, err := os.ReadFile(filepath.Join(dir, "avatars", id.String()+".svg"))
	if err != nil || string(got) != "<svg/>" {
		t.Fatalf("read back: %q, %v", got, err)
	}

	// Overwrite with new content.
	if _, err := s.Save(id, "svg", []byte("<svg>2</svg>")); err != nil {
		t.Fatal(err)
	}
	got, _ = os.ReadFile(filepath.Join(dir, "avatars", id.String()+".svg"))
	if string(got) != "<svg>2</svg>" {
		t.Errorf("overwrite failed: %q", got)
	}

	// No temp files left behind.
	entries, _ := os.ReadDir(filepath.Join(dir, "avatars"))
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			t.Errorf("leftover temp file %s", e.Name())
		}
	}
}

func TestStoreRejectsBadExtension(t *testing.T) {
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Save(uuid.New(), "html", []byte("x")); err == nil {
		t.Fatal("expected error for non-png/svg extension")
	}
}

func TestNewStoreFailsOnUnwritableDir(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("running as root; permissions not enforced")
	}
	parent := t.TempDir()
	locked := filepath.Join(parent, "locked")
	if err := os.MkdirAll(locked, 0o555); err != nil {
		t.Fatal(err)
	}
	if _, err := NewStore(filepath.Join(locked, "avatars")); err == nil {
		t.Fatal("expected error for unwritable dir")
	}
}

func TestMountHTTPServing(t *testing.T) {
	dir := t.TempDir()
	s, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	id := uuid.New()
	if _, err := s.Save(id, "svg", []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`)); err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	MountHTTP(mux, dir)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// Served with the right content type.
	resp, err := http.Get(srv.URL + "/avatars/" + id.String() + ".svg")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "image/svg") {
		t.Errorf("Content-Type = %q, want image/svg+xml", ct)
	}
	if csp := resp.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "sandbox") {
		t.Errorf("svg response CSP = %q, want a sandbox policy", csp)
	}
	if xcto := resp.Header.Get("X-Content-Type-Options"); xcto != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff", xcto)
	}

	// Missing file → 404.
	resp, _ = http.Get(srv.URL + "/avatars/nope.png")
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("missing file = %d, want 404", resp.StatusCode)
	}

	// Traversal and dotfiles → never 200 (don't escape the avatars dir).
	for _, path := range []string{"/avatars/..%2f..%2fetc%2fpasswd", "/avatars/.hidden", "/avatars/"} {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+path, nil)
		resp, err := http.DefaultTransport.RoundTrip(req) // no redirect following, raw path preserved
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			t.Errorf("GET %s = 200, want non-200", path)
		}
	}
}
