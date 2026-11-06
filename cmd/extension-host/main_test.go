// SPDX-License-Identifier: MIT
// Copyright (c) 2026 eshu-hq

// Dark-posture acceptance for the extension-host binary
// (eshu-hq/eshu#8877 Leaf 2): the binary serves operator health only and
// carries no enablement path. Any claim-flow flag is a startup error, a
// bad listen address fails closed, and the metrics surface proves zero
// claim flow observably.
package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// lockedBuffer is a race-safe strings.Builder for capturing log output
// from a server goroutine while the test polls it.
type lockedBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func TestParseFlagsDefaultsToDarkAdminListen(t *testing.T) {
	cfg, err := parseFlags(nil)
	if err != nil {
		t.Fatalf("parseFlags(nil) error = %v", err)
	}
	if cfg.adminListen != defaultAdminListen {
		t.Fatalf("adminListen = %q, want default %q", cfg.adminListen, defaultAdminListen)
	}
	if cfg.showVersion {
		t.Fatal("showVersion = true, want false by default")
	}
}

func TestParseFlagsExposeNoEnablementPath(t *testing.T) {
	// The cutover gate (eshu-hq/eshu#8877) adds exactly two flags to the
	// dark surface: --claims-enabled and --dry-run. The plane URL, lease
	// timings, and producer identity stay environment-only by design, so
	// they never appear here.
	names := flagNames()
	want := []string{"admin-listen", "claims-enabled", "dry-run", "version"}
	if len(names) != len(want) {
		t.Fatalf("flag set = %v, want exactly %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("flag set = %v, want exactly %v", names, want)
		}
	}
}

func TestParseFlagsRejectsClaimEnablement(t *testing.T) {
	// --claims-enabled is now the accepted gate; the plane URL and the
	// deployment mode stay unknown flags and fail closed at parse time.
	for _, args := range [][]string{
		{"--claim-plane-url=http://eshu-workflow-claim-plane:8080"},
		{"--deployment-mode=active"},
	} {
		if _, err := parseFlags(args); err == nil {
			t.Fatalf("parseFlags(%v) = nil error, want fail-closed", args)
		}
	}
}

// TestVersionFlagReportsBuildVersion covers the --version flag plumbing:
// the flag prints the compiled-in build version and exits 0. It does NOT
// prove the release ldflags stamp reaches the binary (in-process `version`
// is whatever the test binary was built with); that path is exercised by
// the `image` lane in ci.yml, which builds with a known VERSION and
// asserts /healthz reports it.
func TestVersionFlagReportsBuildVersion(t *testing.T) {
	var stdout strings.Builder
	if code := run([]string{"--version"}, &stdout, io.Discard); code != 0 {
		t.Fatalf("run(--version) exit = %d, want 0", code)
	}
	if !strings.Contains(stdout.String(), version) {
		t.Fatalf("version output %q does not contain build version %q", stdout.String(), version)
	}
}

func TestBadListenAddrFailsClosed(t *testing.T) {
	var stderr strings.Builder
	if code := run([]string{"--admin-listen=://bad-addr"}, io.Discard, &stderr); code == 0 {
		t.Fatal("run(bad addr) exit = 0, want nonzero")
	}
	if stderr.Len() == 0 {
		t.Fatal("run(bad addr) wrote no diagnostic to stderr")
	}
}

func TestDarkServerEndpoints(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- serve(ctx, ln, "test-version", time.Unix(1_700_000_000, 0), false)
	}()
	base := fmt.Sprintf("http://%s", ln.Addr().String())

	get := func(path string) (int, string) {
		t.Helper()
		var resp *http.Response
		var err error
		for i := 0; i < 50; i++ {
			//nolint:G107 // test-local loopback probe against a 127.0.0.1 listener owned by this test.
			req, reqErr := http.NewRequestWithContext(context.Background(), http.MethodGet, base+path, nil)
			if reqErr != nil {
				t.Fatalf("GET %s: %v", path, reqErr)
			}
			resp, err = http.DefaultClient.Do(req)
			if err == nil {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		defer func() { _ = resp.Body.Close() }()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("GET %s read: %v", path, err)
		}
		return resp.StatusCode, string(body)
	}

	if code, body := get("/healthz"); code != http.StatusOK || !strings.Contains(body, `"mode":"dark"`) {
		t.Fatalf("/healthz = %d %q, want 200 with dark mode", code, body)
	}
	if code, _ := get("/readyz"); code != http.StatusOK {
		t.Fatalf("/readyz = %d, want 200", code)
	}
	if code, body := get("/metrics"); code != http.StatusOK ||
		!strings.Contains(body, "extension_host_claims_enabled 0") ||
		!strings.Contains(body, `extension_host_build_info{version="test-version"} 1`) {
		t.Fatalf("/metrics = %d %q, want build info plus zero claim flow", code, body)
	}
	if code, _ := get("/claims"); code != http.StatusNotFound {
		t.Fatalf("/claims = %d, want 404 (no claim surface exists)", code)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serve after cancel: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serve did not stop after context cancel")
	}
}

func TestServeOnClosedListenerFails(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	if err := ln.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := serve(context.Background(), ln, version, time.Now(), false); err == nil {
		t.Fatal("serve(closed listener) = nil, want an error")
	}
}

func TestRunServesDarkAndShutsDownCleanly(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var stderr lockedBuffer
	done := make(chan int, 1)
	go func() {
		done <- runWithContext(ctx, []string{"--admin-listen=127.0.0.1:0"}, io.Discard, &stderr)
	}()
	addr := ""
	for i := 0; i < 100; i++ {
		addr = scanAddr(stderr.String())
		if addr != "" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if addr == "" {
		t.Fatalf("no startup addr logged in %q", stderr.String())
	}
	resp, err := http.Get("http://" + addr + "/healthz") //nolint:G107 // test-local loopback probe at the logged dark addr.
	if err != nil {
		t.Fatalf("GET healthz: %v", err)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read healthz: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"mode":"dark"`) {
		t.Fatalf("healthz = %d %q, want 200 dark", resp.StatusCode, body)
	}
	cancel()
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("run exit = %d, want 0", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run did not exit after context cancel")
	}
}

// scanAddr extracts the addr value from the JSON startup log line the
// binary emits before serving.
func scanAddr(logged string) string {
	const key = `"addr":"`
	i := strings.Index(logged, key)
	if i < 0 {
		return ""
	}
	rest := logged[i+len(key):]
	j := strings.Index(rest, `"`)
	if j < 0 {
		return ""
	}
	return rest[:j]
}
