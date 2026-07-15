package netbox

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/kentikethan/kentik_sync_agent/internal/core"
)

type netboxSite struct {
	ID          int         `json:"id"`
	Name        string      `json:"name"`
	Slug        string      `json:"slug"`
	Status      statusField `json:"status"`
	Latitude    *float64    `json:"latitude"`
	Longitude   *float64    `json:"longitude"`
	PhysicalAdr string      `json:"physical_address"`
	Region      *nestedRef  `json:"region"`
	Tags        []tagRef    `json:"tags"`
	LastUpdated string      `json:"last_updated"`
}

func siteStatus(v string) core.SiteStatus {
	switch v {
	case "active":
		return core.SiteStatusActive
	case "decommissioning", "retired":
		return core.SiteStatusInactive
	case "planned", "staging":
		return core.SiteStatusPlanned
	default:
		return core.SiteStatusUnknown
	}
}

func (site netboxSite) toCore() core.Site {
	return core.Site{
		ExternalID: strconv.Itoa(site.ID),
		Name:       site.Name,
		Status:     siteStatus(site.Status.Value),
		Latitude:   site.Latitude,
		Longitude:  site.Longitude,
		Address:    site.PhysicalAdr,
		Region:     site.Region.label(),
		Tags:       tagNames(site.Tags),
	}
}

// FetchSites fetches sites from NetBox's dcim/sites/ endpoint, honoring the
// same incremental `since` semantics as FetchDevices.
func (s *Source) FetchSites(ctx context.Context, since string) (core.FetchResult[core.Site], error) {
	fetchStart := time.Now()
	raw, truncated, err := listAll[netboxSite](ctx, s, "/api/dcim/sites/", sinceQuery(since))
	if err != nil {
		return core.FetchResult[core.Site]{}, fmt.Errorf("netbox: fetching sites: %w", err)
	}
	items := make([]core.Site, len(raw))
	for i, r := range raw {
		items[i] = r.toCore()
	}
	return core.FetchResult[core.Site]{
		Items:     items,
		Cursor:    nowCursor(fetchStart),
		Truncated: truncated,
	}, nil
}
