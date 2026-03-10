package nails

import (
	"context"
	"sync/atomic"
	"testing"
)

// countingCollector tracks how many times Collect is called.
type countingCollector struct {
	name  string
	calls atomic.Int32
}

func (c *countingCollector) Name() string { return c.name }
func (c *countingCollector) Collect(_ context.Context) (map[string]any, error) {
	c.calls.Add(1)
	return map[string]any{"collected": true}, nil
}

func TestCachedCollectors_FirstCycleRunsAll(t *testing.T) {
	cc := NewCachedCollectors(10)

	// Replace with counting collectors.
	stable := &countingCollector{name: "stable"}
	volatile := &countingCollector{name: "volatile"}
	cc.stable = []Collector{stable}
	cc.volatile = []Collector{volatile}

	result, err := cc.CollectAll(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	if stable.calls.Load() != 1 {
		t.Errorf("stable calls = %d, want 1", stable.calls.Load())
	}
	if volatile.calls.Load() != 1 {
		t.Errorf("volatile calls = %d, want 1", volatile.calls.Load())
	}
	if _, ok := result["stable"]; !ok {
		t.Error("missing 'stable' key in result")
	}
	if _, ok := result["volatile"]; !ok {
		t.Error("missing 'volatile' key in result")
	}
}

func TestCachedCollectors_StableCachedBetweenCycles(t *testing.T) {
	cc := NewCachedCollectors(5)

	stable := &countingCollector{name: "stable"}
	volatile := &countingCollector{name: "volatile"}
	cc.stable = []Collector{stable}
	cc.volatile = []Collector{volatile}

	// Run 5 cycles (cycle 0 through 4).
	for i := 0; i < 5; i++ {
		result, err := cc.CollectAll(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		// Every cycle should have both keys.
		if _, ok := result["stable"]; !ok {
			t.Errorf("cycle %d: missing 'stable' key", i)
		}
		if _, ok := result["volatile"]; !ok {
			t.Errorf("cycle %d: missing 'volatile' key", i)
		}
	}

	// Stable should run on cycle 0 only (first call).
	if stable.calls.Load() != 1 {
		t.Errorf("stable calls = %d, want 1 (only first cycle)", stable.calls.Load())
	}
	// Volatile runs every cycle.
	if volatile.calls.Load() != 5 {
		t.Errorf("volatile calls = %d, want 5", volatile.calls.Load())
	}
}

func TestCachedCollectors_StableRefreshesAtInterval(t *testing.T) {
	cc := NewCachedCollectors(3)

	stable := &countingCollector{name: "stable"}
	volatile := &countingCollector{name: "volatile"}
	cc.stable = []Collector{stable}
	cc.volatile = []Collector{volatile}

	// Run 7 cycles (0,1,2,3,4,5,6).
	// Stable runs at: cycle 0 (first), cycle 3 (3%3==0), cycle 6 (6%3==0) = 3 times.
	for i := 0; i < 7; i++ {
		_, err := cc.CollectAll(context.Background())
		if err != nil {
			t.Fatal(err)
		}
	}

	if stable.calls.Load() != 3 {
		t.Errorf("stable calls = %d, want 3 (cycles 0, 3, 6)", stable.calls.Load())
	}
	if volatile.calls.Load() != 7 {
		t.Errorf("volatile calls = %d, want 7", volatile.calls.Load())
	}
}

func TestCachedCollectors_IntervalClampedToOne(t *testing.T) {
	cc := NewCachedCollectors(0) // should clamp to 1

	stable := &countingCollector{name: "stable"}
	cc.stable = []Collector{stable}
	cc.volatile = nil

	for i := 0; i < 3; i++ {
		cc.CollectAll(context.Background())
	}

	// With interval=1, stable runs every cycle.
	if stable.calls.Load() != 3 {
		t.Errorf("stable calls = %d, want 3 (interval clamped to 1)", stable.calls.Load())
	}
}

func TestVolatileAndStableCollectors_Cover13(t *testing.T) {
	volatile := VolatileCollectors()
	stable := StableCollectors()

	total := len(volatile) + len(stable)
	if total != 18 {
		t.Errorf("volatile(%d) + stable(%d) = %d, want 18", len(volatile), len(stable), total)
	}

	// Verify no duplicates.
	seen := make(map[string]bool)
	for _, c := range volatile {
		if seen[c.Name()] {
			t.Errorf("duplicate collector: %s", c.Name())
		}
		seen[c.Name()] = true
	}
	for _, c := range stable {
		if seen[c.Name()] {
			t.Errorf("duplicate collector: %s", c.Name())
		}
		seen[c.Name()] = true
	}
}
