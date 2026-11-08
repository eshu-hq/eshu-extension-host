// SPDX-License-Identifier: MIT
// Copyright (c) 2026 eshu-hq

// Attempt-and-deadline forwarding tests: the grant's attempt count and
// lease expiry reach the lease, because the commit door requires the
// child claim to echo attempt >= 1 and a non-zero deadline, and the
// child artifact echoes the host claim verbatim. A grant without a
// usable attempt fails closed at claim time instead of stranding every
// held claim at the door; a grant without an expiry derives the hold
// the worker requested, mirroring the in-tree lease-expiry deadline.
package claimplane

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const (
	testAttemptCount = 41
	testLeaseExpiry  = "2026-11-08T21:00:00Z"
)

// TestClaimNextCarriesAttemptAndDeadline proves the grant's attempt
// count and work-item lease expiry reach the lease: the door checks
// both on the echoed child claim, and the in-tree host sends the
// item's attempt count with its lease expiry as the deadline.
func TestClaimNextCarriesAttemptAndDeadline(t *testing.T) {
	stub := &stubPlane{bodies: map[string]stubResponse{
		"/workflow.v1/ClaimNext": {status: 200, body: `{"issued":true,` +
			`"grant":{"work_item":{"work_item_id":"l1","scope_id":"scope-1",` +
			`"source_run_id":"sr-1","generation_id":"gen-1","attempt_count":41,` +
			`"lease_expires_at":"2026-11-08T21:00:00Z"},` +
			`"claim":{"claim_id":"c1","fencing_token":7}}}`},
	}}
	srv := httptest.NewServer(stub.handler())
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	lease, found, err := c.ClaimNext(context.Background(), testClaimRequest())
	if err != nil || !found {
		t.Fatalf("ClaimNext = %v,%v, want found lease", lease, err)
	}
	if lease.Attempt != testAttemptCount {
		t.Errorf("lease.Attempt = %d, want %d from the grant", lease.Attempt, testAttemptCount)
	}
	wantDeadline, _ := time.Parse(time.RFC3339, testLeaseExpiry)
	if !lease.Deadline.Equal(wantDeadline) {
		t.Errorf("lease.Deadline = %v, want the granted %v", lease.Deadline, wantDeadline)
	}
}

// TestClaimNextRejectsNonPositiveAttempt proves a grant without a
// usable attempt count fails closed at claim time: the door turns
// every such commit away, so holding the claim only burns child runs.
func TestClaimNextRejectsNonPositiveAttempt(t *testing.T) {
	stub := &stubPlane{bodies: map[string]stubResponse{
		"/workflow.v1/ClaimNext": {status: 200, body: `{"issued":true,` +
			`"grant":{"work_item":{"work_item_id":"l1","scope_id":"scope-1",` +
			`"source_run_id":"sr-1","generation_id":"gen-1","attempt_count":0},` +
			`"claim":{"claim_id":"c1","fencing_token":7}}}`},
	}}
	srv := httptest.NewServer(stub.handler())
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	_, _, err := c.ClaimNext(context.Background(), testClaimRequest())
	if err == nil || !strings.Contains(err.Error(), "attempt") {
		t.Fatalf("ClaimNext zero attempt = %v, want fail-closed naming attempt", err)
	}
}

// TestClaimNextPrefersWorkItemExpiry proves the deadline prefers the
// work-item lease expiry over the claim-half stamp: the in-tree host
// deadlines its child claims from the work item, so the order is load
// bearing, not cosmetic.
func TestClaimNextPrefersWorkItemExpiry(t *testing.T) {
	stub := &stubPlane{bodies: map[string]stubResponse{
		"/workflow.v1/ClaimNext": {status: 200, body: `{"issued":true,` +
			`"grant":{"work_item":{"work_item_id":"l1","scope_id":"scope-1",` +
			`"source_run_id":"sr-1","generation_id":"gen-1","attempt_count":6,` +
			`"lease_expires_at":"2026-11-08T21:00:00Z"},` +
			`"claim":{"claim_id":"c1","fencing_token":7,` +
			`"lease_expires_at":"2026-11-08T22:00:00Z"}}}`},
	}}
	srv := httptest.NewServer(stub.handler())
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	lease, found, err := c.ClaimNext(context.Background(), testClaimRequest())
	if err != nil || !found {
		t.Fatalf("ClaimNext = %v,%v, want found lease", lease, err)
	}
	want, _ := time.Parse(time.RFC3339, testLeaseExpiry)
	if !lease.Deadline.Equal(want) {
		t.Errorf("lease.Deadline = %v, want the work-item expiry %v, not the claim-half stamp", lease.Deadline, want)
	}
}

// TestClaimNextDerivesDeadlineWhenGrantOmitsExpiry proves an older
// plane grant without lease-expiry fields still yields a usable
// deadline: the worker requested the hold, so the hold end is the
// claim time plus the requested TTL.
func TestClaimNextDerivesDeadlineWhenGrantOmitsExpiry(t *testing.T) {
	stub := &stubPlane{bodies: map[string]stubResponse{
		"/workflow.v1/ClaimNext": {status: 200, body: specGrantBody("l1", "c1")},
	}}
	srv := httptest.NewServer(stub.handler())
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	req := testClaimRequest()
	before := time.Now().UTC()
	lease, found, err := c.ClaimNext(context.Background(), req)
	if err != nil || !found {
		t.Fatalf("ClaimNext = %v,%v, want found lease", lease, err)
	}
	if lease.Attempt < 1 {
		t.Errorf("lease.Attempt = %d, want >= 1 even without the granted count", lease.Attempt)
	}
	lower := before.Add(req.LeaseTTL).Add(-time.Minute)
	upper := time.Now().UTC().Add(req.LeaseTTL).Add(time.Minute)
	if lease.Deadline.Before(lower) || lease.Deadline.After(upper) {
		t.Errorf("lease.Deadline = %v, want about claim time plus the requested %v", lease.Deadline, req.LeaseTTL)
	}
}
