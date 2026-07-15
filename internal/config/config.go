// Package config loads and validates the agent's top-level YAML
// configuration: Kentik connection details, local state storage, and the
// list of configured sources. Each source's `connection`/`mapping` blocks
// are kept as raw maps here and only decoded into a typed struct inside
// that source plugin's own package — this file never needs to change when
// a new source type (Nautobot, Infoblox, ...) is added.
package config

import (
	"fmt"
	"os"
	"regexp"
	"time"

	"github.com/kentikethan/kentik_sync_agent/internal/core"
	"github.com/kentikethan/kentik_sync_agent/internal/source"
	"gopkg.in/yaml.v3"
)

// Config is the agent's fully-parsed configuration.
type Config struct {
	Kentik        KentikConfig        `yaml:"kentik"`
	State         StateConfig         `yaml:"state"`
	Observability ObservabilityConfig `yaml:"observability"`
	Sources       []SourceConfig      `yaml:"sources"`
}

// KentikConfig holds the destination connection details. Kentik is the
// agent's single, fixed destination (unlike sources, which are pluggable),
// so its fields are typed directly rather than passed as a raw map.
type KentikConfig struct {
	APIURL   string        `yaml:"api_url"`
	Email    string        `yaml:"email"`
	APIToken string        `yaml:"api_token"`
	Timeout  time.Duration `yaml:"timeout"`

	// DefaultPlanID is the Kentik billing plan ID new devices are created
	// under. Required by Kentik's Device API; find it in Kentik under
	// Admin > Plans, or via the Plan API.
	DefaultPlanID uint32 `yaml:"default_plan_id"`

	// DefaultDeviceSubtype is the Kentik device_subtype assigned to synced
	// devices unless a source overrides it per-device. Must match one of
	// the device subtypes enabled on the account (e.g.
	// "host-nprobe-dns-www"); see Admin > Devices in the Kentik UI.
	DefaultDeviceSubtype string `yaml:"default_device_subtype"`

	// CustomDimensionID targets an existing Custom Dimension (found under
	// Admin > Custom Dimensions) that IP-group Populators are written into.
	// Kentik's Populator API is scoped to one Custom Dimension per call, so
	// this must be created once via the Kentik UI/API before first sync.
	CustomDimensionID string `yaml:"custom_dimension_id"`

	RateLimit RateLimitConfig `yaml:"rate_limit"`
}

type RateLimitConfig struct {
	RequestsPerMinute int `yaml:"requests_per_minute"`
}

type StateConfig struct {
	Driver string `yaml:"driver"` // "sqlite" (only supported driver in v1)
	Path   string `yaml:"path"`
}

type ObservabilityConfig struct {
	LogLevel    string `yaml:"log_level"`
	LogFormat   string `yaml:"log_format"`
	MetricsAddr string `yaml:"metrics_addr"`
	HealthAddr  string `yaml:"health_addr"`
}

// SourceConfig is one configured source instance. Connection and Mapping
// are decoded into a typed struct by the corresponding source plugin.
type SourceConfig struct {
	Name       string         `yaml:"name"`
	Type       string         `yaml:"type"`
	Connection map[string]any `yaml:"connection"`
	Mapping    map[string]any `yaml:"mapping"`
	Sync       SyncConfig     `yaml:"sync"`
}

type SyncConfig struct {
	Objects               []core.ObjectType `yaml:"objects"`
	Interval              time.Duration     `yaml:"interval"`
	FullReconcileInterval time.Duration     `yaml:"full_reconcile_interval"`
	Incremental           bool              `yaml:"incremental"`
}

const (
	defaultSyncInterval      = 15 * time.Minute
	defaultFullReconcile     = 24 * time.Hour
	defaultKentikTimeout     = 30 * time.Second
	defaultRequestsPerMinute = 100
)

