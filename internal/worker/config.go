// SPDX-License-Identifier: MIT
// Copyright (c) 2026 eshu-hq

package worker

import (
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"time"
)

// Environment knobs mirror the in-tree worker names
// (eshu go/cmd/collector-component-extension/config.go and
// cutover.go resolveCutoverPlan) so operators carry one vocabulary across
// the extraction: the producer pin, the collector instance id, the scope
// kind, the config handle, the claim-plane URL, and the poll/lease
// timings.
const (
	envProducerID      = "ESHU_EXTENSIONHOST_CUTOVER_PRODUCER_ID"
	envProducerVersion = "ESHU_EXTENSIONHOST_CUTOVER_PRODUCER_VERSION"
	envInstanceID      = "ESHU_COMPONENT_COLLECTOR_INSTANCE_ID"
	envScopeKind       = "ESHU_COMPONENT_COLLECTOR_SCOPE_KIND"
	envConfigHandle    = "ESHU_COMPONENT_COLLECTOR_CONFIG_HANDLE"
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

// Config is the enabled-flow worker wiring. Every field but Logger
// comes from the environment via ConfigFromEnv; Loop trusts a
// constructed Config and fails closed only on collaborator or identity
// misuse. Logger carries the host's structured logger for claim
// outcome lines (a nil logger stays silent); it is set by the host
// process, never from the environment.
type Config struct {
	// ProducerID and ProducerVersion pin the holder identity every
	// grant decision evaluates under. They are never derived silently.
	ProducerID      string
	ProducerVersion string
	// CollectorInstanceID is the plane's (kind, instance) match key
	// carried on every ClaimNext. It is a dedicated knob, never derived
	// from the producer id: folding the producer id in here strands any
	// producer whose id differs from the instance id with an
	// unclaimable selector.
	CollectorInstanceID string
	// Scopes is the declared collector kind carried on each ClaimNext.
	Scopes string
	// ScopeKind is the collector's static scope kind rendered into each
	// child claim scope. It mirrors the in-tree collector config: the
	// satellite door requires the echo, core matches the scope id only,
	// and kind, id, and kind-of-scope stay three separate facts even
	// when one lane spells them the same.
	ScopeKind string
	// ConfigHandle is the static config handle rendered into each
	// child claim. The door requires the echo; the rehearsal producer
	// has no activation record to derive the in-tree handle from, so
	// the operator pins a rehearsal-namespaced handle here, never
	// derived silently.
	ConfigHandle string
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
	// Logger receives claim outcome lines. See the Config doc: set by
	// the host process, never from the environment.
	Logger *slog.Logger
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
	instanceID := strings.TrimSpace(getenv(envInstanceID))
	if instanceID == "" {
		return Config{}, fmt.Errorf("%s is required: the claim match key is pinned, never derived from the producer id", envInstanceID)
	}
	scopes := strings.TrimSpace(getenv(envScopeKind))
	if scopes == "" {
		scopes = defaultScopeKind
	}
	configHandle := strings.TrimSpace(getenv(envConfigHandle))
	if configHandle == "" {
		return Config{}, fmt.Errorf("%s is required: the door requires the echo and the rehearsal producer has no activation record to derive it from", envConfigHandle)
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
		ProducerID:          producerID,
		ProducerVersion:     producerVersion,
		CollectorInstanceID: instanceID,
		Scopes:              scopes,
		ScopeKind:           scopes,
		ConfigHandle:        configHandle,
		ClaimPlaneURL:       planeURL,
		PollInterval:        pollInterval,
		LeaseTTL:            leaseTTL,
		HeartbeatInterval:   leaseTTL / 3,
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
