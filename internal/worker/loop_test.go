// SPDX-License-Identifier: MIT
// Copyright (c) 2026 eshu-hq

package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/eshu-hq/eshu-extension-host/internal/cutovercontract"
)

type claimAnswer struct {
	lease cutovercontract.Lease
	found bool
	err   error
}

type planeCall struct {
	op     string
	lease  cutovercontract.Lease
	result []byte
	req    cutovercontract.ClaimRequest
}

type fakePlane struct {
	mu          sync.Mutex
	answers     []claimAnswer
	calls       []planeCall
	completeErr error
	commitErr   error
	heartbeats  int
}

func (f *fakePlane) ClaimNext(_ context.Context, req cutovercontract.ClaimRequest) (cutovercontract.Lease, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, planeCall{op: "ClaimNext", req: req})
	if len(f.answers) == 0 {
		return cutovercontract.Lease{}, false, nil
	}
	head := f.answers[0]
	f.answers = f.answers[1:]
	return head.lease, head.found, head.err
}

func (f *fakePlane) Heartbeat(_ context.Context, lease cutovercontract.Lease) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, planeCall{op: "Heartbeat", lease: lease})
	f.heartbeats++
	return nil
}

func (f *fakePlane) Complete(_ context.Context, lease cutovercontract.Lease, result []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, planeCall{op: "Complete", lease: lease, result: result})
	return f.completeErr
}

func (f *fakePlane) CommitClaimFacts(_ context.Context, lease cutovercontract.Lease, result []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, planeCall{op: "CommitClaimFacts", lease: lease, result: result})
	return f.commitErr
}

func (f *fakePlane) Release(_ context.Context, lease cutovercontract.Lease) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, planeCall{op: "Release", lease: lease})
	return nil
}

func (f *fakePlane) FailRetryable(_ context.Context, lease cutovercontract.Lease) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, planeCall{op: "FailRetryable", lease: lease})
	return nil
}

func (f *fakePlane) FailTerminal(_ context.Context, lease cutovercontract.Lease) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, planeCall{op: "FailTerminal", lease: lease})
	return nil
}

func (f *fakePlane) ops() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	ops := make([]string, 0, len(f.calls))
	for _, c := range f.calls {
		ops = append(ops, c.op)
	}
	return ops
}

func (f *fakePlane) heartbeatCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.heartbeats
}

type fakeRunner struct {
	mu        sync.Mutex
	contracts [][]byte
	run       func(ctx context.Context, contract []byte) ([]byte, error)
}

func (f *fakeRunner) RunCollector(ctx context.Context, contract []byte) ([]byte, error) {
	f.mu.Lock()
	f.contracts = append(f.contracts, contract)
	f.mu.Unlock()
	return f.run(ctx, contract)
}

func testConfig() Config {
	return Config{
		ProducerID:          "test-producer",
		ProducerVersion:     "v0.1.0",
		CollectorInstanceID: "test-instance",
		Scopes:              "component",
		ScopeKind:           "scope-kind-1",
		ConfigHandle:        "rehearsal:test",
		ClaimPlaneURL:       "https://plane.internal",
		PollInterval:        time.Millisecond,
		LeaseTTL:            60 * time.Second,
		HeartbeatInterval:   5 * time.Millisecond,
	}
}

// testLease is a complete grant: the fenced epoch, the planned identity
// the commit matches the pin against, the attempt and deadline the door
// requires on the child echo, and the tenant grant the commit re-checks
// before core persists source facts.
func testLease() cutovercontract.Lease {
	return cutovercontract.Lease{
		ClaimID: "c1", LeaseID: "l1", FencingToken: 1193046,
		ScopeID: "scope-1", SourceRunID: "sr-1", GenerationID: "gen-1",
		SourceSystem: "src-1",
		Attempt:      41,
		Deadline:     time.Date(2026, 11, 8, 21, 0, 0, 0, time.UTC),
		TenantID:     "t-1", WorkspaceID: "w-1", SubjectClass: "s-1",
		PolicyRevisionHash: "p-1",
	}
}

func runLoop(t *testing.T, cfg Config, runner *fakeRunner, plane *fakePlane, timeout time.Duration) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return Loop(ctx, cfg, runner, plane)
}

