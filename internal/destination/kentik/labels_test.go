package kentik

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/kentik/api-schema-public/gen/go/kentik/device/v202504beta2"
	"github.com/kentik/api-schema-public/gen/go/kentik/label/v202210"
	"golang.org/x/time/rate"
	"google.golang.org/grpc"

	"github.com/kentikethan/kentik_sync_agent/internal/core"
)

// fakeLabelClient is an in-memory stand-in for label.LabelServiceClient, so
// LabelApplier can be tested without a real (or bufconn) gRPC server.
type fakeLabelClient struct {
	label.LabelServiceClient
	labels []*label.Label
	nextID uint32
}

func (f *fakeLabelClient) ListLabels(context.Context, *label.ListLabelsRequest, ...grpc.CallOption) (*label.ListLabelsResponse, error) {
	return &label.ListLabelsResponse{Labels: f.labels}, nil
}

func (f *fakeLabelClient) CreateLabel(_ context.Context, in *label.CreateLabelRequest, _ ...grpc.CallOption) (*label.CreateLabelResponse, error) {
	f.nextID++
	l := &label.Label{Id: strconv.Itoa(int(f.nextID)), Name: in.GetLabel().GetName()}
	f.labels = append(f.labels, l)
	return &label.CreateLabelResponse{Label: l}, nil
}

// fakeDeviceClient is an in-memory stand-in for device.DeviceServiceClient,
// covering only the two RPCs LabelApplier calls.
type fakeDeviceClient struct {
	device.DeviceServiceClient
	devices     map[string]*device.DeviceDetailed
	updateCalls []*device.UpdateDeviceLabelsRequest
}

func (f *fakeDeviceClient) GetDevice(_ context.Context, in *device.GetDeviceRequest, _ ...grpc.CallOption) (*device.GetDeviceResponse, error) {
	return &device.GetDeviceResponse{Device: f.devices[in.GetId()]}, nil
}

func (f *fakeDeviceClient) UpdateDeviceLabels(_ context.Context, in *device.UpdateDeviceLabelsRequest, _ ...grpc.CallOption) (*device.UpdateDeviceLabelsResponse, error) {
	f.updateCalls = append(f.updateCalls, in)
	return &device.UpdateDeviceLabelsResponse{}, nil
}

func newTestLabelApplier(labels []*label.Label, devices map[string]*device.DeviceDetailed) (*LabelApplier, *fakeDeviceClient) {
	devClient := &fakeDeviceClient{devices: devices}
	client := &Client{
		Labels:  &fakeLabelClient{labels: labels},
		Devices: devClient,
		cfg:     Config{Timeout: 5 * time.Second},
		limiter: rate.NewLimiter(rate.Inf, 1),
	}
	return NewLabelApplier(client), devClient
}

func labelIDs(req *device.UpdateDeviceLabelsRequest) map[uint32]bool {
	ids := map[uint32]bool{}
	for _, l := range req.GetLabels() {
		ids[l.GetId()] = true
	}
	return ids
}

func TestLabelApplier_Apply_PreservesUnmanagedAndDropsStaleManaged(t *testing.T) {
	ctx := context.Background()
	existing := []*label.Label{{Id: "11", Name: "Netbox:Device:Role:old-role"}}
	devices := map[string]*device.DeviceDetailed{
		"kentik-1": {
			Id: "kentik-1",
			Labels: []*device.Label{
				{Id: "10", Name: "Custom:Hand-Added"},           // unmanaged, must survive
				{Id: "11", Name: "Netbox:Device:Role:old-role"}, // stale managed, must be dropped
			},
		},
	}
	applier, devClient := newTestLabelApplier(existing, devices)

	items := []core.DeviceLabels{
		{ExternalID: "1", Labels: []string{"Netbox:Device:Tenant:acme", "Netbox:Device:Role:router"}},
	}
	applied, failed := applier.Apply(ctx, items, map[string]string{"1": "kentik-1"})

	if len(failed) != 0 {
		t.Fatalf("expected no failures, got %+v", failed)
	}
	if !applied["1"] {
		t.Fatalf("expected item 1 to be applied")
	}
	if len(devClient.updateCalls) != 1 {
		t.Fatalf("expected exactly one UpdateDeviceLabels call, got %d", len(devClient.updateCalls))
	}

	req := devClient.updateCalls[0]
	if req.GetId() != "kentik-1" {
		t.Fatalf("expected update targeted at kentik-1, got %q", req.GetId())
	}
	ids := labelIDs(req)
	if !ids[10] {
		t.Fatalf("expected unmanaged label id 10 preserved, got %+v", ids)
	}
	if ids[11] {
		t.Fatalf("expected stale managed label id 11 dropped, got %+v", ids)
	}
	if len(ids) != 3 { // unmanaged(10) + newly resolved Tenant + Role
		t.Fatalf("expected 3 labels in final set (1 preserved + 2 new), got %+v", ids)
	}
}

func TestLabelApplier_Apply_ReusesExistingLabelByName(t *testing.T) {
	ctx := context.Background()
	existing := []*label.Label{{Id: "42", Name: "Netbox:Device:Tenant:acme"}}
	devices := map[string]*device.DeviceDetailed{"kentik-1": {Id: "kentik-1"}}
	applier, devClient := newTestLabelApplier(existing, devices)

	items := []core.DeviceLabels{{ExternalID: "1", Labels: []string{"Netbox:Device:Tenant:acme"}}}
	if _, failed := applier.Apply(ctx, items, map[string]string{"1": "kentik-1"}); len(failed) != 0 {
		t.Fatalf("expected no failures, got %+v", failed)
	}

	req := devClient.updateCalls[0]
	ids := labelIDs(req)
	if !ids[42] {
		t.Fatalf("expected existing label id 42 reused rather than recreated, got %+v", ids)
	}
}

func TestLabelApplier_Apply_MissingKentikDeviceIDFails(t *testing.T) {
	ctx := context.Background()
	applier, _ := newTestLabelApplier(nil, map[string]*device.DeviceDetailed{})

	items := []core.DeviceLabels{{ExternalID: "1", Labels: []string{"Netbox:Device:Tenant:acme"}}}
	applied, failed := applier.Apply(ctx, items, map[string]string{})

	if len(applied) != 0 {
		t.Fatalf("expected no applied items, got %+v", applied)
	}
	if failed["1"] == nil {
		t.Fatal("expected a failure for a device with no known Kentik id")
	}
}

func TestLabelApplier_Clear_RemovesManagedKeepsUnmanaged(t *testing.T) {
	ctx := context.Background()
	devices := map[string]*device.DeviceDetailed{
		"kentik-1": {
			Id: "kentik-1",
			Labels: []*device.Label{
				{Id: "10", Name: "Custom:Hand-Added"},
				{Id: "11", Name: "Netbox:Device:Role:old-role"},
			},
		},
	}
	applier, devClient := newTestLabelApplier(nil, devices)

	cleared, failed := applier.Clear(ctx, map[string]string{"1": "kentik-1"})
	if len(failed) != 0 {
		t.Fatalf("expected no failures, got %+v", failed)
	}
	if !cleared["1"] {
		t.Fatalf("expected item 1 to be cleared")
	}

	ids := labelIDs(devClient.updateCalls[0])
	if len(ids) != 1 || !ids[10] {
		t.Fatalf("expected only the unmanaged label to remain, got %+v", ids)
	}
}
