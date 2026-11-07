// SPDX-License-Identifier: MIT
// Copyright (c) 2026 eshu-hq

// Lifecycle mutations: heartbeat renews the held epoch; complete,
// release, and the two fail outcomes move it. Complete stays
// state-only: facts commit through the commit lane first.
package claimplane

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/eshu-hq/eshu-extension-host/internal/cutovercontract"
)

// Heartbeat renews the lease on a held claim epoch.
func (c *Client) Heartbeat(ctx context.Context, lease cutovercontract.Lease) error {
	return c.mutate(ctx, "HeartbeatClaim", pathHeartbeatClaim, lease, nil)
}

// Complete finishes a held claim after its generation committed. The result
// must be non-empty verbatim JSON; anything else fails closed locally.
func (c *Client) Complete(ctx context.Context, lease cutovercontract.Lease, resultJSON []byte) error {
	if len(resultJSON) == 0 {
		return errors.New("claimplane CompleteClaim: result is required")
	}
	return c.mutate(ctx, "CompleteClaim", pathCompleteClaim, lease, resultJSON)
}

// Release returns a held claim unworked.
func (c *Client) Release(ctx context.Context, lease cutovercontract.Lease) error {
	return c.mutate(ctx, "ReleaseClaim", pathReleaseClaim, lease, nil)
}

// FailRetryable records a retryable claim failure.
func (c *Client) FailRetryable(ctx context.Context, lease cutovercontract.Lease) error {
	return c.mutate(ctx, "FailClaimRetryable", pathFailClaimRetryable, lease, nil)
}

// FailTerminal records a terminal claim failure.
func (c *Client) FailTerminal(ctx context.Context, lease cutovercontract.Lease) error {
	return c.mutate(ctx, "FailClaimTerminal", pathFailClaimTerminal, lease, nil)
}

// mutate sends one fenced lifecycle mutation. A 2xx counts as success only
// when the body is exactly the {"ok":true} acknowledgement.
func (c *Client) mutate(ctx context.Context, op, path string, lease cutovercontract.Lease, result []byte) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("claimplane %s: %w", op, err)
	}
	if strings.TrimSpace(lease.ClaimID) == "" || strings.TrimSpace(lease.LeaseID) == "" {
		return fmt.Errorf("claimplane %s: claim and lease ids are required", op)
	}
	if err := c.checkGrant(ctx); err != nil {
		return err
	}
	req := cutovercontract.ClaimOutcome{ProducerID: c.prodID, ClaimID: lease.ClaimID, LeaseID: lease.LeaseID}
	if result != nil {
		req.Result = json.RawMessage(result)
	}
	status, envelope, body, err := c.roundTrip(ctx, op, path, req)
	if err != nil {
		return err
	}
	if status < 200 || status > 299 {
		return c.callError(op, status, envelope)
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
