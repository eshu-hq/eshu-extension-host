// SPDX-License-Identifier: MIT
// Copyright (c) 2026 eshu-hq

// Commit-then-complete tests: a successful run commits the verbatim
// collector result first and only the {"ok":true} ack lets the held
// claim proceed to the state-only Complete under the same fenced epoch.
// Every commit failure fails per the existing retry classifier without
// ever completing.
package worker

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/eshu-hq/eshu-extension-host/internal/claimplane"
	"github.com/eshu-hq/eshu-extension-host/internal/cutovercontract"
)

// testClaimTimeout bounds one served claim the way the loop tests do: the
// loop absorbs the empty queue as backoff, so the timeout only ends the
// run after the outcome lands.
const testClaimTimeout = 2 * time.Second

// fencedLease is a held claim whose grant carried the epoch fencing
// token and the planned identity the commit matches the pin against.
func fencedLease() cutovercontract.Lease {
	return cutovercontract.Lease{
		ClaimID:            "c1",
		LeaseID:            "l1",
		FencingToken:       1193046,
		ScopeID:            "scope-1",
		SourceRunID:        "sr-1",
		GenerationID:       "gen-1",
		SourceSystem:       "src-1",
		TenantID:           "t-1",
		WorkspaceID:        "w-1",
		SubjectClass:       "s-1",
		PolicyRevisionHash: "p-1",
	}
}

func commitResultCalls(plane *fakePlane, op string) []planeCall {
	plane.mu.Lock()
	defer plane.mu.Unlock()
	var out []planeCall
	for _, c := range plane.calls {
		if c.op == op {
			out = append(out, c)
		}
	}
	return out
}

func TestLoopCommitsBeforeCompleting(t *testing.T) {
	want := []byte(`{"protocol_version":"collector-sdk/v1alpha1","state":"complete","facts":[{"id":"f-1"}]}`)
	runner := &fakeRunner{run: func(context.Context, []byte) ([]byte, error) { return want, nil }}
	plane := &fakePlane{answers: []claimAnswer{{lease: fencedLease(), found: true}}}
	if err := runLoop(t, testConfig(), runner, plane, testClaimTimeout); err != nil {
		t.Fatalf("Loop: %v", err)
	}
	ops := opsAfterClaim(plane.ops())
	if len(ops) < 2 || ops[0] != "CommitClaimFacts" || ops[1] != "Complete" {
		t.Fatalf("ops after claim = %v, want [CommitClaimFacts Complete ...]", ops)
	}
	for _, op := range ops {
		if op == "FailRetryable" || op == "FailTerminal" || op == "Release" {
			t.Fatalf("ops after claim = %v, want no failure outcome on the acked path", ops)
		}
	}
	commits := commitResultCalls(plane, "CommitClaimFacts")
	completes := commitResultCalls(plane, "Complete")
	if len(commits) != 1 || len(completes) != 1 {
		t.Fatalf("commit/complete calls = %d/%d, want 1/1", len(commits), len(completes))
	}
	if commits[0].lease != completes[0].lease {
		t.Fatalf("commit lease = %+v, complete lease = %+v, want one fenced epoch",
			commits[0].lease, completes[0].lease)
	}
	if string(commits[0].result) != string(want) {
		t.Fatalf("commit result = %s, want the exact collector bytes", commits[0].result)
	}
	if string(completes[0].result) != string(want) {
		t.Fatalf("complete result = %s, want the collector bytes carried through", completes[0].result)
	}
}

func TestLoopAckGatesComplete(t *testing.T) {
	// A failed commit never completes: the existing retry classifier
	// absorbs the failure (retryable + release here) with no Complete.
	runner := &fakeRunner{run: func(context.Context, []byte) ([]byte, error) { return completeResult(), nil }}
	plane := &fakePlane{
		answers:   []claimAnswer{{lease: fencedLease(), found: true}},
		commitErr: errors.New("plane transport blew up"),
	}
	if err := runLoop(t, testConfig(), runner, plane, testClaimTimeout); err != nil {
		t.Fatalf("Loop: %v", err)
	}
	ops := opsAfterClaim(plane.ops())
	if len(ops) < 3 || ops[0] != "CommitClaimFacts" || ops[1] != "FailRetryable" || ops[2] != "Release" {
		t.Errorf("ops after claim = %v, want [CommitClaimFacts FailRetryable Release]", ops)
	}
	for _, op := range ops {
		if op == "Complete" || op == "FailTerminal" {
			t.Errorf("ops after claim = %v, want no %q on a failed commit", ops, op)
		}
	}
}

