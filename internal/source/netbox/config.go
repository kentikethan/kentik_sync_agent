package netbox

import (
	"crypto/tls"
	"fmt"
	"net/http"

	"gopkg.in/yaml.v3"
)

// decodeConfig converts the raw `connection`/`mapping` maps from the shared
// config schema into a typed Config, by round-tripping through YAML. This
// keeps internal/config free of any per-plugin field knowledge: it only
// ever passes map[string]any across the source.Factory boundary.
func decodeConfig(raw map[string]any) (Config, error) {
	var cfg Config
	b, err := yaml.Marshal(raw)
	if err != nil {
		return cfg, fmt.Errorf("re-marshaling config: %w", err)
	}
	if err := yaml.Unmarshal(b, &cfg); err != nil {
		return cfg, fmt.Errorf("decoding into netbox.Config: %w", err)
	}
	if cfg.URL == "" {
		return cfg, fmt.Errorf("connection.url is required")
	}
	if cfg.Token == "" {
		return cfg, fmt.Errorf("connection.token is required")
	}
	if cfg.Mapping.IPGroups.Source == "" {
		cfg.Mapping.IPGroups.Source = "prefixes"
	}
	if cfg.Mapping.IPGroups.LabelField == "" {
		cfg.Mapping.IPGroups.LabelField = "site"
	}
	if cfg.Mapping.IPGroups.Direction == "" {
		cfg.Mapping.IPGroups.Direction = "either"
	}
	return cfg, nil
}

func insecureTransport() http.RoundTripper {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // explicit opt-in via config for lab/self-signed NetBox instances
	return t
}
