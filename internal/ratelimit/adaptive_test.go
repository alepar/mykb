package ratelimit

import (
	"math"
	"sync"
	"testing"
	"time"
)

func TestAdaptiveLimiter_RateDecreasesOnFailure(t *testing.T) {
	limiter := NewAdaptiveLimiter(Config{
		StartingRate: 1.0,
		FloorRate:    0.1,
		SafetyMargin: 0.9,
	})
	defer limiter.Close()

	// Report a failure at starting rate
	limiter.ReportFailure()

	stats := limiter.Stats()
	// First failure: ceiling = 1.0, rate = 1.0 * 0.9 = 0.9
	if math.Abs(stats.CurrentRate-0.9) > 0.001 {
		t.Errorf("expected rate 0.9 after failure, got %f", stats.CurrentRate)
	}
	if stats.TotalFailures != 1 {
		t.Errorf("expected 1 failure, got %d", stats.TotalFailures)
	}

	// Report another failure at the new rate
	limiter.ReportFailure()

	stats = limiter.Stats()
	// Second failure: ceiling = EMA(0.9, 1.0) = 0.3*0.9 + 0.7*1.0 = 0.97
	// rate = 0.97 * 0.9 = 0.873
	expectedCeiling := 0.3*0.9 + 0.7*1.0
	expectedRate := expectedCeiling * 0.9
	if math.Abs(stats.CurrentRate-expectedRate) > 0.001 {
		t.Errorf("expected rate %.3f after second failure, got %f", expectedRate, stats.CurrentRate)
	}
}

func TestAdaptiveLimiter_RespectsFloor(t *testing.T) {
	limiter := NewAdaptiveLimiter(Config{
		StartingRate: 1.0,
		FloorRate:    0.5,
		SafetyMargin: 0.9,
	})
	defer limiter.Close()

	// Report many failures to drive rate down
	for range 20 {
		limiter.ReportFailure()
	}

	stats := limiter.Stats()
	if stats.CurrentRate < 0.5 {
		t.Errorf("expected rate to be at or above floor 0.5, got %f", stats.CurrentRate)
	}
}

func TestAdaptiveLimiter_ProbesAfterSuccesses(t *testing.T) {
	limiter := NewAdaptiveLimiter(Config{
		StartingRate:      1.0,
		SuccessesForProbe: 5,
		SafetyMargin:      0.9,
		ProbeMultiplier:   1.1,
	})
	defer limiter.Close()

	// First, slow it down with a failure
	limiter.ReportFailure()
	stats := limiter.Stats()
	// ceiling = 1.0, rate = 0.9
	if math.Abs(stats.CurrentRate-0.9) > 0.001 {
		t.Errorf("expected rate 0.9 after failure, got %f", stats.CurrentRate)
	}

	// Report 5 successes (threshold for probe)
	for range 5 {
		limiter.ReportSuccess()
	}

	stats = limiter.Stats()
	// After 5 successes: probe at ceiling * 1.1 = 1.0 * 1.1 = 1.1
	// But capped at startingRate = 1.0
	if math.Abs(stats.CurrentRate-1.0) > 0.001 {
		t.Errorf("expected rate 1.0 after probing (capped at starting), got %f", stats.CurrentRate)
	}
	if stats.TotalSuccesses != 5 {
		t.Errorf("expected 5 successes, got %d", stats.TotalSuccesses)
	}
}

func TestAdaptiveLimiter_SuccessCounterResetsOnFailure(t *testing.T) {
	limiter := NewAdaptiveLimiter(Config{
		StartingRate:      1.0,
		SuccessesForProbe: 5,
		SafetyMargin:      0.9,
	})
	defer limiter.Close()

	// Slow it down first
	limiter.ReportFailure()
	rateAfterFirstFailure := limiter.Stats().CurrentRate

	// Report 4 successes (not enough for probe)
	for range 4 {
		limiter.ReportSuccess()
	}

	// Failure should reset the counter
	limiter.ReportFailure()

	// Report 4 more successes (still not enough since counter reset)
	for range 4 {
		limiter.ReportSuccess()
	}

	stats := limiter.Stats()
	// Rate should still be at safe rate (no probe triggered)
	// The rate decreased from the second failure
	if stats.CurrentRate >= rateAfterFirstFailure {
		t.Errorf("expected rate to have decreased from second failure, got %f (was %f)",
			stats.CurrentRate, rateAfterFirstFailure)
	}
}

