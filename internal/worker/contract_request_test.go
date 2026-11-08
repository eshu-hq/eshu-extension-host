// SPDX-License-Identifier: MIT
// Copyright (c) 2026 eshu-hq

// Child-request shape: contractFor renders the full extension request
// the collector artifact decodes.
package worker

import (
	"encoding/json"
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
		ClaimID: "c1", LeaseID: "w1", FencingToken: 9,
		ScopeID: "scope-1", SourceRunID: "sr-1", GenerationID: "gen-1",
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
	if req.ProtocolVersion == "" || len(req.Contract) == 0 {
		t.Errorf("contract %s misses protocol versions", raw)
	}
	str(req.Claim, "component_id", "test-producer")
	str(req.Claim, "instance_id", "test-instance")
	str(req.Claim, "collector_kind", "component")
	str(req.Claim, "source_run_id", "sr-1")
	str(req.Claim, "generation_id", "gen-1")
	str(req.Claim, "work_item_id", "w1")
	str(req.Claim, "fencing_token", "9")
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
