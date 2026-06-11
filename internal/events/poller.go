package events

import (
	"context"
	"time"

	"github.com/cbarraford/office-fleet/internal/domain"
	"github.com/cbarraford/office-fleet/internal/plugin"
)

// CursorStore persists poll cursors per plugin. *repo.CursorRepo and MemStore
// satisfy it.
type CursorStore interface {
	Get(ctx context.Context, plugin string) (string, error)
	Set(ctx context.Context, plugin, cursor string) error
}

// IngestFunc matches Ingestor.Ingest.
type IngestFunc func(ctx context.Context, evs []domain.Event) (int, error)

// RunPoller polls src at interval until ctx is done, persisting the cursor
// only after a successful poll AND ingest (so nothing is skipped on failure;
// re-polling is harmless thanks to event-level dedup). The first poll runs
// immediately.
func RunPoller(ctx context.Context, pluginName string, src plugin.PollSource, interval time.Duration,
	cursors CursorStore, ingest IngestFunc, logf func(format string, args ...any)) {

	tick := func() {
		cursor, err := cursors.Get(ctx, pluginName)
		if err != nil {
			logf("poller %s: load cursor: %v", pluginName, err)
			return
		}
		evs, newCursor, err := src.Poll(ctx, cursor)
		if err != nil {
			logf("poller %s: poll: %v", pluginName, err)
			return
		}
		if _, err := ingest(ctx, evs); err != nil {
			logf("poller %s: ingest: %v", pluginName, err)
			return // cursor not advanced; next tick re-polls
		}
		if newCursor != cursor {
			if err := cursors.Set(ctx, pluginName, newCursor); err != nil {
				logf("poller %s: save cursor: %v", pluginName, err)
			}
		}
	}

	tick()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			tick()
		}
	}
}
