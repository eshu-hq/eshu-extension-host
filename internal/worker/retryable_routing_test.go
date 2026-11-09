// SPDX-License-Identifier: MIT
// Copyright (c) 2026 eshu-hq

// Retryable-routing tests (issue #25): a non-complete satellite result
// must land as a retryable claim outcome — FailRetryable plus Release
// for requeue/backoff through the claim plane — before ever reaching
// the terminal CommitClaimFacts lane. The single-call lane takes only
// complete results; the core gate is never widened to commit anything
// else.
package worker

import (
	"context"
	"testing"
)

// retryableResult is the live shape from issue #25: a satellite result
// whose state asks for another attempt, not a terminal commit.
func retryableResult() []byte {
	return []byte(`{"protocol_version":"collector-sdk/v1alpha1","state":"retryable","claim":{},"generation":{},"statuses":[]}`)
}

// completeResult is a commit-eligible satellite result: only state
// "complete" reaches the single-call lane, so every test that pins the
// commit path must run on this shape, never on a stateless stub.
func completeResult() []byte {
	return []byte(`{"protocol_version":"collector-sdk/v1alpha1","state":"complete","facts":[]}`)
}

func TestLoopRetryableResultFailsRetryableWithoutCommit(t *testing.T) {
	// The issue acceptance: one retryable satellite result lands as
	// retryable claim state, never as a terminal-carrier 400. The
	// commit lane must see zero calls, and so must Complete and
	// FailTerminal — the retry either succeeds later and emits
	// complete, or exhausts to terminal through the normal path.
	runner := &fakeRunner{run: func(context.Context, []byte) ([]byte, error) { return retryableResult(), nil }}
	plane := &fakePlane{answers: []claimAnswer{{lease: fencedLease(), found: true}}}
	if err := runLoop(t, testConfig(), runner, plane, testClaimTimeout); err != nil {
		t.Fatalf("Loop: %v", err)
	}
	ops := opsAfterClaim(plane.ops())
	if len(ops) < 2 || ops[0] != "FailRetryable" || ops[1] != "Release" {
		t.Errorf("ops after claim = %v, want [FailRetryable Release]", ops)
	}
	for _, op := range ops {
		if op == "CommitClaimFacts" || op == "Complete" || op == "FailTerminal" {
			t.Errorf("ops after claim = %v, want no %q for a retryable result", ops, op)
		}
	}
	if got := len(commitResultCalls(plane, "CommitClaimFacts")); got != 0 {
		t.Errorf("CommitClaimFacts calls = %d, want 0 (retryable never reaches the commit lane)", got)
	}
}

func TestLoopNonCompleteStatesNeverReachCommit(t *testing.T) {
	// Every non-complete state — not just retryable — stays out of the
	// single-call lane: only state "complete" commits. A missing or
	// unreadable state is non-complete too, so it retries rather than
	// committing an unclassified carrier.
	cases := map[string][]byte{
		"retryable": []byte(`{"protocol_version":"collector-sdk/v1alpha1","state":"retryable"}`),
		"terminal":  []byte(`{"protocol_version":"collector-sdk/v1alpha1","state":"terminal"}`),
		"unchanged": []byte(`{"protocol_version":"collector-sdk/v1alpha1","state":"unchanged"}`),
		"partial":   []byte(`{"protocol_version":"collector-sdk/v1alpha1","state":"partial"}`),
		"missing":   []byte(`{"protocol_version":"collector-sdk/v1alpha1"}`),
		"empty":     []byte(`{}`),
		"garbage":   []byte(`not json at all`),
	}
	for name, result := range cases {
		t.Run(name, func(t *testing.T) {
			runner := &fakeRunner{run: func(context.Context, []byte) ([]byte, error) { return result, nil }}
			plane := &fakePlane{answers: []claimAnswer{{lease: fencedLease(), found: true}}}
			if err := runLoop(t, testConfig(), runner, plane, testClaimTimeout); err != nil {
				t.Fatalf("Loop: %v", err)
			}
			ops := opsAfterClaim(plane.ops())
			if len(ops) < 2 || ops[0] != "FailRetryable" || ops[1] != "Release" {
				t.Errorf("ops after claim = %v, want [FailRetryable Release]", ops)
			}
			for _, op := range ops {
				if op == "CommitClaimFacts" || op == "Complete" || op == "FailTerminal" {
					t.Errorf("ops after claim = %v, want no %q for state %q", ops, op, name)
				}
			}
		})
	}
}

func TestLoopCompleteResultStillCommits(t *testing.T) {
	// The routing gate is exact: a complete-state result still takes
	// the commit-then-complete path with the verbatim bytes.
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
	commits := commitResultCalls(plane, "CommitClaimFacts")
	if len(commits) != 1 || string(commits[0].result) != string(want) {
		t.Fatalf("commit result = %v, want the exact complete collector bytes", commits)
	}
}
