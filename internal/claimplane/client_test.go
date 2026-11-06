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
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
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
			var req claimNextRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err == nil {
				s.mu.Lock()
				s.observed = append(s.observed, req.ObservedAt)
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
		BaseURL:         url,
		ProducerID:      "test-producer",
		ProducerVersion: "1.0.0",
		Scopes:          []string{"ec2:DescribeInstances"},
		Grants:          &StaticGrants{Grants: []Grant{{ProducerID: "test-producer"}}},
		Clock:           func() time.Time { return time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC) },
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
		"/workflow.v1/ClaimNext": {status: 200, body: `{"issued":true,"claim_id":"c","lease_id":"l"}`},
	}}
	srv := httptest.NewServer(stub.handler())
	defer srv.Close()

	cfg := testConfig(srv.URL)
	cfg.Clock = nil
	c, err := NewClient(cfg)
	if err != nil {
		t.Fatalf("NewClient nil clock: %v", err)
	}
	if _, _, err := c.ClaimNext(context.Background(), "test-producer", "ec2:DescribeInstances"); err != nil {
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
	inputs := []struct {
		name   string
		pid    string
		scopes string
		ctx    context.Context
	}{
		{name: "empty producer", pid: "", scopes: "s", ctx: ctx},
		{name: "empty scopes", pid: "test-producer", scopes: "  ", ctx: ctx},
		{name: "cancelled ctx", pid: "test-producer", scopes: "s", ctx: cancelled},
	}
	for _, in := range inputs {
		if _, _, err := c.ClaimNext(in.ctx, in.pid, in.scopes); err == nil {
			t.Fatalf("ClaimNext %s = nil, want error", in.name)
		}
	}
	if n := stub.hitCount(); n != 0 {
		t.Fatalf("invalid input touched the wire %d times, want 0", n)
	}
}

func TestClaimNextTruncatedBody(t *testing.T) {
	stub := &stubPlane{bodies: map[string]stubResponse{
		"/workflow.v1/ClaimNext": {status: 200, body: `{"issued":true,"claim_id":"c","lease_id":"l"}`},
	}}
	srv := httptest.NewServer(stub.handler())
	defer srv.Close()

	cfg := testConfig(srv.URL)
	cfg.MaxResponseBytes = 16
	c, err := NewClient(cfg)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	_, _, err = c.ClaimNext(context.Background(), "test-producer", "ec2:DescribeInstances")
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
	_, _, err := c.ClaimNext(context.Background(), "test-producer", "ec2:DescribeInstances")
	var call *CallError
	if !errors.As(err, &call) || call.Code != "invalid" || call.Op != "ClaimNext" {
		t.Fatalf("ClaimNext 500 = %v, want op/ClaimNext code/invalid", err)
	}
}

func TestClaimNextEmptyQueue(t *testing.T) {
	srv := httptest.NewServer((&stubPlane{}).handler())
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	_, found, err := c.ClaimNext(context.Background(), "test-producer", "ec2:DescribeInstances")
	if err != nil || found {
		t.Fatalf("ClaimNext empty = found %v err %v, want false/nil", found, err)
	}
}

func TestClaimNextRoundTrip(t *testing.T) {
	stub := &stubPlane{bodies: map[string]stubResponse{
		"/workflow.v1/ClaimNext": {status: 200, body: `{"issued":true,"claim_id":"c1","lease_id":"l1"}`},
	}}
	srv := httptest.NewServer(stub.handler())
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	lease, found, err := c.ClaimNext(context.Background(), "test-producer", "ec2:DescribeInstances")
	if err != nil || !found {
		t.Fatalf("ClaimNext = %v,%v, want found lease", lease, err)
	}
	if lease.ClaimID != "c1" || lease.LeaseID != "l1" {
		t.Fatalf("lease = %+v, want c1/l1", lease)
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
	stub := &stubPlane{bodies: map[string]stubResponse{
		"/workflow.v1/ClaimNext": {status: 200, body: `{"issued":false,"retry_after_seconds":7}`},
	}}
	srv := httptest.NewServer(stub.handler())
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	_, found, err := c.ClaimNext(context.Background(), "test-producer", "ec2:DescribeInstances")
	var call *CallError
	if !errors.As(err, &call) || !call.Paced() || found {
		t.Fatalf("ClaimNext issued=false = %v,%v, want paced", found, err)
	}
	if call.RetryAfter != 7*time.Second {
		t.Fatalf("RetryAfter = %v, want 7s", call.RetryAfter)
	}
}

func TestClaimNext429IsPaced(t *testing.T) {
	stub := &stubPlane{bodies: map[string]stubResponse{
		"/workflow.v1/ClaimNext": {status: 429, body: `{"code":"paced","message":"slow","retry_after_seconds":3}`},
	}}
	srv := httptest.NewServer(stub.handler())
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	_, _, err := c.ClaimNext(context.Background(), "test-producer", "ec2:DescribeInstances")
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
	_, _, err := c.ClaimNext(context.Background(), "test-producer", "ec2:DescribeInstances")
	var call *CallError
	if !errors.As(err, &call) || call.Code != CodeInvalid {
		t.Fatalf("ClaimNext id-less issue = %v, want invalid", err)
	}
}

func TestClaimNextDecodeErrors(t *testing.T) {
	for _, body := range []string{"", `{"issued":true,"claim_id":"c","lease_id":"l","bogus":1}`} {
		stub := &stubPlane{bodies: map[string]stubResponse{
			"/workflow.v1/ClaimNext": {status: 200, body: body},
		}}
		srv := httptest.NewServer(stub.handler())
		c := newTestClient(t, srv.URL)
		_, _, err := c.ClaimNext(context.Background(), "test-producer", "ec2:DescribeInstances")
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
	_, _, err := c.ClaimNext(context.Background(), "test-producer", "ec2:DescribeInstances")
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
	_, _, err := c.ClaimNext(context.Background(), "test-producer", "ec2:DescribeInstances")
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
	_, _, err := c.ClaimNext(context.Background(), "test-producer", "ec2:DescribeInstances")
	var call *CallError
	if !errors.As(err, &call) {
		t.Fatalf("ClaimNext 500 = %v, want CallError", err)
	}
	if len(call.Message) > maxErrorMessage+3 || !strings.HasSuffix(call.Message, "…") {
		t.Fatalf("message not bounded: len %d", len(call.Message))
	}
}
