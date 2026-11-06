// SPDX-License-Identifier: MIT
// Copyright (c) 2026 eshu-hq

package cutovercontract

import (
	"context"
	"errors"
	"testing"
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

func (f *fakePlane) ClaimNext(_ context.Context, _, _ string) (Lease, bool, error) {
	return Lease{ClaimID: "c1", LeaseID: "l1"}, true, nil
}

func (f *fakePlane) Heartbeat(_ context.Context, _ Lease) error { return nil }

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
