package kentik

import (
	"context"

	"github.com/kentik/api-schema-public/gen/go/kentik/site/v202509"

	"github.com/kentikethan/kentik_sync_agent/internal/core"
)

// SiteApplier applies core.Site values to Kentik's Site API. Unlike
// Device, Kentik's Site service has no batch create/update/delete RPCs, so
// this issues one call per site.
type SiteApplier struct {
	client *Client
}

func NewSiteApplier(c *Client) *SiteApplier { return &SiteApplier{client: c} }

func buildSite(s core.Site, kentikID string) *site.Site {
	out := &site.Site{
		Id:    kentikID,
		Title: s.Name,
		Type:  site.SiteType_SITE_TYPE_OTHER,
	}
	if s.Latitude != nil {
		out.Lat = *s.Latitude
	}
	if s.Longitude != nil {
		out.Lon = *s.Longitude
	}
	if s.Address != "" || s.Region != "" {
		out.PostalAddress = &site.PostalAddress{
			Address: s.Address,
			Region:  s.Region,
		}
	}
	return out
}

// Create creates each site individually, returning the Kentik-assigned ID
// for each successfully created ExternalID, and an error for each that
// failed.
func (a *SiteApplier) Create(ctx context.Context, items []core.Site) (created map[string]string, failed map[string]error) {
	created = map[string]string{}
	failed = map[string]error{}
	for _, s := range items {
		req := &site.CreateSiteRequest{Site: buildSite(s, "")}
		var resp *site.CreateSiteResponse
		err := a.client.call(ctx, func(ctx context.Context) error {
			var callErr error
			resp, callErr = a.client.Sites.CreateSite(ctx, req)
			return callErr
		})
		if err != nil {
			failed[s.ExternalID] = err
			continue
		}
		created[s.ExternalID] = resp.GetSite().GetId()
	}
	return created, failed
}

// Update updates each site individually. kentikIDs maps ExternalID to the
// previously-assigned Kentik site ID, from the local state store.
func (a *SiteApplier) Update(ctx context.Context, items []core.Site, kentikIDs map[string]string) (updated map[string]bool, failed map[string]error) {
	updated = map[string]bool{}
	failed = map[string]error{}
	for _, s := range items {
		kentikID, ok := kentikIDs[s.ExternalID]
		if !ok {
			failed[s.ExternalID] = errNoKentikID(s.ExternalID)
			continue
		}
		req := &site.UpdateSiteRequest{Site: buildSite(s, kentikID)}
		err := a.client.call(ctx, func(ctx context.Context) error {
			_, callErr := a.client.Sites.UpdateSite(ctx, req)
			return callErr
		})
		if err != nil {
			failed[s.ExternalID] = err
			continue
		}
		updated[s.ExternalID] = true
	}
	return updated, failed
}

// Delete deletes each site individually by its Kentik ID, keyed by
// ExternalID so callers can remove the right local state-store row.
func (a *SiteApplier) Delete(ctx context.Context, kentikIDsByExternalID map[string]string) (deleted map[string]bool, failed map[string]error) {
	deleted = map[string]bool{}
	failed = map[string]error{}
	for externalID, kentikID := range kentikIDsByExternalID {
		req := &site.DeleteSiteRequest{Id: kentikID}
		err := a.client.call(ctx, func(ctx context.Context) error {
			_, callErr := a.client.Sites.DeleteSite(ctx, req)
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
