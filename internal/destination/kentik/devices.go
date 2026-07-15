package kentik

import (
	"context"
	"fmt"

	"github.com/kentik/api-schema-public/gen/go/kentik/device/v202504beta2"

	"github.com/kentikethan/kentik_sync_agent/internal/core"
)

// maxBatchSize caps how many items go into a single Kentik batch RPC, so a
// sync of thousands of objects doesn't send one enormous request.
const maxBatchSize = 500

// DeviceApplier applies core.Device values to Kentik's Device API.
type DeviceApplier struct {
	client *Client
	cfg    Config
}

func NewDeviceApplier(c *Client) *DeviceApplier {
	return &DeviceApplier{client: c, cfg: c.cfg}
}

// buildDeviceConcise translates a normalized Device into Kentik's create/
// update request shape. kentikSiteID is the Kentik-assigned numeric site ID
// for d.SiteExternalID, resolved by the caller via the state store (see
// internal/sync). kentikID is empty for a create, and set to the
// previously-assigned Kentik device ID for an update.
func buildDeviceConcise(d core.Device, kentikSiteID uint32, kentikID string, cfg Config) *device.DeviceConcise {
	dc := &device.DeviceConcise{
		Id:            kentikID,
		DeviceName:    d.Name,
		DeviceSubtype: cfg.DefaultDeviceSubtype,
		PlanId:        cfg.DefaultPlanID,
		SiteId:        kentikSiteID,
	}
	// Kentik devices report flow from a "sending IP" rather than carrying a
	// generic primary-IP field; the device's primary address is the closest
	// available analog from a CMDB source like NetBox.
	if d.PrimaryIPv4 != "" {
		dc.SendingIps = append(dc.SendingIps, d.PrimaryIPv4)
	}
	if d.PrimaryIPv6 != "" {
		dc.SendingIps = append(dc.SendingIps, d.PrimaryIPv6)
	}
	// DeviceConcise has no free-form "device type"/"role" field distinct
	// from device_subtype; fold NetBox's type/role into the description so
	// they're at least visible in the Kentik UI.
	switch {
	case d.DeviceType != "" && d.Role != "":
		dc.DeviceDescription = fmt.Sprintf("role: %s, type: %s", d.Role, d.DeviceType)
	case d.Role != "":
		dc.DeviceDescription = fmt.Sprintf("role: %s", d.Role)
	case d.DeviceType != "":
		dc.DeviceDescription = fmt.Sprintf("type: %s", d.DeviceType)
	}
	return dc
}

// Create batch-creates devices in chunks of maxBatchSize. siteIDs maps each
// item's SiteExternalID to its resolved Kentik numeric site ID; items whose
// site hasn't been synced yet are skipped and reported as failed, since
// Kentik requires a valid site_id.
func (a *DeviceApplier) Create(ctx context.Context, items []core.Device, siteIDs map[string]uint32) (created map[string]string, failed map[string]error) {
	created = map[string]string{}
	failed = map[string]error{}

	for _, chunk := range chunkDevices(items, maxBatchSize) {
		nameToExternalID := map[string]string{}
		req := &device.CreateDevicesRequest{}
		for _, d := range chunk {
			siteID, ok := siteIDs[d.SiteExternalID]
			if !ok {
				failed[d.ExternalID] = fmt.Errorf("kentik: no synced Kentik site for external site id %q", d.SiteExternalID)
				continue
			}
			nameToExternalID[d.Name] = d.ExternalID
			req.Devices = append(req.Devices, buildDeviceConcise(d, siteID, "", a.cfg))
		}
		if len(req.Devices) == 0 {
			continue
		}

		var resp *device.CreateDevicesResponse
		err := a.client.call(ctx, func(ctx context.Context) error {
			var callErr error
			resp, callErr = a.client.Devices.CreateDevices(ctx, req)
			return callErr
		})
		if err != nil {
			for _, extID := range nameToExternalID {
				failed[extID] = err
			}
			continue
		}

		seen := map[string]bool{}
		for _, detail := range resp.GetDevices() {
			if extID, ok := nameToExternalID[detail.GetDeviceName()]; ok {
				created[extID] = detail.GetId()
				seen[detail.GetDeviceName()] = true
			}
		}
		for name, extID := range nameToExternalID {
			if !seen[name] {
				failed[extID] = fmt.Errorf("kentik: device %q was not confirmed created (see failed_devices: %v)", name, resp.GetFailedDevices())
			}
		}
	}
	return created, failed
}

