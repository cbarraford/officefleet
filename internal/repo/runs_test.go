package repo

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/cbarraford/office-fleet/internal/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type fakeRunDB struct {
	exec func(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

func (f *fakeRunDB) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	return f.exec(ctx, sql, args...)
}

func (f *fakeRunDB) Query(context.Context, string, ...any) (pgx.Rows, error) {
	panic("not used")
}

func (f *fakeRunDB) QueryRow(context.Context, string, ...any) pgx.Row {
	panic("not used")
}

func TestRunRepo_ReconcileStaleRunningMarksOldRunningFailed(t *testing.T) {
	cutoff := time.Date(2026, 6, 12, 9, 30, 0, 0, time.UTC)
	reason := "orphaned by restart"
	var gotSQL string
	var gotArgs []any
	db := &fakeRunDB{
		exec: func(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
			gotSQL = sql
			gotArgs = args
			return pgconn.NewCommandTag("UPDATE 2"), nil
		},
	}
	rr := &RunRepo{db: db}

	n, err := rr.ReconcileStaleRunning(context.Background(), cutoff, reason)
	if err != nil {
		t.Fatalf("ReconcileStaleRunning returned error: %v", err)
	}
	if n != 2 {
		t.Fatalf("affected rows = %d, want 2", n)
	}
	if !strings.Contains(gotSQL, "UPDATE runs SET status=$1, error=$2, finished_at=NOW()") {
		t.Fatalf("update SQL does not set terminal failure fields: %s", gotSQL)
	}
	if !strings.Contains(gotSQL, "WHERE status=$3 AND started_at < $4") {
		t.Fatalf("update SQL does not restrict to stale running rows: %s", gotSQL)
	}
	if gotArgs[0] != domain.RunStatusFailed {
		t.Fatalf("status arg = %v, want failed", gotArgs[0])
	}
	if gotArgs[1] != reason {
		t.Fatalf("reason arg = %v, want %q", gotArgs[1], reason)
	}
	if gotArgs[2] != domain.RunStatusRunning {
		t.Fatalf("running arg = %v, want running", gotArgs[2])
	}
	if gotArgs[3] != cutoff {
		t.Fatalf("cutoff arg = %v, want %v", gotArgs[3], cutoff)
	}
}
