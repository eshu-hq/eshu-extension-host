// SPDX-License-Identifier: MIT
// Copyright (c) 2026 eshu-hq

// Package adapter runs one collector call as a child process over stdio
// (eshu-hq/eshu#8877 host build-out, Lane A).
//
// The shape mirrors the in-tree process runner behavior
// (eshu go/internal/collector/extensionhost/process.go: bounded stdout, a
// fail-closed truncation verdict, strict result decode, context cancel
// killing the child) reduced to stdlib-only raw JSON: the method set is
// identical to cutovercontract.Runner, which the worker-loop lane binds
// against. This package must not import the contract package — or anything
// outside the standard library — so the dark binary keeps its stdlib-only
// boundary (see cmd/extension-host TestModuleImportsAreStdlibOnly).
package adapter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"strings"
)

// defaultStdoutLimit caps one adapter stdout read. It matches the seam
// contract (cutovercontract.DefaultStdoutLimit, the in-tree 4 MiB default);
// a non-positive StdoutLimit selects it.
const defaultStdoutLimit int64 = 4 << 20

// errTruncatedText marks adapter output past the byte limit. The message
// carries byte counts and the remediation knob only; no payload bytes ever
// reach it. The worker-loop lane maps this text to the seam's fail-closed
// truncation verdict (cutovercontract.ErrTruncated).
const errTruncatedText = "cutovercontract: adapter output truncated"

// ProcessRunner launches a collector child with JSON stdin/stdout.
type ProcessRunner struct {
	// Path is the child binary (a digest-pinned artifact in production).
	Path string
	// Args are the fixed child arguments.
	Args []string
	// StdoutLimit caps stdout bytes kept per call; <= 0 selects the
	// contract default.
	StdoutLimit int64
}

// RunCollector writes contractJSON to the child stdin, reads bounded
// stdout, strict-decodes exactly one JSON value, and returns the raw
// result bytes. Output past the limit fails closed with a truncation
// error; trailing data after the first JSON value is rejected; every
// returned error is scrubbed of secret-looking values. Cancelling ctx
// kills the child and returns ctx.Err().
func (r ProcessRunner) RunCollector(ctx context.Context, contractJSON []byte) ([]byte, error) {
	if strings.TrimSpace(r.Path) == "" {
		return nil, errors.New(redactSecrets("adapter command is required"))
	}
	cmd := exec.CommandContext(ctx, r.Path, r.Args...)
	cmd.Stdin = bytes.NewReader(contractJSON)
	// Child stderr is discarded by design: error strings carry counts and
	// knob names only, never child output bytes, so a chatty or hostile
	// child cannot smuggle payload material into logs or failure rows.
	cmd.Stderr = io.Discard
	stdout := newLimitedBuffer(r.effectiveLimit())
	cmd.Stdout = stdout

	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New(redactSecrets(fmt.Sprintf("adapter process failed: %v", err)))
	}
	if stdout.truncated {
		return nil, fmt.Errorf(
			"adapter stdout limit exceeded: observed_bytes=%d limit_bytes=%d: raise StdoutLimit or narrow the claim scope: %s",
			stdout.total,
			stdout.limit,
			errTruncatedText,
		)
	}
	return strictDecode(stdout.buf.Bytes())
}

// effectiveLimit resolves the stdout cap for one call.
func (r ProcessRunner) effectiveLimit() int64 {
	if r.StdoutLimit <= 0 {
		return defaultStdoutLimit
	}
	return r.StdoutLimit
}

// strictDecode validates exactly one JSON value and returns the raw bytes.
// A second value — trailing data after the first — fails closed.
func strictDecode(raw []byte) ([]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, errors.New(redactSecrets(fmt.Sprintf("decode adapter result: %v", err)))
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, errors.New(redactSecrets("decode adapter result: trailing JSON value"))
	}
	return raw, nil
}

// secretKeyPattern matches key=value and "key": value shapes whose key
// looks secret-bearing (token, key, secret, password, or passwd,
// case-insensitive) so the value can be masked before the message
// leaves the runner.
var secretKeyPattern = regexp.MustCompile(`(?i)([\w.\-]*?(?:token|key|secret|password|passwd)[\w.\-]*)(["']?\s*[:=]\s*["']?)([^\s"'\,}\]]+)`)

// redactSecrets masks secret-looking values in an error string. Matching
// is key-anchored and case-insensitive; anything without a secret-bearing
// key passes through unchanged.
func redactSecrets(message string) string {
	return secretKeyPattern.ReplaceAllString(message, `${1}${2}[redacted]`)
}

// limitedBuffer keeps at most limit bytes while counting every byte
// offered, so an oversize verdict can name the observed size.
type limitedBuffer struct {
	limit     int64
	buf       bytes.Buffer
	total     int64
	truncated bool
}

func newLimitedBuffer(limit int64) *limitedBuffer {
	return &limitedBuffer{limit: limit}
}

func (b *limitedBuffer) Write(payload []byte) (int, error) {
	b.total += int64(len(payload))
	remaining := b.limit - int64(b.buf.Len())
	if remaining <= 0 {
		b.truncated = true
		return len(payload), nil
	}
	if int64(len(payload)) > remaining {
		b.truncated = true
		_, _ = b.buf.Write(payload[:remaining])
		return len(payload), nil
	}
	_, _ = b.buf.Write(payload)
	return len(payload), nil
}
