package monitoring

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
