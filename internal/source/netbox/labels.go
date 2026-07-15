package netbox

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/kentikethan/kentik_sync_agent/internal/core"
)

// deviceLabelsFor builds the "Netbox:Device:..." label set for one device
// from its tenant and role, in the format the Kentik destination expects.
// A component with no NetBox value is omitted rather than emitted empty.
func deviceLabelsFor(d netboxDevice) []string {
	var labels []string
	if name := d.Tenant.label(); name != "" {
		labels = append(labels, "Netbox:Device:Tenant:"+name)
	}
	if name := d.Role.label(); name != "" {
		labels = append(labels, "Netbox:Device:Role:"+name)
	}
	return labels
}

// FetchDeviceLabels fetches every NetBox device and derives the Kentik
// label set for each from its tenant and role. Unlike FetchDevices, since is
// ignored and every fetch is a full fetch: the label set for a device must
// reflect its current tenant/role even if NetBox's last_updated timestamp on
// the device itself didn't change (e.g. only its tenant object was edited).
func (s *Source) FetchDeviceLabels(ctx context.Context, since string) (core.FetchResult[core.DeviceLabels], error) {
	fetchStart := time.Now()
	raw, truncated, err := listAll[netboxDevice](ctx, s, "/api/dcim/devices/", nil)
	if err != nil {
		return core.FetchResult[core.DeviceLabels]{}, fmt.Errorf("netbox: fetching devices for label sync: %w", err)
	}
	items := make([]core.DeviceLabels, len(raw))
	for i, d := range raw {
		items[i] = core.DeviceLabels{
			ExternalID: strconv.Itoa(d.ID),
			Labels:     deviceLabelsFor(d),
		}
	}
	return core.FetchResult[core.DeviceLabels]{
		Items:     items,
		Cursor:    nowCursor(fetchStart),
		Truncated: truncated,
	}, nil
}
