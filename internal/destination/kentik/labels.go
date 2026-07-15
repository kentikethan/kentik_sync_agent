package kentik

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/kentik/api-schema-public/gen/go/kentik/device/v202504beta2"
	"github.com/kentik/api-schema-public/gen/go/kentik/label/v202210"

	"github.com/kentikethan/kentik_sync_agent/internal/core"
)

// managedLabelPrefix marks a Kentik label as owned by this agent. Kentik's
// UpdateDeviceLabels call replaces a device's entire label set in one shot
// (see docs/plugins.md), so LabelApplier must read a device's current
// labels back before writing: labels with this prefix are this agent's to
// add/remove/replace; anything else (e.g. a label a person added by hand in
// the Kentik UI) is left untouched.
const managedLabelPrefix = "Netbox:"

// LabelApplier applies core.DeviceLabels to already-synced Kentik devices,
// via Kentik's Label service (get-or-create by name) and the Device
// service's label-replace RPC.
type LabelApplier struct {
	client *Client
}

func NewLabelApplier(c *Client) *LabelApplier {
	return &LabelApplier{client: c}
}

// resolveLabelIDs returns the numeric Kentik label ID for every name in
// names, creating any that don't already exist as a Kentik label. Kentik's
// Label service has no batch create, but ListLabels returns every label in
// one call, so this only issues one create RPC per genuinely new name.
func (a *LabelApplier) resolveLabelIDs(ctx context.Context, names map[string]bool) (map[string]uint32, error) {
	var listResp *label.ListLabelsResponse
	err := a.client.call(ctx, func(ctx context.Context) error {
		var callErr error
		listResp, callErr = a.client.Labels.ListLabels(ctx, &label.ListLabelsRequest{})
		return callErr
	})
	if err != nil {
		return nil, fmt.Errorf("kentik: listing labels: %w", err)
	}

	ids := make(map[string]uint32, len(names))
	for _, l := range listResp.GetLabels() {
		if id, err := strconv.ParseUint(l.GetId(), 10, 32); err == nil {
			ids[l.GetName()] = uint32(id)
		}
	}

	for name := range names {
		if _, ok := ids[name]; ok {
			continue
		}
		var createResp *label.CreateLabelResponse
		err := a.client.call(ctx, func(ctx context.Context) error {
			var callErr error
			createResp, callErr = a.client.Labels.CreateLabel(ctx, &label.CreateLabelRequest{
				Label: &label.Label{Name: name},
			})
			return callErr
		})
		if err != nil {
			return nil, fmt.Errorf("kentik: creating label %q: %w", name, err)
		}
		id, err := strconv.ParseUint(createResp.GetLabel().GetId(), 10, 32)
		if err != nil {
			return nil, fmt.Errorf("kentik: created label %q returned non-numeric id %q", name, createResp.GetLabel().GetId())
		}
		ids[name] = uint32(id)
	}
	return ids, nil
}

// currentLabelIDs fetches a device's current Kentik labels, split into IDs
// this agent doesn't manage (preserved as-is) and the names it does manage
// (to be replaced by the caller's desired set).
func (a *LabelApplier) currentLabelIDs(ctx context.Context, kentikDeviceID string) (unmanaged []uint32, err error) {
	var resp *device.GetDeviceResponse
	err = a.client.call(ctx, func(ctx context.Context) error {
		var callErr error
		resp, callErr = a.client.Devices.GetDevice(ctx, &device.GetDeviceRequest{Id: kentikDeviceID})
		return callErr
	})
	if err != nil {
		return nil, fmt.Errorf("kentik: fetching current labels for device %q: %w", kentikDeviceID, err)
	}
	for _, l := range resp.GetDevice().GetLabels() {
		if strings.HasPrefix(l.GetName(), managedLabelPrefix) {
			continue
		}
		if id, err := strconv.ParseUint(l.GetId(), 10, 32); err == nil {
			unmanaged = append(unmanaged, uint32(id))
		}
	}
	return unmanaged, nil
}

// Apply resolves and writes the desired label set for each item onto its
// Kentik device, preserving any existing labels this agent doesn't manage
// (see managedLabelPrefix). kentikDeviceIDs maps each item's ExternalID to
// its already-synced Kentik device ID (see internal/sync's device sync).
func (a *LabelApplier) Apply(ctx context.Context, items []core.DeviceLabels, kentikDeviceIDs map[string]string) (applied map[string]bool, failed map[string]error) {
	applied = map[string]bool{}
	failed = map[string]error{}
	if len(items) == 0 {
		return applied, failed
	}

	wanted := map[string]bool{}
	for _, it := range items {
		for _, l := range it.Labels {
			wanted[l] = true
		}
	}

	labelIDs, err := a.resolveLabelIDs(ctx, wanted)
	if err != nil {
		for _, it := range items {
			failed[it.ExternalID] = err
		}
		return applied, failed
	}

	for _, it := range items {
		kentikID, ok := kentikDeviceIDs[it.ExternalID]
		if !ok {
			failed[it.ExternalID] = errNoKentikID(it.ExternalID)
			continue
		}

		unmanaged, err := a.currentLabelIDs(ctx, kentikID)
		if err != nil {
			failed[it.ExternalID] = err
			continue
		}

		final := make(map[uint32]bool, len(unmanaged)+len(it.Labels))
		for _, id := range unmanaged {
			final[id] = true
		}
		for _, name := range it.Labels {
			final[labelIDs[name]] = true
		}

		req := &device.UpdateDeviceLabelsRequest{Id: kentikID}
		for id := range final {
			req.Labels = append(req.Labels, &device.LabelConcise{Id: id})
		}

		err = a.client.call(ctx, func(ctx context.Context) error {
			_, callErr := a.client.Devices.UpdateDeviceLabels(ctx, req)
			return callErr
		})
		if err != nil {
			failed[it.ExternalID] = err
			continue
		}
		applied[it.ExternalID] = true
	}
	return applied, failed
}

// Clear removes this agent's managed labels (see managedLabelPrefix) from
// each device in kentikDeviceIDs (external ID -> Kentik device ID),
// preserving any other labels already on it. Used when a previously-labeled
// device disappears from the source's label fetch (see internal/sync).
func (a *LabelApplier) Clear(ctx context.Context, kentikDeviceIDs map[string]string) (cleared map[string]bool, failed map[string]error) {
	items := make([]core.DeviceLabels, 0, len(kentikDeviceIDs))
	for externalID := range kentikDeviceIDs {
		items = append(items, core.DeviceLabels{ExternalID: externalID})
	}
	return a.Apply(ctx, items, kentikDeviceIDs)
}
