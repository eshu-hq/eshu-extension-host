// SPDX-License-Identifier: MIT
// Copyright (c) 2026 eshu-hq

// Cutover wiring for the extension-host binary (eshu-hq/eshu#8877): the
// enabled claim flow behind --claims-enabled or
// ESHU_EXTENSIONHOST_CUTOVER_ENABLED=1. It builds the worker config, the
// claim-plane client, and the collector runner from the environment, then
// runs the worker loop until ctx ends. Every construction error is a
// startup failure: nonzero exit with the offending variable named and no
// value logged. --dry-run keeps claims, leases, and heartbeats live while
// the commit step only logs the result shape through the compare sink.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"strings"
	"time"

	"github.com/eshu-hq/eshu-extension-host/internal/adapter"
	"github.com/eshu-hq/eshu-extension-host/internal/claimplane"
	"github.com/eshu-hq/eshu-extension-host/internal/cutovercontract"
	"github.com/eshu-hq/eshu-extension-host/internal/worker"
)

// Environment knobs the enabled flow reads beyond the worker's own
// contract. The gate is boolean by construction: only "1" enables, empty
// stays dark, anything else fails closed so a typo never ships an
// unintended posture. The collector path names the child binary the
// adapter executes per held claim; it is required whenever the gate is
// open and is never derived silently.
const (
	envCutoverEnabled = "ESHU_EXTENSIONHOST_CUTOVER_ENABLED"
	envCollectorPath  = "ESHU_EXTENSIONHOST_COLLECTOR_PATH"
)

// cutoverEnabledByEnv resolves the environment half of the cutover gate.
// Only "1" enables; blank stays dark. Any other value is a startup error
// naming the variable, never the value.
func cutoverEnabledByEnv(getenv func(string) string) (bool, error) {
	raw := strings.TrimSpace(getenv(envCutoverEnabled))
	switch raw {
	case "":
		return false, nil
	case "1":
		return true, nil
	default:
		return false, fmt.Errorf("%s must be \"1\" or unset", envCutoverEnabled)
	}
}

// Committer is the outcome half of the claim-plane seam: finish a held
// claim or record its failure. The claim-plane client satisfies it; the
// dry-run compare sink satisfies it without ever completing. The worker
// loop keeps taking the full ClaimPlane; commitRoutedPlane binds the
// chosen committer into that shape.
type Committer interface {
	Complete(ctx context.Context, lease cutovercontract.Lease, resultJSON []byte) error
	FailRetryable(ctx context.Context, lease cutovercontract.Lease) error
	FailTerminal(ctx context.Context, lease cutovercontract.Lease) error
}

// The real client is the production committer: dry-run off commits.
var _ Committer = (*claimplane.Client)(nil)

// commitRoutedPlane binds a Committer into the full ClaimPlane the worker
// loop takes. Claim, heartbeat, and release ride the embedded plane
// untouched; only the outcome trio routes to the committer, which is the
// real client in production and the compare sink in dry-run.
type commitRoutedPlane struct {
	cutovercontract.ClaimPlane
	committer Committer
}

// The routed plane still speaks the whole seam.
var _ cutovercontract.ClaimPlane = commitRoutedPlane{}

func (p commitRoutedPlane) Complete(ctx context.Context, lease cutovercontract.Lease, resultJSON []byte) error {
	return p.committer.Complete(ctx, lease, resultJSON)
}

func (p commitRoutedPlane) FailRetryable(ctx context.Context, lease cutovercontract.Lease) error {
	return p.committer.FailRetryable(ctx, lease)
}

func (p commitRoutedPlane) FailTerminal(ctx context.Context, lease cutovercontract.Lease) error {
	return p.committer.FailTerminal(ctx, lease)
}

// compareSink is the shadow-mode committer: it observes the collector
// result, logs its byte count and sha256, and drops it without calling
// Complete. Failure outcomes still delegate downstream so a failed shadow
// run releases its hold instead of squatting the lease to expiry. It
// never completes, by construction: Complete has no downstream call.
type compareSink struct {
	downstream Committer
	logger     *slog.Logger
}