func opsAfterClaim(ops []string) []string {
	for i, op := range ops {
		if op == "ClaimNext" {
			return ops[i+1:]
		}
	}
	return nil
}

func TestLoopRejectsNilCollaborators(t *testing.T) {
	cfg := testConfig()
	plane := &fakePlane{}
	runner := &fakeRunner{run: func(context.Context, []byte) ([]byte, error) { return []byte(`{}`), nil }}
	if err := Loop(context.Background(), cfg, nil, plane); err == nil {
		t.Error("nil runner: got nil error, want fail-closed")
	}
	if err := Loop(context.Background(), cfg, runner, nil); err == nil {
		t.Error("nil plane: got nil error, want fail-closed")
	}
	bad := cfg
	bad.ProducerID = ""
	if err := Loop(context.Background(), bad, runner, plane); err == nil {
		t.Error("empty producer: got nil error, want fail-closed")
	}
	bad = cfg
	bad.PollInterval = 0
	if err := Loop(context.Background(), bad, runner, plane); err == nil {
		t.Error("zero poll interval: got nil error, want fail-closed")
	}
	bad = cfg
	bad.ProducerVersion = "  "
	if err := Loop(context.Background(), bad, runner, plane); err == nil {
		t.Error("empty producer version: got nil error, want fail-closed")
	}
	bad = cfg
	bad.CollectorInstanceID = ""
	if err := Loop(context.Background(), bad, runner, plane); err == nil {
		t.Error("empty instance id: got nil error, want fail-closed")
	}
	bad = cfg
	bad.ScopeKind = "  "
	if err := Loop(context.Background(), bad, runner, plane); err == nil {
		t.Error("blank scope kind: got nil error, want fail-closed (door requires the echo)")
	}
}

func TestClaimRequestCarriesDistinctInstanceID(t *testing.T) {
	// The plane matches (kind, instance) by equality: a producer whose
	// id differs from the instance id can never claim unless the
	// selector carries the dedicated instance knob, not the producer id.
	cfg := testConfig()
	cfg.ProducerID = "producer-9"
	cfg.CollectorInstanceID = "instance-7"
	req := claimRequestFor(cfg)
	if req.Selector.CollectorInstanceID != "instance-7" {
		t.Errorf("CollectorInstanceID = %q, want the dedicated instance knob %q",
			req.Selector.CollectorInstanceID, "instance-7")
	}
	if req.Selector.OwnerID != "producer-9" {
		t.Errorf("OwnerID = %q, want the holder %q", req.Selector.OwnerID, "producer-9")
	}
}

func TestLoopClaimRequestFromConfig(t *testing.T) {
	// The worker builds the spec-shaped request from Config: the scope
	// kind selects the collector kind, the dedicated instance knob feeds
	// the plane's (kind, instance) match key, the producer id pins the
	// owner slot, the lease TTL rides as the requested hold, and every
	// poll mints a fresh claim id so the plane never sees a replayed
	// selector.
	runner := &fakeRunner{run: func(context.Context, []byte) ([]byte, error) { return []byte(`{}`), nil }}
	plane := &fakePlane{answers: []claimAnswer{
		{lease: testLease(), found: true},
		{lease: testLease(), found: true},
	}}
	cfg := testConfig()
	if err := runLoop(t, cfg, runner, plane, 2*time.Second); err != nil {
		t.Fatalf("Loop: %v", err)
	}
	plane.mu.Lock()
	defer plane.mu.Unlock()
	var reqs []cutovercontract.ClaimRequest
	for _, c := range plane.calls {
		if c.op == "ClaimNext" {
			reqs = append(reqs, c.req)
		}
	}
	if len(reqs) < 2 {
		t.Fatalf("ClaimNext calls = %d, want at least 2", len(reqs))
	}
	for i, req := range reqs {
		sel := req.Selector
		if string(sel.CollectorKind) != cfg.Scopes {
			t.Errorf("req %d collector_kind = %q, want scopes %q", i, sel.CollectorKind, cfg.Scopes)
		}
		if sel.CollectorInstanceID != cfg.CollectorInstanceID {
			t.Errorf("req %d instance = %q, want dedicated instance %q",
				i, sel.CollectorInstanceID, cfg.CollectorInstanceID)
		}
		if sel.OwnerID != cfg.ProducerID {
			t.Errorf("req %d owner = %q, want holder %q",
				i, sel.OwnerID, cfg.ProducerID)
		}
		if sel.ClaimID == "" {
			t.Errorf("req %d claim id is empty, want a fresh nonce per poll", i)
		}
		if req.LeaseTTL != cfg.LeaseTTL {
			t.Errorf("req %d lease TTL = %v, want %v", i, req.LeaseTTL, cfg.LeaseTTL)
		}
	}
	if reqs[0].Selector.ClaimID == reqs[1].Selector.ClaimID {
		t.Errorf("claim ids repeat %q, want a fresh nonce per poll", reqs[0].Selector.ClaimID)
	}
}

