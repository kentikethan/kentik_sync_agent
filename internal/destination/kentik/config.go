// Package kentik implements the sync engine's destination: it takes
// normalized core.Device/core.Site/core.IPGroup values and applies them to
// Kentik via the generated gRPC clients in
// github.com/kentik/api-schema-public. Kentik's Device/Site/Populator
// messages have no generic custom-field or label slot suitable for storing
// a source's external ID (confirmed by reading the generated .pb.go files),
// so this package never tries to "discover" prior state from Kentik itself
// — internal/state's local mapping table is the authoritative record of
// what this agent has previously created.
package kentik

import "time"

// Config is the Kentik destination's connection and mapping configuration,
// decoded directly from the top-level agent config's `kentik:` block (see
// internal/config.KentikConfig, which this mirrors).
type Config struct {
	APIURL   string
	Email    string
	APIToken string
	Timeout  time.Duration

	DefaultPlanID        uint32
	DefaultDeviceSubtype string
	CustomDimensionID    string

	RequestsPerMinute int
}
