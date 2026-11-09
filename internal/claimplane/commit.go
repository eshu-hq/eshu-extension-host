// SPDX-License-Identifier: MIT
// Copyright (c) 2026 eshu-hq

// Commit lane: CommitClaimFacts persists one held claim's verbatim
// collector result through the claimed-commit fence. It posts the spec
// CommitClaimFactsRequest (eshu-workflow@v0.2.0 workflowv1/commit.go)
// and only the {"ok":true} ack counts as committed.
package claimplane

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/eshu-hq/eshu-extension-host/internal/cutovercontract"
)

// commitEnvelope is the spec CommitClaimFactsRequest (eshu-workflow@v0.2.0
// workflowv1/commit.go): the fenced mutation flat on the wire beside the
// idempotency key, the planned identity, and the verbatim result. The
// result rides as raw JSON so neither this client nor the satellite proxy
// interprets it — only core strict-decodes it.
type commitEnvelope struct {
	WorkItemID         string          `json:"work_item_id"`
	ClaimID            string          `json:"claim_id"`
	FencingToken       int64           `json:"fencing_token"`
	OwnerID            string          `json:"owner_id"`
	ObservedAt         time.Time       `json:"observed_at"`
	LeaseDuration      time.Duration   `json:"lease_duration_ns"`
	TenantID           string          `json:"tenant_id,omitempty"`
	WorkspaceID        string          `json:"workspace_id,omitempty"`
	SubjectClass       string          `json:"subject_class,omitempty"`
	PolicyRevisionHash string          `json:"policy_revision_hash,omitempty"`
	ScopeID            string          `json:"scope_id"`
	SourceRunID        string          `json:"source_run_id"`
	GenerationID       string          `json:"generation_id"`
	IdempotencyKey     string          `json:"idempotency_key"`
	ResultJSON         json.RawMessage `json:"result_json"`
}

// CommitClaimFacts persists one held claim's verbatim collector result
// through the claimed-commit fence and returns only when the bytes are
// durable: the {"ok":true} ack means facts committed, never bytes
// received. The idempotency key is the claim epoch id, so a retried
// commit after an ambiguous ack lands exactly once. A stale fence, an
// expired lease, an oversized carrier, or a carrier missing its planned
// IDs fails before any byte is committed; the caller then completes the
// claim with Complete, which stays state-only.
func (c *Client) CommitClaimFacts(ctx context.Context, lease cutovercontract.Lease, resultJSON []byte) error {
	const op = "CommitClaimFacts"
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("claimplane %s: %w", op, err)
	}
	if strings.TrimSpace(lease.ClaimID) == "" || strings.TrimSpace(lease.LeaseID) == "" {
		return fmt.Errorf("claimplane %s: claim and lease ids are required", op)
	}
	if lease.FencingToken <= 0 {
		return fmt.Errorf("claimplane %s: fencing token is required", op)
	}
	if strings.TrimSpace(lease.ScopeID) == "" || strings.TrimSpace(lease.SourceRunID) == "" ||
		strings.TrimSpace(lease.GenerationID) == "" {
		return fmt.Errorf("claimplane %s: planned scope, source run, and generation ids are required", op)
	}
	if len(resultJSON) == 0 {
		return fmt.Errorf("claimplane %s: result is required", op)
	}
	if len(resultJSON) > cutovercontract.DefaultCommitResultBytes {
		return c.callErrorText(op, CodeInvalid, fmt.Sprintf("result is %d bytes, over the %d-byte cap",
			len(resultJSON), cutovercontract.DefaultCommitResultBytes))
	}
	if err := c.checkGrant(ctx); err != nil {
		return err
	}
	raw, err := json.Marshal(commitEnvelope{
		WorkItemID:         lease.LeaseID,
		ClaimID:            lease.ClaimID,
		FencingToken:       lease.FencingToken,
		OwnerID:            c.prodID,
		ObservedAt:         c.clock().UTC(),
		LeaseDuration:      c.leaseDuration,
		TenantID:           lease.TenantID,
		WorkspaceID:        lease.WorkspaceID,
		SubjectClass:       lease.SubjectClass,
		PolicyRevisionHash: lease.PolicyRevisionHash,
		ScopeID:            lease.ScopeID,
		SourceRunID:        lease.SourceRunID,
		GenerationID:       lease.GenerationID,
		IdempotencyKey:     lease.ClaimID,
		ResultJSON:         resultJSON,
	})
	if err != nil {
		return c.callErrorText(op, CodeInvalid, err.Error())
	}
	if len(raw) > cutovercontract.DefaultCommitBodyBytes {
		return c.callErrorText(op, CodeInvalid, fmt.Sprintf("body is %d bytes, over the %d-byte cap",
			len(raw), cutovercontract.DefaultCommitBodyBytes))
	}
	// Pass the already-marshaled envelope through: RawMessage embeds the
	// result verbatim, and the length above is the exact wire length.
	status, envelope, body, err := c.roundTrip(ctx, op, pathCommitClaimFacts, json.RawMessage(raw))
	if err != nil {
		return err
	}
	if status < 200 || status > 299 {
		err := c.callError(op, status, envelope)
		if status < 500 && strings.TrimSpace(envelope.Code) == CodeInvalid {
			// The plane judged the carrier, not the wire: a 4xx
			// code invalid refusal (e.g. the core identity gate
			// rejecting a non-complete result state) is
			// deterministic for the carrier at hand, so the
			// worker must fail terminal instead of retrying
			// forever. A 5xx carrying code invalid stays
			// retryable — the wire failed, not the carrier.
			// Both errors stay reachable: the CallError keeps
			// the op/code taxonomy and the sentinel marks the
			// terminal class.
			return fmt.Errorf("claimplane %s: %w: %w", op, err, cutovercontract.ErrTerminalCarrier)
		}
		return err
	}
	var ack ackResponse
	if err := decodeStrict(body, &ack); err != nil {
		return c.callErrorText(op, CodeInvalid, err.Error())
	}
	if !ack.OK {
		return c.callErrorText(op, CodeInvalid, "acknowledgement did not report ok")
	}
	return nil
}
