// SPDX-License-Identifier: MIT
// Copyright (c) 2026 eshu-hq

// Package claimplane is the host-side remote claimed-service seam: claim,
// heartbeat, terminal outcomes, and release over HTTP with a per-call
// producer-grant check. It is the stdlib-only reduction of the in-tree
// eshu go/internal/collector/extensionhost/remote/claim client (never
// imported: this module stays dependency-free by design, enforced by
// TestModuleImportsAreStdlibOnly).
//
// Wire mapping (spec-pinned routes, headers, envelope, ack, code
// vocabulary, and ClaimNext request/response BODY field names —
// spec_golden_test.go pins the literals and names the remaining gap):
//
//	every request carries Content-Type: application/json and
//	Workflow-Protocol-Version: 1 (the server rejects other majors
//	before any store call); every route is POST with a JSON body.
//
//	ClaimNext          POST /workflow.v1/ClaimNext
//	                   request  {selector: {collector_kind,
//	                             collector_instance_id, owner_id, claim_id},
//	                             observed_at, lease_ttl_ns} — the spec
//	                             ClaimNextRequest (workflowv1/claim.go:133)
//	                   response {issued, grant: {work_item: {work_item_id,
//	                             scope_id, source_run_id, generation_id,
//	                             source_system, attempt_count,
//	                             lease_expires_at, tenant_*}, claim: {claim_id,
//	                             fencing_token, lease_expires_at}},
//	                             deferral: {signal,
//	                             retry_after_ns}} — the spec ClaimNextResponse
//	                             read-subset (workflowv1/claim.go:154,
//	                             pacing.go:203); a full server grant with
//	                   extra fields decodes, the pre-conformance flat shape
//	                   fails closed on the missing grant half; a grant
//	                   without a fencing token, planned ids, or a usable
//	                   attempt count (attempt >= 1, the door requires the
//	                   echo there) fails closed — it could never commit.
//	                   The deadline rides the granted lease expiry where
//	                   the plane sends one, else the stamped claim time
//	                   plus the requested TTL.
//	                   empty queue: 404 with code "not_found" -> found=false, nil error
//	                   unissued 200 always carries the deferral; a deferral-less
//	                   unissued answer is invalid, never paced
//	HeartbeatClaim     POST /workflow.v1/HeartbeatClaim
//	CommitClaimFacts   POST /workflow.v1/CommitClaimFacts (eshu-workflow@v0.2.0
//	                   workflowv1/commit.go): the fenced mutation flat on the
//	                   wire beside idempotency_key (the claim epoch id),
//	                   scope_id/source_run_id/generation_id, and the verbatim
//	                   result_json. Result and body caps mirror the spec
//	                   (4 MiB + 4,259,840 B). The {"ok":true} ack means
//	                   facts committed; only it proceeds to Complete.
//	CompleteClaim      POST /workflow.v1/CompleteClaim (state-only: the
//	                   verbatim result already committed through
//	                   CommitClaimFacts, so no result rides this body)
//	ReleaseClaim       POST /workflow.v1/ReleaseClaim
//	FailClaimRetryable POST /workflow.v1/FailClaimRetryable
//	FailClaimTerminal  POST /workflow.v1/FailClaimTerminal
//	                   mutation request {work_item_id, claim_id,
//	                   fencing_token, owner_id, observed_at,
//	                   lease_duration_ns} — the spec ClaimMutation
//	                   (eshu-workflow@v0.2.0 workflowv1/claim.go:42)
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
// Seam conformance: the Client implements cutovercontract.ClaimPlane
// exactly (a compile assertion in client.go, not a structural echo):
// ClaimNext takes the spec-shaped ClaimRequest and returns the seam
// Lease; Heartbeat/CommitClaimFacts/Complete/Release/FailRetryable/
// FailTerminal take the seam Lease, and every mutation sends the spec
// fenced epoch: LeaseID holds the granted work item id and the fencing
// token plus planned identity ride the Lease from the grant. The
// claimplane package imports cutovercontract directly; the
// whole-module boundary test (cmd/extension-host) carves this module
// itself out of the stdlib-only guard, so no allowlist change was
// needed.
package claimplane