func TestAdaptiveLimiter_CappedAtStartingRate(t *testing.T) {
	limiter := NewAdaptiveLimiter(Config{
		StartingRate:      1.0,
		SuccessesForProbe: 2,
		ProbeMultiplier:   10.0, // Aggressive probe
	})
	defer limiter.Close()

	// First failure to establish ceiling
	limiter.ReportFailure()

	// Report many successes
	for range 10 {
		limiter.ReportSuccess()
	}

	stats := limiter.Stats()
	if stats.CurrentRate > 1.0 {
		t.Errorf("expected rate capped at starting rate 1.0, got %f", stats.CurrentRate)
	}
}

func TestAdaptiveLimiter_AcquireBlocks(t *testing.T) {
	limiter := NewAdaptiveLimiter(Config{
		StartingRate: 10.0, // 10 tokens/sec = 100ms per token
		BucketSize:   1,    // Only 1 token at a time
	})
	defer limiter.Close()

	// Drain the pre-filled token
	limiter.Acquire()

	// Next acquire should block until a new token is generated
	start := time.Now()
	limiter.Acquire()
	elapsed := time.Since(start)

	// Should have waited approximately 100ms (with some tolerance)
	if elapsed < 80*time.Millisecond {
		t.Errorf("expected to wait ~100ms, but only waited %v", elapsed)
	}
	if elapsed > 200*time.Millisecond {
		t.Errorf("expected to wait ~100ms, but waited %v", elapsed)
	}
}

func TestAdaptiveLimiter_ConcurrentAccess(t *testing.T) {
	limiter := NewAdaptiveLimiter(Config{
		StartingRate:      100.0,
		BucketSize:        10,
		SuccessesForProbe: 5,
	})
	defer limiter.Close()

	var wg sync.WaitGroup
	const numGoroutines = 20
	const opsPerGoroutine = 10

	for range numGoroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range opsPerGoroutine {
				limiter.Acquire()
				// Randomly report success or failure
				if time.Now().UnixNano()%2 == 0 {
					limiter.ReportSuccess()
				} else {
					limiter.ReportFailure()
				}
			}
		}()
	}

	wg.Wait()

	stats := limiter.Stats()
	totalOps := stats.TotalSuccesses + stats.TotalFailures
	if totalOps != numGoroutines*opsPerGoroutine {
		t.Errorf("expected %d total operations, got %d", numGoroutines*opsPerGoroutine, totalOps)
	}
}

func TestAdaptiveLimiter_DefaultConfig(t *testing.T) {
	limiter := NewAdaptiveLimiter(Config{
		StartingRate: 1.0,
	})
	defer limiter.Close()

	// Check defaults were applied
	if limiter.floorRate != 1.0/16.0 {
		t.Errorf("expected default floor rate %f, got %f", 1.0/16.0, limiter.floorRate)
	}
	if limiter.successesForProbe != 25 {
		t.Errorf("expected default successesForProbe 25, got %d", limiter.successesForProbe)
	}
	if limiter.safetyMargin != 0.9 {
		t.Errorf("expected default safetyMargin 0.9, got %f", limiter.safetyMargin)
	}
	if limiter.probeMultiplier != 1.1 {
		t.Errorf("expected default probeMultiplier 1.1, got %f", limiter.probeMultiplier)
	}
	if limiter.emaAlpha != 0.3 {
		t.Errorf("expected default emaAlpha 0.3, got %f", limiter.emaAlpha)
	}
}

func TestAdaptiveLimiter_CeilingConverges(t *testing.T) {
	limiter := NewAdaptiveLimiter(Config{
		StartingRate: 2.0,
		SafetyMargin: 0.9,
		EMAAlpha:     0.5, // Faster convergence for testing
	})
	defer limiter.Close()

	// Simulate failures at decreasing rates (converging toward limit)
	// Each failure updates the ceiling via EMA
	limiter.ReportFailure() // fail at 2.0, ceiling = 2.0, rate = 1.8
	limiter.ReportFailure() // fail at 1.8, ceiling = EMA(1.8, 2.0) = 1.9, rate = 1.71
	limiter.ReportFailure() // fail at 1.71, ceiling = EMA(1.71, 1.9) = 1.805, rate = 1.62

	stats := limiter.Stats()

	// Rate should be converging downward
	if stats.CurrentRate >= 1.8 {
		t.Errorf("expected rate to converge below 1.8, got %f", stats.CurrentRate)
	}
	if stats.CurrentRate < 1.0 {
		t.Errorf("expected rate to stay above 1.0, got %f", stats.CurrentRate)
	}
}

