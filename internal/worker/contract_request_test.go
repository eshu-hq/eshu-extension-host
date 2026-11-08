// SPDX-License-Identifier: MIT
// Copyright (c) 2026 eshu-hq

// Child-request shape: contractFor renders the full extension request
// the collector artifact decodes.
package worker

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/eshu-hq/eshu-extension-host/internal/cutovercontract"
)

// TestContractForRendersExtensionRequest pins the child stdin to the
// full extension request the collector artifact decodes: protocol
// version, claim (component, instance, kind, scope id, source run,
// generation, work item, decimal fencing token), and contract. The
// pre-conformance flat envelope ({producer_id, claim_id, lease_id,
// scopes}) leaves the claim zero, so the child exits 1 on
// claim.fencing_token every held claim and no delivery ever runs.
func TestContractForRendersExtensionRequest(t *testing.T) {
	cfg := testConfig()
	lease := cutovercontract.Lease{
		ClaimID: "c1", LeaseID: "w1", FencingToken: 1193046,
		ScopeID: "scope-1", SourceRunID: "sr-1", GenerationID: "gen-1",
		SourceSystem: "src-1",
		TenantID:     "t-1", WorkspaceID: "w-1", SubjectClass: "s-1",
		PolicyRevisionHash: "p-1",
	}
	raw := contractFor(cfg, lease)
	var req struct {
		ProtocolVersion string                     `json:"protocol_version"`
		Claim           map[string]json.RawMessage `json:"claim"`
		Contract        map[string]json.RawMessage `json:"contract"`
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		t.Fatalf("contract is not a request: %v", err)
	}
	str := func(m map[string]json.RawMessage, key, want string) {
		t.Helper()
		var got string
		if err := json.Unmarshal(m[key], &got); err != nil || got != want {
			t.Errorf("contract %s: %s = %s, want %q", raw, key, m[key], want)
		}
	}
	if req.ProtocolVersion != "collector-sdk/v1alpha1" {
		t.Errorf("contract %s: protocol_version = %q, want the single-call contract the saver commits", raw, req.ProtocolVersion)
	}
	var contract struct {
		ProtocolVersion string `json:"protocol_version"`
	}
	if err := json.Unmarshal(req.Contract["protocol_version"], &contract.ProtocolVersion); err != nil || contract.ProtocolVersion != "collector-sdk/v1alpha1" {
		t.Errorf("contract %s: contract.protocol_version = %s, want the single-call contract", raw, req.Contract["protocol_version"])
	}
	str(req.Claim, "component_id", "test-producer")
	str(req.Claim, "instance_id", "test-instance")
	str(req.Claim, "collector_kind", "component")
	str(req.Claim, "source_run_id", "sr-1")
	str(req.Claim, "generation_id", "gen-1")
	str(req.Claim, "work_item_id", "w1")
	str(req.Claim, "fencing_token", "1193046")
	var scope struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(req.Claim["scope"], &scope); err != nil || scope.ID != "scope-1" {
		t.Errorf("contract %s: scope = %s, want scope-1", raw, req.Claim["scope"])
	}
	var flat map[string]json.RawMessage
	if err := json.Unmarshal(raw, &flat); err != nil {
		t.Fatalf("contract is not JSON: %v", err)
	}
	for _, key := range []string{"producer_id", "lease_id", "scopes"} {
		if _, ok := flat[key]; ok {
			t.Errorf("contract %s carries flat envelope field %q the artifact rejects", raw, key)
		}
	}
}

