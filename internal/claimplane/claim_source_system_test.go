// SPDX-License-Identifier: MIT
// Copyright (c) 2026 eshu-hq

// Source-system forwarding tests: the grant's planned source system
// reaches the lease, and a blank one passes through for the worker loop
// to terminal (split from client_test.go at the 500-line cap).
package claimplane

import (
	"context"
	"net/http/httptest"
	"testing"
)

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

// TestClaimNextPassesBlankSourceSystemThrough proves a grant without a
// planned source system still decodes to a lease: the fail-closed
// decision lives in the worker loop (terminal before child launch,
// holding the lease the terminal outcome needs), never in the decode.
func TestClaimNextPassesBlankSourceSystemThrough(t *testing.T) {
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
	if lease.SourceSystem != "" {
		t.Errorf("lease.SourceSystem = %q, want blank passed through for the loop to terminal", lease.SourceSystem)
	}
}