func TestLoopCompletesClaim(t *testing.T) {
	runner := &fakeRunner{run: func(context.Context, []byte) ([]byte, error) { return completeResult(), nil }}
	plane := &fakePlane{answers: []claimAnswer{{lease: testLease(), found: true}}}
	if err := runLoop(t, testConfig(), runner, plane, 2*time.Second); err != nil {
		t.Fatalf("Loop: %v", err)
	}
	ops := opsAfterClaim(plane.ops())
	if len(ops) < 2 || ops[0] != "CommitClaimFacts" || ops[1] != "Complete" {
		t.Fatalf("ops after claim = %v, want [CommitClaimFacts Complete ...]", ops)
	}
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if len(runner.contracts) != 1 || len(runner.contracts[0]) == 0 {
		t.Fatalf("runner got %d contracts, want 1 non-empty", len(runner.contracts))
	}
	var envelope struct {
		ProtocolVersion string `json:"protocol_version"`
		Claim           struct {
			ComponentID  string `json:"component_id"`
			WorkItemID   string `json:"work_item_id"`
			FencingToken string `json:"fencing_token"`
			Scope        struct {
				ID string `json:"id"`
			} `json:"scope"`
		} `json:"claim"`
	}
	if err := json.Unmarshal(runner.contracts[0], &envelope); err != nil {
		t.Fatalf("contract is not a request: %v", err)
	}
	if envelope.ProtocolVersion == "" || envelope.Claim.ComponentID != "test-producer" || envelope.Claim.WorkItemID != "l1" {
		t.Errorf("contract = %+v, want the extension request shape", envelope)
	}
	if envelope.Claim.FencingToken != "1193046" || envelope.Claim.Scope.ID != "scope-1" {
		t.Errorf("contract = %+v, want the fenced epoch of a grantable lease", envelope)
	}
}

func TestLoopRetryableAndReleaseOnRunError(t *testing.T) {
	runner := &fakeRunner{run: func(context.Context, []byte) ([]byte, error) { return nil, errors.New("child blew up") }}
	plane := &fakePlane{answers: []claimAnswer{{lease: testLease(), found: true}}}
	if err := runLoop(t, testConfig(), runner, plane, 2*time.Second); err != nil {
		t.Fatalf("Loop: %v", err)
	}
	ops := opsAfterClaim(plane.ops())
	if len(ops) < 2 || ops[0] != "FailRetryable" || ops[1] != "Release" {
		t.Errorf("ops after claim = %v, want [FailRetryable Release]", ops)
	}
}

func TestLoopTerminalOnRunGrantDeny(t *testing.T) {
	denied := fmt.Errorf("admission: %w", cutovercontract.ErrGrantDenied)
	runner := &fakeRunner{run: func(context.Context, []byte) ([]byte, error) { return nil, denied }}
	plane := &fakePlane{answers: []claimAnswer{{lease: testLease(), found: true}}}
	if err := runLoop(t, testConfig(), runner, plane, 2*time.Second); err != nil {
		t.Fatalf("Loop: %v", err)
	}
	ops := opsAfterClaim(plane.ops())
	if len(ops) == 0 || ops[0] != "FailTerminal" {
		t.Errorf("ops after claim = %v, want FailTerminal first", ops)
	}
	for _, op := range ops {
		if op == "FailRetryable" || op == "Release" || op == "Complete" {
			t.Errorf("ops after claim = %v, want no %q after grant deny", ops, op)
		}
	}
}

