package mapping

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/cloud-barista/ai-o11y/internal/monitoring"
)

// DefaultSyncPause is the wait between two existence checks, the spacing
// mc-observability keeps between its CB-Tumblebug calls.
const DefaultSyncPause = 400 * time.Millisecond

// ErrSyncRunning is returned when a sync is asked for while one is running.
var ErrSyncRunning = errors.New("a sync is already running")

// ErrListFailed wraps a failure to list the platform's targets; the sync
// changed nothing.
var ErrListFailed = errors.New("failed to list targets, nothing changed")

// Syncer brings the target table in step with the management platform. Queries
// read the table only; syncing is the one place the platform is asked, and it
// runs only when called.
//
// It never adds a target: targets are saved when their agent is installed.
type Syncer struct {
	store  *Store
	source monitoring.TargetSource
	// pause is the wait between two existence checks.
	pause time.Duration
	mu    sync.Mutex
}

// NewSyncer returns a Syncer for the targets in store, checked against source.
func NewSyncer(store *Store, source monitoring.TargetSource, pause time.Duration) *Syncer {
	return &Syncer{store: store, source: source, pause: pause}
}

// SyncResult is what a sync changed, by target key.
type SyncResult struct {
	// Updated targets were listed by the platform; their stored description
	// was replaced with the listed one.
	Updated []string `json:"updated"`
	// Deleted targets were missing from the listing and the platform answered
	// that they are gone.
	Deleted []string `json:"deleted"`
	// Kept targets were missing from the listing but could not be confirmed
	// gone, with the reason; they stay in the table.
	Kept map[string]string `json:"kept"`
}

// Sync lists the platform's targets, then:
//
//   - updates every stored target the listing has;
//   - for every stored target the listing lacks, asks the platform for that
//     one target and deletes it only when the platform answers that it is
//     gone. Any other answer keeps it, so a platform fault never empties the
//     table.
//
// If the listing fails, nothing is changed.
func (s *Syncer) Sync(ctx context.Context) (SyncResult, error) {
	if !s.mu.TryLock() {
		return SyncResult{}, ErrSyncRunning
	}
	defer s.mu.Unlock()

	result := SyncResult{Kept: map[string]string{}}
	keys, err := s.store.Keys(ctx)
	if err != nil {
		return result, fmt.Errorf("failed to read stored targets: %w", err)
	}
	listed, err := s.source.List(ctx, nil)
	if err != nil {
		return result, fmt.Errorf("%w: %w", ErrListFailed, err)
	}
	byKey := make(map[string]monitoring.Target, len(listed))
	for _, t := range listed {
		byKey[t.EntityID] = t
	}

	var missing []string
	for _, key := range keys {
		t, ok := byKey[key]
		if !ok {
			missing = append(missing, key)
			continue
		}
		if err := s.store.Save(ctx, t); err != nil {
			return result, fmt.Errorf("failed to update target %q: %w", key, err)
		}
		result.Updated = append(result.Updated, key)
	}

	for i, key := range missing {
		if i > 0 && s.pause > 0 {
			timer := time.NewTimer(s.pause)
			select {
			case <-ctx.Done():
				timer.Stop()
				return result, ctx.Err()
			case <-timer.C:
			}
		}
		stored, err := s.store.Lookup(ctx, key)
		if err != nil {
			return result, fmt.Errorf("failed to read stored target %q: %w", key, err)
		}
		exists, err := s.source.Exists(ctx, stored)
		switch {
		case err != nil:
			log.Warn().Str("targetId", key).Err(err).Msg("Keeping target that could not be checked")
			result.Kept[key] = "the platform could not be asked whether it still has it"
		case exists:
			result.Kept[key] = "not listed, but the platform still has it"
		default:
			if err := s.store.Delete(ctx, key); err != nil {
				return result, fmt.Errorf("failed to delete target %q: %w", key, err)
			}
			result.Deleted = append(result.Deleted, key)
		}
	}
	return result, nil
}
