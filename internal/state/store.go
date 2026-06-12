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

	// ClaimProcessed atomically records a dedupKey and reports whether this
	// caller won the claim. A false result means another run already owns it.
	ClaimProcessed(ctx context.Context, assignmentID, dedupKey string) (bool, error)

	// DeleteProcessed releases a claimed dedupKey after a non-successful run.
	DeleteProcessed(ctx context.Context, assignmentID, dedupKey string) error
}