func TestAdaptiveLimiter_ProbeFailureDoesNotInflateCeiling(t *testing.T) {
	limiter := NewAdaptiveLimiter(Config{
		StartingRate:      1.0,
		SuccessesForProbe: 3,
		SafetyMargin:      0.9,
		ProbeMultiplier:   1.1,
		EMAAlpha:          0.5,
	})
	defer limiter.Close()

	// First failure establishes ceiling at 1.0
	limiter.ReportFailure()
	// ceiling = 1.0, rate = 0.9

	// Get 3 successes to trigger probe
	for range 3 {
		limiter.ReportSuccess()
	}
	// Now at probe rate = min(1.0 * 1.1, 1.0) = 1.0 (capped)

	stats := limiter.Stats()
	rateBeforeProbeFailure := stats.CurrentRate

	// Simulate a probe failure (at rate above ceiling would be if ceiling were lower)
	// Since we're at startingRate (1.0), this is essentially a probe
	limiter.ReportFailure()

	stats = limiter.Stats()

	// The ceiling should NOT have increased from the probe failure
	// It should have stayed at 1.0 or decreased
	// Rate should be 0.9 (90% of ceiling 1.0)
	expectedRate := 0.9
	if math.Abs(stats.CurrentRate-expectedRate) > 0.001 {
		t.Errorf("expected rate %.3f after probe failure, got %f (was %f)",
			expectedRate, stats.CurrentRate, rateBeforeProbeFailure)
	}
}

func TestAdaptiveLimiter_SuccessfulProbeUpdatesCeiling(t *testing.T) {
	limiter := NewAdaptiveLimiter(Config{
		StartingRate:      2.0, // Higher starting rate to allow ceiling growth
		SuccessesForProbe: 3,
		SafetyMargin:      0.9,
		ProbeMultiplier:   1.1,
		EMAAlpha:          0.5,
	})
	defer limiter.Close()

	// Failure at 2.0 establishes ceiling
	limiter.ReportFailure()
	// ceiling = 2.0, rate = 1.8

	// Get 3 successes to trigger probe
	for range 3 {
		limiter.ReportSuccess()
	}
	// probe rate = 2.0 * 1.1 = 2.2, capped at startingRate = 2.0
	// No change since we're already capped

	// Now simulate a scenario where ceiling is lower
	limiter.ReportFailure() // fail at 2.0, rate drops
	limiter.ReportFailure() // fail again, ceiling converges lower

	// Get ceiling value by checking behavior
	stats := limiter.Stats()
	rateBeforeProbe := stats.CurrentRate

	// Get 3 successes to trigger probe
	for range 3 {
		limiter.ReportSuccess()
	}

	stats = limiter.Stats()

	// Should have probed higher
	if stats.CurrentRate <= rateBeforeProbe {
		t.Errorf("expected rate to increase after probe, got %f (was %f)",
			stats.CurrentRate, rateBeforeProbe)
	}
}

func TestAdaptiveLimiter_StabilizesAtSafeRate(t *testing.T) {
	limiter := NewAdaptiveLimiter(Config{
		StartingRate:      1.5,
		SuccessesForProbe: 100, // High threshold so we don't probe during test
		SafetyMargin:      0.9,
		EMAAlpha:          0.5, // Faster convergence
	})
	defer limiter.Close()

	// Simulate several failures - each failure drops rate
	const numFailures = 10
	rates := make([]float64, 0, numFailures)
	for range numFailures {
		limiter.ReportFailure()
		rates = append(rates, limiter.Stats().CurrentRate)
	}

	// Verify convergence: rate changes should get smaller over time
	// (difference between consecutive rates decreases)
	lastDelta := rates[1] - rates[0]
	for i := 2; i < len(rates); i++ {
		delta := math.Abs(rates[i] - rates[i-1])
		// Allow some tolerance for floating point
		if delta > math.Abs(lastDelta)*1.1 {
			t.Errorf("expected convergence (decreasing deltas), but delta[%d]=%.4f > delta[%d]=%.4f",
				i, delta, i-1, math.Abs(lastDelta))
		}
		lastDelta = rates[i] - rates[i-1]
	}

	// Final rate should be well below starting rate
	finalRate := rates[len(rates)-1]
	if finalRate >= 1.5 {
		t.Errorf("expected rate well below starting rate 1.5, got %f", finalRate)
	}
}
