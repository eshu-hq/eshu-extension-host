// SPDX-License-Identifier: MIT
// Copyright (c) 2026 eshu-hq

// Spec-derived wire golden: every literal the client sends or decodes
// that the real plane also defines is pinned here against the pinned
// spec, eshu-workflow@v0.1.0 in the module cache, and the in-tree
// reduction source
// (eshu go/internal/collector/extensionhost/remote/claim). A change to
// any pinned literal fails here before it can break plane interop.
//
// Pinned: ClaimNext request/response BODY field names. The request body
// is the spec ClaimNextRequest (workflowv1/claim.go:133-136) and the
// response decodes the spec ClaimNextResponse read-subset
// (workflowv1/claim.go:154-157, pacing.go:203-210). Deliberately NOT
// pinned: the mutation bodies, which still speak the host-seam
// ClaimOutcome — the spec ClaimMutation needs work_item_id plus the
// fencing token, which arrive with the commit lane (see doc.go).
package claimplane

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"sort"
	"testing"

	"github.com/eshu-hq/eshu-extension-host/internal/cutovercontract"
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
		"/workflow.v1/ClaimNext":      {status: 200, body: specGrantBody("l", "c")},
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
	if _, _, err := c.ClaimNext(context.Background(), testClaimRequest()); err != nil {
		t.Fatalf("ClaimNext: %v", err)
	}
	if err := c.Heartbeat(context.Background(), cutovercontract.Lease{ClaimID: "c", LeaseID: "l"}); err != nil {
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
	_, _, err := c.ClaimNext(context.Background(), testClaimRequest())
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
	if err := c.Heartbeat(context.Background(), cutovercontract.Lease{ClaimID: "c", LeaseID: "l"}); err != nil {
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
	_, found, err := c.ClaimNext(context.Background(), testClaimRequest())
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

func wantKeys(t *testing.T, what, raw string, want ...string) map[string]json.RawMessage {
	t.Helper()
	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &obj); err != nil {
		t.Fatalf("%s is not a JSON object: %v", what, err)
	}
	var got []string
	for k := range obj {
		got = append(got, k)
	}
	sort.Strings(got)
	sort.Strings(want)
	if len(got) != len(want) {
		t.Fatalf("%s keys = %v, want %v", what, got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("%s keys = %v, want %v", what, got, want)
		}
	}
	return obj
}

func TestSpecGoldenClaimNextRequestBody(t *testing.T) {
	// The ClaimNext request body is the spec ClaimNextRequest
	// (workflowv1/claim.go:133-136): exactly {selector, observed_at,
	// lease_ttl_ns}, with the selector exactly {collector_kind,
	// collector_instance_id, owner_id, claim_id} (claim.go:12-21).
	// Producer identity arrives folded into the selector: the spec has
	// no top-level producer or version slots, so those names must NOT
	// appear on the wire.
	stub := &stubPlane{bodies: map[string]stubResponse{
		"/workflow.v1/ClaimNext": {status: 200, body: specGrantBody("l", "c")},
	}}
	srv := httptest.NewServer(stub.handler())
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	if _, _, err := c.ClaimNext(context.Background(), testClaimRequest()); err != nil {
		t.Fatalf("ClaimNext: %v", err)
	}
	stub.mu.Lock()
	defer stub.mu.Unlock()
	if len(stub.rawClaim) != 1 {
		t.Fatalf("recorded %d ClaimNext bodies, want 1", len(stub.rawClaim))
	}
	obj := wantKeys(t, "ClaimNext body", stub.rawClaim[0], "lease_ttl_ns", "observed_at", "selector")
	sel := wantKeys(t, "ClaimNext selector", string(obj["selector"]),
		"claim_id", "collector_instance_id", "collector_kind", "owner_id")
	if string(sel["collector_kind"]) != `"ec2:DescribeInstances"` {
		t.Errorf("collector_kind = %s, want the configured scope kind", sel["collector_kind"])
	}
	if string(sel["collector_instance_id"]) != `"test-producer"` || string(sel["owner_id"]) != `"test-producer"` {
		t.Errorf("selector identity = %s/%s, want the configured holder on both",
			sel["collector_instance_id"], sel["owner_id"])
	}
	var ttlNs int64
	if err := json.Unmarshal(obj["lease_ttl_ns"], &ttlNs); err != nil || ttlNs <= 0 {
		t.Errorf("lease_ttl_ns = %s, want a positive nanosecond count", obj["lease_ttl_ns"])
	}
}

func TestSpecGoldenClaimNextResponseBody(t *testing.T) {
	// The ClaimNext answer decodes the spec ClaimNextResponse
	// (workflowv1/claim.go:154-157): {issued, grant: {work_item:
	// {work_item_id}, claim: {claim_id}}} maps to the seam lease, and a
	// full server grant carrying fields outside the read-subset still
	// decodes — the pre-conformance flat shape fails closed.
	full := `{"issued":true,"degraded_reason":"","grant":{"work_item":{"work_item_id":"w","run_id":"r",` +
		`"collector_kind":"ec2:DescribeInstances","collector_instance_id":"i","status":"claimed"},` +
		`"claim":{"claim_id":"c","work_item_id":"w","fencing_token":3,"owner_id":"test-producer",` +
		`"status":"active"}},"deferral":{"signal":"","retry_after_ns":0}}`
	stub := &stubPlane{bodies: map[string]stubResponse{
		"/workflow.v1/ClaimNext": {status: 200, body: full},
	}}
	srv := httptest.NewServer(stub.handler())
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	lease, found, err := c.ClaimNext(context.Background(), testClaimRequest())
	if err != nil || !found {
		t.Fatalf("full spec grant = (%v, %v), want found lease", found, err)
	}
	if lease.ClaimID != "c" || lease.LeaseID != "w" {
		t.Fatalf("lease = %+v, want claim c on work w", lease)
	}

	flat := &stubPlane{bodies: map[string]stubResponse{
		"/workflow.v1/ClaimNext": {status: 200, body: `{"issued":true,"claim_id":"c","lease_id":"l"}`},
	}}
	srv2 := httptest.NewServer(flat.handler())
	defer srv2.Close()

	c2 := newTestClient(t, srv2.URL)
	if _, _, err := c2.ClaimNext(context.Background(), testClaimRequest()); err == nil {
		t.Fatal("pre-conformance flat body decoded, want invalid for the missing grant half")
	}
}

func TestSpecGoldenClaimOutcomeTags(t *testing.T) {
	// The seam ClaimOutcome marshals claim_id exactly as the spec
	// ClaimMutation names it (workflowv1/claim.go:44); producer_id,
	// lease_id, and result_json are host-seam names pinned here as the
	// seam contract (the spec Complete carries no result).
	out := cutovercontract.ClaimOutcome{
		ProducerID: "test-producer",
		ClaimID:    "c",
		LeaseID:    "l",
		Result:     json.RawMessage(`{"kind":"ec2"}`),
	}
	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal ClaimOutcome: %v", err)
	}
	obj := wantKeys(t, "ClaimOutcome body", string(raw), "claim_id", "lease_id", "producer_id", "result_json")
	var result map[string]string
	if err := json.Unmarshal(obj["result_json"], &result); err != nil || result["kind"] != "ec2" {
		t.Fatalf("result_json = %s, want the verbatim collector result", obj["result_json"])
	}
}
