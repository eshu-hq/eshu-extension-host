// SPDX-License-Identifier: MIT
// Copyright (c) 2026 eshu-hq

// Package cutovercontract pins the seam between the worker loop and its
// two collaborators: the adapter runner (child process over stdio) and
// the claim-plane client (claim/lease/heartbeat/commit over HTTP).
//
// The shapes mirror the in-tree behavior the Leaf 1 cutover client
// speaks (eshu go/cmd/collector-component-extension, PREP §5), reduced
// to stdlib-only JSON: this module keeps no SDK dependency by design,
// so wire values cross the seam as raw JSON with explicit byte limits.
// Later lanes implement both sides; the worker loop depends only on
// these interfaces.
package cutovercontract

import (
	"context"
	"errors"
	"time"
)

// DefaultStdoutLimit bounds one adapter stdout read. It matches the
// in-tree 4 MiB default; truncation fails closed (see ErrTruncated).
const DefaultStdoutLimit int64 = 4 << 20

// DefaultCommitResultBytes caps one commit's result payload. It pairs
// with the adapter stdout bound: results the producer legally emits
// must pass, so the cap never shrinks below what the adapter may write.
// It mirrors workflowv1.MaxCommitResultBytes (eshu-workflow@v0.2.0
// workflowv1/commit.go) so the two sides cannot drift.
const DefaultCommitResultBytes = 4 << 20

// DefaultCommitBodyBytes caps the whole CommitClaimFacts envelope on
// this seam: the result plus a 64 KiB budget for the mutation,
// idempotency key, planned IDs, and JSON framing (4,194,304 + 65,536
// = 4,259,840 bytes). The body cap itself is the bound on unbounded
// identifier fields. It mirrors workflowv1.MaxCommitBodyBytes.
const DefaultCommitBodyBytes = DefaultCommitResultBytes + 64<<10

// ErrTruncated reports adapter output past the byte limit.
var ErrTruncated = errors.New("cutovercontract: adapter output truncated")

// ErrGrantDenied reports a refused producer grant at admission.
var ErrGrantDenied = errors.New("cutovercontract: grant denied")

// Runner executes one collector call: it writes contractJSON to the
// child stdin, reads bounded stdout, strict-decodes the result, and
// redacts secrets from any returned error.
type Runner interface {
	RunCollector(ctx context.Context, contractJSON []byte) (resultJSON []byte, err error)
}

// Lease identifies one held claim. IDs are opaque strings minted by
// the plane; the worker never interprets them. ClaimID is the spec
// claim id (workflowv1 Claim.claim_id); LeaseID carries the granted work
// item id as the opaque epoch key (workflowv1 WorkItem.work_item_id).
// The commit lane carries the fenced epoch with it: FencingToken is the
// spec claim fencing token, ScopeID/SourceRunID/GenerationID are the
// planned identity core matches the commit pin against, SourceSystem is
// the planned source the satellite door requires on the echo, Attempt
// is the granted attempt count and Deadline the hold end the child
// child echo must carry (the door requires attempt >= 1 and a non-zero
// deadline), and the tenant fields re-check the active grant
// before core persists source facts — all copied from the granted work
// item and claim at claim time.
type Lease struct {
	ClaimID            string
	LeaseID            string
	FencingToken       int64
	ScopeID            string
	SourceRunID        string
	GenerationID       string
	SourceSystem       string
	Attempt            int
	Deadline           time.Time
	TenantID           string
	WorkspaceID        string
	SubjectClass       string
	PolicyRevisionHash string
}

// ClaimSelector identifies which collector actor is attempting to claim
// work. It mirrors workflowv1.ClaimSelector field-for-field
// (eshu-workflow@v0.1.0 workflowv1/claim.go:12-21); the JSON tags are
// exactly the spec names, so a marshalled selector is the spec
// ClaimNextRequest selector body.
type ClaimSelector struct {
	CollectorKind       string `json:"collector_kind"`
	CollectorInstanceID string `json:"collector_instance_id"`
	OwnerID             string `json:"owner_id"`
	ClaimID             string `json:"claim_id"`
	RotationSalt        string `json:"rotation_salt,omitempty"`
}

// ClaimRequest asks the plane for the next eligible work item. It mirrors
// workflowv1.ClaimNextRequest field-for-field
// (eshu-workflow@v0.1.0 workflowv1/claim.go:133-136): selector,
// observed_at, lease_ttl_ns. Duration encodes as the nanosecond number
// the _ns suffix promises; time encodes RFC3339.
//
// Producer identity, the collector instance id, and scopes are not
// separate arguments: the spec request carries no top-level producer or
// version slots, so they fold into Selector (the in-tree client takes a
// full workflowv1.ClaimSelector the same way). The worker builds it:
// CollectorKind from scopes, CollectorInstanceID from the dedicated
// instance knob (never the producer id — the plane matches (kind,
// instance) by equality), OwnerID from the producer id, ClaimID as a fresh
// per-call nonce. ProducerVersion has no spec slot — it is validated
// non-empty at Loop entry and client construction (the in-tree client
// stores it without ever serializing it) and is never sent.
type ClaimRequest struct {
	Selector   ClaimSelector `json:"selector"`
	ObservedAt time.Time     `json:"observed_at"`
	LeaseTTL   time.Duration `json:"lease_ttl_ns"`
}

// ClaimMutation carries one fenced lifecycle mutation: the epoch the
// plane must move. It mirrors workflowv1.ClaimMutation field-for-field
// (eshu-workflow@v0.2.0 workflowv1/claim.go:42): work_item_id, claim_id,
// fencing_token, owner_id, observed_at, lease_duration_ns. The spec
// Complete carries no result — facts commit through CommitClaimFacts
// first, so Complete stays a state-only epoch move and the verbatim
// result never rides a mutation body.
type ClaimMutation struct {
	WorkItemID    string        `json:"work_item_id"`
	ClaimID       string        `json:"claim_id"`
	FencingToken  int64         `json:"fencing_token"`
	OwnerID       string        `json:"owner_id"`
	ObservedAt    time.Time     `json:"observed_at"`
	LeaseDuration time.Duration `json:"lease_duration_ns"`
}

// ClaimPlane is the remote claimed-service seam: claim, commit, the
// state-only complete, heartbeat, terminal outcomes, and release.
// CommitClaimFacts persists the verbatim collector result through the
// fenced epoch and reports only when the bytes are durable: the caller
// then completes the claim with Complete, which stays state-only —
// commit first, complete second, one fencing token for both.
// Implementations must be safe for concurrent use.
type ClaimPlane interface {
	ClaimNext(ctx context.Context, req ClaimRequest) (Lease, bool, error)
	Heartbeat(ctx context.Context, lease Lease) error
	CommitClaimFacts(ctx context.Context, lease Lease, resultJSON []byte) error
	Complete(ctx context.Context, lease Lease, resultJSON []byte) error
	Release(ctx context.Context, lease Lease) error
	FailRetryable(ctx context.Context, lease Lease) error
	FailTerminal(ctx context.Context, lease Lease) error
}
