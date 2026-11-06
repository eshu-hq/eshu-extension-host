// SPDX-License-Identifier: MIT
// Copyright (c) 2026 eshu-hq

// Spec-derived wire golden: every literal the client sends or decodes
// that the real plane also defines is pinned here against the pinned
// spec, eshu-workflow@v0.1.0 in the module cache, and the in-tree
// reduction source
// (eshu go/internal/collector/extensionhost/remote/claim). A change to
// any pinned literal fails here before it can break plane interop.
//
// Deliberately NOT pinned: request/response body field names. The spec
// ClaimNextRequest carries {selector, observed_at, lease_ttl_ns}, the
// spec ClaimMutation carries {work_item_id, claim_id, fencing_token,
// owner_id, observed_at, lease_duration_ns, ...}, and the spec
// ClaimNextResponse carries {issued, grant, deferral}; this seam speaks
// the cutovercontract.ClaimPlane minimal surface ((producerID, scopes)
// in, opaque Lease out) and cannot populate spec bodies without
// inventing identity mappings. See the P1 reply on host PR #7.
package claimplane

import (
	"context"
	"net/http/httptest"
	"testing"
)

// Spec pins below name the module-cache file and line that fixes them.
const (
	// workflowv1/version.go:13 (VersionHeader).
	specVersionHeader = "Workflow-Protocol-Version"
	// workflowv1/version.go:11 (ProtocolVersion).
	specVersionValue = "1"
	// In-tree roundTrip sets it on every request; the v0.1.0 server
	// decodes every body as JSON (server/server.go decode).
	specContentType = "application/json"
)

// specRoutes is the v0.1.0 RouteTable subset this seam speaks
// (workflowv1/http.go RouteTable, all POST with a JSON body).
var specRoutes = map[string]string{
	"ClaimNext":          "/workflow.v1/ClaimNext",
	"HeartbeatClaim":     "/workflow.v1/HeartbeatClaim",
	"CompleteClaim":      "/workflow.v1/CompleteClaim",
	"ReleaseClaim":       "/workflow.v1/ReleaseClaim",
	"FailClaimRetryable": "/workflow.v1/FailClaimRetryable",
	"FailClaimTerminal":  "/workflow.v1/FailClaimTerminal",
}

func TestSpecGoldenRoutes(t *testing.T) {
	got := map[string]string{
		"ClaimNext":          pathClaimNext,
		"HeartbeatClaim":     pathHeartbeatClaim,
		"CompleteClaim":      pathCompleteClaim,
		"ReleaseClaim":       pathReleaseClaim,
		"FailClaimRetryable": pathFailClaimRetryable,
		"FailClaimTerminal":  pathFailClaimTerminal,
	}
	for op, want := range specRoutes {
		if got[op] != want {
			t.Errorf("route %s = %q, want spec literal %q", op, got[op], want)
		}
	}
}

func TestSpecGoldenRequestHeaders(t *testing.T) {
	stub := &stubPlane{bodies: map[string]stubResponse{
		"/workflow.v1/ClaimNext":      {status: 200, body: `{"issued":true,"claim_id":"c","lease_id":"l"}`},
		"/workflow.v1/HeartbeatClaim": {status: 200, body: "{\"ok\":true}\n"},
	}}
	srv := httptest.NewServer(stub.handler())
	defer srv.Close()

	// The v0.1.0 server enforces the version header before any store
	// call (server/server.go begin → CheckVersion): a missing or
	// unknown major fails every call with version_mismatch.
	if protocolVersionHeader != specVersionHeader || protocolVersion != specVersionValue {
		t.Fatalf("version literal = %q:%q, want spec %q:%q",
			protocolVersionHeader, protocolVersion, specVersionHeader, specVersionValue)
	}
	c := newTestClient(t, srv.URL)
	if _, _, err := c.ClaimNext(context.Background(), "test-producer", "ec2:DescribeInstances"); err != nil {
		t.Fatalf("ClaimNext: %v", err)
	}
	if err := c.Heartbeat(context.Background(), Lease{ClaimID: "c", LeaseID: "l"}); err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}
	stub.mu.Lock()
	defer stub.mu.Unlock()
	if len(stub.headers) != 2 {
		t.Fatalf("recorded %d request headers, want 2", len(stub.headers))
	}
	for i, h := range stub.headers {
		if got := h.Get("Content-Type"); got != specContentType {
			t.Errorf("request %d Content-Type = %q, want %q", i, got, specContentType)
		}
		if got := h.Get(specVersionHeader); got != specVersionValue {
			t.Errorf("request %d %s = %q, want %q", i, specVersionHeader, got, specVersionValue)
		}
	}
	for i, m := range stub.methods {
		if m != "POST" {
			t.Errorf("request %d method = %q, want POST (every spec route is POST)", i, m)
		}
	}
}

