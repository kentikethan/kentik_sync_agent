package config

import (
	"os"
	"path/filepath"
	"testing"

	_ "github.com/kentikethan/kentik_sync_agent/internal/source/netbox"
)

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("writing test config: %v", err)
	}
	return path
}

const validConfig = `
kentik:
  api_url: "grpc.api.kentik.com:443"
  email: "${TEST_KENTIK_EMAIL}"
  api_token: "${TEST_KENTIK_TOKEN}"
  default_plan_id: 123
  default_device_subtype: "host-nprobe-dns-www"
  custom_dimension_id: "1"
state:
  driver: sqlite
  path: /tmp/state.db
sources:
  - name: netbox-primary
    type: netbox
    connection:
      url: "https://netbox.example.com"
      token: "${TEST_NETBOX_TOKEN}"
    sync:
      objects: [sites, devices, ip_groups]
      interval: 15m
`

func TestLoad_ValidConfig(t *testing.T) {
	t.Setenv("TEST_KENTIK_EMAIL", "user@example.com")
	t.Setenv("TEST_KENTIK_TOKEN", "secret-token")
	t.Setenv("TEST_NETBOX_TOKEN", "netbox-secret")

	path := writeConfig(t, validConfig)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Kentik.Email != "user@example.com" {
		t.Fatalf("expected env interpolation, got %q", cfg.Kentik.Email)
	}
	if cfg.Sources[0].Connection["token"] != "netbox-secret" {
		t.Fatalf("expected env interpolation in nested map, got %+v", cfg.Sources[0].Connection)
	}
	if cfg.Sources[0].Sync.Interval.String() != "15m0s" {
		t.Fatalf("expected parsed interval, got %v", cfg.Sources[0].Sync.Interval)
	}
	// FullReconcileInterval wasn't set, so the default should have been applied.
	if cfg.Sources[0].Sync.FullReconcileInterval != defaultFullReconcile {
		t.Fatalf("expected default full reconcile interval, got %v", cfg.Sources[0].Sync.FullReconcileInterval)
	}
}

func TestLoad_MissingRequiredKentikFields(t *testing.T) {
	t.Setenv("TEST_NETBOX_TOKEN", "netbox-secret")
	path := writeConfig(t, `
kentik:
  api_url: "grpc.api.kentik.com:443"
sources:
  - name: netbox-primary
    type: netbox
    connection:
      url: "https://netbox.example.com"
      token: "${TEST_NETBOX_TOKEN}"
    sync:
      objects: [devices]
`)
	if _, err := Load(path); err == nil {
		t.Fatal("expected an error for missing kentik email/token/plan_id")
	}
}

func TestLoad_DuplicateSourceNames(t *testing.T) {
	t.Setenv("TEST_KENTIK_EMAIL", "user@example.com")
	t.Setenv("TEST_KENTIK_TOKEN", "secret-token")
	t.Setenv("TEST_NETBOX_TOKEN", "netbox-secret")

	path := writeConfig(t, `
kentik:
  api_url: "grpc.api.kentik.com:443"
  email: "${TEST_KENTIK_EMAIL}"
  api_token: "${TEST_KENTIK_TOKEN}"
  default_plan_id: 123
sources:
  - name: dup
    type: netbox
    connection: {url: "https://a.example.com", token: "${TEST_NETBOX_TOKEN}"}
    sync: {objects: [devices]}
  - name: dup
    type: netbox
    connection: {url: "https://b.example.com", token: "${TEST_NETBOX_TOKEN}"}
    sync: {objects: [devices]}
`)
	if _, err := Load(path); err == nil {
		t.Fatal("expected an error for duplicate source names")
	}
}

func TestLoad_UnknownSourceType(t *testing.T) {
	t.Setenv("TEST_KENTIK_EMAIL", "user@example.com")
	t.Setenv("TEST_KENTIK_TOKEN", "secret-token")

	path := writeConfig(t, `
kentik:
  api_url: "grpc.api.kentik.com:443"
  email: "${TEST_KENTIK_EMAIL}"
  api_token: "${TEST_KENTIK_TOKEN}"
  default_plan_id: 123
sources:
  - name: mystery
    type: not-a-real-source
    connection: {}
    sync: {objects: [devices]}
`)
	if _, err := Load(path); err == nil {
		t.Fatal("expected an error for an unregistered source type")
	}
}

func TestLoad_DeviceLabelsRequiresDevices(t *testing.T) {
	t.Setenv("TEST_KENTIK_EMAIL", "user@example.com")
	t.Setenv("TEST_KENTIK_TOKEN", "secret-token")
	t.Setenv("TEST_NETBOX_TOKEN", "netbox-secret")

	path := writeConfig(t, `
kentik:
  api_url: "grpc.api.kentik.com:443"
  email: "${TEST_KENTIK_EMAIL}"
  api_token: "${TEST_KENTIK_TOKEN}"
  default_plan_id: 123
sources:
  - name: netbox-primary
    type: netbox
    connection: {url: "https://netbox.example.com", token: "${TEST_NETBOX_TOKEN}"}
    sync: {objects: [device_labels]}
`)
	if _, err := Load(path); err == nil {
		t.Fatal("expected an error when device_labels is requested without devices")
	}

	pathOK := writeConfig(t, `
kentik:
  api_url: "grpc.api.kentik.com:443"
  email: "${TEST_KENTIK_EMAIL}"
  api_token: "${TEST_KENTIK_TOKEN}"
  default_plan_id: 123
sources:
  - name: netbox-primary
    type: netbox
    connection: {url: "https://netbox.example.com", token: "${TEST_NETBOX_TOKEN}"}
    sync: {objects: [devices, device_labels]}
`)
	if _, err := Load(pathOK); err != nil {
		t.Fatalf("expected device_labels alongside devices to be valid, got: %v", err)
	}
}

func TestLoad_ObjectTypeUnsupportedBySource(t *testing.T) {
	t.Setenv("TEST_KENTIK_EMAIL", "user@example.com")
	t.Setenv("TEST_KENTIK_TOKEN", "secret-token")
	t.Setenv("TEST_NETBOX_TOKEN", "netbox-secret")

	path := writeConfig(t, `
kentik:
  api_url: "grpc.api.kentik.com:443"
  email: "${TEST_KENTIK_EMAIL}"
  api_token: "${TEST_KENTIK_TOKEN}"
  default_plan_id: 123
sources:
  - name: netbox-primary
    type: netbox
    connection: {url: "https://netbox.example.com", token: "${TEST_NETBOX_TOKEN}"}
    sync: {objects: [not_a_real_object_type]}
`)
	if _, err := Load(path); err == nil {
		t.Fatal("expected an error for an object type the source doesn't support")
	}
}