// TestContractForMatchesGolden pins the exact wire bytes the child
// reads: any schema drift — renamed field, dropped epoch, reverted
// flat envelope — breaks this test before it can reach the live
// window. The golden is the artifact-facing proof the request shape
// holds byte-for-byte, not only field-by-field.
func TestContractForMatchesGolden(t *testing.T) {
	lease := cutovercontract.Lease{
		ClaimID: "c1", LeaseID: "w1", FencingToken: 1193046,
		ScopeID: "scope-1", SourceRunID: "sr-1", GenerationID: "gen-1",
		SourceSystem: "src-1",
		TenantID:     "t-1", WorkspaceID: "w-1", SubjectClass: "s-1",
		PolicyRevisionHash: "p-1",
	}
	raw := contractFor(testConfig(), lease)
	const golden = "testdata/child-request.json"
	if os.Getenv("UPDATE_GOLDEN") != "" {
		if err := os.WriteFile(golden, append(raw, '\n'), 0o644); err != nil {
			t.Fatalf("write golden: %v", err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	if string(raw)+"\n" != string(want) {
		t.Errorf("contract bytes differ from %s:\n got: %s\nwant: %s", golden, raw, want)
	}
}

// TestContractForCarriesDivergentSourceSystem proves the child claim
// carries the planned source system byte-exact even when it differs from
// the collector kind: the two are independent planned facts (the live
// window only ever showed them equal by planner coincidence), and the
// satellite door requires the echo to match the plan.
func TestContractForCarriesDivergentSourceSystem(t *testing.T) {
	cfg := testConfig()
	lease := cutovercontract.Lease{
		ClaimID: "c1", LeaseID: "w1", FencingToken: 1193046,
		ScopeID: "scope-1", SourceRunID: "sr-1", GenerationID: "gen-1",
		SourceSystem: "src-9",
		TenantID:     "t-1", WorkspaceID: "w-1", SubjectClass: "s-1",
		PolicyRevisionHash: "p-1",
	}
	raw := contractFor(cfg, lease)
	var req struct {
		Claim map[string]json.RawMessage `json:"claim"`
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		t.Fatalf("contract is not a request: %v", err)
	}
	var kind, system string
	if err := json.Unmarshal(req.Claim["collector_kind"], &kind); err != nil || kind != "component" {
		t.Errorf("contract kind = %s, want component (from config scopes)", req.Claim["collector_kind"])
	}
	if err := json.Unmarshal(req.Claim["source_system"], &system); err != nil || system != "src-9" {
		t.Errorf("contract source_system = %s, want the planned src-9 from the lease", req.Claim["source_system"])
	}
}

// TestLoopTerminalOnBlankSourceSystem proves a grant without a planned
// source system fails terminal before any child launch: the satellite
// door would refuse the echo, so running the child can never commit and
// only burns the hold. The verdict for #8877 orders terminal, never a
// retryable spin.
func TestLoopTerminalOnBlankSourceSystem(t *testing.T) {
	runner := &fakeRunner{run: func(context.Context, []byte) ([]byte, error) {
		return []byte(`{}`), nil
	}}
	lease := testLease()
	lease.SourceSystem = ""
	plane := &fakePlane{answers: []claimAnswer{{lease: lease, found: true}}}
	if err := runLoop(t, testConfig(), runner, plane, testClaimTimeout); err != nil {
		t.Fatalf("Loop: %v", err)
	}
	ops := opsAfterClaim(plane.ops())
	if len(ops) == 0 || ops[0] != "FailTerminal" {
		t.Errorf("ops after claim = %v, want FailTerminal first", ops)
	}
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if len(runner.contracts) != 0 {
		t.Errorf("child launched %d times, want zero (blank source system fails before launch)", len(runner.contracts))
	}
}

// TestContractForCarriesConfiguredScopeKind proves the child claim scope
// carries the configured scope kind, not the collector kind and not the
// scope id: the satellite door requires the echo, and kind, id, and kind
// of scope are three independent facts (the live window only ever showed
// all three equal by domain coincidence).
func TestContractForCarriesConfiguredScopeKind(t *testing.T) {
	cfg := testConfig()
	cfg.Scopes = "component"
	cfg.ScopeKind = "scope-kind-9"
	lease := testLease()
	raw := contractFor(cfg, lease)
	var req struct {
		Claim struct {
			Kind  string `json:"collector_kind"`
			Scope struct {
				ID   string `json:"id"`
				Kind string `json:"kind"`
			} `json:"scope"`
		} `json:"claim"`
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		t.Fatalf("contract is not a request: %v", err)
	}
	if req.Claim.Kind != "component" || req.Claim.Scope.ID != "scope-1" || req.Claim.Scope.Kind != "scope-kind-9" {
		t.Errorf("contract claim = %+v, want kind component, scope scope-1/scope-kind-9", req.Claim)
	}
}
