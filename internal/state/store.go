package state

import "context"

// Store provides per-assignment private state: KV and structured notes.
// Keyed by assignmentID, not dutyID — two agents running the same duty need independent state.
type Store interface {
	// KV operations for dedup keys, cursors, and small memory blobs.
	Get(ctx context.Context, assignmentID, key string) ([]byte, bool, error)
	Set(ctx context.Context, assignmentID, key string, val []byte) error
	Delete(ctx context.Context, assignmentID, key string) error

	// List returns all key/value pairs stored for the given assignmentID.
	List(ctx context.Context, assignmentID string) (map[string][]byte, error)

	// AppendNote adds a structured memory row for an assignment.
	AppendNote(ctx context.Context, assignmentID string, note any) error

	// HasProcessed returns true if the given dedupKey has already been recorded.
	HasProcessed(ctx context.Context, assignmentID, dedupKey string) (bool, error)

	// MarkProcessed records a dedupKey as processed.
	MarkProcessed(ctx context.Context, assignmentID, dedupKey string) error

	// ClaimProcessed atomically records a dedupKey as processed and reports
	// whether THIS call won the claim. A concurrent second caller for the same
	// key gets false (it must not run) — this is the race-free guard the
	// pipeline uses before a run instead of a check-then-mark (TOCTOU).
	ClaimProcessed(ctx context.Context, assignmentID, dedupKey string) (bool, error)

	// UnmarkProcessed releases a claim, allowing the key to be re-claimed. The
	// pipeline calls this when a claimed run fails so the event can be retried.
	UnmarkProcessed(ctx context.Context, assignmentID, dedupKey string) error
}