// The sink satisfies the committer seam so it plugs into the router.
var _ Committer = (*compareSink)(nil)

func newCompareSink(downstream Committer, stderr io.Writer) *compareSink {
	return &compareSink{
		downstream: downstream,
		logger:     slog.New(slog.NewJSONHandler(stderr, nil)),
	}
}

func (s *compareSink) Complete(_ context.Context, lease cutovercontract.Lease, resultJSON []byte) error {
	sum := sha256.Sum256(resultJSON)
	s.logger.Info("dry-run compare: result observed, commit dropped",
		"event_name", "runtime.dryrun.compare",
		"claim_id", lease.ClaimID,
		"result_bytes", len(resultJSON),
		"result_sha256", hex.EncodeToString(sum[:]))
	return nil
}

func (s *compareSink) FailRetryable(ctx context.Context, lease cutovercontract.Lease) error {
	return s.downstream.FailRetryable(ctx, lease)
}

func (s *compareSink) FailTerminal(ctx context.Context, lease cutovercontract.Lease) error {
	return s.downstream.FailTerminal(ctx, lease)
}

// runEnabled builds the enabled flow and runs it until ctx ends. Any
// construction error fails closed before the admin listener opens: the
// binary never half-serves. Cancellation is a clean stop: the admin
// server drains, the worker loop abandons its hold to plane-side expiry,
// and the exit is 0.
func runEnabled(ctx context.Context, cfg serverConfig, logger *slog.Logger, stderr io.Writer) int {
	wcfg, err := worker.ConfigFromEnv(os.Getenv)
	if err != nil {
		logger.Error("cutover config invalid, failing closed",
			"event_name", "runtime.startup.failed", "error", err)
		return 1
	}
	plane, err := claimplane.NewClient(claimplane.Config{
		BaseURL:         wcfg.ClaimPlaneURL,
		ProducerID:      wcfg.ProducerID,
		ProducerVersion: wcfg.ProducerVersion,
		Scopes:          []string{wcfg.Scopes},
		Grants:          &claimplane.StaticGrants{Grants: []claimplane.Grant{{ProducerID: wcfg.ProducerID}}},
	})
	if err != nil {
		logger.Error("claim-plane client invalid, failing closed",
			"event_name", "runtime.startup.failed", "error", err)
		return 1
	}
	collectorPath := strings.TrimSpace(os.Getenv(envCollectorPath))
	if collectorPath == "" {
		logger.Error("collector path invalid, failing closed",
			"event_name", "runtime.startup.failed",
			"error", fmt.Errorf("%s is required: the collector child binary is never derived silently", envCollectorPath))
		return 1
	}
	runner := adapter.ProcessRunner{Path: collectorPath}
	var committer Committer = plane
	if cfg.dryRun {
		committer = newCompareSink(plane, stderr)
	}
	routed := commitRoutedPlane{ClaimPlane: plane, committer: committer}
	ln, err := net.Listen("tcp", cfg.adminListen)
	if err != nil {
		logger.Error("admin listen failed, failing closed",
			"event_name", "runtime.startup.failed", "error", err)
		return 1
	}
	logger.Info("extension-host cutover enabled: claiming work until signal",
		"event_name", "runtime.startup.ready",
		"mode", "enabled", "claims_enabled", true, "dry_run", cfg.dryRun,
		"addr", ln.Addr().String(), "version", version)
	loopDone := make(chan error, 1)
	go func() { loopDone <- worker.Loop(ctx, wcfg, runner, routed) }()
	if err := serve(ctx, ln, version, time.Now(), true); err != nil {
		logger.Error("admin server failed", "event_name", "runtime.serve.failed", "error", err)
		return 1
	}
	if err := <-loopDone; err != nil {
		logger.Error("worker loop failed", "event_name", "runtime.serve.failed", "error", err)
		return 1
	}
	return 0
}
