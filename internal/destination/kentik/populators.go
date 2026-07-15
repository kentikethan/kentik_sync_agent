package kentik

import (
	"context"

	customdimension "github.com/kentik/api-schema-public/gen/go/kentik/custom_dimension/v202411alpha1"

	"github.com/kentikethan/kentik_sync_agent/internal/core"
)

// PopulatorApplier applies core.IPGroup values as Populators within a
// single, pre-existing Kentik Custom Dimension (cfg.CustomDimensionID).
// Kentik's Populator API has no batch create/update/delete, so this issues
// one call per IP group.
type PopulatorApplier struct {
	client            *Client
	customDimensionID string
}

func NewPopulatorApplier(c *Client, customDimensionID string) *PopulatorApplier {
	return &PopulatorApplier{client: c, customDimensionID: customDimensionID}
}

func buildPopulator(g core.IPGroup, kentikID string) *customdimension.Populator {
	dir := string(g.Direction)
	if dir == "" {
		dir = "either"
	}
	return &customdimension.Populator{
		Id:        kentikID,
		Value:     g.Label,
		Direction: dir,
		Addr:      []string{g.CIDR},
	}
}

// ListExisting fetches every populator currently defined on the configured
// Custom Dimension. Since Populators carry no external-id marker, the sync
// engine relies on the local state store rather than this call for
// reconciliation; it's exposed mainly for diagnostics and first-run sanity
// checks (see docs/plugins.md).
func (a *PopulatorApplier) ListExisting(ctx context.Context) ([]*customdimension.Populator, error) {
	var resp *customdimension.GetCustomDimensionInfoResponse
	err := a.client.call(ctx, func(ctx context.Context) error {
		var callErr error
		resp, callErr = a.client.CustomDimensions.GetCustomDimensionInfo(ctx, &customdimension.GetCustomDimensionInfoRequest{
			CustomDimensionId: a.customDimensionID,
		})
		return callErr
	})
	if err != nil {
		return nil, err
	}
	return resp.GetDimension().GetPopulators(), nil
}

func (a *PopulatorApplier) Create(ctx context.Context, items []core.IPGroup) (created map[string]string, failed map[string]error) {
	created = map[string]string{}
	failed = map[string]error{}
	for _, g := range items {
		req := &customdimension.CreatePopulatorRequest{
			CustomDimensionId: a.customDimensionID,
			Populator:         buildPopulator(g, ""),
		}
		var resp *customdimension.CreatePopulatorResponse
		err := a.client.call(ctx, func(ctx context.Context) error {
			var callErr error
			resp, callErr = a.client.CustomDimensions.CreatePopulator(ctx, req)
			return callErr
		})
		if err != nil {
			failed[g.ExternalID] = err
			continue
		}
		created[g.ExternalID] = resp.GetPopulator().GetId()
	}
	return created, failed
}

func (a *PopulatorApplier) Update(ctx context.Context, items []core.IPGroup, kentikIDs map[string]string) (updated map[string]bool, failed map[string]error) {
	updated = map[string]bool{}
	failed = map[string]error{}
	for _, g := range items {
		kentikID, ok := kentikIDs[g.ExternalID]
		if !ok {
			failed[g.ExternalID] = errNoKentikID(g.ExternalID)
			continue
		}
		req := &customdimension.UpdatePopulatorRequest{
			CustomDimensionId: a.customDimensionID,
			PopulatorId:       kentikID,
			Populator:         buildPopulator(g, kentikID),
		}
		err := a.client.call(ctx, func(ctx context.Context) error {
			_, callErr := a.client.CustomDimensions.UpdatePopulator(ctx, req)
			return callErr
		})
		if err != nil {
			failed[g.ExternalID] = err
			continue
		}
		updated[g.ExternalID] = true
	}
	return updated, failed
}

func (a *PopulatorApplier) Delete(ctx context.Context, kentikIDsByExternalID map[string]string) (deleted map[string]bool, failed map[string]error) {
	deleted = map[string]bool{}
	failed = map[string]error{}
	for externalID, kentikID := range kentikIDsByExternalID {
		req := &customdimension.DeletePopulatorRequest{
			CustomDimensionId: a.customDimensionID,
			PopulatorId:       kentikID,
		}
		err := a.client.call(ctx, func(ctx context.Context) error {
			_, callErr := a.client.CustomDimensions.DeletePopulator(ctx, req)
			return callErr
		})
		if err != nil {
			failed[externalID] = err
			continue
		}
		deleted[externalID] = true
	}
	return deleted, failed
}
