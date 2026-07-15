// Package netbox implements the source.Source interface against a NetBox
// instance's REST API, fetching devices, sites, and IP prefixes/ranges and
// normalizing them into core.Device/Site/IPGroup.
package netbox

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/kentikethan/kentik_sync_agent/internal/core"
	"github.com/kentikethan/kentik_sync_agent/internal/source"
)

func init() {
	source.Register("netbox", newFromRawConfig)
}

// Config is netbox's typed connection/mapping configuration, decoded from
// the shared config's per-source `connection`/`mapping` raw maps.
type Config struct {
	URL                string        `yaml:"url"`
	Token              string        `yaml:"token"`
	Timeout            time.Duration `yaml:"timeout"`
	InsecureSkipVerify bool          `yaml:"insecure_skip_verify"`
	PageSize           int           `yaml:"page_size"`

	Mapping MappingConfig `yaml:"mapping"`
}

// MappingConfig controls how NetBox's IPAM objects become core.IPGroup.
type MappingConfig struct {
	IPGroups IPGroupMapping `yaml:"ip_groups"`
}

type IPGroupMapping struct {
	// Source selects which NetBox IPAM objects feed IPGroup: "prefixes",
	// "ip_ranges", or "both". Defaults to "prefixes".
	Source string `yaml:"source"`
	// LabelField selects which NetBox field becomes the Populator label
	// value, e.g. "vrf", "role", "tenant", or "site". Defaults to "site".
	LabelField string `yaml:"label_field"`
	// Direction is the default Populator match direction: "src", "dst", or
	// "either". Defaults to "either".
	Direction core.IPGroupDirection `yaml:"direction"`
}

func (c Config) pageSize() int {
	if c.PageSize > 0 {
		return c.PageSize
	}
	return 500
}

func (c Config) timeout() time.Duration {
	if c.Timeout > 0 {
		return c.Timeout
	}
	return 15 * time.Second
}

// Source implements source.Source against a single NetBox instance.
type Source struct {
	cfg  Config
	http *http.Client
}

func newFromRawConfig(raw map[string]any) (source.Source, error) {
	cfg, err := decodeConfig(raw)
	if err != nil {
		return nil, fmt.Errorf("netbox: invalid config: %w", err)
	}
	return New(cfg), nil
}

// New constructs a netbox Source directly from a typed Config (used by
// newFromRawConfig and directly by tests).
func New(cfg Config) *Source {
	transport := http.DefaultTransport
	if cfg.InsecureSkipVerify {
		transport = insecureTransport()
	}
	return &Source{
		cfg: cfg,
		http: &http.Client{
			Timeout:   cfg.timeout(),
			Transport: transport,
		},
	}
}

func (s *Source) Name() string { return "netbox" }

func (s *Source) Capabilities() []core.ObjectType {
	return []core.ObjectType{core.ObjectDevices, core.ObjectSites, core.ObjectIPGroups, core.ObjectDeviceLabels}
}

// SupportsIncremental is true: NetBox's `last_updated__gte` filter lets us
// fetch only changed objects since the last successful cursor.
func (s *Source) SupportsIncremental() bool { return true }

func (s *Source) HealthCheck(ctx context.Context) error {
	req, err := s.newRequest(ctx, "/api/status/", nil)
	if err != nil {
		return err
	}
	resp, err := s.http.Do(req)
	if err != nil {
		return fmt.Errorf("netbox: health check request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("netbox: health check returned %s", resp.Status)
	}
	return nil
}

func (s *Source) newRequest(ctx context.Context, path string, query url.Values) (*http.Request, error) {
	base := strings.TrimRight(s.cfg.URL, "/")
	u := base + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("netbox: building request for %s: %w", path, err)
	}
	req.Header.Set("Authorization", "Token "+s.cfg.Token)
	req.Header.Set("Accept", "application/json")
	return req, nil
}

// listResponse mirrors NetBox's standard paginated REST list envelope.
type listResponse[T any] struct {
	Count    int     `json:"count"`
	Next     *string `json:"next"`
	Previous *string `json:"previous"`
	Results  []T     `json:"results"`
}

// maxPages caps pagination as a safety valve against a misbehaving/huge
// NetBox instance; FetchResult.Truncated is set true if this is hit.
const maxPages = 200

// listAll fetches every page of a NetBox list endpoint, following `next`
// URLs verbatim (they already carry the query string NetBox expects).
func listAll[T any](ctx context.Context, s *Source, path string, query url.Values) (items []T, truncated bool, err error) {
	query = cloneValues(query)
	query.Set("limit", fmt.Sprint(s.cfg.pageSize()))

	nextURL := ""
	for page := 0; page < maxPages; page++ {
		var req *http.Request
		if nextURL == "" {
			req, err = s.newRequest(ctx, path, query)
		} else {
			req, err = http.NewRequestWithContext(ctx, http.MethodGet, nextURL, nil)
			if err == nil {
				req.Header.Set("Authorization", "Token "+s.cfg.Token)
				req.Header.Set("Accept", "application/json")
			}
		}
		if err != nil {
			return nil, false, err
		}

		resp, err := s.http.Do(req)
		if err != nil {
			return nil, false, fmt.Errorf("netbox: request to %s failed: %w", path, err)
		}
		body, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode == http.StatusTooManyRequests {
			return nil, false, fmt.Errorf("netbox: rate limited (429) fetching %s", path)
		}
		if resp.StatusCode != http.StatusOK {
			return nil, false, fmt.Errorf("netbox: %s returned %s: %s", path, resp.Status, string(body))
		}
		if readErr != nil {
			return nil, false, fmt.Errorf("netbox: reading response body from %s: %w", path, readErr)
		}

		var page listResponse[T]
		if err := json.Unmarshal(body, &page); err != nil {
			return nil, false, fmt.Errorf("netbox: decoding response from %s: %w", path, err)
		}
		items = append(items, page.Results...)

		if page.Next == nil || *page.Next == "" {
			return items, false, nil
		}
		nextURL = *page.Next
	}
	return items, true, nil
}

func cloneValues(v url.Values) url.Values {
	out := make(url.Values, len(v)+2)
	for k, vals := range v {
		out[k] = append([]string(nil), vals...)
	}
	return out
}

// sinceQuery builds the `last_updated__gte` filter for an incremental fetch,
// or an empty Values for a full fetch (since == "").
func sinceQuery(since string) url.Values {
	q := url.Values{}
	if since != "" {
		q.Set("last_updated__gte", since)
	}
	return q
}

// nowCursor is the cursor value to persist after a successful fetch: "now"
// per RFC3339, so the next incremental run only asks for changes after this
// point. Callers pass in the fetch start time to avoid missing updates that
// land mid-fetch.
func nowCursor(fetchStartedAt time.Time) string {
	return fetchStartedAt.UTC().Format(time.RFC3339)
}
