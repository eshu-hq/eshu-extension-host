// SPDX-License-Identifier: MIT
// Copyright (c) 2026 eshu-hq

package worker

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testEnv(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

func requiredEnv(t *testing.T) map[string]string {
	t.Helper()
	return map[string]string{
		"ESHU_EXTENSIONHOST_CUTOVER_PRODUCER_ID":      "test-producer",
		"ESHU_EXTENSIONHOST_CUTOVER_PRODUCER_VERSION": "v0.1.0",
		"ESHU_COMPONENT_COLLECTOR_INSTANCE_ID":        "test-instance",
		"ESHU_COMPONENT_COLLECTOR_CONFIG_HANDLE":      "rehearsal:test",
		"ESHU_COMPONENT_COLLECTOR_CONFIG_PATH":        writeExtensionConfig(t, `{"collector_instance_id":"test-instance","targets":["t1"]}`),
		"ESHU_WORKFLOW_CLAIM_PLANE_URL":               "https://plane.internal",
	}
}

// writeExtensionConfig materializes one extension config file for a
// test and returns its path. ConfigFromEnv reads the real file, so
// these tests prove the load path, not a stub of it.
func writeExtensionConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "extension-config.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write extension config: %v", err)
	}
	return path
}

func TestConfigFromEnvRequiresProducerPin(t *testing.T) {
	for _, key := range []string{
		"ESHU_EXTENSIONHOST_CUTOVER_PRODUCER_ID",
		"ESHU_EXTENSIONHOST_CUTOVER_PRODUCER_VERSION",
		"ESHU_COMPONENT_COLLECTOR_INSTANCE_ID",
		"ESHU_WORKFLOW_CLAIM_PLANE_URL",
	} {
		values := requiredEnv(t)
		delete(values, key)
		if _, err := ConfigFromEnv(testEnv(values)); err == nil {
			t.Errorf("missing %s: got nil error, want fail-closed", key)
		}
	}
}

func TestConfigFromEnvRequiresInstanceID(t *testing.T) {
	// The collector instance id is the plane's (kind, instance) match
	// key: it is never derived from the producer id, so a missing knob
	// fails closed naming the variable, never the value.
	values := requiredEnv(t)
	delete(values, "ESHU_COMPONENT_COLLECTOR_INSTANCE_ID")
	_, err := ConfigFromEnv(testEnv(values))
	if err == nil {
		t.Fatal("missing ESHU_COMPONENT_COLLECTOR_INSTANCE_ID: got nil error, want fail-closed")
	}
	if got := err.Error(); !strings.Contains(got, "ESHU_COMPONENT_COLLECTOR_INSTANCE_ID") {
		t.Fatalf("error %q does not name the variable", got)
	}
}

func TestConfigFromEnvLoadsExtensionConfig(t *testing.T) {
	cfg, err := ConfigFromEnv(testEnv(requiredEnv(t)))
	if err != nil {
		t.Fatalf("ConfigFromEnv: %v", err)
	}
	if cfg.ExtensionConfig["collector_instance_id"] != "test-instance" {
		t.Errorf("ExtensionConfig[collector_instance_id] = %v, want test-instance",
			cfg.ExtensionConfig["collector_instance_id"])
	}
	targets, ok := cfg.ExtensionConfig["targets"].([]any)
	if !ok || len(targets) != 1 || targets[0] != "t1" {
		t.Errorf("ExtensionConfig[targets] = %v, want [t1]", cfg.ExtensionConfig["targets"])
	}
}

func TestConfigFromEnvRequiresExtensionConfig(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "absent.json")
	for _, tc := range []struct {
		name  string
		value string
	}{
		{"unset", ""},
		{"missing file", missing},
	} {
		t.Run(tc.name, func(t *testing.T) {
			values := requiredEnv(t)
			if tc.value == "" {
				delete(values, "ESHU_COMPONENT_COLLECTOR_CONFIG_PATH")
			} else {
				values["ESHU_COMPONENT_COLLECTOR_CONFIG_PATH"] = tc.value
			}
			_, err := ConfigFromEnv(testEnv(values))
			if err == nil {
				t.Fatalf("%s: got nil error, want fail-closed", tc.name)
			}
			if got := err.Error(); !strings.Contains(got, "ESHU_COMPONENT_COLLECTOR_CONFIG_PATH") {
				t.Fatalf("error %q does not name the variable", got)
			}
			if tc.value != "" {
				if got := err.Error(); strings.Contains(got, tc.value) {
					t.Fatalf("error %q leaks the file path", got)
				}
			}
		})
	}
}

func TestConfigFromEnvRejectsBadExtensionConfig(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content string
	}{
		{"unparseable", "{oops"},
		{"array", `["t1"]`},
		{"string", `"t1"`},
		{"null", "null"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			values := requiredEnv(t)
			values["ESHU_COMPONENT_COLLECTOR_CONFIG_PATH"] = writeExtensionConfig(t, tc.content)
			_, err := ConfigFromEnv(testEnv(values))
			if err == nil {
				t.Fatalf("content %q: got nil error, want fail-closed", tc.content)
			}
			if got := err.Error(); !strings.Contains(got, "ESHU_COMPONENT_COLLECTOR_CONFIG_PATH") {
				t.Fatalf("error %q does not name the variable", got)
			}
		})
	}
}