func TestLoopNoCompleteOnCommitConflict(t *testing.T) {
	// A 409-class commit refusal (stale fence, expired lease, claim
	// conflict) fails per the existing classifier without completing:
	// the epoch moved, so Complete on it would be wrong.
	runner := &fakeRunner{run: func(context.Context, []byte) ([]byte, error) { return completeResult(), nil }}
	plane := &fakePlane{
		answers: []claimAnswer{{lease: fencedLease(), found: true}},
		commitErr: &claimplane.CallError{
			Op: "CommitClaimFacts", Code: claimplane.CodeStaleFence, Message: "epoch moved",
		},
	}
	if err := runLoop(t, testConfig(), runner, plane, testClaimTimeout); err != nil {
		t.Fatalf("Loop: %v", err)
	}
	ops := opsAfterClaim(plane.ops())
	if len(ops) == 0 || ops[0] != "CommitClaimFacts" {
		t.Fatalf("ops after claim = %v, want CommitClaimFacts first", ops)
	}
	for _, op := range ops {
		if op == "Complete" {
			t.Fatalf("ops after claim = %v, want no Complete on a 409 commit", ops)
		}
	}
	if len(ops) < 3 || ops[1] != "FailRetryable" || ops[2] != "Release" {
		t.Errorf("ops after claim = %v, want [CommitClaimFacts FailRetryable Release]", ops)
	}
}

func TestLoopTerminalOnTerminalCarrierRefusal(t *testing.T) {
	// The worker routes non-complete results before any commit call
	// (issue #25), so this pins the defense-in-depth classifier for a
	// complete-state carrier the plane still refuses — the seam
	// sentinel chained with the CommitClaimFacts/invalid CallError
	// (the claimplane test pins the client-side shape; this test pins
	// the outcome). One attempt must reach a bounded terminal state:
	// FailTerminal with no Complete, no retryable fail, and no release
	// back into the loop.
	refusal := &claimplane.CallError{
		Op: "CommitClaimFacts", Code: claimplane.CodeInvalid,
		Message: `invalid carrier: commit claim facts: echoed generation does not match the planned pin`,
	}
	runner := &fakeRunner{run: func(context.Context, []byte) ([]byte, error) { return completeResult(), nil }}
	plane := &fakePlane{
		answers:   []claimAnswer{{lease: fencedLease(), found: true}},
		commitErr: fmt.Errorf("%w: %w", refusal, cutovercontract.ErrTerminalCarrier),
	}
	if err := runLoop(t, testConfig(), runner, plane, testClaimTimeout); err != nil {
		t.Fatalf("Loop: %v", err)
	}
	ops := opsAfterClaim(plane.ops())
	if len(ops) < 2 || ops[0] != "CommitClaimFacts" || ops[1] != "FailTerminal" {
		t.Errorf("ops after claim = %v, want [CommitClaimFacts FailTerminal]", ops)
	}
	for _, op := range ops {
		if op == "Complete" || op == "FailRetryable" || op == "Release" {
			t.Errorf("ops after claim = %v, want no %q after a refused carrier", ops, op)
		}
	}
}

func TestLoopRetryableOnCommitServerError(t *testing.T) {
	// A 5xx the plane did not judge invalid stays retryable + released:
	// the wire failed, not the carrier.
	runner := &fakeRunner{run: func(context.Context, []byte) ([]byte, error) { return completeResult(), nil }}
	plane := &fakePlane{
		answers: []claimAnswer{{lease: fencedLease(), found: true}},
		commitErr: &claimplane.CallError{
			Op: "CommitClaimFacts", Code: claimplane.CodeUnexpectedStatus, Message: "boom",
		},
	}
	if err := runLoop(t, testConfig(), runner, plane, testClaimTimeout); err != nil {
		t.Fatalf("Loop: %v", err)
	}
	ops := opsAfterClaim(plane.ops())
	if len(ops) < 3 || ops[0] != "CommitClaimFacts" || ops[1] != "FailRetryable" || ops[2] != "Release" {
		t.Errorf("ops after claim = %v, want [CommitClaimFacts FailRetryable Release]", ops)
	}
	for _, op := range ops {
		if op == "Complete" || op == "FailTerminal" {
			t.Errorf("ops after claim = %v, want no %q on a server failure", ops, op)
		}
	}
}

func TestLoopTerminalOnCommitGrantDeny(t *testing.T) {
	// A commit refused on the producer grant fails terminal, like every
	// other grant denial on the outcome path — and never completes.
	runner := &fakeRunner{run: func(context.Context, []byte) ([]byte, error) { return completeResult(), nil }}
	plane := &fakePlane{
		answers:   []claimAnswer{{lease: fencedLease(), found: true}},
		commitErr: fmt.Errorf("grant check: %w", cutovercontract.ErrGrantDenied),
	}
	if err := runLoop(t, testConfig(), runner, plane, testClaimTimeout); err != nil {
		t.Fatalf("Loop: %v", err)
	}
	ops := opsAfterClaim(plane.ops())
	if len(ops) < 2 || ops[0] != "CommitClaimFacts" || ops[1] != "FailTerminal" {
		t.Errorf("ops after claim = %v, want [CommitClaimFacts FailTerminal]", ops)
	}
	for _, op := range ops {
		if op == "Complete" || op == "FailRetryable" || op == "Release" {
			t.Errorf("ops after claim = %v, want no %q after a denied commit", ops, op)
		}
	}
}