func TestLoopTerminalOnCompleteGrantDeny(t *testing.T) {
	runner := &fakeRunner{run: func(context.Context, []byte) ([]byte, error) { return completeResult(), nil }}
	plane := &fakePlane{
		answers:     []claimAnswer{{lease: testLease(), found: true}},
		completeErr: fmt.Errorf("grant check: %w", cutovercontract.ErrGrantDenied),
	}
	if err := runLoop(t, testConfig(), runner, plane, 2*time.Second); err != nil {
		t.Fatalf("Loop: %v", err)
	}
	ops := opsAfterClaim(plane.ops())
	if len(ops) < 3 || ops[0] != "CommitClaimFacts" || ops[1] != "Complete" || ops[2] != "FailTerminal" {
		t.Errorf("ops after claim = %v, want [CommitClaimFacts Complete FailTerminal]", ops)
	}
}

func TestLoopRetryableAndReleaseOnCompleteError(t *testing.T) {
	runner := &fakeRunner{run: func(context.Context, []byte) ([]byte, error) { return completeResult(), nil }}
	plane := &fakePlane{
		answers:     []claimAnswer{{lease: testLease(), found: true}},
		completeErr: errors.New("plane transport blew up"),
	}
	if err := runLoop(t, testConfig(), runner, plane, 2*time.Second); err != nil {
		t.Fatalf("Loop: %v", err)
	}
	ops := opsAfterClaim(plane.ops())
	if len(ops) < 4 || ops[0] != "CommitClaimFacts" || ops[1] != "Complete" || ops[2] != "FailRetryable" || ops[3] != "Release" {
		t.Errorf("ops after claim = %v, want [CommitClaimFacts Complete FailRetryable Release]", ops)
	}
}

func TestLoopSurvivesClaimErrors(t *testing.T) {
	runner := &fakeRunner{run: func(context.Context, []byte) ([]byte, error) { return completeResult(), nil }}
	plane := &fakePlane{answers: []claimAnswer{
		{err: errors.New("plane flap one")},
		{err: fmt.Errorf("admission: %w", cutovercontract.ErrGrantDenied)},
		{lease: testLease(), found: true},
	}}
	if err := runLoop(t, testConfig(), runner, plane, 2*time.Second); err != nil {
		t.Fatalf("Loop: %v", err)
	}
	found := false
	for _, op := range plane.ops() {
		if op == "Complete" {
			found = true
		}
	}
	if !found {
		t.Errorf("ops = %v, want a Complete after the claim errors", plane.ops())
	}
}

func TestLoopHeartbeatsDuringLongRun(t *testing.T) {
	release := make(chan struct{})
	runner := &fakeRunner{run: func(ctx context.Context, _ []byte) ([]byte, error) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-release:
			return completeResult(), nil
		}
	}}
	plane := &fakePlane{answers: []claimAnswer{{lease: testLease(), found: true}}}
	cfg := testConfig()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- Loop(ctx, cfg, runner, plane) }()
	deadline := time.Now().Add(2 * time.Second)
	for plane.heartbeatCount() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("no heartbeat observed during a blocked run")
		}
		time.Sleep(time.Millisecond)
	}
	close(release)
	completeBy := time.Now().Add(2 * time.Second)
	for {
		completed := false
		for _, op := range plane.ops() {
			if op == "Complete" {
				completed = true
			}
		}
		if completed {
			break
		}
		if time.Now().After(completeBy) {
			t.Fatal("Complete never landed after the blocked run finished")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Loop: %v", err)
	}
	lease := testLease()
	for _, c := range plane.calls {
		if c.op == "Heartbeat" && c.lease != lease {
			t.Errorf("heartbeat lease = %+v, want %+v", c.lease, lease)
		}
	}
}

func TestLoopStopsCleanlyOnCancel(t *testing.T) {
	started := make(chan struct{})
	var once sync.Once
	runner := &fakeRunner{run: func(ctx context.Context, _ []byte) ([]byte, error) {
		once.Do(func() { close(started) })
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	plane := &fakePlane{answers: []claimAnswer{{lease: testLease(), found: true}}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Loop(ctx, testConfig(), runner, plane) }()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("run never started")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Loop after cancel = %v, want nil (clean stop)", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Loop did not stop after cancel")
	}
	for _, op := range plane.ops() {
		if op == "Complete" || strings.HasPrefix(op, "Fail") {
			t.Errorf("op %q recorded after cancel, want none", op)
		}
	}
}
