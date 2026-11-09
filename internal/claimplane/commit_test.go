// SPDX-License-Identifier: MIT
// Copyright (c) 2026 eshu-hq

// Commit-lane tests: CommitClaimFacts posts the fenced commit envelope
// and only the {"ok":true} ack counts as committed. The result travels
// byte-verbatim, the shared caps fail closed before any byte reaches the
// wire, and every failure shape maps to the caller taxonomy.
package claimplane

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/eshu-hq/eshu-extension-host/internal/cutovercontract"
)

// commitLease is a fully fenced held claim: the grant carried the epoch
// fencing token and the planned identity core matches the pin against.
func commitLease() cutovercontract.Lease {
	return cutovercontract.Lease{
		ClaimID:      "c1",
		LeaseID:      "w1",
		FencingToken: 7,
		ScopeID:      "scope-1",
		SourceRunID:  "sr-1",
		GenerationID: "gen-1",
	}
}

// commitStub records commit envelopes and answers them scripted.
type commitStub struct {
	mu     sync.Mutex
	bodies []string
	resp   stubResponse
}

func (s *commitStub) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.bodies = append(s.bodies, string(raw))
		s.mu.Unlock()
		if r.Method != http.MethodPost || r.URL.Path != "/workflow.v1/CommitClaimFacts" {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.WriteHeader(s.resp.status)
		_, _ = w.Write([]byte(s.resp.body))
	}
}

func (s *commitStub) firstBody(t *testing.T) map[string]json.RawMessage {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.bodies) != 1 {
		t.Fatalf("commit bodies = %d, want 1", len(s.bodies))
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(s.bodies[0]), &obj); err != nil {
		t.Fatalf("commit body is not a JSON object: %v", err)
	}
	return obj
}

func TestCommitCapsShareArithmetic(t *testing.T) {
	if cutovercontract.DefaultCommitResultBytes != 4<<20 {
		t.Fatalf("DefaultCommitResultBytes = %d, want %d", cutovercontract.DefaultCommitResultBytes, 4<<20)
	}
	if cutovercontract.DefaultCommitBodyBytes != cutovercontract.DefaultCommitResultBytes+64<<10 {
		t.Fatalf("DefaultCommitBodyBytes = %d, want result+64KiB", cutovercontract.DefaultCommitBodyBytes)
	}
	if cutovercontract.DefaultCommitBodyBytes != 4259840 {
		t.Fatalf("DefaultCommitBodyBytes = %d, want 4259840", cutovercontract.DefaultCommitBodyBytes)
	}
}

func TestCommitClaimFactsRoundTrip(t *testing.T) {
	stub := &commitStub{resp: stubResponse{status: 200, body: `{"ok":true}`}}
	srv := httptest.NewServer(stub.handler())
	defer srv.Close()

	rawResult := `{"facts":[{"id":"f-1"}],"unknown_future_field":[1,2,{}]}`
	c := newTestClient(t, srv.URL)
	if err := c.CommitClaimFacts(context.Background(), commitLease(), []byte(rawResult)); err != nil {
		t.Fatalf("CommitClaimFacts: %v", err)
	}
	obj := stub.firstBody(t)
	var gotKeys, wantKeys []string
	for k := range obj {
		gotKeys = append(gotKeys, k)
	}
	wantKeys = []string{
		"work_item_id", "claim_id", "fencing_token", "owner_id",
		"observed_at", "lease_duration_ns", "scope_id", "source_run_id",
		"generation_id", "idempotency_key", "result_json",
	}
	sort.Strings(gotKeys)
	sort.Strings(wantKeys)
	if len(gotKeys) != len(wantKeys) {
		t.Fatalf("commit keys = %v, want %v", gotKeys, wantKeys)
	}
	for i := range gotKeys {
		if gotKeys[i] != wantKeys[i] {
			t.Fatalf("commit keys = %v, want %v", gotKeys, wantKeys)
		}
	}
	if string(obj["work_item_id"]) != `"w1"` {
		t.Errorf("work_item_id = %s, want the granted work item", obj["work_item_id"])
	}
	if string(obj["claim_id"]) != `"c1"` {
		t.Errorf("claim_id = %s, want the held epoch", obj["claim_id"])
	}
	if string(obj["fencing_token"]) != `7` {
		t.Errorf("fencing_token = %s, want the grant epoch token", obj["fencing_token"])
	}
	if string(obj["owner_id"]) != `"test-producer"` {
		t.Errorf("owner_id = %s, want the configured holder", obj["owner_id"])
	}
	if string(obj["scope_id"]) != `"scope-1"` || string(obj["source_run_id"]) != `"sr-1"` ||
		string(obj["generation_id"]) != `"gen-1"` {
		t.Errorf("planned ids = %s/%s/%s, want scope-1/sr-1/gen-1",
			obj["scope_id"], obj["source_run_id"], obj["generation_id"])
	}
	if string(obj["idempotency_key"]) != `"c1"` {
		t.Errorf("idempotency_key = %s, want the claim epoch id", obj["idempotency_key"])
	}
	if string(obj["result_json"]) != rawResult {
		t.Errorf("result_json = %s, want the exact collector bytes", obj["result_json"])
	}
	var ttl int64
	if err := json.Unmarshal(obj["lease_duration_ns"], &ttl); err != nil || ttl <= 0 {
		t.Errorf("lease_duration_ns = %s, want a positive nanosecond count", obj["lease_duration_ns"])
	}
}

