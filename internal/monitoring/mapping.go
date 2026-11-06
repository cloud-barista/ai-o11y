package monitoring

import (
	"context"
	"errors"
	"strconv"
)

// ErrTargetNotFound is returned, wrapped, for a target that is not there.
var ErrTargetNotFound = errors.New("target not found")

// Mapper is the mapping layer. It follows the management platform's own keys
// and attributes, so monitoring adapts to whichever platform a deployment runs
// with: series carry only the key, and which targets a screen means (a
// namespace, a provider, the GPU hosts) is resolved here when querying. The
// targets come from the platform adapter; ai-o11y keeps no groups of its own.
type Mapper interface {
	// Select returns the targets whose attributes match every condition of
	// sel, sorted by key. An empty selector selects every target.
	Select(ctx context.Context, sel Selector) ([]Target, error)
	// Lookup returns the target with key, for showing a query result.
	Lookup(ctx context.Context, key string) (Target, error)
}

// Selector picks targets by attribute: every name must have exactly the given
// value. Names are those of Target.Attributes.
type Selector map[string]string

// Attribute names of the common target fields. Target.Extra keys are used as
// they are.
const (
	AttrKind     = "kind"
	AttrName     = "name"
	AttrProvider = "provider"
	AttrRegion   = "region"
	AttrOS       = "os"
	AttrGPUCount = "gpuCount"
	AttrGPUModel = "gpuModel"
)

// Attributes returns every attribute of t a Selector can match: the common
// fields under the Attr names, and Extra. A common field wins over an Extra key
// of the same name. Empty values are left out.
func (t Target) Attributes() map[string]string {
	attrs := make(map[string]string, len(t.Extra)+7)
	for k, v := range t.Extra {
		if v != "" {
			attrs[k] = v
		}
	}
	for k, v := range map[string]string{
		AttrKind:     t.Kind,
		AttrName:     t.Name,
		AttrProvider: t.Provider,
		AttrRegion:   t.Region,
		AttrOS:       t.OS,
		AttrGPUModel: t.GPUModel,
	} {
		if v != "" {
			attrs[k] = v
		}
	}
	attrs[AttrGPUCount] = strconv.Itoa(t.GPUCount)
	return attrs
}

// Matches reports whether t has every attribute sel asks for.
func (sel Selector) Matches(t Target) bool {
	attrs := t.Attributes()
	for k, want := range sel {
		if got, ok := attrs[k]; !ok || got != want {
			return false
		}
	}
	return true
}
