package kentik

import (
	"context"
	"crypto/tls"
	"fmt"
	"strings"

	customdimension "github.com/kentik/api-schema-public/gen/go/kentik/custom_dimension/v202411alpha1"
	"github.com/kentik/api-schema-public/gen/go/kentik/device/v202504beta2"
	"github.com/kentik/api-schema-public/gen/go/kentik/label/v202210"
	"github.com/kentik/api-schema-public/gen/go/kentik/site/v202509"
	"golang.org/x/time/rate"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

// defaultHost is Kentik's public gRPC API endpoint, per
// `option (google.api.default_host) = "grpc.api.kentik.com"` in Kentik's
// proto definitions.
const defaultHost = "grpc.api.kentik.com:443"

// Client wraps the generated Device/Site/CustomDimension gRPC clients with
// Kentik's header-based auth, a rate limiter shared across all three, and
// the config needed to translate core types into Kentik requests.
type Client struct {
	conn *grpc.ClientConn

	Devices          device.DeviceServiceClient
	Sites            site.SiteServiceClient
	CustomDimensions customdimension.CustomDimensionServiceClient
	Labels           label.LabelServiceClient

	cfg     Config
	limiter *rate.Limiter
}

// authCreds implements credentials.PerRPCCredentials, attaching Kentik's
// documented X-CH-Auth-Email / X-CH-Auth-API-Token headers as gRPC metadata
// on every call. Kentik's schema repo doesn't ship example Go client code
// for this, so this wiring is inferred from the proto's OpenAPI security
// definitions rather than copied from a reference implementation.
type authCreds struct {
	email, token string
}

func (a authCreds) GetRequestMetadata(context.Context, ...string) (map[string]string, error) {
	return map[string]string{
		"X-CH-Auth-Email":     a.email,
		"X-CH-Auth-API-Token": a.token,
	}, nil
}

func (a authCreds) RequireTransportSecurity() bool { return true }

// grpcTarget converts a config API URL (which may be a bare host, a
// host:port, or an https:// URL as customers might paste from Kentik's
// docs) into a grpc.NewClient-compatible "host:port" target.
func grpcTarget(apiURL string) string {
	target := strings.TrimPrefix(strings.TrimPrefix(apiURL, "https://"), "http://")
	target = strings.TrimRight(target, "/")
	if target == "" {
		return defaultHost
	}
	if !strings.Contains(target, ":") {
		target += ":443"
	}
	return target
}

// NewClient dials Kentik's gRPC API and constructs the three service
// clients used by this destination.
func NewClient(cfg Config) (*Client, error) {
	if cfg.RequestsPerMinute <= 0 {
		cfg.RequestsPerMinute = 100
	}
	target := grpcTarget(cfg.APIURL)

	conn, err := grpc.NewClient(target,
		grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{})), //nolint:gosec // default TLS config, no InsecureSkipVerify
		grpc.WithPerRPCCredentials(authCreds{email: cfg.Email, token: cfg.APIToken}),
	)
	if err != nil {
		return nil, fmt.Errorf("kentik: dialing %s: %w", target, err)
	}

	return &Client{
		conn:             conn,
		Devices:          device.NewDeviceServiceClient(conn),
		Sites:            site.NewSiteServiceClient(conn),
		CustomDimensions: customdimension.NewCustomDimensionServiceClient(conn),
		Labels:           label.NewLabelServiceClient(conn),
		cfg:              cfg,
		limiter:          rate.NewLimiter(rate.Limit(float64(cfg.RequestsPerMinute)/60.0), cfg.RequestsPerMinute),
	}, nil
}

func (c *Client) Close() error { return c.conn.Close() }