func TestCommitClaimFactsAckGating(t *testing.T) {
	for _, body := range []string{`{"ok":false}`, `{"ok":true} trailing`, `{"ok":true,"x":1}`} {
		stub := &commitStub{resp: stubResponse{status: 200, body: body}}
		srv := httptest.NewServer(stub.handler())
		c := newTestClient(t, srv.URL)
		err := c.CommitClaimFacts(context.Background(), commitLease(), []byte(`{}`))
		srv.Close()
		var call *CallError
		if !errors.As(err, &call) || call.Code != CodeInvalid || call.Op != "CommitClaimFacts" {
			t.Errorf("commit body %q = %v, want op/CommitClaimFacts code/invalid", body, err)
		}
	}
}

func TestCommitClaimFactsFailureMapping(t *testing.T) {
	conflict := &commitStub{resp: stubResponse{status: 409, body: `{"code":"stale_fence","message":"epoch moved"}`}}
	srv := httptest.NewServer(conflict.handler())
	defer srv.Close()
	c := newTestClient(t, srv.URL)
	err := c.CommitClaimFacts(context.Background(), commitLease(), []byte(`{}`))
	var call *CallError
	if !errors.As(err, &call) || !call.Stale() {
		t.Fatalf("commit 409 = %v, want a stale conflict", err)
	}

	broken := &commitStub{resp: stubResponse{status: 500, body: `{"code":"invalid","message":"boom"}`}}
	srv2 := httptest.NewServer(broken.handler())
	defer srv2.Close()
	c2 := newTestClient(t, srv2.URL)
	err = c2.CommitClaimFacts(context.Background(), commitLease(), []byte(`{}`))
	if err == nil {
		t.Fatal("commit 500 = nil, want an error")
	}
	if errors.Is(err, cutovercontract.ErrTerminalCarrier) {
		t.Errorf("commit 500+invalid = %v, want no terminal-carrier sentinel on a server failure", err)
	}

	srv3 := httptest.NewServer((&commitStub{}).handler())
	url3 := srv3.URL
	srv3.Close()
	c3 := newTestClient(t, url3)
	err = c3.CommitClaimFacts(context.Background(), commitLease(), []byte(`{}`))
	if !errors.As(err, &call) || call.Code != CodeTransport {
		t.Fatalf("commit transport = %v, want transport", err)
	}
}

