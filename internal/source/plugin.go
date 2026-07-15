// Package source defines the Source plugin interface that every inventory
// integration (NetBox, and later Nautobot, Infoblox, etc.) implements, plus
// a registry so plugins can be selected by name from config. This is the
// primary extension point for adding a new source: implement Source in a
// new package, register it via init(), and nothing else in the codebase
// needs to change.
package source

import (
	"context"
	"fmt"
	"sync"

	"github.com/kentikethan/kentik_sync_agent/internal/core"
)

// Source is implemented once per integration. A configured source in the
// agent's config targets exactly one Source instance.
type Source interface {
	// Name returns the plugin's registered type name, e.g. "netbox".
	Name() string

	// Capabilities lists which object types this source can produce. The
	// config layer validates that a source is only asked to sync object
	// types it declares here.
	Capabilities() []core.ObjectType

	// SupportsIncremental reports whether this source can honor a non-empty
	// `since` cursor. Sources that can't should ignore `since` and always
	// return a full FetchResult; the sync engine treats every fetch from
	// such a source as a full reconciliation pass.
	SupportsIncremental() bool

	FetchDevices(ctx context.Context, since string) (core.FetchResult[core.Device], error)
	FetchSites(ctx context.Context, since string) (core.FetchResult[core.Site], error)
	FetchIPGroups(ctx context.Context, since string) (core.FetchResult[core.IPGroup], error)
	FetchDeviceLabels(ctx context.Context, since string) (core.FetchResult[core.DeviceLabels], error)

	// HealthCheck verifies connectivity/auth to the source, used at startup
	// and by the agent's /readyz endpoint.
	HealthCheck(ctx context.Context) error
}

// Factory constructs a Source from its config's `connection`/`mapping`
// blocks, decoded as a raw map so the shared config package never needs to
// know about any individual plugin's fields.
type Factory func(rawConfig map[string]any) (Source, error)

var (
	registryMu sync.RWMutex
	registry   = map[string]Factory{}
)

// Register makes a source type available under the given config `type:`
// name. Intended to be called from an init() in each plugin package.
func Register(sourceType string, f Factory) {
	registryMu.Lock()
	defer registryMu.Unlock()
	if _, exists := registry[sourceType]; exists {
		panic(fmt.Sprintf("source: type %q already registered", sourceType))
	}
	registry[sourceType] = f
}

// New constructs a Source for the given registered type name.
func New(sourceType string, rawConfig map[string]any) (Source, error) {
	registryMu.RLock()
	f, ok := registry[sourceType]
	registryMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("source: unknown type %q (is its package imported for side-effect registration?)", sourceType)
	}
	return f(rawConfig)
}

// Registered returns the currently registered type names, primarily for
// config validation error messages and tests.
func Registered() []string {
	registryMu.RLock()
	defer registryMu.RUnlock()
	names := make([]string, 0, len(registry))
	for name := range registry {
		names = append(names, name)
	}
	return names
}

// HasCapability reports whether a Source declares support for obj.
func HasCapability(s Source, obj core.ObjectType) bool {
	for _, c := range s.Capabilities() {
		if c == obj {
			return true
		}
	}
	return false
}
