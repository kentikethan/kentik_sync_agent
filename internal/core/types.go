// Package core defines the normalized domain model shared by every source
// plugin and the Kentik destination layer. Source plugins translate their
// native API responses into these types; the destination layer translates
// these types into Kentik API calls. Neither side depends on the other's
// native types.
package core

// ObjectType identifies one of the syncable object kinds.
type ObjectType string

const (
	ObjectDevices      ObjectType = "devices"
	ObjectSites        ObjectType = "sites"
	ObjectIPGroups     ObjectType = "ip_groups"
	ObjectDeviceLabels ObjectType = "device_labels"
)

// DeviceStatus is a normalized device lifecycle state, mapped from each
// source's own status vocabulary (e.g. NetBox's "active"/"offline"/"planned").
type DeviceStatus string

const (
	DeviceStatusActive   DeviceStatus = "active"
	DeviceStatusInactive DeviceStatus = "inactive"
	DeviceStatusPlanned  DeviceStatus = "planned"
	DeviceStatusUnknown  DeviceStatus = "unknown"
)

// SiteStatus is a normalized site lifecycle state.
type SiteStatus string

const (
	SiteStatusActive   SiteStatus = "active"
	SiteStatusInactive SiteStatus = "inactive"
	SiteStatusPlanned  SiteStatus = "planned"
	SiteStatusUnknown  SiteStatus = "unknown"
)

// IPGroupDirection mirrors Kentik Populator's match direction.
type IPGroupDirection string

const (
	IPGroupDirectionSrc    IPGroupDirection = "src"
	IPGroupDirectionDst    IPGroupDirection = "dst"
	IPGroupDirectionEither IPGroupDirection = "either"
)

// Device is a normalized network device record.
type Device struct {
	ExternalID     string
	Name           string
	SiteExternalID string
	DeviceType     string
	Role           string
	Status         DeviceStatus
	PrimaryIPv4    string
	PrimaryIPv6    string
	SerialNumber   string
	Platform       string
	Tags           []string
	CustomFields   map[string]string

	// SourcePlugin is stamped by the sync engine (not the plugin itself) to
	// scope managed-object tagging and state-store lookups per source.
	SourcePlugin string
}

// ExternalKey is the stable identity used for diffing against prior state.
func (d Device) ExternalKey() string { return d.SourcePlugin + ":" + d.ExternalID }

// Site is a normalized site/location record.
type Site struct {
	ExternalID string
	Name       string
	Status     SiteStatus
	Latitude   *float64
	Longitude  *float64
	Address    string
	Region     string
	Tags       []string

	SourcePlugin string
}

func (s Site) ExternalKey() string { return s.SourcePlugin + ":" + s.ExternalID }

// IPGroup is the source-agnostic form of an "IP address grouping" — a CIDR
// (or single address) mapped to a label value, which the Kentik destination
// layer maps onto a Custom Dimension Populator.
type IPGroup struct {
	ExternalID string
	CIDR       string
	Label      string
	Direction  IPGroupDirection

	SourcePlugin string
}

func (g IPGroup) ExternalKey() string { return g.SourcePlugin + ":" + g.ExternalID }

// DeviceLabels is the fully-formatted set of Kentik labels a source wants
// applied to one already-synced device (see core.Device), e.g.
// "Netbox:Device:Tenant:acme" or "Netbox:Device:Role:router". The Kentik
// destination resolves each name to a label ID (creating it if needed) and
// applies the set via Kentik's device-scoped label API.
type DeviceLabels struct {
	// ExternalID matches the Device.ExternalID this label set belongs to.
	ExternalID string
	Labels     []string

	SourcePlugin string
}

func (d DeviceLabels) ExternalKey() string { return d.SourcePlugin + ":" + d.ExternalID }

// FetchResult carries a source plugin's fetch output for one object type.
// Deleted/Cursor support both incremental and full-diff source behavior
// uniformly — see internal/sync for how the engine consumes this.
type FetchResult[T any] struct {
	// Items is the current/changed set of objects, depending on whether the
	// fetch was incremental (since != "") or full (since == "").
	Items []T

	// Deleted holds ExternalIDs the source explicitly knows were removed
	// (e.g. from a change log). Sources that can't detect deletes leave this
	// nil; the sync engine falls back to full-diff-based delete detection.
	Deleted []string

	// Cursor is an opaque value to persist and pass as `since` on the next
	// incremental fetch (e.g. an RFC3339 timestamp for NetBox). Empty if the
	// source doesn't support incremental fetches.
	Cursor string

	// Truncated is true if a paginated fetch was cut short by a safety limit
	// rather than reaching the natural end of the result set.
	Truncated bool
}
