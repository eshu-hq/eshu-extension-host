// SPDX-License-Identifier: MIT
// Copyright (c) 2026 eshu-hq

// ClaimNext contract tests: fail-closed construction, input validation,
// bounded bodies, non-2xx mapping, pacing, and the happy-path round trip
// against httptest stubs only (no live network).
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

// stubPlane records hits and serves scripted bodies per path.
type stubPlane struct {
	mu       sync.Mutex
	hits     int
	paths    []string
	methods  []string
	headers  []http.Header
	bodies   map[string]stubResponse
	observed []string
	rawClaim []string
}

type stubResponse struct {
	status int
	body   string
}

func (s *stubPlane) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.hits++
		s.paths = append(s.paths, r.URL.Path)
		s.methods = append(s.methods, r.Method)
		s.headers = append(s.headers, r.Header.Clone())
		resp, ok := s.bodies[r.URL.Path]
		s.mu.Unlock()
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"code":"not_found","message":"no claim waiting"}`))
			return
		}
		if r.URL.Path == "/workflow.v1/ClaimNext" {
			raw, _ := io.ReadAll(r.Body)
			s.mu.Lock()
			s.rawClaim = append(s.rawClaim, string(raw))
			s.mu.Unlock()
			var req cutovercontract.ClaimRequest
			if err := json.Unmarshal(raw, &req); err == nil {
				s.mu.Lock()
				s.observed = append(s.observed, req.ObservedAt.UTC().Format(time.RFC3339))
				s.mu.Unlock()
			}
		}
		w.WriteHeader(resp.status)
		_, _ = w.Write([]byte(resp.body))
	}
}

func (s *stubPlane) hitCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hits
}

func testConfig(url string) Config {
	return Config{
		BaseURL:             url,
		ProducerID:          "test-producer",
		ProducerVersion:     "1.0.0",
		CollectorInstanceID: "test-instance",
		Scopes:              []string{"ec2:DescribeInstances"},
		Grants:              &StaticGrants{Grants: []Grant{{ProducerID: "test-producer"}}},
		Clock:               func() time.Time { return time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC) },
	}
}

func newTestClient(t *testing.T, url string) *Client {
	t.Helper()
	c, err := NewClient(testConfig(url))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return c
}

// testClaimRequest builds the spec-shaped request the worker sends: the
// producer identity, the dedicated collector instance id, and scopes
// folded into the selector, ObservedAt left zero for the client clock to
// stamp.
func testClaimRequest() cutovercontract.ClaimRequest {
	return cutovercontract.ClaimRequest{
		Selector: cutovercontract.ClaimSelector{
			CollectorKind:       "ec2:DescribeInstances",
			CollectorInstanceID: "test-instance",
			OwnerID:             "test-producer",
			ClaimID:             "claim-1",
		},
		LeaseTTL: time.Minute,
	}
}

// specGrantBody renders the spec ClaimNextResponse grant half: issued with
// a grant carrying the work item and claim ids, the epoch fencing token,
// and the planned identity the commit matches the pin against. Extra
// server fields are fine — the client decodes a forward-compatible
// read-subset.
func specGrantBody(workID, claimID string) string {
	return `{"issued":true,"grant":{"work_item":{"work_item_id":"` + workID +
		`","scope_id":"scope-1","source_run_id":"sr-1","generation_id":"gen-1"},` +
		`"claim":{"claim_id":"` + claimID + `","fencing_token":7}}}`
}

func TestNewClientRejectsBadBaseURL(t *testing.T) {
	for _, url := range []string{"", "://bad", "ftp://example.com", "gopher://example.com/x"} {
		cfg := testConfig("http://example.com")
		cfg.BaseURL = url
		if _, err := NewClient(cfg); err == nil {
			t.Errorf("NewClient(%q) = nil error, want fail-closed", url)
		}
	}
}

func TestNewClientRejectsEmptyIdentity(t *testing.T) {
	base := testConfig("http://example.com")
	cases := map[string]func(*Config){
		"empty producer": func(c *Config) { c.ProducerID = "  " },
		"empty version":  func(c *Config) { c.ProducerVersion = "" },
		"empty instance": func(c *Config) { c.CollectorInstanceID = "" },
		"empty scopes":   func(c *Config) { c.Scopes = nil },
		"nil grants":     func(c *Config) { c.Grants = nil },
	}
	for name, mutate := range cases {
		cfg := base
		mutate(&cfg)
		if _, err := NewClient(cfg); err == nil {
			t.Errorf("NewClient (%s) = nil error, want fail-closed", name)
		}
	}
}

func TestNewClientDefaultsMaxBytes(t *testing.T) {
	c := newTestClient(t, "http://example.com")
	if c.max != DefaultMaxResponseBytes {
		t.Errorf("maxBytes = %d, want default %d", c.max, DefaultMaxResponseBytes)
	}
}

func TestNewClientNilClock(t *testing.T) {
	stub := &stubPlane{bodies: map[string]stubResponse{
		"/workflow.v1/ClaimNext": {status: 200, body: specGrantBody("l", "c")},
	}}
	srv := httptest.NewServer(stub.handler())
	defer srv.Close()

	cfg := testConfig(srv.URL)
	cfg.Clock = nil
	c, err := NewClient(cfg)
	if err != nil {
		t.Fatalf("NewClient nil clock: %v", err)
	}
	if _, _, err := c.ClaimNext(context.Background(), testClaimRequest()); err != nil {
		t.Fatalf("ClaimNext nil clock = %v, want lease", err)
	}
}

func TestClaimNextValidation(t *testing.T) {
	stub := &stubPlane{bodies: map[string]stubResponse{}}
	srv := httptest.NewServer(stub.handler())
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	ctx := context.Background()
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	mutate := func(f func(*cutovercontract.ClaimRequest)) cutovercontract.ClaimRequest {
		req := testClaimRequest()
		f(&req)
		return req
	}
	inputs := []struct {
		name string
		req  cutovercontract.ClaimRequest
		ctx  context.Context
	}{
		{name: "empty kind", req: mutate(func(r *cutovercontract.ClaimRequest) {
			r.Selector.CollectorKind = "  "
		}), ctx: ctx},
		{name: "mismatched owner", req: mutate(func(r *cutovercontract.ClaimRequest) {
			r.Selector.OwnerID = "other-producer"
		}), ctx: ctx},
		{name: "mismatched instance", req: mutate(func(r *cutovercontract.ClaimRequest) {
			r.Selector.CollectorInstanceID = "other-producer"
		}), ctx: ctx},
		{name: "empty claim id", req: mutate(func(r *cutovercontract.ClaimRequest) {
			r.Selector.ClaimID = ""
		}), ctx: ctx},
		{name: "undeclared kind", req: mutate(func(r *cutovercontract.ClaimRequest) {
			r.Selector.CollectorKind = "s3:NotDeclared"
		}), ctx: ctx},
		{name: "non-positive TTL", req: mutate(func(r *cutovercontract.ClaimRequest) {
			r.LeaseTTL = 0
		}), ctx: ctx},
		{name: "cancelled ctx", req: testClaimRequest(), ctx: cancelled},
	}
	for _, in := range inputs {
		if _, _, err := c.ClaimNext(in.ctx, in.req); err == nil {
			t.Fatalf("ClaimNext %s = nil, want error", in.name)
		}
	}
	if n := stub.hitCount(); n != 0 {
		t.Fatalf("invalid input touched the wire %d times, want 0", n)
	}
}

func TestClaimNextTruncatedBody(t *testing.T) {
	stub := &stubPlane{bodies: map[string]stubResponse{
		"/workflow.v1/ClaimNext": {status: 200, body: specGrantBody("l", "c")},
	}}
	srv := httptest.NewServer(stub.handler())
	defer srv.Close()

	cfg := testConfig(srv.URL)
	cfg.MaxResponseBytes = 16
	c, err := NewClient(cfg)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	_, _, err = c.ClaimNext(context.Background(), testClaimRequest())
	var call *CallError
	if !errors.As(err, &call) || call.Code != CodeResponseTooLarge {
		t.Fatalf("ClaimNext oversize = %v, want code %q", err, CodeResponseTooLarge)
	}
}

func TestClaimNextNon2xx(t *testing.T) {
	stub := &stubPlane{bodies: map[string]stubResponse{
		"/workflow.v1/ClaimNext": {status: 500, body: `{"code":"invalid","message":"boom"}`},
	}}
	srv := httptest.NewServer(stub.handler())
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	_, _, err := c.ClaimNext(context.Background(), testClaimRequest())
	var call *CallError
	if !errors.As(err, &call) || call.Code != "invalid" || call.Op != "ClaimNext" {
		t.Fatalf("ClaimNext 500 = %v, want op/ClaimNext code/invalid", err)
	}
}

func TestClaimNextEmptyQueue(t *testing.T) {
	srv := httptest.NewServer((&stubPlane{}).handler())
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	_, found, err := c.ClaimNext(context.Background(), testClaimRequest())
	if err != nil || found {
		t.Fatalf("ClaimNext empty = found %v err %v, want false/nil", found, err)
	}
}

func TestClaimNextRoundTrip(t *testing.T) {
	stub := &stubPlane{bodies: map[string]stubResponse{
		"/workflow.v1/ClaimNext": {status: 200, body: specGrantBody("l1", "c1")},
	}}
	srv := httptest.NewServer(stub.handler())
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	lease, found, err := c.ClaimNext(context.Background(), testClaimRequest())
	if err != nil || !found {
		t.Fatalf("ClaimNext = %v,%v, want found lease", lease, err)
	}
	if lease.ClaimID != "c1" || lease.LeaseID != "l1" {
		t.Fatalf("lease = %+v, want c1/l1", lease)
	}
	if lease.FencingToken != 7 || lease.ScopeID != "scope-1" ||
		lease.SourceRunID != "sr-1" || lease.GenerationID != "gen-1" {
		t.Fatalf("lease = %+v, want the fenced epoch and planned ids from the grant", lease)
	}
	stub.mu.Lock()
	defer stub.mu.Unlock()
	if len(stub.paths) != 1 || stub.paths[0] != "/workflow.v1/ClaimNext" {
		t.Fatalf("paths = %v, want one ClaimNext POST", stub.paths)
	}
	if len(stub.observed) != 1 || stub.observed[0] != "2026-11-06T12:00:00Z" {
		t.Fatalf("observed_at = %v, want injected clock value", stub.observed)
	}
}

func TestClaimNextIssuedFalseIsPaced(t *testing.T) {
	// The spec carries the pacing verdict inline: an unissued 200 always
	// carries a deferral with retry_after_ns (workflowv1/pacing.go:203).
	stub := &stubPlane{bodies: map[string]stubResponse{
		"/workflow.v1/ClaimNext": {status: 200, body: `{"issued":false,"deferral":{"signal":"depth","retry_after_ns":7000000000}}`},
	}}
	srv := httptest.NewServer(stub.handler())
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	_, found, err := c.ClaimNext(context.Background(), testClaimRequest())
	var call *CallError
	if !errors.As(err, &call) || !call.Paced() || found {
		t.Fatalf("ClaimNext issued=false = %v,%v, want paced", found, err)
	}
	if call.RetryAfter != 7*time.Second {
		t.Fatalf("RetryAfter = %v, want 7s", call.RetryAfter)
	}
}

func TestClaimNextIssuedFalseWithoutDeferralIsInvalid(t *testing.T) {
	// An unissued 200 without a deferral violates the spec response
	// contract (ClaimNextResponse.Validate: the unissued path requires a
	// valid deferral), so it fails closed as invalid, never as paced.
	stub := &stubPlane{bodies: map[string]stubResponse{
		"/workflow.v1/ClaimNext": {status: 200, body: `{"issued":false}`},
	}}
	srv := httptest.NewServer(stub.handler())
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	_, _, err := c.ClaimNext(context.Background(), testClaimRequest())
	var call *CallError
	if !errors.As(err, &call) || call.Code != CodeInvalid {
		t.Fatalf("ClaimNext deferral-less unissued = %v, want invalid", err)
	}
}

func TestClaimNext429IsPaced(t *testing.T) {
	stub := &stubPlane{bodies: map[string]stubResponse{
		"/workflow.v1/ClaimNext": {status: 429, body: `{"code":"paced","message":"slow","retry_after_seconds":3}`},
	}}
	srv := httptest.NewServer(stub.handler())
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	_, _, err := c.ClaimNext(context.Background(), testClaimRequest())
	var call *CallError
	if !errors.As(err, &call) || !call.Paced() || call.RetryAfter != 3*time.Second {
		t.Fatalf("ClaimNext 429 = %v, want paced with 3s", err)
	}
	if call.Message != "slow" {
		t.Fatalf("ClaimNext 429 message = %q, want the server message to survive the bound", call.Message)
	}
}

func TestClaimNextIssuedWithoutIDs(t *testing.T) {
	stub := &stubPlane{bodies: map[string]stubResponse{
		"/workflow.v1/ClaimNext": {status: 200, body: `{"issued":true}`},
	}}
	srv := httptest.NewServer(stub.handler())
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	_, _, err := c.ClaimNext(context.Background(), testClaimRequest())
	var call *CallError
	if !errors.As(err, &call) || call.Code != CodeInvalid {
		t.Fatalf("ClaimNext id-less issue = %v, want invalid", err)
	}
}

func TestClaimNextRejectsGrantWithoutFencing(t *testing.T) {
	// An epoch without a fencing token can never commit, so it fails
	// closed at claim time instead of stranding the hold.
	stub := &stubPlane{bodies: map[string]stubResponse{
		"/workflow.v1/ClaimNext": {status: 200, body: `{"issued":true,` +
			`"grant":{"work_item":{"work_item_id":"w","scope_id":"s","source_run_id":"r","generation_id":"g"},` +
			`"claim":{"claim_id":"c"}}}`},
	}}
	srv := httptest.NewServer(stub.handler())
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	_, _, err := c.ClaimNext(context.Background(), testClaimRequest())
	var call *CallError
	if !errors.As(err, &call) || call.Code != CodeInvalid {
		t.Fatalf("ClaimNext fenceless issue = %v, want invalid", err)
	}
}

func TestClaimNextRejectsGrantWithoutPlannedIDs(t *testing.T) {
	// Core matches the commit pin against the planned identity: a grant
	// missing it can never commit, so it fails closed at claim time.
	stub := &stubPlane{bodies: map[string]stubResponse{
		"/workflow.v1/ClaimNext": {status: 200, body: `{"issued":true,` +
			`"grant":{"work_item":{"work_item_id":"w"},"claim":{"claim_id":"c","fencing_token":3}}}`},
	}}
	srv := httptest.NewServer(stub.handler())
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	_, _, err := c.ClaimNext(context.Background(), testClaimRequest())
	var call *CallError
	if !errors.As(err, &call) || call.Code != CodeInvalid {
		t.Fatalf("ClaimNext plan-less issue = %v, want invalid", err)
	}
}

func TestClaimNextDecodeErrors(t *testing.T) {
	// The pre-conformance minimal shape is not the spec: an issued body
	// without the grant half fails closed even though unknown fields
	// decode tolerantly.
	for _, body := range []string{"", "{oops", `{"issued":true,"claim_id":"c","lease_id":"l"}`} {
		stub := &stubPlane{bodies: map[string]stubResponse{
			"/workflow.v1/ClaimNext": {status: 200, body: body},
		}}
		srv := httptest.NewServer(stub.handler())
		c := newTestClient(t, srv.URL)
		_, _, err := c.ClaimNext(context.Background(), testClaimRequest())
		var call *CallError
		if !errors.As(err, &call) || call.Code != CodeInvalid {
			t.Errorf("ClaimNext body %q = %v, want invalid", body, err)
		}
		srv.Close()
	}
}

func TestTransportError(t *testing.T) {
	srv := httptest.NewServer((&stubPlane{}).handler())
	url := srv.URL
	srv.Close()

	c := newTestClient(t, url)
	_, _, err := c.ClaimNext(context.Background(), testClaimRequest())
	var call *CallError
	if !errors.As(err, &call) || call.Code != CodeTransport {
		t.Fatalf("ClaimNext closed server = %v, want transport", err)
	}
}

func TestUnexpectedStatus(t *testing.T) {
	stub := &stubPlane{bodies: map[string]stubResponse{
		"/workflow.v1/ClaimNext": {status: 599, body: `{}`},
	}}
	srv := httptest.NewServer(stub.handler())
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	_, _, err := c.ClaimNext(context.Background(), testClaimRequest())
	var call *CallError
	if !errors.As(err, &call) || call.Code != CodeUnexpectedStatus {
		t.Fatalf("ClaimNext 599 = %v, want %q", err, CodeUnexpectedStatus)
	}
}

func TestBoundMessageTruncates(t *testing.T) {
	stub := &stubPlane{bodies: map[string]stubResponse{
		"/workflow.v1/ClaimNext": {status: 500, body: `{"code":"invalid","message":"` + strings.Repeat("m", 600) + `"}`},
	}}
	srv := httptest.NewServer(stub.handler())
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	_, _, err := c.ClaimNext(context.Background(), testClaimRequest())
	var call *CallError
	if !errors.As(err, &call) {
		t.Fatalf("ClaimNext 500 = %v, want CallError", err)
	}
	if len(call.Message) > maxErrorMessage+3 || !strings.HasSuffix(call.Message, "…") {
		t.Fatalf("message not bounded: len %d", len(call.Message))
	}
}

// TestClaimNextCarriesSourceSystem proves the grant's planned source
// system reaches the lease: the satellite door requires the echo, and
// the in-tree host forwards the planned value, so dropping it here
// strands every held claim at the door.
func TestClaimNextCarriesSourceSystem(t *testing.T) {
	stub := &stubPlane{bodies: map[string]stubResponse{
		"/workflow.v1/ClaimNext": {status: 200, body: `{"issued":true,` +
			`"grant":{"work_item":{"work_item_id":"l1","scope_id":"scope-1",` +
			`"source_run_id":"sr-1","generation_id":"gen-1","source_system":"src-1"},` +
			`"claim":{"claim_id":"c1","fencing_token":7}}}`},
	}}
	srv := httptest.NewServer(stub.handler())
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	lease, found, err := c.ClaimNext(context.Background(), testClaimRequest())
	if err != nil || !found {
		t.Fatalf("ClaimNext = %v,%v, want found lease", lease, err)
	}
	if lease.SourceSystem != "src-1" {
		t.Errorf("lease.SourceSystem = %q, want the planned src-1 from the grant", lease.SourceSystem)
	}
}
