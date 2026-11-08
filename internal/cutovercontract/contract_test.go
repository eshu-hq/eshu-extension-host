// SPDX-License-Identifier: MIT
// Copyright (c) 2026 eshu-hq

package cutovercontract

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"
)

var errTestSentinel = errors.New("test: sentinel")

// fakeRunner and fakePlane prove the seam interfaces are implementable and
// wire-compatible: the worker loop (a later lane) depends only on these
// shapes, never on a concrete adapter or plane client.

type fakeRunner struct{ result []byte }

func (f fakeRunner) RunCollector(_ context.Context, contract []byte) ([]byte, error) {
	if len(contract) == 0 {
		return nil, errTestSentinel
	}
	return f.result, nil
}

type fakePlane struct{ done bool }

func (f *fakePlane) ClaimNext(_ context.Context, req ClaimRequest) (Lease, bool, error) {
	if req.Selector.ClaimID == "" {
		return Lease{}, false, errTestSentinel
	}
	return Lease{ClaimID: "c1", LeaseID: "l1"}, true, nil
}

func (f *fakePlane) Heartbeat(_ context.Context, _ Lease) error { return nil }

func (f *fakePlane) CommitClaimFacts(_ context.Context, _ Lease, result []byte) error { return nil }

func (f *fakePlane) Complete(_ context.Context, _ Lease, result []byte) error {
	if len(result) == 0 {
		return errTestSentinel
	}
	f.done = true
	return nil
}

func (f *fakePlane) Release(_ context.Context, _ Lease) error       { return nil }
func (f *fakePlane) FailRetryable(_ context.Context, _ Lease) error { return nil }
func (f *fakePlane) FailTerminal(_ context.Context, _ Lease) error  { return nil }

func TestSeamFakesSatisfyInterfaces(t *testing.T) {
	var _ Runner = fakeRunner{}
	var _ ClaimPlane = &fakePlane{}
}

// wireKeys marshals v and returns its top-level JSON object keys.
func wireKeys(t *testing.T, v any) []string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return keysOf(t, raw)
}

// keysOf returns the top-level keys of a JSON object document.
func keysOf(t *testing.T, raw json.RawMessage) []string {
	t.Helper()
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		t.Fatalf("remarshal %s: %v", raw, err)
	}
	return mapKeys(obj)
}

// mapKeys returns the keys of an already-decoded JSON object.
func mapKeys(obj map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}
	return keys
}

func sameKeys(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	set := make(map[string]bool, len(a))
	for _, k := range a {
		set[k] = true
	}
	for _, k := range b {
		if !set[k] {
			return false
		}
	}
	return true
}

// TestClaimSelectorWireNames pins the selector JSON names against the spec
// ClaimSelector (workflowv1/claim.go:12-21): collector_kind,
// collector_instance_id, owner_id, claim_id, rotation_salt.
func TestClaimSelectorWireNames(t *testing.T) {
	sel := ClaimSelector{
		CollectorKind:       "component",
		CollectorInstanceID: "test-producer",
		OwnerID:             "test-producer",
		ClaimID:             "c1",
	}
	if got, want := wireKeys(t, sel), []string{"collector_kind", "collector_instance_id", "owner_id", "claim_id"}; !sameKeys(got, want) {
		t.Fatalf("selector keys = %v, want %v", got, want)
	}
	sel.RotationSalt = "salt"
	if got, want := wireKeys(t, sel), []string{"collector_kind", "collector_instance_id", "owner_id", "claim_id", "rotation_salt"}; !sameKeys(got, want) {
		t.Fatalf("selector keys with salt = %v, want %v", got, want)
	}
}

// TestClaimRequestWireNames pins the request JSON names against the spec
// ClaimNextRequest (workflowv1/claim.go:133-136): selector, observed_at,
// lease_ttl_ns.
func TestClaimRequestWireNames(t *testing.T) {
	req := ClaimRequest{
		Selector:   ClaimSelector{CollectorKind: "k", CollectorInstanceID: "i", OwnerID: "o", ClaimID: "c"},
		ObservedAt: time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC),
		LeaseTTL:   60 * time.Second,
	}
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		t.Fatalf("remarshal %s: %v", raw, err)
	}
	if got, want := mapKeys(obj), []string{"selector", "observed_at", "lease_ttl_ns"}; !sameKeys(got, want) {
		t.Fatalf("request keys = %v, want %v", got, want)
	}
	var ttl time.Duration
	if err := json.Unmarshal(obj["lease_ttl_ns"], &ttl); err != nil || ttl != 60*time.Second {
		t.Fatalf("lease_ttl_ns = %s, want 60000000000 (nanoseconds)", obj["lease_ttl_ns"])
	}
	if string(obj["observed_at"]) != `"2026-11-06T12:00:00Z"` {
		t.Fatalf("observed_at = %s, want RFC3339", obj["observed_at"])
	}
	var nested map[string]json.RawMessage
	if err := json.Unmarshal(obj["selector"], &nested); err != nil {
		t.Fatalf("selector remarshal: %v", err)
	}
	if got, want := mapKeys(nested), []string{"collector_kind", "collector_instance_id", "owner_id", "claim_id"}; !sameKeys(got, want) {
		t.Fatalf("nested selector keys = %v, want %v", got, want)
	}
	if !reflect.DeepEqual(nested["claim_id"], json.RawMessage(`"c"`)) {
		t.Fatalf("nested claim_id = %s, want %q", nested["claim_id"], "c")
	}
}

// TestClaimMutationWireNames pins the mutation JSON names to the spec
// fenced epoch (eshu-workflow@v0.2.0 workflowv1/claim.go:42):
// work_item_id, claim_id, fencing_token, owner_id, observed_at,
// lease_duration_ns. The pre-conformance host-seam names (producer_id,
// lease_id, result_json) have no spec slot — the live plane rejects
// them — so they must not appear here.
func TestClaimMutationWireNames(t *testing.T) {
	out := ClaimMutation{
		WorkItemID: "w", ClaimID: "c", FencingToken: 7,
		OwnerID: "p", ObservedAt: time.Date(2026, 11, 8, 1, 0, 0, 0, time.UTC),
		LeaseDuration: 60 * time.Second,
	}
	want := []string{"work_item_id", "claim_id", "fencing_token", "owner_id", "observed_at", "lease_duration_ns"}
	if got := wireKeys(t, out); !sameKeys(got, want) {
		t.Fatalf("mutation keys = %v, want %v", got, want)
	}
}
