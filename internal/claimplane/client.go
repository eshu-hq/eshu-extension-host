// SPDX-License-Identifier: MIT
// Copyright (c) 2026 eshu-hq

package claimplane

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/eshu-hq/eshu-extension-host/internal/cutovercontract"
)

// DefaultMaxResponseBytes bounds one protocol response body. Claim grants
// are small JSON documents; anything past the bound fails closed instead of
// buffering an untrusted server's output without limit.
const DefaultMaxResponseBytes = 1 << 20

// HTTP routes mirror the workflow.v1 route table (all POST with a JSON
// body). Paths are pinned here as literals so the mapping stays reviewable
// without the workflow dependency.
const (
	pathClaimNext          = "/workflow.v1/ClaimNext"
	pathHeartbeatClaim     = "/workflow.v1/HeartbeatClaim"
	pathCompleteClaim      = "/workflow.v1/CompleteClaim"
	pathReleaseClaim       = "/workflow.v1/ReleaseClaim"
	pathFailClaimRetryable = "/workflow.v1/FailClaimRetryable"
	pathFailClaimTerminal  = "/workflow.v1/FailClaimTerminal"
)

// protocolVersionHeader carries the caller protocol major on every
// request. The v0.1.0 server enforces it before any store call: a
// missing or unknown major fails the call with version_mismatch, so a
// client that omits it fails every call against the real plane.
const (
	protocolVersionHeader = "Workflow-Protocol-Version"
	protocolVersion       = "1"
)

// GrantSource re-reads the live producer-grant set. The client calls it on
// every call, so a grant revoked during execution fails the next result
// closed without rebuilding the client. Implementations must be safe for
// concurrent use. A deny carries a reason from the grant vocabulary;
// an empty reason degrades to grants_unreadable, never to an allow.
type GrantSource interface {
	Granted(producerID string) (ok bool, reason string)
}

// Config builds a Client. BaseURL, ProducerID, ProducerVersion, Scopes, and
// Grants are required; everything else has a safe default. The producer
// identity is fixed here at construction and never taken from the wire, so
// a tampered response cannot reassign which holder this client proves.
type Config struct {
	// BaseURL is the claim-plane origin, for example
	// "https://workflow.internal". Only http and https are accepted.
	BaseURL string
	// ProducerID and ProducerVersion name the granted component. Every
	// grant decision evaluates under exactly this identity.
	ProducerID      string
	ProducerVersion string
	// Scopes are the declared collector kinds. A per-call scope outside
	// this set fails closed locally with scope_mismatch before any byte
	// touches the wire; the plane still refuses out-of-scope claims.
	Scopes []string
	// Grants re-reads the live producer-grant set on every call.
	Grants GrantSource
	// MaxResponseBytes bounds one response body. Values <= 0 select
	// DefaultMaxResponseBytes.
	MaxResponseBytes int64
	// Clock supplies now for the observed_at stamp. Nil reads the wall
	// clock (UTC) on every call.
	Clock func() time.Time
}

// Client is the remote claimed-service seam: claim, heartbeat, terminal
// outcomes, and release over HTTP. A Client holds no core implementation
// handle and is safe for concurrent use. It implements
// cutovercontract.ClaimPlane exactly: the seam unification is a compile
// check, not a structural echo.
type Client struct {
	baseURL string
	http    *http.Client
	prodID  string
	version string
	scopes  []string
	clock   func() time.Time
	grants  GrantSource
	max     int64
}

// compile check: the client is the ClaimPlane seam implementation.
var _ cutovercontract.ClaimPlane = (*Client)(nil)

// NewClient validates the seam configuration fail-closed: a client that
// cannot name its holder, its scopes, or its grant source is never built.
func NewClient(cfg Config) (*Client, error) {
	base := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	endpoint, err := url.ParseRequestURI(base)
	if err != nil {
		return nil, fmt.Errorf("claimplane: base URL is required: %w", err)
	}
	if endpoint.Scheme != "http" && endpoint.Scheme != "https" {
		return nil, fmt.Errorf("claimplane: base URL scheme %q must be http or https", endpoint.Scheme)
	}
	if strings.TrimSpace(cfg.ProducerID) == "" {
		return nil, errors.New("claimplane: producer id is required")
	}
	if strings.TrimSpace(cfg.ProducerVersion) == "" {
		return nil, errors.New("claimplane: producer version is required")
	}
	if len(cfg.Scopes) == 0 {
		return nil, errors.New("claimplane: at least one declared scope is required")
	}
	if cfg.Grants == nil {
		return nil, errors.New("claimplane: grant source is required")
	}
	clock := cfg.Clock
	if clock == nil {
		clock = func() time.Time { return time.Now().UTC() }
	}
	max := cfg.MaxResponseBytes
	if max <= 0 {
		max = DefaultMaxResponseBytes
	}
	return &Client{
		baseURL: base,
		http:    &http.Client{Timeout: 30 * time.Second},
		prodID:  strings.TrimSpace(cfg.ProducerID),
		version: strings.TrimSpace(cfg.ProducerVersion),
		scopes:  append([]string(nil), cfg.Scopes...),
		clock:   clock,
		grants:  cfg.Grants,
		max:     max,
	}, nil
}

