// SPDX-License-Identifier: MIT
// Copyright (c) 2026 eshu-hq

// Lifecycle tests: Heartbeat, Complete, Release, FailRetryable, and
// FailTerminal against httptest stubs, the strict {"ok":true} ack, input
// validation, concurrent use, and the error-taxonomy predicates.
package claimplane

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
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
	lease := cutovercontract.Lease{ClaimID: "c1", LeaseID: "l1", FencingToken: 7}
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
	err := c.Heartbeat(context.Background(), cutovercontract.Lease{ClaimID: "c", LeaseID: "l", FencingToken: 7})
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
	if err := c.Heartbeat(context.Background(), cutovercontract.Lease{ClaimID: "c", LeaseID: "l", FencingToken: 7}); err == nil {
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
	if err := c.Heartbeat(context.Background(), cutovercontract.Lease{ClaimID: "c", LeaseID: "l", FencingToken: 7}); err == nil {
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
	err := c.Heartbeat(context.Background(), cutovercontract.Lease{ClaimID: "c", LeaseID: "l", FencingToken: 7})
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
			errs <- c.Heartbeat(context.Background(), cutovercontract.Lease{ClaimID: "c", LeaseID: "l", FencingToken: 7})
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

// TestMutateSendsSpecClaimMutation pins the lifecycle mutation wire
// shape to the spec fenced epoch (eshu-workflow workflowv1.ClaimMutation):
// work_item_id, claim_id, fencing_token, owner_id, observed_at, and
// lease_duration_ns. The live plane strict-decodes mutations, so the
// pre-conformance host-seam body ({producer_id, claim_id, lease_id})
// dies CodeInvalid on every heartbeat, complete, release, and fail —
// leases expire un-heartbeated and no claim ever completes.
func TestMutateSendsSpecClaimMutation(t *testing.T) {
	var mu sync.Mutex
	bodies := map[string][]byte{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies[r.URL.Path] = raw
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	lease := cutovercontract.Lease{ClaimID: "c1", LeaseID: "w1", FencingToken: 7}
	ctx := context.Background()
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

	for _, path := range []string{
		"/workflow.v1/HeartbeatClaim",
		"/workflow.v1/CompleteClaim",
		"/workflow.v1/ReleaseClaim",
		"/workflow.v1/FailClaimRetryable",
		"/workflow.v1/FailClaimTerminal",
	} {
		raw, ok := bodies[path]
		if !ok {
			t.Fatalf("%s: no request captured", path)
		}
		var body map[string]json.RawMessage
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Fatalf("%s: body is not JSON: %v", path, err)
		}
		for _, key := range []string{
			"work_item_id", "claim_id", "fencing_token",
			"owner_id", "observed_at", "lease_duration_ns",
		} {
			if _, ok := body[key]; !ok {
				t.Errorf("%s: body %s misses spec field %q", path, raw, key)
			}
		}
		for _, key := range []string{"producer_id", "lease_id", "result_json"} {
			if _, ok := body[key]; ok {
				t.Errorf("%s: body %s carries host-seam field %q the spec plane rejects", path, raw, key)
			}
		}
		var epoch struct {
			WorkItemID    string `json:"work_item_id"`
			ClaimID       string `json:"claim_id"`
			FencingToken  int64  `json:"fencing_token"`
			OwnerID       string `json:"owner_id"`
			ObservedAt    string `json:"observed_at"`
			LeaseDuration int64  `json:"lease_duration_ns"`
		}
		if err := json.Unmarshal(raw, &epoch); err != nil {
			t.Fatalf("%s: spec decode: %v", path, err)
		}
		if epoch.WorkItemID != "w1" || epoch.ClaimID != "c1" || epoch.FencingToken != 7 {
			t.Errorf("%s: epoch = %+v, want w1/c1/fencing 7", path, epoch)
		}
		if epoch.OwnerID != "test-producer" {
			t.Errorf("%s: owner_id = %q, want the holder", path, epoch.OwnerID)
		}
		if epoch.ObservedAt != "2026-11-06T12:00:00Z" {
			t.Errorf("%s: observed_at = %q, want the frozen client clock", path, epoch.ObservedAt)
		}
		if epoch.LeaseDuration != int64(time.Minute) {
			t.Errorf("%s: lease_duration_ns = %d, want one minute", path, epoch.LeaseDuration)
		}
	}
}

// TestMutateRejectsUnfencedLease fails a mutation whose lease carries no
// fencing token before any byte touches the wire: an unfenced epoch can
// never move the plane, so sending it only burns the call.
func TestMutateRejectsUnfencedLease(t *testing.T) {
	stub := &stubPlane{bodies: map[string]stubResponse{}}
	srv := httptest.NewServer(stub.handler())
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	lease := cutovercontract.Lease{ClaimID: "c1", LeaseID: "w1"}
	if err := c.Heartbeat(context.Background(), lease); err == nil {
		t.Fatal("Heartbeat unfenced lease = nil, want fail-closed error")
	}
	if n := stub.hitCount(); n != 0 {
		t.Fatalf("unfenced mutation touched the wire %d times, want 0", n)
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
