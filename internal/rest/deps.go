package rest

import (
	"context"

	"github.com/cloud-barista/ai-o11y/internal/monitoring"
	"github.com/cloud-barista/ai-o11y/internal/monitoring/mapping"
)

// TargetStore is the target table: targets saved at agent install, looked up
// when querying.
type TargetStore interface {
	monitoring.Mapper
	Save(ctx context.Context, t monitoring.Target) error
	Delete(ctx context.Context, key string) error
}

// TargetSyncer brings the target table in step with the management platform.
type TargetSyncer interface {
	Sync(ctx context.Context) (mapping.SyncResult, error)
}

// Deps is what the monitoring routes run on. A nil part leaves the routes
// that need it answering 503, so the service starts with what is configured.
type Deps struct {
	Targets TargetStore
	Syncer  TargetSyncer
	// Source is the management platform adapter, used to describe a target
	// registered with its platform names only.
	Source     monitoring.TargetSource
	Catalog    monitoring.MetricCatalog
	Backend    monitoring.Backend
	Collectors []monitoring.CollectorKind
}

// handler serves the monitoring routes.
type handler struct {
	Deps
}
