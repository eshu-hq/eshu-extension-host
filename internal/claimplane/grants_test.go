// SPDX-License-Identifier: MIT
// Copyright (c) 2026 eshu-hq

// Per-call grant-denial tests: revoked and expired grants (the latter via
// the injected clock), unknown holders, unreadable sources, the lifecycle
// recheck, and the fixed-holder identity gate. Denials never touch the
// wire; every denial wraps ErrGrantDenied.
package claimplane

import (
	"context"
	"errors"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// mutableGrants is a concurrency-safe scriptable GrantSource.
type mutableGrants struct {
	mu     sync.Mutex
	ok     bool
	reason string
}

func (m *mutableGrants) Granted(_ string) (bool, string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.ok, m.reason
}

func (m *mutableGrants) set(ok bool, reason string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ok, m.reason = ok, reason
}

func TestClaimNextRevokedGrantDeniesWithoutWire(t *testing.T) {
	stub := &stubPlane{bodies: map[string]stubResponse{
		"/workflow.v1/ClaimNext": {status: 200, body: `{"issued":true,"claim_id":"c","lease_id":"l"}`},
	}}
	srv := httptest.NewServer(stub.handler())
	defer srv.Close()

	cfg := testConfig(srv.URL)
	cfg.Grants = &StaticGrants{Grants: []Grant{{ProducerID: "test-producer", Revoked: true}}}
	c, err := NewClient(cfg)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	_, _, err = c.ClaimNext(context.Background(), "test-producer", "ec2:DescribeInstances")
	if !errors.Is(err, ErrGrantDenied) {
		t.Fatalf("ClaimNext revoked = %v, want wrapped ErrGrantDenied", err)
	}
	var deny *DenyError
	if !errors.As(err, &deny) || deny.Reason != ReasonRevoked {
		t.Fatalf("ClaimNext revoked reason = %v, want %q", deny, ReasonRevoked)
	}
	if n := stub.hitCount(); n != 0 {
		t.Fatalf("revoked grant touched the wire %d times, want 0", n)
	}
}

func TestClaimNextExpiredGrantViaInjectedClock(t *testing.T) {
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	stub := &stubPlane{bodies: map[string]stubResponse{
		"/workflow.v1/ClaimNext": {status: 200, body: `{"issued":true,"claim_id":"c","lease_id":"l"}`},
	}}
	srv := httptest.NewServer(stub.handler())
	defer srv.Close()

	grants := &StaticGrants{
		Grants: []Grant{{ProducerID: "test-producer", ExpiresAt: now.Add(-time.Hour)}},
		Now:    func() time.Time { return now },
	}
	cfg := testConfig(srv.URL)
	cfg.Grants = grants
	cfg.Clock = func() time.Time { return now }
	c, err := NewClient(cfg)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	_, _, err = c.ClaimNext(context.Background(), "test-producer", "ec2:DescribeInstances")
	if !errors.Is(err, ErrGrantDenied) {
		t.Fatalf("ClaimNext expired = %v, want wrapped ErrGrantDenied", err)
	}
	var deny *DenyError
	if !errors.As(err, &deny) || deny.Reason != ReasonExpired {
		t.Fatalf("ClaimNext expired reason = %v, want %q", deny, ReasonExpired)
	}
	if n := stub.hitCount(); n != 0 {
		t.Fatalf("expired grant touched the wire %d times, want 0", n)
	}

	// Control: the same grant set before expiry proceeds to the wire.
	grants.Now = func() time.Time { return now.Add(-2 * time.Hour) }
	lease, found, err := c.ClaimNext(context.Background(), "test-producer", "ec2:DescribeInstances")
	if err != nil || !found || lease.ClaimID != "c" || lease.LeaseID != "l" {
		t.Fatalf("ClaimNext unexpired = %+v,%v,%v, want lease c/l", lease, found, err)
	}
}

func TestGrantRecheckDeniesLifecycleWithoutWire(t *testing.T) {
	stub := &stubPlane{bodies: map[string]stubResponse{
		"/workflow.v1/HeartbeatClaim": {status: 200, body: `{"ok":true}`},
	}}
	srv := httptest.NewServer(stub.handler())
	defer srv.Close()

	mutable := &mutableGrants{ok: true}
	cfg := testConfig(srv.URL)
	cfg.Grants = mutable
	c, err := NewClient(cfg)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := c.Heartbeat(context.Background(), Lease{ClaimID: "c", LeaseID: "l"}); err != nil {
		t.Fatalf("Heartbeat allowed = %v, want nil", err)
	}
	mutable.set(false, ReasonRevoked)
	before := stub.hitCount()
	err = c.Heartbeat(context.Background(), Lease{ClaimID: "c", LeaseID: "l"})
	if !errors.Is(err, ErrGrantDenied) {
		t.Fatalf("Heartbeat revoked = %v, want wrapped ErrGrantDenied", err)
	}
	if n := stub.hitCount(); n != before {
		t.Fatalf("revoked heartbeat touched the wire (hits %d -> %d)", before, n)
	}
}

func TestProducerMismatchDenied(t *testing.T) {
	stub := &stubPlane{bodies: map[string]stubResponse{}}
	srv := httptest.NewServer(stub.handler())
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	_, _, err := c.ClaimNext(context.Background(), "other-producer", "ec2:DescribeInstances")
	if err == nil {
		t.Fatal("ClaimNext mismatched producer = nil, want deny")
	}
	if n := stub.hitCount(); n != 0 {
		t.Fatalf("mismatched producer touched the wire %d times, want 0", n)
	}
}

func TestNoMatchingGrant(t *testing.T) {
	stub := &stubPlane{bodies: map[string]stubResponse{}}
	srv := httptest.NewServer(stub.handler())
	defer srv.Close()

	cfg := testConfig(srv.URL)
	cfg.Grants = &StaticGrants{Grants: []Grant{{ProducerID: "someone-else"}}}
	c, err := NewClient(cfg)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	_, _, err = c.ClaimNext(context.Background(), "test-producer", "ec2:DescribeInstances")
	var deny *DenyError
	if !errors.As(err, &deny) || deny.Reason != ReasonNoMatchingGrant {
		t.Fatalf("ClaimNext unmatched = %v, want %q", err, ReasonNoMatchingGrant)
	}
	if n := stub.hitCount(); n != 0 {
		t.Fatalf("unmatched grant touched the wire %d times, want 0", n)
	}
}

func TestClaimNextUndeclaredScopeDeniedWithoutWire(t *testing.T) {
	stub := &stubPlane{bodies: map[string]stubResponse{
		"/workflow.v1/ClaimNext": {status: 200, body: `{"issued":true,"claim_id":"c","lease_id":"l"}`},
	}}
	srv := httptest.NewServer(stub.handler())
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	_, _, err := c.ClaimNext(context.Background(), "test-producer", "s3:NotDeclared")
	var deny *DenyError
	if !errors.As(err, &deny) || deny.Reason != ReasonScopeMismatch {
		t.Fatalf("ClaimNext undeclared scope = %v, want reason %q", deny, ReasonScopeMismatch)
	}
	if !errors.Is(err, ErrGrantDenied) {
		t.Fatalf("ClaimNext undeclared scope = %v, want wrapped ErrGrantDenied", err)
	}
	if n := stub.hitCount(); n != 0 {
		t.Fatalf("undeclared scope touched the wire %d times, want 0", n)
	}
}

func TestEmptyReasonDegradesToUnreadable(t *testing.T) {
	stub := &stubPlane{bodies: map[string]stubResponse{}}
	srv := httptest.NewServer(stub.handler())
	defer srv.Close()

	cfg := testConfig(srv.URL)
	cfg.Grants = &mutableGrants{}
	c, err := NewClient(cfg)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	_, _, err = c.ClaimNext(context.Background(), "test-producer", "ec2:DescribeInstances")
	var deny *DenyError
	if !errors.As(err, &deny) || deny.Reason != ReasonGrantsUnreadable {
		t.Fatalf("ClaimNext empty reason = %v, want %q", err, ReasonGrantsUnreadable)
	}
}