func TestSpecGoldenEnvelopeNames(t *testing.T) {
	// ErrorEnvelope JSON names (workflowv1/http.go ErrorEnvelope):
	// code, message, retry_after_seconds.
	stub := &stubPlane{bodies: map[string]stubResponse{
		"/workflow.v1/ClaimNext": {status: 429, body: `{"code":"paced","message":"slow down","retry_after_seconds":4}`},
	}}
	srv := httptest.NewServer(stub.handler())
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	_, _, err := c.ClaimNext(context.Background(), "test-producer", "ec2:DescribeInstances")
	call, ok := err.(*CallError)
	if !ok || call.Code != CodePaced || call.Message != "slow down" || call.RetryAfter.Seconds() != 4 {
		t.Fatalf("429 envelope decoded = %v, want code/message/retry_after_seconds mapped", err)
	}
}

func TestSpecGoldenAckShape(t *testing.T) {
	// The v0.1.0 server answers every lifecycle mutation with ack{OK:true}
	// encoded by json.Encoder, so the wire bytes are {"ok":true} plus a
	// newline (server/server.go ack). Strict decode must accept it.
	stub := &stubPlane{bodies: map[string]stubResponse{
		"/workflow.v1/HeartbeatClaim": {status: 200, body: "{\"ok\":true}\n"},
	}}
	srv := httptest.NewServer(stub.handler())
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	if err := c.Heartbeat(context.Background(), Lease{ClaimID: "c", LeaseID: "l"}); err != nil {
		t.Fatalf("Heartbeat server ack bytes = %v, want success", err)
	}
}

func TestSpecGoldenEmptyQueue404(t *testing.T) {
	// An unissued response without a deferral is a 404 with the
	// not_found code, never an empty 200 (server/handlers_claim.go
	// serveClaimNext idle branch; protocol code workflowv1/errors.go).
	stub := &stubPlane{bodies: map[string]stubResponse{
		"/workflow.v1/ClaimNext": {status: 404, body: `{"code":"not_found","message":"no eligible work"}`},
	}}
	srv := httptest.NewServer(stub.handler())
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	_, found, err := c.ClaimNext(context.Background(), "test-producer", "ec2:DescribeInstances")
	if err != nil || found {
		t.Fatalf("404 not_found = (found=%v, err=%v), want (false, nil)", found, err)
	}
}

func TestSpecGoldenCodeVocabulary(t *testing.T) {
	// Protocol codes (workflowv1/errors.go); response_too_large is the
	// client-side bound the server never sends (in-tree client.go).
	want := map[string]string{
		"invalid":            CodeInvalid,
		"not_found":          CodeNotFound,
		"claim_conflict":     CodeClaimConflict,
		"stale_fence":        CodeStaleFence,
		"lease_expired":      CodeLeaseExpired,
		"paced":              CodePaced,
		"version_mismatch":   CodeVersionMismatch,
		"response_too_large": CodeResponseTooLarge,
	}
	for spec, got := range want {
		if spec != got {
			t.Errorf("code literal = %q, want spec %q", got, spec)
		}
	}
}