// claimNextResponse decodes the spec ClaimNextResponse read-subset
// (eshu-workflow@v0.1.0 workflowv1/claim.go:154-157): issued plus exactly
// one of a grant or a deferral. Only the fields this seam reads are
// modelled — the granted work item and claim ids, and the deferral signal
// with its retry delay. Decode is deliberately non-strict
// (json.Unmarshal: unknown fields tolerated, trailing data rejected), so
// the full server grant with its thirty-odd fields decodes while the
// pre-conformance flat shape fails closed on the missing grant half. This
// mirrors the in-tree client, which decodes with a plain Decoder.
type claimNextResponse struct {
	Issued   bool          `json:"issued"`
	Grant    claimGrant    `json:"grant"`
	Deferral claimDeferral `json:"deferral"`
}

// claimGrant is the spec ClaimedWorkItem read-subset: the work item and
// claim ids this seam maps into a Lease.
type claimGrant struct {
	WorkItem claimWorkItem `json:"work_item"`
	Claim    claimRef      `json:"claim"`
}

// claimWorkItem carries the granted work item id.
type claimWorkItem struct {
	WorkItemID string `json:"work_item_id"`
}

// claimRef carries the granted claim id.
type claimRef struct {
	ClaimID string `json:"claim_id"`
}

// claimDeferral is the spec ClaimPacingDeferral read-subset
// (eshu-workflow@v0.1.0 workflowv1/pacing.go:203-210): the pacing signal
// and the grant delay in nanoseconds the runner must wait.
type claimDeferral struct {
	Signal     string        `json:"signal"`
	RetryAfter time.Duration `json:"retry_after_ns"`
}

// ackResponse is the acknowledgement every lifecycle operation answers
// with: exactly {"ok":true}.
type ackResponse struct {
	OK bool `json:"ok"`
}

// errorEnvelope is the JSON body for every protocol failure.
type errorEnvelope struct {
	Code              string `json:"code"`
	Message           string `json:"message"`
	RetryAfterSeconds int64  `json:"retry_after_seconds,omitempty"`
}

