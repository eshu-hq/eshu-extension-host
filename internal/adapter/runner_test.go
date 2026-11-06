// SPDX-License-Identifier: MIT
// Copyright (c) 2026 eshu-hq

// Adapter acceptance for the dark extension host (eshu-hq/eshu#8877):
// ProcessRunner speaks the worker-loop runner seam over stdio using only
// the standard library. Child processes are POSIX sh scripts so the suite
// runs anywhere sh exists, including with -race and GOMAXPROCS=2.
package adapter

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// contractRunner mirrors cutovercontract.Runner method-for-method. The
// adapter package stays importable by the stdlib-only dark binary, so it
// cannot import the contract package (see cmd/extension-host
// TestModuleImportsAreStdlibOnly); this assignment proves the method set
// matches the seam the worker-loop lane will bind against.
type contractRunner interface {
	RunCollector(ctx context.Context, contractJSON []byte) (resultJSON []byte, err error)
}

func TestRunnerMatchesContractSignature(t *testing.T) {
	var _ contractRunner = ProcessRunner{}
}

func TestRunCollectorRoundTrip(t *testing.T) {
	r := ProcessRunner{Path: "sh", Args: []string{"-c", `printf '{"ok":true,"n":1}'`}}
	got, err := r.RunCollector(context.Background(), []byte(`{"claim":"c1"}`))
	if err != nil {
		t.Fatalf("RunCollector error = %v", err)
	}
	if string(got) != `{"ok":true,"n":1}` {
		t.Fatalf("RunCollector result = %q, want %q", got, `{"ok":true,"n":1}`)
	}
}

func TestRunCollectorDeliversStdin(t *testing.T) {
	payload := `{"claim":"c1","producer":"dev.eshu.test"}`
	r := ProcessRunner{Path: "sh", Args: []string{"-c", "cat"}}
	got, err := r.RunCollector(context.Background(), []byte(payload))
	if err != nil {
		t.Fatalf("RunCollector error = %v", err)
	}
	if string(got) != payload {
		t.Fatalf("RunCollector result = %q, want stdin echo %q", got, payload)
	}
}

func TestRunCollectorRejectsTrailingData(t *testing.T) {
	r := ProcessRunner{Path: "sh", Args: []string{"-c", `printf '{"ok":true} trailing'`}}
	_, err := r.RunCollector(context.Background(), []byte(`{"claim":"c1"}`))
	if err == nil {
		t.Fatal("RunCollector error = nil for trailing data, want rejection")
	}
	if !strings.Contains(err.Error(), "trailing") {
		t.Fatalf("RunCollector error = %v, want it to mention trailing data", err)
	}
}

func TestRunCollectorTruncatesBoundedStdout(t *testing.T) {
	script := `i=0; while [ "$i" -lt 100 ]; do printf '0123456789'; i=$((i+1)); done`
	r := ProcessRunner{Path: "sh", Args: []string{"-c", script}, StdoutLimit: 16}
	_, err := r.RunCollector(context.Background(), []byte(`{"claim":"c1"}`))
	if err == nil {
		t.Fatal("RunCollector error = nil for 1000-byte output past a 16-byte limit, want truncation")
	}
	if !strings.Contains(err.Error(), "truncated") {
		t.Fatalf("RunCollector error = %v, want it to report truncation", err)
	}
	if !strings.Contains(err.Error(), errTruncatedText) {
		t.Fatalf("RunCollector error = %v, want the exact sentinel %q", err, errTruncatedText)
	}
}

func TestRunCollectorDefaultLimit(t *testing.T) {
	// StdoutLimit <= 0 selects the contract 4 MiB default: a ~100-byte
	// JSON value must pass where a zero-byte cap would truncate it.
	const want = `{"pad":"01234567890123456789012345678901234567890123456789"}`
	r := ProcessRunner{Path: "sh", Args: []string{"-c", `printf '` + want + `'`}, StdoutLimit: 0}
	got, err := r.RunCollector(context.Background(), []byte(`{"claim":"c1"}`))
	if err != nil {
		t.Fatalf("RunCollector error = %v, want success under the default limit", err)
	}
	if string(got) != want {
		t.Fatalf("RunCollector result = %q, want %q", got, want)
	}
}

func TestRunCollectorEmptyPath(t *testing.T) {
	r := ProcessRunner{}
	_, err := r.RunCollector(context.Background(), []byte(`{"claim":"c1"}`))
	if err == nil {
		t.Fatal("RunCollector error = nil for empty path, want a launch refusal")
	}
}

func TestRedactSecrets(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		leak string
	}{
		{name: "equals", in: `launch failed api_key=hunter2-value`, leak: "hunter2-value"},
		{name: "json colon", in: `decode failed for {"api_token": "s3cr3t-value"}`, leak: "s3cr3t-value"},
		{name: "case insensitive", in: `bad SECRET=topsecret-value here`, leak: "topsecret-value"},
		{name: "quoted key", in: `"client_secret"='hidden-value'`, leak: "hidden-value"},
		{name: "password", in: `launch failed password=hunter2-value`, leak: "hunter2-value"},
		{name: "passwd", in: `launch failed passwd=hunter2-value`, leak: "hunter2-value"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := redactSecrets(tc.in)
			if strings.Contains(got, tc.leak) {
				t.Fatalf("redactSecrets(%q) = %q, still leaks %q", tc.in, got, tc.leak)
			}
		})
	}
	plain := "extension process failed: exit status 1 (no secrets here)"
	if got := redactSecrets(plain); got != plain {
		t.Fatalf("redactSecrets(%q) = %q, want it unchanged", plain, got)
	}
}

func TestRunCollectorRedactsErrorStrings(t *testing.T) {
	// The exec failure names the missing binary, so a secret-looking path
	// segment must not survive in the returned error.
	r := ProcessRunner{Path: "/nonexistent-dir/api_key=hunter2-secret-value"}
	_, err := r.RunCollector(context.Background(), []byte(`{"claim":"c1"}`))
	if err == nil {
		t.Fatal("RunCollector error = nil for a missing binary, want a launch failure")
	}
	if strings.Contains(err.Error(), "hunter2-secret-value") {
		t.Fatalf("RunCollector error = %v, leaks a secret-looking value", err)
	}
}

func TestRunCollectorContextCancelKillsChild(t *testing.T) {
	r := ProcessRunner{Path: "sh", Args: []string{"-c", `while true; do :; done`}}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := r.RunCollector(ctx, []byte(`{"claim":"c1"}`))
	if err == nil {
		t.Fatal("RunCollector error = nil for a cancelled context, want ctx.Err()")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("RunCollector error = %v, want context.DeadlineExceeded", err)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("RunCollector took %v after cancel, want the child reaped promptly", elapsed)
	}
}
