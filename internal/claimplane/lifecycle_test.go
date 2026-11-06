// SPDX-License-Identifier: MIT
// Copyright (c) 2026 eshu-hq

// Lifecycle tests: Heartbeat, Complete, Release, FailRetryable, and
// FailTerminal against httptest stubs, the strict {"ok":true} ack, input
// validation, concurrent use, and the error-taxonomy predicates.
package claimplane

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/eshu-hq/eshu-extension-host/internal/cutovercontract"
)

func TestLifecycleRoundTrip(t *testing.T) {
	ack := `{"ok":true}`
	stub := &stubPlane{bodies: map[string]stubResponse{
		"/workflow.v1/HeartbeatClaim":     {status: 200, body: ack},
		"/workflow.v1/CompleteClaim":      {status: 200, body: ack},
		"/workflow.v1/ReleaseClaim":       {status: 200, body: ack},
		"/workflow.v1/FailClaimRetryable": {status: 200, body: ack},
		"/workflow.v1/FailClaimTerminal":  {status: 200, body: ack},
	}}
	srv := httptest.NewServer(stub.handler())
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	ctx := context.Background()
	lease := cutovercontract.Lease{ClaimID: "c1", LeaseID: "l1"}
	if err := c.Heartbeat(ctx, lease); err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}
	if err := c.Complete(ctx, lease, []byte(`{"kind":"ec2"}`)); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if err := c.Release(ctx, lease); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if err := c.FailRetryable(ctx, lease); err != nil {
		t.Fatalf("FailRetryable: %v", err)
	}
	if err := c.FailTerminal(ctx, lease); err != nil {
		t.Fatalf("FailTerminal: %v", err)
	}
	stub.mu.Lock()
	defer stub.mu.Unlock()
	if stub.hits != 5 {
		t.Fatalf("hits = %d, want 5 lifecycle POSTs", stub.hits)
	}
}

func TestLifecycleAckNotOK(t *testing.T) {
	stub := &stubPlane{bodies: map[string]stubResponse{
		"/workflow.v1/HeartbeatClaim": {status: 200, body: `{"ok":false}`},
	}}
	srv := httptest.NewServer(stub.handler())
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	err := c.Heartbeat(context.Background(), cutovercontract.Lease{ClaimID: "c", LeaseID: "l"})
	var call *CallError
	if !errors.As(err, &call) || call.Code != CodeInvalid {
		t.Fatalf("Heartbeat ok=false = %v, want code %q", err, CodeInvalid)
	}
}

func TestStrictDecodeRejectsTrailingData(t *testing.T) {
	stub := &stubPlane{bodies: map[string]stubResponse{
		"/workflow.v1/HeartbeatClaim": {status: 200, body: "{\"ok\":true} trailing"},
	}}
	srv := httptest.NewServer(stub.handler())
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	if err := c.Heartbeat(context.Background(), cutovercontract.Lease{ClaimID: "c", LeaseID: "l"}); err == nil {
		t.Fatal("Heartbeat trailing data = nil, want invalid error")
	}
}

func TestAckUnknownFieldRejected(t *testing.T) {
	stub := &stubPlane{bodies: map[string]stubResponse{
		"/workflow.v1/HeartbeatClaim": {status: 200, body: `{"ok":true,"x":1}`},
	}}
	srv := httptest.NewServer(stub.handler())
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	if err := c.Heartbeat(context.Background(), cutovercontract.Lease{ClaimID: "c", LeaseID: "l"}); err == nil {
		t.Fatal("Heartbeat unknown field = nil, want invalid error")
	}
}

func TestMutateValidation(t *testing.T) {
	stub := &stubPlane{bodies: map[string]stubResponse{}}
	srv := httptest.NewServer(stub.handler())
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := c.Heartbeat(context.Background(), cutovercontract.Lease{}); err == nil {
		t.Fatal("Heartbeat empty lease = nil, want error")
	}
	if err := c.Heartbeat(cancelled, cutovercontract.Lease{ClaimID: "c", LeaseID: "l"}); err == nil {
		t.Fatal("Heartbeat cancelled ctx = nil, want error")
	}
	if n := stub.hitCount(); n != 0 {
		t.Fatalf("invalid mutation touched the wire %d times, want 0", n)
	}
}

