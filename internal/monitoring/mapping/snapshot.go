// Package mapping holds the implementation of the monitoring mapping layer.
package mapping

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"

	"github.com/cloud-barista/ai-o11y/internal/monitoring"
)

// Snapshot is a Mapper over the latest target list the management platform
// adapter returned. The platform is the source of truth, so nothing is stored
// beyond the list itself: losing it costs one more listing, not data.
type Snapshot struct {
	mu    sync.RWMutex
	byKey map[string]monitoring.Target
}

var _ monitoring.Mapper = (*Snapshot)(nil)

// NewSnapshot returns an empty Snapshot.
func NewSnapshot() *Snapshot {
	return &Snapshot{byKey: map[string]monitoring.Target{}}
}

// Replace swaps in a new target list. A target that fails Validate, or two
// targets with the same key, reject the whole list and keep the previous one,
// so a broken listing never reaches queries.
func (s *Snapshot) Replace(targets []monitoring.Target) error {
	byKey := make(map[string]monitoring.Target, len(targets))
	for _, t := range targets {
		if err := t.Validate(); err != nil {
			return err
		}
		if _, dup := byKey[t.EntityID]; dup {
			return fmt.Errorf("two targets have key %q", t.EntityID)
		}
		t.Extra = maps.Clone(t.Extra)
		byKey[t.EntityID] = t
	}
	s.mu.Lock()
	s.byKey = byKey
	s.mu.Unlock()
	return nil
}

// Select implements monitoring.Mapper.
func (s *Snapshot) Select(_ context.Context, sel monitoring.Selector) ([]monitoring.Target, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []monitoring.Target
	for _, t := range s.byKey {
		if sel.Matches(t) {
			out = append(out, clone(t))
		}
	}
	slices.SortFunc(out, func(a, b monitoring.Target) int { return strings.Compare(a.EntityID, b.EntityID) })
	return out, nil
}

// Lookup implements monitoring.Mapper.
func (s *Snapshot) Lookup(_ context.Context, key string) (monitoring.Target, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	t, ok := s.byKey[key]
	if !ok {
		return monitoring.Target{}, fmt.Errorf("unknown target %q", key)
	}
	return clone(t), nil
}

func clone(t monitoring.Target) monitoring.Target {
	t.Extra = maps.Clone(t.Extra)
	return t
}
