package monitoring

import (
	"fmt"
	"regexp"
	"strings"
)

// labelNameRE is the classic Prometheus label name syntax. Names outside it,
// such as "cloud.provider", are rewritten by the backend's OTLP translation
// ("cloud_provider") or refused (Mimir), so queries would have to guess the
// stored name.
var labelNameRE = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

// ValidateLabelName reports whether name can be stored and queried unchanged
// by every backend: classic syntax and not reserved by Prometheus ("__").
func ValidateLabelName(name string) error {
	if !labelNameRE.MatchString(name) {
		return fmt.Errorf("label name %q must match %s", name, labelNameRE)
	}
	if strings.HasPrefix(name, "__") {
		return fmt.Errorf("label name %q is reserved", name)
	}
	return nil
}

// IdentityProcessor is the collector processor that stamps a target's key.
const IdentityProcessor = "attributes/identity"

// IdentityFragment returns the collector configuration that stamps entity_id,
// the target's management platform key, on every data point it produces.
// Nothing else is stamped: the target's other attributes stay in the mapping
// layer and are resolved when querying. The label goes on data points rather
// than on the resource, so it reaches every series without backend-side
// promotion of resource attributes, and the action upserts, so a receiver
// cannot replace it. Adding the processor to pipelines is left to the caller
// that merges fragments.
func IdentityFragment(t Target) (ConfigFragment, error) {
	if err := t.Validate(); err != nil {
		return ConfigFragment{}, err
	}
	return ConfigFragment{
		Processors: map[string]any{
			IdentityProcessor: map[string]any{
				"actions": []any{
					map[string]any{"key": LabelEntityID, "value": t.EntityID, "action": "upsert"},
				},
			},
		},
	}, nil
}
