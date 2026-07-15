package netbox

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/kentikethan/kentik_sync_agent/internal/core"
)

// nestedRef mirrors NetBox's "brief" nested-object representation used for
// foreign keys like site/device_type/role/platform/tenant.
type nestedRef struct {
	ID      int    `json:"id"`
	Name    string `json:"name"`
	Slug    string `json:"slug"`
	Display string `json:"display"`
}

func (r *nestedRef) idString() string {
	if r == nil {
		return ""
	}
	return strconv.Itoa(r.ID)
}

func (r *nestedRef) label() string {
	if r == nil {
		return ""
	}
	if r.Slug != "" {
		return r.Slug
	}
	if r.Name != "" {
		return r.Name
	}
	return r.Display
}

// statusField mirrors NetBox's {"value": "active", "label": "Active"} status
// representation, used on devices, sites, prefixes, and IP ranges.
type statusField struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

type ipAddressRef struct {
	ID      int    `json:"id"`
	Address string `json:"address"`
}

type tagRef struct {
	Name string `json:"name"`
	Slug string `json:"slug"`
}

type netboxDevice struct {
	ID           int            `json:"id"`
	Name         string         `json:"name"`
	Site         *nestedRef     `json:"site"`
	DeviceType   *nestedRef     `json:"device_type"`
	Role         *nestedRef     `json:"role"`
	Tenant       *nestedRef     `json:"tenant"`
	Status       statusField    `json:"status"`
	PrimaryIP4   *ipAddressRef  `json:"primary_ip4"`
	PrimaryIP6   *ipAddressRef  `json:"primary_ip6"`
	Serial       string         `json:"serial"`
	Platform     *nestedRef     `json:"platform"`
	Tags         []tagRef       `json:"tags"`
	CustomFields map[string]any `json:"custom_fields"`
	LastUpdated  string         `json:"last_updated"`
}

func deviceStatus(v string) core.DeviceStatus {
	switch v {
	case "active":
		return core.DeviceStatusActive
	case "offline", "inactive", "decommissioning":
		return core.DeviceStatusInactive
	case "planned", "staged":
		return core.DeviceStatusPlanned
	default:
		return core.DeviceStatusUnknown
	}
}

func stripCIDR(addr string) string {
	if i := strings.IndexByte(addr, '/'); i >= 0 {
		return addr[:i]
	}
	return addr
}

func customFieldsToStrings(cf map[string]any) map[string]string {
	if len(cf) == 0 {
		return nil
	}
	out := make(map[string]string, len(cf))
	for k, v := range cf {
		if v == nil {
			continue
		}
		out[k] = fmt.Sprint(v)
	}
	return out
}

func tagNames(tags []tagRef) []string {
	if len(tags) == 0 {
		return nil
	}
	names := make([]string, len(tags))
	for i, t := range tags {
		names[i] = t.Slug
		if names[i] == "" {
			names[i] = t.Name
		}
	}
	return names
}

func (d netboxDevice) toCore() core.Device {
	dev := core.Device{
		ExternalID:     strconv.Itoa(d.ID),
		Name:           d.Name,
		SiteExternalID: d.Site.idString(),
		DeviceType:     d.DeviceType.label(),
		Role:           d.Role.label(),
		Status:         deviceStatus(d.Status.Value),
		SerialNumber:   d.Serial,
		Platform:       d.Platform.label(),
		Tags:           tagNames(d.Tags),
		CustomFields:   customFieldsToStrings(d.CustomFields),
	}
	if d.PrimaryIP4 != nil {
		dev.PrimaryIPv4 = stripCIDR(d.PrimaryIP4.Address)
	}
	if d.PrimaryIP6 != nil {
		dev.PrimaryIPv6 = stripCIDR(d.PrimaryIP6.Address)
	}
	return dev
}

// FetchDevices fetches devices from NetBox's dcim/devices/ endpoint. If
// since is non-empty, only devices updated at or after that RFC3339
// timestamp are returned (incremental fetch); otherwise every device is
// returned (full fetch).
func (s *Source) FetchDevices(ctx context.Context, since string) (core.FetchResult[core.Device], error) {
	fetchStart := time.Now()
	raw, truncated, err := listAll[netboxDevice](ctx, s, "/api/dcim/devices/", sinceQuery(since))
	if err != nil {
		return core.FetchResult[core.Device]{}, fmt.Errorf("netbox: fetching devices: %w", err)
	}
	items := make([]core.Device, len(raw))
	for i, d := range raw {
		items[i] = d.toCore()
	}
	return core.FetchResult[core.Device]{
		Items:     items,
		Cursor:    nowCursor(fetchStart),
		Truncated: truncated,
	}, nil
}
