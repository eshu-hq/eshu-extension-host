// SPDX-License-Identifier: MIT
// Copyright (c) 2026 eshu-hq

package claimplane

import (
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/eshu-hq/eshu-extension-host/internal/cutovercontract"
)

// ErrGrantDenied reports a refused producer grant at admission. It is the
// seam-wide sentinel: an alias of cutovercontract.ErrGrantDenied, so every
// grant denial on either side of the seam answers the same errors.Is
// admission check; the *DenyError reason names the cause.
var ErrGrantDenied = cutovercontract.ErrGrantDenied

// Grant reasons mirror the in-tree component grant vocabulary as plain
// strings (this reduction carries no component dependency).
const (
	ReasonGranted          = "granted"
	ReasonRevoked          = "revoked"
	ReasonExpired          = "expired"
	ReasonNoMatchingGrant  = "no_matching_grant"
	ReasonScopeMismatch    = "scope_mismatch"
	ReasonGrantsUnreadable = "grants_unreadable"
)

// DenyError is one fail-closed grant denial. It names the holder identity
// and the reason only: never payloads or credentials.
type DenyError struct {
	Reason     string
	ProducerID string
}

func (e *DenyError) Error() string {
	return fmt.Sprintf("claimplane: producer grant denied for %q: %s", e.ProducerID, e.Reason)
}

// Unwrap exposes ErrGrantDenied for errors.Is admission checks.
func (e *DenyError) Unwrap() error { return ErrGrantDenied }

// Revoked reports a deny where every matching grant is revoked.
func (e *DenyError) Revoked() bool { return e.Reason == ReasonRevoked }

// Expired reports a deny where every unrevoked matching grant is past its
// expiry at the evaluation clock.
func (e *DenyError) Expired() bool { return e.Reason == ReasonExpired }

// Protocol codes mirror the workflow.v1 error vocabulary, plus the
// client-side response_too_large bound (never sent by the server).
const (
	CodeInvalid          = "invalid"
	CodeNotFound         = "not_found"
	CodeClaimConflict    = "claim_conflict"
	CodeStaleFence       = "stale_fence"
	CodeLeaseExpired     = "lease_expired"
	CodePaced            = "paced"
	CodeVersionMismatch  = "version_mismatch"
	CodeResponseTooLarge = "response_too_large"
	CodeUnexpectedStatus = "unexpected_status"
	CodeTransport        = "transport"
)

// maxErrorMessage bounds one surfaced server message.
const maxErrorMessage = 512

// CallError is one failed protocol call. Code is the stable contract;
// Message is bounded operator detail that never carries credentials.
type CallError struct {
	Op         string
	Code       string
	Message    string
	RetryAfter time.Duration
}

func (e *CallError) Error() string {
	return fmt.Sprintf("%s %s: %s", e.Op, e.Code, e.Message)
}

// Stale reports a 409-class conflict: the held epoch is no longer honored.
// The caller must re-claim, never retry the same mutation.
func (e *CallError) Stale() bool {
	return e.Code == CodeStaleFence || e.Code == CodeLeaseExpired || e.Code == CodeClaimConflict
}

// Paced reports flow control: no claim was issued; back off for RetryAfter.
func (e *CallError) Paced() bool { return e.Code == CodePaced }

// NotFound reports a missing row (on ClaimNext: the empty queue, which the
// client translates to found=false before a CallError is built).
func (e *CallError) NotFound() bool { return e.Code == CodeNotFound }

// VersionMismatch reports a protocol-major disagreement.
func (e *CallError) VersionMismatch() bool { return e.Code == CodeVersionMismatch }

// boundMessage truncates surfaced detail to the operator bound on a rune
// boundary. There is no auth token in this reduction, so no scrub step.
func boundMessage(message string) string {
	trimmed := message
	if len(trimmed) <= maxErrorMessage {
		return trimmed
	}
	cut := maxErrorMessage
	for cut > 0 && !utf8.RuneStart(trimmed[cut]) {
		cut--
	}
	return trimmed[:cut] + "…"
}
