// SPDX-License-Identifier: MIT
// Copyright (c) 2026 eshu-hq

// Package claimplane is the host-side remote claimed-service seam: claim,
// heartbeat, terminal outcomes, and release over HTTP with a per-call
// producer-grant check. It is the stdlib-only reduction of the in-tree
// eshu go/internal/collector/extensionhost/remote/claim client (never
// imported: this module stays dependency-free by design, enforced by
// TestModuleImportsAreStdlibOnly).
//
// Wire mapping (spec-pinned routes, headers, envelope, ack, and code
// vocabulary; seam-minimal bodies for the cutovercontract surface —
// spec_golden_test.go pins the literals and names the body-shape gap):
//
//	every request carries Content-Type: application/json and
//	Workflow-Protocol-Version: 1 (the server rejects other majors
//	before any store call); every route is POST with a JSON body.
//
//	ClaimNext          POST /workflow.v1/ClaimNext
//	                   request  {producer_id, producer_version, scopes, observed_at}
//	                   response {issued, claim_id, lease_id, retry_after_seconds}
//	                   empty queue: 404 with code "not_found" -> found=false, nil error
//	HeartbeatClaim     POST /workflow.v1/HeartbeatClaim
//	CompleteClaim      POST /workflow.v1/CompleteClaim (+ result_json, verbatim JSON)
//	ReleaseClaim       POST /workflow.v1/ReleaseClaim
//	FailClaimRetryable POST /workflow.v1/FailClaimRetryable
//	FailClaimTerminal  POST /workflow.v1/FailClaimTerminal
//	                   mutation request {producer_id, claim_id, lease_id}
//	                   mutation response is exactly {"ok":true} (strict decode,
//	                   unknown fields and trailing data fail closed).
//	Failures carry {"code","message","retry_after_seconds"}; the code
//	vocabulary mirrors the protocol (invalid, not_found, claim_conflict,
//	stale_fence, lease_expired, paced, version_mismatch) plus the
//	client-side response_too_large bound.
//
// Grant mapping: the in-tree GrantReader/Observer pair reduces to the
// GrantSource interface evaluated on every call (ClaimNext and every
// lifecycle mutation), with reason strings mirroring the component grant
// vocabulary (granted, revoked, expired, no_matching_grant,
// scope_mismatch, grants_unreadable). A deny returns a *DenyError
// wrapping ErrGrantDenied before any byte touches the wire. A per-call
// scope outside the declared set fails closed locally with
// scope_mismatch; the plane still refuses out-of-scope claims.
//
// Clock mapping: Config.Clock supplies now for the observed_at stamp; the
// StaticGrants source carries its own Now so tests freeze both with one
// func value. A zero ExpiresAt means no expiry.
//
// Seam conformance: the Client method set matches
// cutovercontract.ClaimPlane exactly (ClaimNext on (producerID, scopes)
// returning (Lease, bool, error); Heartbeat/Complete/Release/
// FailRetryable/FailTerminal on Lease), and Lease is field-identical to
// cutovercontract.Lease so conversion is a field copy. There is no import:
// a same-module import path still counts as non-stdlib under the
// whole-module boundary test, so conformance is structural until the
// worker-loop lane extends that list deliberately.
package claimplane