// ClaimNext asks the plane for the next eligible work item. It returns
// found=false with a nil error on the empty-queue signal (404 not_found).
// The request is the spec ClaimNextRequest: producer identity and scopes
// arrive folded into req.Selector (there is no top-level producer slot in
// the spec), so the selector is validated against the configured holder
// before any byte touches the wire. A zero ObservedAt is stamped from the
// client clock. The answer decodes the spec ClaimNextResponse read-subset:
// issued maps the granted claim and work item ids into a Lease while an
// unissued answer carries the pacing deferral.
func (c *Client) ClaimNext(ctx context.Context, req cutovercontract.ClaimRequest) (cutovercontract.Lease, bool, error) {
	if err := ctx.Err(); err != nil {
		return cutovercontract.Lease{}, false, fmt.Errorf("claimplane ClaimNext: %w", err)
	}
	sel := req.Selector
	if strings.TrimSpace(string(sel.CollectorKind)) == "" {
		return cutovercontract.Lease{}, false, errors.New("claimplane ClaimNext: collector kind is required")
	}
	if sel.CollectorInstanceID != c.prodID || sel.OwnerID != c.prodID {
		return cutovercontract.Lease{}, false, fmt.Errorf(
			"claimplane ClaimNext: selector identity %q/%q differs from configured holder %q",
			sel.CollectorInstanceID, sel.OwnerID, c.prodID,
		)
	}
	if strings.TrimSpace(sel.ClaimID) == "" {
		return cutovercontract.Lease{}, false, errors.New("claimplane ClaimNext: selector claim id is required")
	}
	if !scopeDeclared(c.scopes, string(sel.CollectorKind)) {
		return cutovercontract.Lease{}, false, &DenyError{Reason: ReasonScopeMismatch, ProducerID: c.prodID}
	}
	if req.LeaseTTL <= 0 {
		return cutovercontract.Lease{}, false, errors.New("claimplane ClaimNext: lease TTL must be positive")
	}
	if err := c.checkGrant(ctx); err != nil {
		return cutovercontract.Lease{}, false, err
	}
	wire := req
	if wire.ObservedAt.IsZero() {
		wire.ObservedAt = c.clock().UTC()
	}
	status, envelope, body, err := c.roundTrip(ctx, "ClaimNext", pathClaimNext, wire)
	if err != nil {
		return cutovercontract.Lease{}, false, err
	}
	switch {
	case status == http.StatusNotFound && envelope.Code == CodeNotFound:
		return cutovercontract.Lease{}, false, nil
	case status == http.StatusTooManyRequests || envelope.Code == CodePaced:
		retry := time.Duration(envelope.RetryAfterSeconds) * time.Second
		return cutovercontract.Lease{}, false, &CallError{
			Op:         "ClaimNext",
			Code:       CodePaced,
			Message:    boundMessage(envelope.Message),
			RetryAfter: retry,
		}
	case status < 200 || status > 299:
		return cutovercontract.Lease{}, false, c.callError("ClaimNext", status, envelope)
	}
	var resp claimNextResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return cutovercontract.Lease{}, false, c.callErrorText("ClaimNext", CodeInvalid, err.Error())
	}
	if !resp.Issued {
		if strings.TrimSpace(resp.Deferral.Signal) == "" {
			return cutovercontract.Lease{}, false, c.callErrorText("ClaimNext", CodeInvalid, "unissued claim without deferral")
		}
		return cutovercontract.Lease{}, false, &CallError{
			Op:         "ClaimNext",
			Code:       CodePaced,
			Message:    boundMessage("claim deferred: " + resp.Deferral.Signal),
			RetryAfter: resp.Deferral.RetryAfter,
		}
	}
	if strings.TrimSpace(resp.Grant.Claim.ClaimID) == "" || strings.TrimSpace(resp.Grant.WorkItem.WorkItemID) == "" {
		return cutovercontract.Lease{}, false, c.callErrorText("ClaimNext", CodeInvalid, "issued claim without ids")
	}
	return cutovercontract.Lease{ClaimID: resp.Grant.Claim.ClaimID, LeaseID: resp.Grant.WorkItem.WorkItemID}, true, nil
}

// Heartbeat renews the lease on a held claim epoch.
func (c *Client) Heartbeat(ctx context.Context, lease cutovercontract.Lease) error {
	return c.mutate(ctx, "HeartbeatClaim", pathHeartbeatClaim, lease, nil)
}

// Complete finishes a held claim after its generation committed. The result
// must be non-empty verbatim JSON; anything else fails closed locally.
func (c *Client) Complete(ctx context.Context, lease cutovercontract.Lease, resultJSON []byte) error {
	if len(resultJSON) == 0 {
		return errors.New("claimplane CompleteClaim: result is required")
	}
	return c.mutate(ctx, "CompleteClaim", pathCompleteClaim, lease, resultJSON)
}

// Release returns a held claim unworked.
func (c *Client) Release(ctx context.Context, lease cutovercontract.Lease) error {
	return c.mutate(ctx, "ReleaseClaim", pathReleaseClaim, lease, nil)
}

// FailRetryable records a retryable claim failure.
func (c *Client) FailRetryable(ctx context.Context, lease cutovercontract.Lease) error {
	return c.mutate(ctx, "FailClaimRetryable", pathFailClaimRetryable, lease, nil)
}

// FailTerminal records a terminal claim failure.
func (c *Client) FailTerminal(ctx context.Context, lease cutovercontract.Lease) error {
	return c.mutate(ctx, "FailClaimTerminal", pathFailClaimTerminal, lease, nil)
}

