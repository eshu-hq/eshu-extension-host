// SPDX-License-Identifier: MIT
// Copyright (c) 2026 eshu-hq

package claimplane

import (
	"time"
)

// Grant is one static producer-grant row. A zero ExpiresAt means no expiry.
type Grant struct {
	ProducerID string
	Revoked    bool
	ExpiresAt  time.Time
}

// StaticGrants is the in-memory GrantSource for tests and fixed
// deployments. Now supplies the evaluation clock; nil reads the wall clock
// (UTC) on every call.
type StaticGrants struct {
	Grants []Grant
	Now    func() time.Time
}

// Granted evaluates the rows for one producer under the evaluation clock.
func (s *StaticGrants) Granted(producerID string) (bool, string) {
	now := s.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	at := now()
	var matched, live bool
	for _, g := range s.Grants {
		if g.ProducerID != producerID {
			continue
		}
		matched = true
		if g.Revoked {
			continue
		}
		if !g.ExpiresAt.IsZero() && !at.Before(g.ExpiresAt) {
			continue
		}
		live = true
	}
	switch {
	case !matched:
		return false, ReasonNoMatchingGrant
	case !live:
		for _, g := range s.Grants {
			if g.ProducerID == producerID && !g.Revoked {
				return false, ReasonExpired
			}
		}
		return false, ReasonRevoked
	default:
		return true, ReasonGranted
	}
}