func TestCompleteRejectsEmptyResult(t *testing.T) {
	stub := &stubPlane{bodies: map[string]stubResponse{}}
	srv := httptest.NewServer(stub.handler())
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	if err := c.Complete(context.Background(), cutovercontract.Lease{ClaimID: "c", LeaseID: "l"}, nil); err == nil {
		t.Fatal("Complete empty result = nil, want validation error")
	}
	if n := stub.hitCount(); n != 0 {
		t.Fatalf("empty result touched the wire %d times, want 0", n)
	}
}

func TestCompleteRejectsNonJSONResult(t *testing.T) {
	stub := &stubPlane{bodies: map[string]stubResponse{}}
	srv := httptest.NewServer(stub.handler())
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	if err := c.Complete(context.Background(), cutovercontract.Lease{ClaimID: "c", LeaseID: "l"}, []byte("{oops")); err == nil {
		t.Fatal("Complete non-JSON result = nil, want marshal error")
	}
	if n := stub.hitCount(); n != 0 {
		t.Fatalf("non-JSON result touched the wire %d times, want 0", n)
	}
}

func TestRetryAfterMapping(t *testing.T) {
	stub := &stubPlane{bodies: map[string]stubResponse{
		"/workflow.v1/HeartbeatClaim": {status: 500, body: `{"code":"invalid","message":"boom","retry_after_seconds":9}`},
	}}
	srv := httptest.NewServer(stub.handler())
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	err := c.Heartbeat(context.Background(), cutovercontract.Lease{ClaimID: "c", LeaseID: "l"})
	var call *CallError
	if !errors.As(err, &call) || call.RetryAfter != 9*time.Second {
		t.Fatalf("Heartbeat 500 = %v, want 9s retry", err)
	}
}

func TestConcurrentUse(t *testing.T) {
	stub := &stubPlane{bodies: map[string]stubResponse{
		"/workflow.v1/HeartbeatClaim": {status: 200, body: `{"ok":true}`},
	}}
	srv := httptest.NewServer(stub.handler())
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	errs := make(chan error, 16)
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- c.Heartbeat(context.Background(), cutovercontract.Lease{ClaimID: "c", LeaseID: "l"})
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent Heartbeat = %v, want nil on every goroutine", err)
		}
	}
	if n := stub.hitCount(); n != 16 {
		t.Fatalf("concurrent hits = %d, want 16", stub.hits)
	}
}

func TestErrorPredicates(t *testing.T) {
	stale := &CallError{Op: "HeartbeatClaim", Code: CodeStaleFence}
	if !stale.Stale() || stale.Paced() || stale.NotFound() {
		t.Fatalf("stale predicates wrong: %v", stale)
	}
	paced := &CallError{Op: "ClaimNext", Code: CodePaced}
	if !paced.Paced() || paced.Stale() {
		t.Fatalf("paced predicates wrong: %v", paced)
	}
	missing := &CallError{Op: "ClaimNext", Code: CodeNotFound}
	if !missing.NotFound() {
		t.Fatalf("notfound predicate wrong: %v", missing)
	}
	deny := &DenyError{Reason: ReasonRevoked, ProducerID: "p"}
	if !deny.Revoked() || deny.Expired() {
		t.Fatalf("deny predicates wrong: %v", deny)
	}
	if msg := deny.Error(); !strings.Contains(msg, `"p"`) || !strings.Contains(msg, ReasonRevoked) {
		t.Fatalf("DenyError.Error = %q, want producer and reason", msg)
	}
	ver := &CallError{Op: "ClaimNext", Code: CodeVersionMismatch}
	if !ver.VersionMismatch() || ver.Stale() || ver.Paced() || ver.NotFound() {
		t.Fatalf("version predicates wrong: %v", ver)
	}
	if !errors.Is(deny, ErrGrantDenied) {
		t.Fatalf("DenyError does not wrap ErrGrantDenied: %v", deny)
	}
	if !strings.Contains((&CallError{Op: "o", Code: "c", Message: "m"}).Error(), "o c: m") {
		t.Fatal("CallError.Error shape wrong")
	}
}
