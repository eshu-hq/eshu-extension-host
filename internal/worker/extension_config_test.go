// SPDX-License-Identifier: MIT
// Copyright (c) 2026 eshu-hq

// Extension-config delivery: the child request carries the
// deploy-pinned config object cloned per claim.
package worker

import (
	"encoding/json"
	"sync"
	"testing"
)

// extensionConfigFixture is the deploy-pinned shape: the collector
// instance id plus targets, pinned from the fleet worker entry.
func extensionConfigFixture() map[string]any {
	return map[string]any{
		"collector_instance_id": "test-instance",
		"targets":               []any{"t1", "t2"},
	}
}

// TestContractForCarriesExtensionConfigByteForByte pins the child
// stdin config to the configured object byte-for-byte: the child
// artifact decodes it, so any drift — a dropped key, a renamed
// field, a reordered render — breaks this test before it can reach
// a live window.
func TestContractForCarriesExtensionConfigByteForByte(t *testing.T) {
	cfg := testConfig()
	cfg.ExtensionConfig = extensionConfigFixture()
	raw := contractFor(cfg, testLease())
	var req struct {
		Config json.RawMessage `json:"config"`
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		t.Fatalf("contract is not a request: %v", err)
	}
	const want = `{"collector_instance_id":"test-instance","targets":["t1","t2"]}`
	var decoded any
	if err := json.Unmarshal(req.Config, &decoded); err != nil {
		t.Fatalf("config is not JSON: %v", err)
	}
	roundTrip, err := json.Marshal(decoded)
	if err != nil {
		t.Fatalf("re-marshal config: %v", err)
	}
	if string(roundTrip) != want {
		t.Errorf("child config = %s, want byte-for-byte %s", roundTrip, want)
	}
}

// TestCloneExtensionConfigDeepCopies proves one claim's config never
// shares nested maps or slices with the configured object: mutating
// a delivered clone leaves the source — and therefore the next
// claim — untouched.
func TestCloneExtensionConfigDeepCopies(t *testing.T) {
	if cloneExtensionConfig(nil) != nil {
		t.Error("clone of nil config is not nil, want nil so omitempty drops the field")
	}
	source := map[string]any{
		"nested":  map[string]any{"key": "original"},
		"targets": []any{"t1"},
	}
	cloned := cloneExtensionConfig(source)
	cloned["nested"].(map[string]any)["key"] = "mutated"
	cloned["targets"].([]any)[0] = "mutated"
	if source["nested"].(map[string]any)["key"] != "original" {
		t.Errorf("source nested key = %q, want original (clone shared the nested map)",
			source["nested"].(map[string]any)["key"])
	}
	if source["targets"].([]any)[0] != "t1" {
		t.Errorf("source targets[0] = %q, want t1 (clone shared the slice)",
			source["targets"].([]any)[0])
	}
}

// TestContractForExtensionConfigConcurrentIsolation renders parallel
// claims against one shared Config and proves every request carries
// the configured object byte-for-byte while the configured object
// itself never moves: no claim observes another through shared
// mutation. Run with -race: shared backing arrays would trip it.
func TestContractForExtensionConfigConcurrentIsolation(t *testing.T) {
	cfg := testConfig()
	cfg.ExtensionConfig = extensionConfigFixture()
	const want = `{"collector_instance_id":"test-instance","targets":["t1","t2"]}`
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 25 {
				raw := contractFor(cfg, testLease())
				var req struct {
					Config json.RawMessage `json:"config"`
				}
				if err := json.Unmarshal(raw, &req); err != nil {
					t.Errorf("claim %d: contract is not a request: %v", i, err)
					return
				}
				var decoded any
				if err := json.Unmarshal(req.Config, &decoded); err != nil {
					t.Errorf("claim %d: config is not JSON: %v", i, err)
					return
				}
				roundTrip, err := json.Marshal(decoded)
				if err != nil {
					t.Errorf("claim %d: re-marshal config: %v", i, err)
					return
				}
				if string(roundTrip) != want {
					t.Errorf("claim %d: child config = %s, want %s", i, roundTrip, want)
					return
				}
			}
		}()
	}
	wg.Wait()
	roundTrip, err := json.Marshal(cfg.ExtensionConfig)
	if err != nil {
		t.Fatalf("re-marshal configured object: %v", err)
	}
	if string(roundTrip) != want {
		t.Errorf("configured object moved to %s, want %s (claims mutated shared state)", roundTrip, want)
	}
}