// mutate sends one fenced lifecycle mutation. A 2xx counts as success only
// when the body is exactly the {"ok":true} acknowledgement.
func (c *Client) mutate(ctx context.Context, op, path string, lease cutovercontract.Lease, result []byte) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("claimplane %s: %w", op, err)
	}
	if strings.TrimSpace(lease.ClaimID) == "" || strings.TrimSpace(lease.LeaseID) == "" {
		return fmt.Errorf("claimplane %s: claim and lease ids are required", op)
	}
	if err := c.checkGrant(ctx); err != nil {
		return err
	}
	req := cutovercontract.ClaimOutcome{ProducerID: c.prodID, ClaimID: lease.ClaimID, LeaseID: lease.LeaseID}
	if result != nil {
		req.Result = json.RawMessage(result)
	}
	status, envelope, body, err := c.roundTrip(ctx, op, path, req)
	if err != nil {
		return err
	}
	if status < 200 || status > 299 {
		return c.callError(op, status, envelope)
	}
	var ack ackResponse
	if err := decodeStrict(body, &ack); err != nil {
		return c.callErrorText(op, CodeInvalid, err.Error())
	}
	if !ack.OK {
		return c.callErrorText(op, CodeInvalid, "acknowledgement did not report ok")
	}
	return nil
}

// scopeDeclared reports whether scope is one of the declared collector
// kinds. Empty scopes never match, mirroring the in-tree grant decision:
// a call whose scope is not declared never authorizes.
func scopeDeclared(scopes []string, scope string) bool {
	if strings.TrimSpace(scope) == "" {
		return false
	}
	for _, declared := range scopes {
		if scope == declared {
			return true
		}
	}
	return false
}

// checkGrant re-reads the live grant set for the configured holder. A deny
// fails closed before any wire contact.
func (c *Client) checkGrant(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("claimplane grant check: %w", err)
	}
	ok, reason := c.grants.Granted(c.prodID)
	if ok {
		return nil
	}
	if strings.TrimSpace(reason) == "" {
		reason = ReasonGrantsUnreadable
	}
	return &DenyError{Reason: reason, ProducerID: c.prodID}
}

// roundTrip sends one protocol call and returns the status, the error
// envelope (present on failures), and the bounded body.
func (c *Client) roundTrip(
	ctx context.Context, op, path string, request any,
) (int, errorEnvelope, []byte, error) {
	raw, err := json.Marshal(request)
	if err != nil {
		return 0, errorEnvelope{}, nil, c.callErrorText(op, CodeInvalid, err.Error())
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(raw))
	if err != nil {
		return 0, errorEnvelope{}, nil, c.callErrorText(op, CodeInvalid, err.Error())
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set(protocolVersionHeader, protocolVersion)
	httpResp, err := c.http.Do(httpReq)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return 0, errorEnvelope{}, nil, fmt.Errorf("claimplane %s: %w", op, ctxErr)
		}
		return 0, errorEnvelope{}, nil, c.callErrorText(op, CodeTransport, err.Error())
	}
	defer func() { _ = httpResp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(httpResp.Body, c.max+1))
	if err != nil {
		return 0, errorEnvelope{}, nil, c.callErrorText(op, CodeTransport, err.Error())
	}
	if int64(len(body)) > c.max {
		return 0, errorEnvelope{}, nil, &CallError{
			Op:      op,
			Code:    CodeResponseTooLarge,
			Message: fmt.Sprintf("response exceeds the %d-byte bound", c.max),
		}
	}
	if httpResp.StatusCode < 200 || httpResp.StatusCode > 299 {
		var envelope errorEnvelope
		_ = json.Unmarshal(body, &envelope)
		return httpResp.StatusCode, envelope, body, nil
	}
	return httpResp.StatusCode, errorEnvelope{}, body, nil
}

// callError renders a protocol failure. An unparseable body degrades to the
// status text rather than failing to produce an error.
func (c *Client) callError(op string, status int, envelope errorEnvelope) error {
	code := strings.TrimSpace(envelope.Code)
	message := envelope.Message
	if code == "" {
		code = CodeUnexpectedStatus
		message = http.StatusText(status)
		if strings.TrimSpace(message) == "" {
			message = fmt.Sprintf("unexpected status %d", status)
		}
	}
	callErr := &CallError{Op: op, Code: code, Message: boundMessage(message)}
	if envelope.RetryAfterSeconds > 0 {
		callErr.RetryAfter = time.Duration(envelope.RetryAfterSeconds) * time.Second
	}
	return callErr
}

// callErrorText renders a client-side failure (decode, bound, transport).
func (c *Client) callErrorText(op, code, message string) error {
	return &CallError{Op: op, Code: code, Message: boundMessage(message)}
}

// decodeStrict decodes one JSON document the way the plane answers must
// arrive: unknown fields rejected, trailing data rejected.
func decodeStrict(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("strict decode: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return errors.New("strict decode: trailing data after JSON document")
	}
	return nil
}
