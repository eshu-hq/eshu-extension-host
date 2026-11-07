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
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
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
// nil runner or plane, an empty producer identity or version, an empty
// collector instance id, empty scopes, or a non-positive poll or
// heartbeat interval fails closed at entry. Every poll builds the
// spec-shaped ClaimRequest from Config — the scope kind selects the
// collector kind, the dedicated instance knob feeds the plane's (kind,
// instance) match key, the producer id pins the owner slot, the lease TTL
// rides as the requested hold, and the claim id is a fresh nonce per
// poll — and the client stamps ObservedAt. Every other failure is
// absorbed with backoff: an empty queue or a failed ClaimNext sleeps the
// poll interval and re-polls, so a flapping plane or a revoked grant
// cannot spin the loop or stop the worker. A held claim re-polls
// immediately after its outcome lands.
//
// Outcomes partition on the run: success commits the verbatim result and
// then completes the state-only claim; a run error fails the claim
// retryable and releases the hold; a commit failure fails per the same
// classifier without ever completing, so only the commit ack moves the
// epoch. A grant denial — any error wrapping
// cutovercontract.ErrGrantDenied, from the run, the commit, or the
// complete — fails the claim terminal. Outcome calls are best-effort:
// their errors never stop the loop. A cancelled context skips outcome
// calls and returns, leaving the held lease to plane-side expiry.
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
	if strings.TrimSpace(cfg.ProducerVersion) == "" {
		return errors.New("worker: producer version is required")
	}
	if strings.TrimSpace(cfg.CollectorInstanceID) == "" {
		return errors.New("worker: collector instance id is required")
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
		lease, found, err := plane.ClaimNext(ctx, claimRequestFor(cfg))
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

// claimRequestFor builds the spec-shaped ClaimNext request from the worker
// wiring: the producer identity, the dedicated instance knob, and scopes
// fold into the selector (the spec carries no top-level producer slots),
// the lease TTL rides as the requested hold, and ObservedAt stays zero
// for the client clock to stamp. The instance knob is never the producer
// id: the plane matches (kind, instance) by equality, so folding the
// producer id in strands every producer whose id differs from the
// instance id. The claim id is a fresh per-call nonce (the in-tree
// newClaimID precedent), so the plane never sees a replayed selector.
func claimRequestFor(cfg Config) cutovercontract.ClaimRequest {
	return cutovercontract.ClaimRequest{
		Selector: cutovercontract.ClaimSelector{
			CollectorKind:       cfg.Scopes,
			CollectorInstanceID: cfg.CollectorInstanceID,
			OwnerID:             cfg.ProducerID,
			ClaimID:             newClaimID(),
		},
		LeaseTTL: cfg.LeaseTTL,
	}
}

// newClaimID mints one 128-bit hex claim nonce. A randomness failure is
// near-impossible; it still fails closed with a timestamp-derived fallback
// rather than an empty selector the plane must reject.
func newClaimID() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return fmt.Sprintf("rand-fallback-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(raw[:])
}

// serveOne executes one held claim: renew the lease with a heartbeat
// while the collector runs, commit the verbatim result, then complete the
// state-only epoch — commit first, complete second, one fencing token for
// both. Only the {"ok":true} commit ack proceeds to Complete; every
// commit failure fails per the existing retry classifier without ever
// completing, so a 409-class refusal, a transport loss, or a 5xx never
// moves the epoch. Cancellation abandons the outcome calls; the held
// lease expires plane-side.
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
	if err := plane.CommitClaimFacts(ctx, lease, result); err != nil {
		if ctx.Err() != nil {
			return
		}
		failClaim(ctx, plane, lease, err)
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
