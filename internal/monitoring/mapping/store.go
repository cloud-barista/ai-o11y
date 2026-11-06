// Package mapping holds the implementation of the monitoring mapping layer:
// the target table, kept in MariaDB.
package mapping

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/cloud-barista/ai-o11y/internal/monitoring"
)

// Store is the Mapper over the target table. A target is saved when its agent
// is installed and kept in step with the platform by a Syncer, so queries
// resolve targets from the table and never call the platform.
type Store struct {
	db       *gorm.DB
	platform string
	now      func() time.Time
}

var _ monitoring.Mapper = (*Store)(nil)

// NewStore returns a Store over db for the targets of the named management
// platform adapter.
func NewStore(db *gorm.DB, platform string) *Store {
	return &Store{db: db, platform: platform, now: func() time.Time { return time.Now().UTC() }}
}

// Save stores t as the platform describes it now, replacing what was stored
// for its key. The time it was first saved is kept.
func (s *Store) Save(ctx context.Context, t monitoring.Target) error {
	if err := t.Validate(); err != nil {
		return err
	}
	now := s.now()
	row := targetRow{
		EntityID: t.EntityID, Platform: s.platform, Kind: t.Kind, Name: t.Name,
		Provider: t.Provider, Region: t.Region, OS: t.OS, GPUCount: t.GPUCount, GPUModel: t.GPUModel,
		RegisteredAt: now, UpdatedAt: now,
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "entity_id"}},
			DoUpdates: clause.AssignmentColumns([]string{
				"platform", "kind", "name", "provider", "region", "os", "gpu_count", "gpu_model", "updated_at",
			}),
		}).Create(&row).Error; err != nil {
			return fmt.Errorf("failed to save target %q: %w", t.EntityID, err)
		}
		if err := tx.Where("entity_id = ?", t.EntityID).Delete(&attributeRow{}).Error; err != nil {
			return fmt.Errorf("failed to replace attributes of target %q: %w", t.EntityID, err)
		}
		if attrs := attributeRows(t); len(attrs) > 0 {
			if err := tx.Create(&attrs).Error; err != nil {
				return fmt.Errorf("failed to save attributes of target %q: %w", t.EntityID, err)
			}
		}
		return nil
	})
}

// Delete removes the target with key and its attributes. Deleting a key that
// is not stored is not an error.
func (s *Store) Delete(ctx context.Context, key string) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("entity_id = ?", key).Delete(&attributeRow{}).Error; err != nil {
			return err
		}
		return tx.Where("entity_id = ?", key).Delete(&targetRow{}).Error
	})
}

// Select implements monitoring.Mapper with the (attr, value) index: a target
// matches when it has a row for every condition.
func (s *Store) Select(ctx context.Context, sel monitoring.Selector) ([]monitoring.Target, error) {
	db := s.db.WithContext(ctx)
	var keys []string
	if len(sel) == 0 {
		if err := db.Model(&targetRow{}).Order("entity_id").Pluck("entity_id", &keys).Error; err != nil {
			return nil, fmt.Errorf("failed to list targets: %w", err)
		}
	} else {
		var conds []string
		var args []any
		for _, attr := range slices.Sorted(maps.Keys(sel)) {
			conds = append(conds, "(attr = ? AND value = ?)")
			args = append(args, attr, sel[attr])
		}
		if err := db.Model(&attributeRow{}).
			Where(strings.Join(conds, " OR "), args...).
			Group("entity_id").Having("COUNT(*) = ?", len(sel)).
			Order("entity_id").Pluck("entity_id", &keys).Error; err != nil {
			return nil, fmt.Errorf("failed to select targets: %w", err)
		}
	}
	return s.load(db, keys)
}

// Lookup implements monitoring.Mapper.
func (s *Store) Lookup(ctx context.Context, key string) (monitoring.Target, error) {
	targets, err := s.load(s.db.WithContext(ctx), []string{key})
	if err != nil {
		return monitoring.Target{}, err
	}
	if len(targets) == 0 {
		return monitoring.Target{}, fmt.Errorf("unknown target %q", key)
	}
	return targets[0], nil
}

// Keys returns the key of every stored target, for syncing with the
// platform.
func (s *Store) Keys(ctx context.Context) ([]string, error) {
	var keys []string
	err := s.db.WithContext(ctx).Model(&targetRow{}).Order("entity_id").Pluck("entity_id", &keys).Error
	return keys, err
}

// load reads the targets with keys, in key order.
func (s *Store) load(db *gorm.DB, keys []string) ([]monitoring.Target, error) {
	if len(keys) == 0 {
		return nil, nil
	}
	var rows []targetRow
	if err := db.Where("entity_id IN ?", keys).Order("entity_id").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("failed to read targets: %w", err)
	}
	var attrs []attributeRow
	if err := db.Where("entity_id IN ?", keys).Find(&attrs).Error; err != nil {
		return nil, fmt.Errorf("failed to read target attributes: %w", err)
	}
	byKey := map[string][]attributeRow{}
	for _, a := range attrs {
		byKey[a.EntityID] = append(byKey[a.EntityID], a)
	}
	out := make([]monitoring.Target, 0, len(rows))
	for _, r := range rows {
		out = append(out, targetFromRows(r, byKey[r.EntityID]))
	}
	return out, nil
}

// commonAttrs are the attribute names of the common fields, which have their
// own columns; every other attribute is a platform attribute.
var commonAttrs = map[string]bool{
	monitoring.AttrKind: true, monitoring.AttrName: true, monitoring.AttrProvider: true,
	monitoring.AttrRegion: true, monitoring.AttrOS: true, monitoring.AttrGPUCount: true,
	monitoring.AttrGPUModel: true,
}

func attributeRows(t monitoring.Target) []attributeRow {
	attrs := t.Attributes()
	rows := make([]attributeRow, 0, len(attrs))
	for _, name := range slices.Sorted(maps.Keys(attrs)) {
		rows = append(rows, attributeRow{EntityID: t.EntityID, Attr: name, Value: attrs[name]})
	}
	return rows
}

func targetFromRows(r targetRow, attrs []attributeRow) monitoring.Target {
	t := monitoring.Target{
		EntityID: r.EntityID, Kind: r.Kind, Name: r.Name, Provider: r.Provider,
		Region: r.Region, OS: r.OS, GPUCount: r.GPUCount, GPUModel: r.GPUModel,
	}
	for _, a := range attrs {
		if commonAttrs[a.Attr] {
			continue
		}
		if t.Extra == nil {
			t.Extra = map[string]string{}
		}
		t.Extra[a.Attr] = a.Value
	}
	return t
}
