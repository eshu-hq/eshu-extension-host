// SPDX-License-Identifier: MIT
// Copyright (c) 2026 eshu-hq

// Cutover-gate acceptance for the extension-host binary
// (eshu-hq/eshu#8877): the worker loop stays dark by default and runs
// only behind --claims-enabled or ESHU_EXTENSIONHOST_CUTOVER_ENABLED=1,
// with --dry-run replacing the commit step by a compare sink.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/eshu-hq/eshu-extension-host/internal/cutovercontract"
)

func TestCutoverGateMatrix(t *testing.T) {
	t.Setenv("ESHU_EXTENSIONHOST_CUTOVER_ENABLED", "")
	cases := []struct {
		name string
		env  string
		args []string
		want bool
	}{
		{"dark by default", "", nil, false},
		{"flag enables without env", "", []string{"--claims-enabled"}, true},
		{"env enables without flag", "1", nil, true},
		{"flag and env agree", "1", []string{"--claims-enabled"}, true},
		{"explicit false flag with env still enables", "1", []string{"--claims-enabled=false"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("ESHU_EXTENSIONHOST_CUTOVER_ENABLED", tc.env)
			cfg, err := parseFlags(tc.args)
			if err != nil {
				t.Fatalf("parseFlags(%v) error = %v", tc.args, err)
			}
			if cfg.claimsEnabled != tc.want {
				t.Fatalf("claimsEnabled = %v, want %v", cfg.claimsEnabled, tc.want)
			}
		})
	}
}

func TestCutoverEnvRejectsGarbage(t *testing.T) {
	t.Setenv("ESHU_EXTENSIONHOST_CUTOVER_ENABLED", "true")
	if _, err := parseFlags(nil); err == nil {
		t.Fatal("parseFlags with ESHU_EXTENSIONHOST_CUTOVER_ENABLED=true = nil error, want fail-closed naming the variable")
	} else if !strings.Contains(err.Error(), "ESHU_EXTENSIONHOST_CUTOVER_ENABLED") {
		t.Fatalf("error %q does not name the variable", err)
	}
}

func TestDryRunRequiresEnabled(t *testing.T) {
	t.Setenv("ESHU_EXTENSIONHOST_CUTOVER_ENABLED", "")
	if _, err := parseFlags([]string{"--dry-run"}); err == nil {
		t.Fatal("parseFlags(--dry-run) without enablement = nil error, want fail-closed")
	}
}

func TestDryRunDefaultsOffWhenEnabled(t *testing.T) {
	t.Setenv("ESHU_EXTENSIONHOST_CUTOVER_ENABLED", "")
	cfg, err := parseFlags([]string{"--claims-enabled"})
	if err != nil {
		t.Fatalf("parseFlags error = %v", err)
	}
	if cfg.dryRun {
		t.Fatal("dryRun = true, want false by default (explicit opt-in only)")
	}
}

// recordingCommitter proves what the dry-run sink does and does not call.
type recordingCommitter struct {
	mu        sync.Mutex
	completed int
	retried   int
	terminal  int
	results   [][]byte
}

func (f *recordingCommitter) Complete(_ context.Context, _ cutovercontract.Lease, result []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.completed++
	f.results = append(f.results, result)
	return nil
}

func (f *recordingCommitter) FailRetryable(_ context.Context, _ cutovercontract.Lease) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.retried++
	return nil
}

func (f *recordingCommitter) FailTerminal(_ context.Context, _ cutovercontract.Lease) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.terminal++
	return nil
}

func TestCommitterSatisfiedBySinkAndClient(t *testing.T) {
	var _ Committer = (*compareSink)(nil)
	var _ Committer = (*recordingCommitter)(nil)
}

func TestCompareSinkNeverCallsComplete(t *testing.T) {
	downstream := &recordingCommitter{}
	var logged lockedBuffer
	sink := newCompareSink(downstream, &logged)
	lease := cutovercontract.Lease{ClaimID: "c1", LeaseID: "l1"}
	result := []byte(`{"ok":true}`)
	if err := sink.Complete(context.Background(), lease, result); err != nil {
		t.Fatalf("sink.Complete error = %v", err)
	}
	if downstream.completed != 0 {
		t.Fatalf("downstream Complete calls = %d, want 0 (shadow mode never commits)", downstream.completed)
	}
	sum := sha256.Sum256(result)
	if got := logged.String(); !strings.Contains(got, fmt.Sprintf("%d", len(result))) ||
		!strings.Contains(got, hex.EncodeToString(sum[:])) {
		t.Fatalf("sink log %q lacks result byte count %d or sha256", got, len(result))
	}
	if err := sink.FailRetryable(context.Background(), lease); err != nil {
		t.Fatalf("sink.FailRetryable error = %v", err)
	}
	if err := sink.FailTerminal(context.Background(), lease); err != nil {
		t.Fatalf("sink.FailTerminal error = %v", err)
	}
	if downstream.retried != 1 || downstream.terminal != 1 {
		t.Fatalf("fail-path delegation = retryable %d terminal %d, want 1/1 (only Complete is shadowed)",
			downstream.retried, downstream.terminal)
	}
}