func TestCommitClaimFactsValidation(t *testing.T) {
	stub := &commitStub{resp: stubResponse{status: 200, body: `{"ok":true}`}}
	srv := httptest.NewServer(stub.handler())
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	ctx := context.Background()
	bad := commitLease()
	cases := map[string]func(*cutovercontract.Lease){
		"empty claim id":   func(l *cutovercontract.Lease) { l.ClaimID = "" },
		"empty work id":    func(l *cutovercontract.Lease) { l.LeaseID = "" },
		"zero fencing":     func(l *cutovercontract.Lease) { l.FencingToken = 0 },
		"empty scope":      func(l *cutovercontract.Lease) { l.ScopeID = "" },
		"empty source run": func(l *cutovercontract.Lease) { l.SourceRunID = "" },
		"empty generation": func(l *cutovercontract.Lease) { l.GenerationID = "" },
	}
	for name, mutate := range cases {
		lease := bad
		mutate(&lease)
		if err := c.CommitClaimFacts(ctx, lease, []byte(`{}`)); err == nil {
			t.Errorf("CommitClaimFacts %s = nil, want fail-closed", name)
		}
	}
	if err := c.CommitClaimFacts(ctx, commitLease(), nil); err == nil {
		t.Error("CommitClaimFacts empty result = nil, want fail-closed")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := c.CommitClaimFacts(cancelled, commitLease(), []byte(`{}`)); err == nil {
		t.Error("CommitClaimFacts cancelled ctx = nil, want error")
	}
	stub.mu.Lock()
	defer stub.mu.Unlock()
	if len(stub.bodies) != 0 {
		t.Fatalf("invalid commits touched the wire %d times, want 0", len(stub.bodies))
	}
}

func TestCommitClaimFactsResultCap(t *testing.T) {
	stub := &commitStub{resp: stubResponse{status: 200, body: `{"ok":true}`}}
	srv := httptest.NewServer(stub.handler())
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	atCap := []byte(`"` + strings.Repeat("x", cutovercontract.DefaultCommitResultBytes-2) + `"`)
	if err := c.CommitClaimFacts(context.Background(), commitLease(), atCap); err != nil {
		t.Fatalf("result at cap: %v", err)
	}
	overCap := []byte(`"` + strings.Repeat("x", cutovercontract.DefaultCommitResultBytes-1) + `"`)
	if err := c.CommitClaimFacts(context.Background(), commitLease(), overCap); err == nil {
		t.Fatal("result over cap = nil, want fail-closed")
	}
	stub.mu.Lock()
	defer stub.mu.Unlock()
	if len(stub.bodies) != 1 {
		t.Fatalf("wire commits = %d, want 1 (only the at-cap result posts)", len(stub.bodies))
	}
}

func TestCommitClaimFactsBodyCap(t *testing.T) {
	// The whole envelope (4 MiB result plus the mutation, idempotency
	// key, planned IDs, and framing) stays under 4,259,840 bytes: a
	// result at the result cap plus unbounded identifier fields must
	// still fail closed before any byte reaches the wire.
	stub := &commitStub{resp: stubResponse{status: 200, body: `{"ok":true}`}}
	srv := httptest.NewServer(stub.handler())
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	lease := commitLease()
	lease.ScopeID = strings.Repeat("s", 128<<10)
	result := []byte(`"` + strings.Repeat("x", cutovercontract.DefaultCommitResultBytes-2) + `"`)
	if err := c.CommitClaimFacts(context.Background(), lease, result); err == nil {
		t.Fatal("over-cap envelope = nil, want fail-closed on the 4,259,840-byte body bound")
	}
	stub.mu.Lock()
	defer stub.mu.Unlock()
	if len(stub.bodies) != 0 {
		t.Fatalf("over-cap envelope touched the wire %d times, want 0", len(stub.bodies))
	}
}

func TestCommitClaimFactsTenantForwarding(t *testing.T) {
	stub := &commitStub{resp: stubResponse{status: 200, body: `{"ok":true}`}}
	srv := httptest.NewServer(stub.handler())
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	lease := commitLease()
	lease.TenantID = "t-1"
	lease.WorkspaceID = "w-1"
	lease.SubjectClass = "s-1"
	lease.PolicyRevisionHash = "p-1"
	if err := c.CommitClaimFacts(context.Background(), lease, []byte(`{}`)); err != nil {
		t.Fatalf("CommitClaimFacts: %v", err)
	}
	obj := stub.firstBody(t)
	for k, want := range map[string]string{
		"tenant_id": "t-1", "workspace_id": "w-1",
		"subject_class": "s-1", "policy_revision_hash": "p-1",
	} {
		if string(obj[k]) != `"`+want+`"` {
			t.Errorf("%s = %s, want %q forwarded from the grant", k, obj[k], want)
		}
	}
}

func TestCommitClaimFactsLeaseDuration(t *testing.T) {
	stub := &commitStub{resp: stubResponse{status: 200, body: `{"ok":true}`}}
	srv := httptest.NewServer(stub.handler())
	defer srv.Close()

	cfg := testConfig(srv.URL)
	cfg.LeaseDuration = 90 * time.Second
	c, err := NewClient(cfg)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := c.CommitClaimFacts(context.Background(), commitLease(), []byte(`{}`)); err != nil {
		t.Fatalf("CommitClaimFacts: %v", err)
	}
	obj := stub.firstBody(t)
	var ttl int64
	if err := json.Unmarshal(obj["lease_duration_ns"], &ttl); err != nil || ttl != int64(90*time.Second) {
		t.Fatalf("lease_duration_ns = %s, want the configured hold 90000000000", obj["lease_duration_ns"])
	}
}

func TestCommitClaimFactsTerminalCarrierRefusal(t *testing.T) {
	// Live replay (ops-qa 2026-11-09, issue #22): the plane refuses a
	// terminal-state carrier with code invalid — the core identity gate
	// rejects every non-complete result state, so retrying the same
	// carrier can never succeed. The client must surface the seam
	// terminal-carrier sentinel while keeping the CallError taxonomy.
	refused := &commitStub{resp: stubResponse{status: 400, body: `{"code":"invalid","message":"invalid carrier: commit claim facts: result state \"terminal\" is not complete"}`}}
	srv := httptest.NewServer(refused.handler())
	defer srv.Close()
	c := newTestClient(t, srv.URL)
	err := c.CommitClaimFacts(context.Background(), commitLease(), []byte(`{}`))
	if err == nil {
		t.Fatal("commit refused carrier = nil, want an error")
	}
	if !errors.Is(err, cutovercontract.ErrTerminalCarrier) {
		t.Errorf("commit refused carrier = %v, want the terminal-carrier sentinel", err)
	}
	var call *CallError
	if !errors.As(err, &call) || call.Op != "CommitClaimFacts" || call.Code != CodeInvalid {
		t.Errorf("commit refused carrier = %v, want op/CommitClaimFacts code/invalid", err)
	}
	if !strings.Contains(err.Error(), `result state "terminal" is not complete`) {
		t.Errorf("commit refused carrier = %v, want the observed refusal text", err)
	}

	// A 5xx the plane did not judge invalid carries no sentinel: the
	// wire failed, not the carrier.
	failed := &commitStub{resp: stubResponse{status: 500, body: `{"code":"unexpected_status","message":"boom"}`}}
	srv2 := httptest.NewServer(failed.handler())
	defer srv2.Close()
	c2 := newTestClient(t, srv2.URL)
	err = c2.CommitClaimFacts(context.Background(), commitLease(), []byte(`{}`))
	if err == nil {
		t.Fatal("commit 500 = nil, want an error")
	}
	if errors.Is(err, cutovercontract.ErrTerminalCarrier) {
		t.Errorf("commit 500 = %v, want no terminal-carrier sentinel on a server failure", err)
	}

	// A 5xx carrying code invalid stays retryable too: only a 4xx
	// refusal judges the carrier, so a server failure must never
	// fail the claim terminal even when its code reads invalid.
	invalid500 := &commitStub{resp: stubResponse{status: 500, body: `{"code":"invalid","message":"boom"}`}}
	srv3 := httptest.NewServer(invalid500.handler())
	defer srv3.Close()
	c3 := newTestClient(t, srv3.URL)
	err = c3.CommitClaimFacts(context.Background(), commitLease(), []byte(`{}`))
	if err == nil {
		t.Fatal("commit 500+invalid = nil, want an error")
	}
	if errors.Is(err, cutovercontract.ErrTerminalCarrier) {
		t.Errorf("commit 500+invalid = %v, want no terminal-carrier sentinel on a server failure", err)
	}
	call = nil
	if !errors.As(err, &call) || call.Op != "CommitClaimFacts" || call.Code != CodeInvalid {
		t.Errorf("commit 500+invalid = %v, want op/CommitClaimFacts code/invalid without the sentinel", err)
	}
}
