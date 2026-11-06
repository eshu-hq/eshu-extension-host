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
// the plane; the worker never interprets them.
type Lease struct {
	ClaimID string
	LeaseID string
}

// ClaimPlane is the remote claimed-service seam: claim, heartbeat,
// terminal outcomes, and release. Implementations must be safe for
// concurrent use.
type ClaimPlane interface {
	ClaimNext(ctx context.Context, producerID string, scopes string) (Lease, bool, error)
	Heartbeat(ctx context.Context, lease Lease) error
	Complete(ctx context.Context, lease Lease, resultJSON []byte) error
	Release(ctx context.Context, lease Lease) error
	FailRetryable(ctx context.Context, lease Lease) error
	FailTerminal(ctx context.Context, lease Lease) error
}