func TestConfigFromEnvDefaults(t *testing.T) {
	cfg, err := ConfigFromEnv(testEnv(requiredEnv(t)))
	if err != nil {
		t.Fatalf("ConfigFromEnv: %v", err)
	}
	if cfg.ProducerID != "test-producer" || cfg.ProducerVersion != "v0.1.0" {
		t.Errorf("pin = %q/%q, want test-producer/v0.1.0", cfg.ProducerID, cfg.ProducerVersion)
	}
	if cfg.CollectorInstanceID != "test-instance" {
		t.Errorf("CollectorInstanceID = %q, want test-instance (never derived from the producer id)", cfg.CollectorInstanceID)
	}
	if cfg.Scopes != "component" {
		t.Errorf("Scopes = %q, want in-tree default %q", cfg.Scopes, "component")
	}
	if cfg.ScopeKind != "component" {
		t.Errorf("ScopeKind = %q, want the single-env coupling %q", cfg.ScopeKind, "component")
	}
	if cfg.PollInterval != time.Second {
		t.Errorf("PollInterval = %v, want in-tree default 1s", cfg.PollInterval)
	}
	if cfg.LeaseTTL != 60*time.Second {
		t.Errorf("LeaseTTL = %v, want in-tree default 60s", cfg.LeaseTTL)
	}
	if cfg.HeartbeatInterval != 20*time.Second {
		t.Errorf("HeartbeatInterval = %v, want TTL/3 = 20s", cfg.HeartbeatInterval)
	}
}

func TestConfigFromEnvParsesOverrides(t *testing.T) {
	values := requiredEnv(t)
	values["ESHU_COMPONENT_COLLECTOR_SCOPE_KIND"] = "scanner"
	values["ESHU_COMPONENT_COLLECTOR_POLL_INTERVAL"] = "5s"
	values["ESHU_COMPONENT_COLLECTOR_CLAIM_LEASE_TTL"] = "90s"
	cfg, err := ConfigFromEnv(testEnv(values))
	if err != nil {
		t.Fatalf("ConfigFromEnv: %v", err)
	}
	if cfg.Scopes != "scanner" {
		t.Errorf("Scopes = %q, want scanner", cfg.Scopes)
	}
	if cfg.ScopeKind != "scanner" {
		t.Errorf("ScopeKind = %q, want the single-env coupling %q", cfg.ScopeKind, "scanner")
	}
	if cfg.PollInterval != 5*time.Second {
		t.Errorf("PollInterval = %v, want 5s", cfg.PollInterval)
	}
	if cfg.HeartbeatInterval != 30*time.Second {
		t.Errorf("HeartbeatInterval = %v, want TTL/3 = 30s", cfg.HeartbeatInterval)
	}
}

func TestConfigFromEnvRejectsBadDurations(t *testing.T) {
	for _, tc := range []struct {
		name  string
		key   string
		value string
	}{
		{"poll garbage", "ESHU_COMPONENT_COLLECTOR_POLL_INTERVAL", "bogus"},
		{"poll zero", "ESHU_COMPONENT_COLLECTOR_POLL_INTERVAL", "0s"},
		{"poll negative", "ESHU_COMPONENT_COLLECTOR_POLL_INTERVAL", "-1s"},
		{"lease garbage", "ESHU_COMPONENT_COLLECTOR_CLAIM_LEASE_TTL", "soon"},
		{"lease zero", "ESHU_COMPONENT_COLLECTOR_CLAIM_LEASE_TTL", "0s"},
		{"lease heartbeat rounds to zero", "ESHU_COMPONENT_COLLECTOR_CLAIM_LEASE_TTL", "1ns"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			values := requiredEnv(t)
			values[tc.key] = tc.value
			if _, err := ConfigFromEnv(testEnv(values)); err == nil {
				t.Errorf("%s=%q: got nil error, want fail-closed", tc.key, tc.value)
			}
		})
	}
}

func TestConfigFromEnvAcceptsLocalHTTPPlane(t *testing.T) {
	// A loopback httptest server is the shape an operator pastes for a
	// local plane: plain http with a host and port must validate.
	srv := httptest.NewServer(nil)
	defer srv.Close()
	values := requiredEnv(t)
	values["ESHU_WORKFLOW_CLAIM_PLANE_URL"] = srv.URL
	cfg, err := ConfigFromEnv(testEnv(values))
	if err != nil {
		t.Fatalf("ConfigFromEnv(%q): %v", srv.URL, err)
	}
	if cfg.ClaimPlaneURL != srv.URL {
		t.Errorf("ClaimPlaneURL = %q, want %q", cfg.ClaimPlaneURL, srv.URL)
	}
}

func TestConfigFromEnvRejectsBadURL(t *testing.T) {
	for _, raw := range []string{
		"ftp://plane.internal",
		"http://",
		"://missing-scheme",
		"not-a-url with spaces",
	} {
		values := requiredEnv(t)
		values["ESHU_WORKFLOW_CLAIM_PLANE_URL"] = raw
		if _, err := ConfigFromEnv(testEnv(values)); err == nil {
			t.Errorf("url=%q: got nil error, want fail-closed", raw)
		}
	}
}
