// SPDX-License-Identifier: MIT
// Copyright (c) 2026 eshu-hq

// Child-claim field tests: the attempt count, deadline, and config
// handle reach the child claim, because the commit door requires all
// three on the echoed claim and the child artifact echoes the host
// claim verbatim. Attempt and deadline forward the granted values;
// the handle is static configuration, pinned like the producer id.
package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/eshu-hq/eshu-extension-host/internal/cutovercontract"
)

// TestContractForRendersClaimFields proves the child claim carries
// the granted attempt and deadline with the configured handle: the
// door turns away any commit whose echo misses them.
func TestContractForRendersClaimFields(t *testing.T) {
	deadline := time.Date(2026, 11, 8, 21, 0, 0, 0, time.UTC)
	cfg := Config{
		ProducerID:          "test-producer",
		CollectorInstanceID: "test-instance",
		Scopes:              "sbom_attestation",
		ScopeKind:           "sbom_attestation",
		ConfigHandle:        "rehearsal:test",
	}
	lease := cutovercontract.Lease{
		LeaseID:      "w1",
		FencingToken: 1193046,
		ScopeID:      "scope-1",
		SourceRunID:  "sr-1",
		GenerationID: "gen-1",
		SourceSystem: "src-1",
		Attempt:      41,
		Deadline:     deadline,
	}
	var decoded childRequest
	if err := json.Unmarshal(contractFor(cfg, lease), &decoded); err != nil {
		t.Fatalf("contractFor decode: %v", err)
	}
	if decoded.Claim.Attempt != 41 {
		t.Errorf("child attempt = %d, want the granted 41", decoded.Claim.Attempt)
	}
	if !decoded.Claim.Deadline.Equal(deadline) {
		t.Errorf("child deadline = %v, want the granted %v", decoded.Claim.Deadline, deadline)
	}
	if decoded.Claim.ConfigHandle != "rehearsal:test" {
		t.Errorf("child config handle = %q, want the configured rehearsal:test", decoded.Claim.ConfigHandle)
	}
}

// TestLoopRequiresConfigHandle proves the loop fails closed without
// the static config handle: every commit from such a worker would
// fail at the door, so starting up is the error, not the first
// claim.
func TestLoopRequiresConfigHandle(t *testing.T) {
	cfg := Config{
		ProducerID:          "test-producer",
		ProducerVersion:     "1.0.0",
		CollectorInstanceID: "test-instance",
		Scopes:              "sbom_attestation",
		ScopeKind:           "sbom_attestation",
		ClaimPlaneURL:       "http://example.com",
		PollInterval:        time.Second,
		HeartbeatInterval:   time.Second,
		LeaseTTL:            3 * time.Second,
	}
	// Collaborators come first so the missing handle, not a nil
	// collaborator, is what fails the call.
	err := Loop(t.Context(), cfg, &fakeRunner{}, &fakePlane{})
	if err == nil || !strings.Contains(err.Error(), "config handle") {
		t.Fatalf("Loop without handle = %v, want fail-closed naming config handle", err)
	}
}

// TestFailClaimLogsCause proves a failed claim leaves the cause in
// the host log: the plane outcome rows carry no cause text, so this
// line is the only record of why the door turned a commit away.
func TestFailClaimLogsCause(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	plane := &fakePlane{}
	cause := errors.New("commit claim facts: result state exploded")
	failClaim(context.Background(), plane, cutovercontract.Lease{ClaimID: "c9"}, cause, log)
	line := buf.String()
	if !strings.Contains(line, "exploded") || !strings.Contains(line, "c9") {
		t.Errorf("failClaim log = %q, want the cause text and the claim id", line)
	}
}

// TestFailClaimNilLoggerStaysSilent proves an unwired logger never
// breaks outcome reporting: the plane calls still land.
func TestFailClaimNilLoggerStaysSilent(t *testing.T) {
	plane := &fakePlane{}
	failClaim(context.Background(), plane, cutovercontract.Lease{ClaimID: "c9"}, errors.New("boom"), nil)
	for _, want := range []string{"FailRetryable", "Release"} {
		found := false
		for _, op := range plane.ops() {
			if op == want {
				found = true
			}
		}
		if !found {
			t.Errorf("plane ops = %v, want %s recorded", plane.ops(), want)
		}
	}
}
