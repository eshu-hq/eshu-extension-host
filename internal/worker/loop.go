// SPDX-License-Identifier: MIT
// Copyright (c) 2026 eshu-hq

// Package worker runs the enabled-flow claim loop: poll the plane,
// execute one collector call per held claim, and report the outcome.
// It depends only on the cutovercontract seam (Runner for the child
// process, ClaimPlane for the remote plane), never on a concrete
// adapter or plane client; both sides bind in a later lane.
package worker

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/eshu-hq/eshu-extension-host/internal/cutovercontract"
)

// contractEnvelope is the minimal claim context handed to the collector
// child on stdin. Field names reuse the claim-plane vocabulary; the
// collector artifact owns the input schema and this envelope is its v0
// shape. The adapter validates the result, never this input.
type contractEnvelope struct {
	ProducerID string `json:"producer_id"`
	ClaimID    string `json:"claim_id"`
	LeaseID    string `json:"lease_id"`
	Scopes     string `json:"scopes"`
}

// Loop polls the plane and serves one claim at a time until ctx is done,
// then returns nil: cancellation is a clean stop, never an error. A
// nil runner or plane, an empty producer identity or scopes, or a
// non-positive poll or heartbeat interval fails closed at entry. Every
// other failure is absorbed with backoff: an empty queue or a failed
// ClaimNext sleeps the poll interval and re-polls, so a flapping plane
// or a revoked grant cannot spin the loop or stop the worker. A held
// claim re-polls immediately after its outcome lands.
//
// Outcomes partition on the run: success completes the claim; a run
// error fails the claim retryable and releases the hold; a grant denial
// — any error wrapping cutovercontract.ErrGrantDenied, from the run or
// from Complete — fails the claim terminal. Outcome calls are
// best-effort: their errors never stop the loop. A cancelled context
// skips outcome calls and returns, leaving the held lease to
// plane-side expiry.
func Loop(ctx context.Context, cfg Config, runner cutovercontract.Runner, plane cutovercontract.ClaimPlane) error {
	if runner == nil {
		return errors.New("worker: runner is required")
	}
	if plane == nil {
		return errors.New("worker: claim plane is required")
	}
	if strings.TrimSpace(cfg.ProducerID) == "" {
		return errors.New("worker: producer id is required")
	}
	if strings.TrimSpace(cfg.Scopes) == "" {
		return errors.New("worker: scopes are required")
	}
	if cfg.PollInterval <= 0 {
		return errors.New("worker: poll interval must be positive")
	}
	if cfg.HeartbeatInterval <= 0 {
		return errors.New("worker: heartbeat interval must be positive")
	}
	for {
		if ctx.Err() != nil {
			return nil
		}
		lease, found, err := plane.ClaimNext(ctx, cfg.ProducerID, cfg.Scopes)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			if !wait(ctx, cfg.PollInterval) {
				return nil
			}
			continue
		}
		if !found {
			if !wait(ctx, cfg.PollInterval) {
				return nil
			}
			continue
		}
		serveOne(ctx, cfg, runner, plane, lease)
	}
}

// serveOne executes one held claim: renew the lease with a heartbeat
// while the collector runs, then report the outcome. Cancellation
// abandons the outcome calls; the held lease expires plane-side.
func serveOne(ctx context.Context, cfg Config, runner cutovercontract.Runner, plane cutovercontract.ClaimPlane, lease cutovercontract.Lease) {
	done := make(chan struct{})
	go heartbeatLoop(ctx, plane, lease, cfg.HeartbeatInterval, done)
	result, runErr := runner.RunCollector(ctx, contractFor(cfg, lease))
	close(done)
	if ctx.Err() != nil {
		return
	}
	if runErr != nil {
		failClaim(ctx, plane, lease, runErr)
		return
	}
	if err := plane.Complete(ctx, lease, result); err != nil {
		if ctx.Err() != nil {
			return
		}
		failClaim(ctx, plane, lease, err)
	}
}

// failClaim reports a failed claim: a grant denial fails terminal, any
// other failure fails retryable and releases the hold for re-claim.
func failClaim(ctx context.Context, plane cutovercontract.ClaimPlane, lease cutovercontract.Lease, cause error) {
	if errors.Is(cause, cutovercontract.ErrGrantDenied) {
		_ = plane.FailTerminal(ctx, lease)
		return
	}
	_ = plane.FailRetryable(ctx, lease)
	_ = plane.Release(ctx, lease)
}

// heartbeatLoop renews the held lease until done closes or ctx ends. A
// failed beat is best-effort: staleness surfaces at Complete, where the
// outcome partition retries or releases the claim.
func heartbeatLoop(ctx context.Context, plane cutovercontract.ClaimPlane, lease cutovercontract.Lease, interval time.Duration, done <-chan struct{}) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = plane.Heartbeat(ctx, lease)
		}
	}
}

// contractFor renders the claim envelope for the collector child. The
// fields are strings by construction, so encoding cannot fail; a
// failure would be a coding error and fails closed with an empty
// object the adapter strict-decode rejects.
func contractFor(cfg Config, lease cutovercontract.Lease) []byte {
	raw, err := json.Marshal(contractEnvelope{
		ProducerID: cfg.ProducerID,
		ClaimID:    lease.ClaimID,
		LeaseID:    lease.LeaseID,
		Scopes:     cfg.Scopes,
	})
	if err != nil {
		return []byte(`{}`)
	}
	return raw
}

// wait sleeps d unless ctx ends first, reporting whether the sleep
// completed.
func wait(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
