package avatar

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
)

// Store writes avatar files atomically (temp file + rename) under one
// directory and reports their public URL paths.
type Store struct {
	dir string
}

// NewStore creates the directory (0755) and fails fast when it is not
// writable — serve startup surfaces the misconfiguration immediately.
func NewStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create avatars dir %s: %w", dir, err)
	}
	probe := filepath.Join(dir, ".write-probe")
	if err := os.WriteFile(probe, nil, 0o644); err != nil {
		return nil, fmt.Errorf("avatars dir %s not writable: %w", dir, err)
	}
	_ = os.Remove(probe)
	return &Store{dir: dir}, nil
}

// Save writes <agentID>.<ext> atomically and returns the public URL path
// (/avatars/<file>, no cache-bust — the Service appends ?v=).
func (s *Store) Save(id uuid.UUID, ext string, data []byte) (string, error) {
	if ext != "png" && ext != "svg" {
		return "", fmt.Errorf("unsupported avatar extension %q", ext)
	}
	name := id.String() + "." + ext
	tmp, err := os.CreateTemp(s.dir, ".tmp-avatar-*")
	if err != nil {
		return "", fmt.Errorf("create temp avatar: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		_ = os.Remove(tmp.Name())
		return "", fmt.Errorf("write avatar: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return "", fmt.Errorf("close avatar: %w", err)
	}
	if err := os.Rename(tmp.Name(), filepath.Join(s.dir, name)); err != nil {
		_ = os.Remove(tmp.Name())
		return "", fmt.Errorf("publish avatar: %w", err)
	}
	return "/avatars/" + name, nil
}

// MountHTTP registers the public, unauthenticated avatar route (decision #5
// in the SP4c spec: plain <img> tags must work; images are not sensitive).
// {file} is a single path element by ServeMux contract; the extra checks are
// belt and braces against dotfiles and separators.
func MountHTTP(mux *http.ServeMux, dir string) {
	mux.HandleFunc("GET /avatars/{file}", func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("file")
		if name == "" || name != filepath.Base(name) || strings.HasPrefix(name, ".") {
			http.NotFound(w, r)
			return
		}
		full := filepath.Join(dir, name)
		info, err := os.Stat(full)
		if err != nil || info.IsDir() {
			http.NotFound(w, r)
			return
		}
		http.ServeFile(w, r, full)
	})
}