// Update batch-updates devices already known to Kentik (kentikIDs keyed by
// ExternalID, populated from the local state store).
func (a *DeviceApplier) Update(ctx context.Context, items []core.Device, siteIDs map[string]uint32, kentikIDs map[string]string) (updated map[string]bool, failed map[string]error) {
	updated = map[string]bool{}
	failed = map[string]error{}

	for _, chunk := range chunkDevices(items, maxBatchSize) {
		idToExternalID := map[string]string{}
		req := &device.UpdateDevicesRequest{}
		for _, d := range chunk {
			kentikID, ok := kentikIDs[d.ExternalID]
			if !ok {
				failed[d.ExternalID] = fmt.Errorf("kentik: no known Kentik device id for external id %q", d.ExternalID)
				continue
			}
			siteID, ok := siteIDs[d.SiteExternalID]
			if !ok {
				failed[d.ExternalID] = fmt.Errorf("kentik: no synced Kentik site for external site id %q", d.SiteExternalID)
				continue
			}
			idToExternalID[kentikID] = d.ExternalID
			req.Devices = append(req.Devices, buildDeviceConcise(d, siteID, kentikID, a.cfg))
		}
		if len(req.Devices) == 0 {
			continue
		}

		var resp *device.UpdateDevicesResponse
		err := a.client.call(ctx, func(ctx context.Context) error {
			var callErr error
			resp, callErr = a.client.Devices.UpdateDevices(ctx, req)
			return callErr
		})
		if err != nil {
			for _, extID := range idToExternalID {
				failed[extID] = err
			}
			continue
		}

		seen := map[string]bool{}
		for _, detail := range resp.GetDevices() {
			if extID, ok := idToExternalID[detail.GetId()]; ok {
				updated[extID] = true
				seen[detail.GetId()] = true
			}
		}
		for kentikID, extID := range idToExternalID {
			if !seen[kentikID] {
				failed[extID] = fmt.Errorf("kentik: device id %q was not confirmed updated (see failed_devices: %v)", kentikID, resp.GetFailedDevices())
			}
		}
	}
	return updated, failed
}

// Delete batch-deletes devices by their Kentik IDs.
func (a *DeviceApplier) Delete(ctx context.Context, kentikIDs []string) (deleted map[string]bool, failed map[string]error) {
	deleted = map[string]bool{}
	failed = map[string]error{}

	for _, chunk := range chunkStrings(kentikIDs, maxBatchSize) {
		req := &device.DeleteDevicesRequest{Ids: chunk}
		var resp *device.DeleteDevicesResponse
		err := a.client.call(ctx, func(ctx context.Context) error {
			var callErr error
			resp, callErr = a.client.Devices.DeleteDevices(ctx, req)
			return callErr
		})
		if err != nil {
			for _, id := range chunk {
				failed[id] = err
			}
			continue
		}
		failedSet := map[string]bool{}
		for _, id := range resp.GetFailedDevices() {
			failedSet[id] = true
		}
		for _, id := range chunk {
			if failedSet[id] {
				failed[id] = fmt.Errorf("kentik: device id %q reported in failed_devices", id)
			} else {
				deleted[id] = true
			}
		}
	}
	return deleted, failed
}

func chunkDevices(items []core.Device, size int) [][]core.Device {
	var chunks [][]core.Device
	for len(items) > 0 {
		n := size
		if len(items) < n {
			n = len(items)
		}
		chunks = append(chunks, items[:n])
		items = items[n:]
	}
	return chunks
}

func chunkStrings(items []string, size int) [][]string {
	var chunks [][]string
	for size > 0 && len(items) > 0 {
		n := size
		if len(items) < n {
			n = len(items)
		}
		chunks = append(chunks, items[:n])
		items = items[n:]
	}
	return chunks
}
