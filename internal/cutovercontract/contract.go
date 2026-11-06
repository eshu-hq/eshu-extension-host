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
	"encoding/json"
	"errors"
	"time"
)

// DefaultStdoutLimit bounds one adapter stdout read. It matches the
// in-tree 4 MiB default; truncation fails closed (see ErrTruncated).
const DefaultStdoutLimit int64 = 4 << 20

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
// item id as the opaque epoch key (workflowv1 WorkItem.work_item_id) until
// the mutation path migrates to fenced ClaimMutation bodies, which need
// the work item id alongside the claim id.
type Lease struct {
	ClaimID string
	LeaseID string
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

// ClaimOutcome carries one fenced lifecycle mutation: the epoch the plane
// must move plus, for Complete, the verbatim collector result. claim_id
// matches the spec ClaimMutation field
// (eshu-workflow@v0.1.0 workflowv1/claim.go:44); producer_id, lease_id,
// and result_json are host-seam names with no spec slot — the spec
// Complete carries no result (facts commit through the separate committer
// path), so the result stays a host-seam field the server ignores on its
// non-strict decode pending the commit lane.
type ClaimOutcome struct {
	ProducerID string          `json:"producer_id"`
	ClaimID    string          `json:"claim_id"`
	LeaseID    string          `json:"lease_id"`
	Result     json.RawMessage `json:"result_json,omitempty"`
}

// ClaimPlane is the remote claimed-service seam: claim, heartbeat,
// terminal outcomes, and release. Implementations must be safe for
// concurrent use.
type ClaimPlane interface {
	ClaimNext(ctx context.Context, req ClaimRequest) (Lease, bool, error)
	Heartbeat(ctx context.Context, lease Lease) error
	Complete(ctx context.Context, lease Lease, resultJSON []byte) error
	Release(ctx context.Context, lease Lease) error
	FailRetryable(ctx context.Context, lease Lease) error
	FailTerminal(ctx context.Context, lease Lease) error
}