func TestEnabledConstructionErrorFailsWithoutValues(t *testing.T) {
	secret := "bogus-value-xyz-9f8"
	t.Setenv("ESHU_EXTENSIONHOST_CUTOVER_ENABLED", "")
	t.Setenv("ESHU_EXTENSIONHOST_CUTOVER_PRODUCER_ID", "test-producer")
	t.Setenv("ESHU_EXTENSIONHOST_CUTOVER_PRODUCER_VERSION", "v0.1.0")
	t.Setenv("ESHU_WORKFLOW_CLAIM_PLANE_URL", secret)
	t.Setenv("ESHU_EXTENSIONHOST_COLLECTOR_PATH", "sh")
	var stderr lockedBuffer
	code := runWithContext(context.Background(),
		[]string{"--claims-enabled", "--admin-listen=127.0.0.1:0"}, io.Discard, &stderr)
	if code == 0 {
		t.Fatal("run(enabled, bad plane URL) exit = 0, want nonzero startup failure")
	}
	if got := stderr.String(); !strings.Contains(got, "ESHU_WORKFLOW_CLAIM_PLANE_URL") {
		t.Fatalf("stderr %q does not name the offending variable", got)
	}
	if got := stderr.String(); strings.Contains(got, secret) {
		t.Fatalf("stderr leaks the configured value %q", secret)
	}
}

func TestHealthzReflectsMode(t *testing.T) {
	for _, tc := range []struct {
		enabled      bool
		wantMode     string
		wantClaimBit string
	}{
		{false, `"mode":"dark"`, `"claims_enabled":0`},
		{true, `"mode":"enabled"`, `"claims_enabled":1`},
	} {
		mux := newMux("test-version", time.Unix(1_700_000_000, 0), tc.enabled)
		req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("enabled=%v /healthz = %d, want 200", tc.enabled, rec.Code)
		}
		if body := rec.Body.String(); !strings.Contains(body, tc.wantMode) ||
			!strings.Contains(body, tc.wantClaimBit) {
			t.Fatalf("enabled=%v /healthz = %q, want %s and %s",
				tc.enabled, body, tc.wantMode, tc.wantClaimBit)
		}
		req = httptest.NewRequest(http.MethodGet, "/metrics", nil)
		rec = httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		wantGauge := "extension_host_claims_enabled 0"
		if tc.enabled {
			wantGauge = "extension_host_claims_enabled 1"
		}
		if body := rec.Body.String(); !strings.Contains(body, wantGauge) {
			t.Fatalf("enabled=%v /metrics lacks %q: %q", tc.enabled, wantGauge, body)
		}
	}
}

// emptyQueuePlane answers every ClaimNext with the spec empty-queue 404 so
// the loop polls without ever invoking the runner or the commit path.
func emptyQueuePlane(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"code":"not_found","message":"empty"}`)
	}))
}

func TestSignalCancelStopsEnabledLoop(t *testing.T) {
	plane := emptyQueuePlane(t)
	defer plane.Close()
	t.Setenv("ESHU_EXTENSIONHOST_CUTOVER_ENABLED", "")
	t.Setenv("ESHU_EXTENSIONHOST_CUTOVER_PRODUCER_ID", "test-producer")
	t.Setenv("ESHU_EXTENSIONHOST_CUTOVER_PRODUCER_VERSION", "v0.1.0")
	t.Setenv("ESHU_WORKFLOW_CLAIM_PLANE_URL", plane.URL)
	t.Setenv("ESHU_COMPONENT_COLLECTOR_POLL_INTERVAL", "10ms")
	t.Setenv("ESHU_EXTENSIONHOST_COLLECTOR_PATH", "sh")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var stderr lockedBuffer
	done := make(chan int, 1)
	go func() {
		done <- runWithContext(ctx,
			[]string{"--claims-enabled", "--dry-run", "--admin-listen=127.0.0.1:0"},
			io.Discard, &stderr)
	}()
	addr := ""
	for i := 0; i < 200; i++ {
		addr = scanAddr(stderr.String())
		if addr != "" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if addr == "" {
		t.Fatalf("no startup addr logged in %q", stderr.String())
	}
	health := ""
	for i := 0; i < 100; i++ {
		//nolint:G107,NOSEC // test-local loopback probe at the logged enabled addr.
		resp, err := http.Get("http://" + addr + "/healthz")
		if err == nil {
			body, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			health = string(body)
			if resp.StatusCode == http.StatusOK && strings.Contains(health, `"claims_enabled":1`) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !strings.Contains(health, `"claims_enabled":1`) {
		t.Fatalf("enabled /healthz = %q, want claims_enabled 1", health)
	}
	cancel()
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("enabled run exit = %d, want 0 after cancel", code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("enabled run did not exit after context cancel")
	}
}
