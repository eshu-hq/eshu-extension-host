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
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/eshu-hq/eshu-extension-host/internal/cutovercontract"
)

// extensionProtocolVersion is the collector SDK wire the bundled
// artifact talks (collector-sdk/v1alpha1): the single-call contract the
// saver commits. The artifact echoes the request version onto its
// result, and the saver only commits the single-call versions —
// asking with the paged version would fail every commit even for
// complete, unpaged output. This literal must match both sides.
const extensionProtocolVersion = "collector-sdk/v1alpha1"

// childRequest is the full extension request handed to the collector
// child on stdin. Field names mirror the in-tree extensionhost.Request
// the artifact decodes (eshu go/internal/collector/extensionhost): the
// collector artifact owns the input schema, so this struct tracks it
// field-for-field and the adapter validates the result, never this
// input. The fencing token rides decimal — the child parses base-10.
type childRequest struct {
	ProtocolVersion string        `json:"protocol_version"`
	Claim           childClaim    `json:"claim"`
	Contract        childContract `json:"contract"`
}

// childClaim is the claimed-epoch half of the child request. SourceSystem
// forwards the planned source system: the satellite door requires the echo,
// and the in-tree host sends the planned value, never a guess. Attempt,
// Deadline, and ConfigHandle ride for the same reason: the door requires
// attempt >= 1, a non-zero deadline, and a config handle on the echo,
// and the child artifact echoes this claim verbatim onto its result.
type childClaim struct {
	ComponentID   string     `json:"component_id"`
	InstanceID    string     `json:"instance_id"`
	CollectorKind string     `json:"collector_kind"`
	SourceSystem  string     `json:"source_system"`
	Scope         childScope `json:"scope"`
	SourceRunID   string     `json:"source_run_id"`
	GenerationID  string     `json:"generation_id"`
	WorkItemID    string     `json:"work_item_id"`
	FencingToken  string     `json:"fencing_token"`
	Attempt       int        `json:"attempt"`
	Deadline      time.Time  `json:"deadline"`
	ConfigHandle  string     `json:"config_handle"`
}

// childScope names the claimed scope.
type childScope struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
}

// childContract declares the artifact contract version. Facts ride the
// pinned artifact, not this envelope, so the declaration stays empty.
type childContract struct {
	ProtocolVersion string `json:"protocol_version"`
	Facts           []any  `json:"facts"`
}

// Loop polls the plane and serves one claim at a time until ctx is done,
// then returns nil: cancellation is a clean stop, never an error. A
// nil runner or plane, an empty producer identity or version, an empty
// collector instance id, empty scopes, or a non-positive poll or
// heartbeat interval fails closed at entry. Every poll builds the
// spec-shaped ClaimRequest from Config — the declared kind rides the
// match key, the configured scope kind renders into each child claim
// scope, the dedicated instance knob feeds the plane's (kind,
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
// complete — fails the claim terminal, as does a terminally refused
// carrier wrapping cutovercontract.ErrTerminalCarrier. Outcome calls are best-effort:
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
	if strings.TrimSpace(cfg.ScopeKind) == "" {
		return errors.New("worker: scope kind is required")
	}
	if strings.TrimSpace(cfg.ConfigHandle) == "" {
		return errors.New("worker: config handle is required")
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
		// A grant without a planned source system can never commit: the
		// satellite door requires the echo, so launching the child only
		// burns the hold. Fail terminal before any child launch, never
		// retryable — a re-claim returns the same grant.
		if strings.TrimSpace(lease.SourceSystem) == "" {
			_ = plane.FailTerminal(ctx, lease)
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
// moves the epoch — while a terminally refused carrier fails terminal
// instead of retrying. Cancellation abandons the outcome calls; the held
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
		failClaim(ctx, plane, lease, runErr, cfg.Logger)
		return
	}
	if err := plane.CommitClaimFacts(ctx, lease, result); err != nil {
		if ctx.Err() != nil {
			return
		}
		failClaim(ctx, plane, lease, err, cfg.Logger)
		return
	}
	if err := plane.Complete(ctx, lease, result); err != nil {
		if ctx.Err() != nil {
			return
		}
		failClaim(ctx, plane, lease, err, cfg.Logger)
	}
}

// failClaim reports a failed claim: a grant denial or a terminally
// refused carrier fails terminal, any other failure fails retryable and
// releases the hold for re-claim. A refused carrier (e.g. the plane
// rejecting a terminal-state result that is not complete) can never
// succeed on retry, so it ends in the bounded terminal state instead of
// spinning the claim loop; transport losses and server failures keep
// the retryable path.
// The cause is logged with the claim id when the host wired a logger:
// the plane outcome rows carry no cause text, so without this line the
// next door refusal is as blind as the attempt/deadline/handle refusals
// were. A nil logger stays silent.
func failClaim(ctx context.Context, plane cutovercontract.ClaimPlane, lease cutovercontract.Lease, cause error, log *slog.Logger) {
	outcome := "retryable"
	if errors.Is(cause, cutovercontract.ErrGrantDenied) || errors.Is(cause, cutovercontract.ErrTerminalCarrier) {
		outcome = "terminal"
		_ = plane.FailTerminal(ctx, lease)
	} else {
		_ = plane.FailRetryable(ctx, lease)
		_ = plane.Release(ctx, lease)
	}
	if log != nil {
		log.Error("claim failed",
			"event_name", "runtime.claim.failed",
			"claim_id", lease.ClaimID,
			"outcome", outcome,
			"error", cause,
		)
	}
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

// contractFor renders the extension request for the collector child
// from the worker config and the granted lease: holder and instance
// identity from config, scope/run/generation/work-item plus the decimal
// fencing token from the grant. The fields are strings by construction,
// so encoding cannot fail; a failure would be a coding error and fails
// closed with an empty object the adapter strict-decode rejects.
func contractFor(cfg Config, lease cutovercontract.Lease) []byte {
	raw, err := json.Marshal(childRequest{
		ProtocolVersion: extensionProtocolVersion,
		Claim: childClaim{
			ComponentID:   cfg.ProducerID,
			InstanceID:    cfg.CollectorInstanceID,
			CollectorKind: cfg.Scopes,
			SourceSystem:  lease.SourceSystem,
			Scope:         childScope{ID: lease.ScopeID, Kind: cfg.ScopeKind},
			SourceRunID:   lease.SourceRunID,
			GenerationID:  lease.GenerationID,
			WorkItemID:    lease.LeaseID,
			FencingToken:  strconv.FormatInt(lease.FencingToken, 10),
			Attempt:       lease.Attempt,
			Deadline:      lease.Deadline,
			ConfigHandle:  cfg.ConfigHandle,
		},
		Contract: childContract{
			ProtocolVersion: extensionProtocolVersion,
			Facts:           []any{},
		},
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
