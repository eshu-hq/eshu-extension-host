// SPDX-License-Identifier: MIT
// Copyright (c) 2026 eshu-hq

package worker

import (
	"fmt"
	"net/url"
	"strings"
	"time"
)

// Environment knobs mirror the in-tree worker names
// (eshu go/cmd/collector-component-extension/config.go and
// cutover.go resolveCutoverPlan) so operators carry one vocabulary across
// the extraction: the producer pin, the scope kind, the claim-plane URL,
// and the poll/lease timings.
const (
	envProducerID      = "ESHU_EXTENSIONHOST_CUTOVER_PRODUCER_ID"
	envProducerVersion = "ESHU_EXTENSIONHOST_CUTOVER_PRODUCER_VERSION"
	envScopeKind       = "ESHU_COMPONENT_COLLECTOR_SCOPE_KIND"
	envClaimPlaneURL   = "ESHU_WORKFLOW_CLAIM_PLANE_URL"
	envPollInterval    = "ESHU_COMPONENT_COLLECTOR_POLL_INTERVAL"
	envLeaseTTL        = "ESHU_COMPONENT_COLLECTOR_CLAIM_LEASE_TTL"
)

// Defaults mirror the in-tree fallbacks: the component scope kind, a
// one-second poll, and the 60-second claim lease TTL. The heartbeat
// interval derives as a third of the lease TTL, matching the in-tree
// 20s/60s pair and the heartbeat-must-be-less-than-lease startup law.
const (
	defaultScopeKind    = "component"
	defaultPollInterval = time.Second
	defaultLeaseTTL     = 60 * time.Second
)

// Config is the enabled-flow worker wiring. Every field comes from the
// environment via ConfigFromEnv; Loop trusts a constructed Config and
// fails closed only on collaborator or identity misuse.
type Config struct {
	// ProducerID and ProducerVersion pin the holder identity every
	// grant decision evaluates under. They are never derived silently.
	ProducerID      string
	ProducerVersion string
	// Scopes is the declared collector kind carried on each ClaimNext.
	Scopes string
	// ClaimPlaneURL is the remote plane origin (http or https).
	ClaimPlaneURL string
	// PollInterval paces re-claims on an empty queue or a failed
	// ClaimNext. A completed claim re-polls immediately.
	PollInterval time.Duration
	// LeaseTTL is the plane-side claim hold the heartbeat renews
	// against. The worker never enforces it locally.
	LeaseTTL time.Duration
	// HeartbeatInterval paces Heartbeat during a collector run. It is
	// always a third of LeaseTTL, so the in-tree heartbeat<lease law
	// holds by construction.
	HeartbeatInterval time.Duration
}

// ConfigFromEnv resolves the worker wiring from the environment. It
// mirrors the in-tree fail-closed posture: a missing producer pin or
// plane URL is a startup error naming the variable, never its value;
// durations parse with time.ParseDuration and must be positive.
func ConfigFromEnv(getenv func(string) string) (Config, error) {
	producerID := strings.TrimSpace(getenv(envProducerID))
	if producerID == "" {
		return Config{}, fmt.Errorf("%s is required: the holder identity is pinned, never derived silently", envProducerID)
	}
	producerVersion := strings.TrimSpace(getenv(envProducerVersion))
	if producerVersion == "" {
		return Config{}, fmt.Errorf("%s is required: the holder identity is pinned, never derived silently", envProducerVersion)
	}
	scopes := strings.TrimSpace(getenv(envScopeKind))
	if scopes == "" {
		scopes = defaultScopeKind
	}
	planeURL := strings.TrimSpace(getenv(envClaimPlaneURL))
	if planeURL == "" {
		return Config{}, fmt.Errorf("%s is required: the remote client, claim store, and commit fence share one plane", envClaimPlaneURL)
	}
	if err := checkPlaneURL(planeURL); err != nil {
		return Config{}, err
	}
	pollInterval, err := envDuration(getenv, envPollInterval, defaultPollInterval)
	if err != nil {
		return Config{}, err
	}
	leaseTTL, err := envDuration(getenv, envLeaseTTL, defaultLeaseTTL)
	if err != nil {
		return Config{}, err
	}
	if leaseTTL/3 <= 0 {
		return Config{}, fmt.Errorf("%s must derive a positive heartbeat interval (leaseTTL/3 > 0)", envLeaseTTL)
	}
	return Config{
		ProducerID:        producerID,
		ProducerVersion:   producerVersion,
		Scopes:            scopes,
		ClaimPlaneURL:     planeURL,
		PollInterval:      pollInterval,
		LeaseTTL:          leaseTTL,
		HeartbeatInterval: leaseTTL / 3,
	}, nil
}

// checkPlaneURL fails closed on a plane origin the client could not
// dial. The error names the variable only: the value may carry a
// credential the operator pasted by mistake.
func checkPlaneURL(raw string) error {
	endpoint, err := url.ParseRequestURI(raw)
	if err != nil {
		return fmt.Errorf("%s is not a valid URL", envClaimPlaneURL)
	}
	if endpoint.Scheme != "http" && endpoint.Scheme != "https" {
		return fmt.Errorf("%s must use the http or https scheme", envClaimPlaneURL)
	}
	if endpoint.Hostname() == "" {
		return fmt.Errorf("%s must name a host", envClaimPlaneURL)
	}
	return nil
}

// envDuration mirrors the in-tree parser: blank selects the fallback, a
// present value must parse and be positive.
func envDuration(getenv func(string) string, key string, fallback time.Duration) (time.Duration, error) {
	raw := strings.TrimSpace(getenv(key))
	if raw == "" {
		return fallback, nil
	}
	value, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("parse %s: %w", key, err)
	}
	if value <= 0 {
		return 0, fmt.Errorf("%s must be positive", key)
	}
	return value, nil
}