// applyDefaults fills in zero-value fields with their defaults, once, right
// after parsing, so the rest of the codebase can read Config fields
// directly without every call site re-implementing "or the default".
func (c *Config) applyDefaults() {
	if c.Kentik.Timeout <= 0 {
		c.Kentik.Timeout = defaultKentikTimeout
	}
	if c.Kentik.RateLimit.RequestsPerMinute <= 0 {
		c.Kentik.RateLimit.RequestsPerMinute = defaultRequestsPerMinute
	}
	for i := range c.Sources {
		if c.Sources[i].Sync.Interval <= 0 {
			c.Sources[i].Sync.Interval = defaultSyncInterval
		}
		if c.Sources[i].Sync.FullReconcileInterval <= 0 {
			c.Sources[i].Sync.FullReconcileInterval = defaultFullReconcile
		}
	}
}

var envVarPattern = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// interpolateEnv replaces every ${VAR} occurrence in raw YAML text with the
// value of the VAR environment variable (empty string if unset), so secrets
// never need to be committed in a config file.
func interpolateEnv(raw []byte) []byte {
	return envVarPattern.ReplaceAllFunc(raw, func(match []byte) []byte {
		name := envVarPattern.FindSubmatch(match)[1]
		return []byte(os.Getenv(string(name)))
	})
}

// Load reads, env-interpolates, parses, and validates the config at path.
func Load(path string) (Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("config: reading %s: %w", path, err)
	}
	raw = interpolateEnv(raw)

	var cfg Config
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return Config{}, fmt.Errorf("config: parsing %s: %w", path, err)
	}
	cfg.applyDefaults()
	if err := cfg.Validate(); err != nil {
		return Config{}, fmt.Errorf("config: %s: %w", path, err)
	}
	return cfg, nil
}

// Validate checks the config for structural errors that should fail agent
// startup immediately rather than surfacing at first scheduled run:
// duplicate source names, unknown source types, and object types requested
// from a source that doesn't support them.
func (c Config) Validate() error {
	if c.Kentik.APIURL == "" {
		return fmt.Errorf("kentik.api_url is required")
	}
	if c.Kentik.Email == "" || c.Kentik.APIToken == "" {
		return fmt.Errorf("kentik.email and kentik.api_token are required")
	}
	if c.Kentik.DefaultPlanID == 0 {
		return fmt.Errorf("kentik.default_plan_id is required")
	}
	if len(c.Sources) == 0 {
		return fmt.Errorf("at least one entry under sources is required")
	}

	seenNames := map[string]bool{}
	for _, sc := range c.Sources {
		if sc.Name == "" {
			return fmt.Errorf("every source must set a name")
		}
		if seenNames[sc.Name] {
			return fmt.Errorf("duplicate source name %q", sc.Name)
		}
		seenNames[sc.Name] = true

		if sc.Type == "" {
			return fmt.Errorf("source %q: type is required", sc.Name)
		}
		// Constructing the source validates its connection config and, via
		// Capabilities(), lets us check the requested object types.
		src, err := source.New(sc.Type, sc.Connection)
		if err != nil {
			return fmt.Errorf("source %q: %w", sc.Name, err)
		}
		var wantsDeviceLabels, wantsDevices bool
		for _, obj := range sc.Sync.Objects {
			if !source.HasCapability(src, obj) {
				return fmt.Errorf("source %q: type %q does not support object type %q", sc.Name, sc.Type, obj)
			}
			switch obj {
			case core.ObjectDeviceLabels:
				wantsDeviceLabels = true
			case core.ObjectDevices:
				wantsDevices = true
			}
		}
		if len(sc.Sync.Objects) == 0 {
			return fmt.Errorf("source %q: sync.objects must list at least one object type", sc.Name)
		}
		if wantsDeviceLabels && !wantsDevices {
			return fmt.Errorf("source %q: sync.objects includes %q, which requires %q to also be listed (it labels already-synced devices)", sc.Name, core.ObjectDeviceLabels, core.ObjectDevices)
		}
	}
	return nil
}
