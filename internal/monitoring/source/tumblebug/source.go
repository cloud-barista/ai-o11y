package tumblebug

import (
	"context"
	"fmt"
	"strings"

	"github.com/rs/zerolog/log"

	"github.com/cloud-barista/ai-o11y/internal/monitoring"
)

// ProviderName is the name the adapter is registered under.
const ProviderName = "tumblebug"

// KeyField is the CB-Tumblebug field a target's EntityID holds: the node Uid,
// which CB-Tumblebug issues when it creates the node and never changes. A
// re-created node gets a new Uid and is a new target.
const KeyField = "NodeInfo.Uid"

// ScopeNamespace narrows a listing to one CB-Tumblebug namespace.
const ScopeNamespace = "namespace"

// KindVM is the kind of every target this adapter lists.
const KindVM = "vm"

// Keys of Target.Extra this adapter fills.
const (
	ExtraNamespace     = "namespace"
	ExtraInfra         = "infraId"
	ExtraNode          = "nodeId"
	ExtraConnection    = "connectionName"
	ExtraZone          = "zone"
	ExtraCspResourceID = "cspResourceId"
	ExtraOSType        = "osType"
)

// Source is the management platform adapter for CB-Tumblebug.
type Source struct {
	client *Client
}

// NewSource builds a Source over client.
func NewSource(client *Client) *Source {
	return &Source{client: client}
}

var _ monitoring.TargetSource = (*Source)(nil)

// KeyField implements monitoring.TargetSource.
func (*Source) KeyField() string { return KeyField }

// List returns every node, or the nodes of the namespace scope names. A node
// without a Uid cannot be told apart from a re-created one, so it is left out
// and logged.
func (s *Source) List(ctx context.Context, scope monitoring.Scope) ([]monitoring.Target, error) {
	namespaces, err := s.namespaces(ctx, scope)
	if err != nil {
		return nil, err
	}
	var targets []monitoring.Target
	for _, nsID := range namespaces {
		nodes, err := s.client.listNodes(ctx, nsID)
		if err != nil {
			return nil, fmt.Errorf("failed to list nodes of namespace %q: %w", nsID, err)
		}
		for _, n := range nodes {
			t := toTarget(nsID, n)
			if err := t.Validate(); err != nil {
				log.Warn().Str("namespace", nsID).Str("infraId", n.InfraID).Str("nodeId", n.ID).
					Err(err).Msg("Skipping CB-Tumblebug node")
				continue
			}
			targets = append(targets, t)
		}
	}
	return targets, nil
}

func (s *Source) namespaces(ctx context.Context, scope monitoring.Scope) ([]string, error) {
	if nsID := scope[ScopeNamespace]; nsID != "" {
		return []string{nsID}, nil
	}
	all, err := s.client.listNamespaces(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list namespaces: %w", err)
	}
	ids := make([]string, 0, len(all))
	for _, ns := range all {
		ids = append(ids, ns.ID)
	}
	return ids, nil
}

func toTarget(nsID string, n node) monitoring.Target {
	t := monitoring.Target{
		EntityID: n.UID,
		Kind:     KindVM,
		Name:     n.Name,
		Provider: n.ConnectionConfig.ProviderName,
		Region:   n.Region.Region,
		OS:       osFamily(n.Image.OSType),
		Extra: map[string]string{
			ExtraNamespace: nsID,
			ExtraInfra:     n.InfraID,
			ExtraNode:      n.ID,
		},
	}
	if t.Name == "" {
		t.Name = n.ID
	}
	if strings.EqualFold(n.Spec.AcceleratorType, "gpu") && n.Spec.AcceleratorCount > 0 {
		t.GPUCount = n.Spec.AcceleratorCount
		t.GPUModel = n.Spec.AcceleratorModel
	}
	for key, value := range map[string]string{
		ExtraConnection:    n.ConnectionName,
		ExtraZone:          n.Region.Zone,
		ExtraCspResourceID: n.CspResourceID,
		ExtraOSType:        n.Image.OSType,
	} {
		if value != "" {
			t.Extra[key] = value
		}
	}
	return t
}

// osFamily reduces CB-Tumblebug's free-form osType, such as "ubuntu 22.04",
// to a family. NodeInfo carries no platform field, so any osType naming
// Windows is "windows" and any other non-empty one is "linux".
func osFamily(osType string) string {
	switch {
	case osType == "":
		return ""
	case strings.Contains(strings.ToLower(osType), "windows"):
		return "windows"
	default:
		return "linux"
	}
}
